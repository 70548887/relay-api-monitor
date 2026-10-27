package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"relaylab/internal/artifact"
	"relaylab/internal/domain"
	"relaylab/internal/store"
	"relaylab/internal/upstream"
)

type requestSnapshot struct {
	ModelID                   string            `json:"model_id"`
	RequestedModel            string            `json:"requested_model"`
	ChannelID                 string            `json:"channel_id"`
	ChannelRevision           int               `json:"channel_revision"`
	Protocol                  string            `json:"protocol"`
	TemplateID                string            `json:"template_id,omitempty"`
	TemplateVersion           int               `json:"template_version,omitempty"`
	Prompt                    string            `json:"prompt"`
	Parameters                requestParameters `json:"parameters"`
	VerifiedGroup             string            `json:"verified_group,omitempty"`
	GroupVerificationStatus   string            `json:"group_verification_status,omitempty"`
	GroupVerificationRevision int               `json:"group_verification_revision,omitempty"`
}
type requestParameters struct {
	ContextPolicy   string `json:"context_policy,omitempty"`
	MaxTokens       int    `json:"max_tokens"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	ThinkingMode    string `json:"thinking_mode,omitempty"`
	ThinkingBudget  int    `json:"thinking_budget,omitempty"`
}

func configFor(c domain.Channel, key, protocol, path string) upstream.Config {
	return upstream.Config{BaseURL: c.BaseURL, APIKey: key, Protocol: protocol, DiscoveryProtocol: c.DiscoveryProtocol, ModelsPath: c.ModelsPath, GeneratePath: path, AnthropicVersion: c.AnthropicVersion, ChatTokenField: c.ChatTokenField}
}
func (w *Worker) execute(parent context.Context, j domain.Job) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	lost := &atomic.Bool{}
	watched := make(chan struct{})
	go w.watchJob(ctx, j, cancel, lost, watched)
	defer func() { cancel(); <-watched }()
	failBeforeDispatch := func(code, message string) error {
		j.State = "failed"
		j.ErrorCode = code
		j.ErrorMessage = message
		if ctx.Err() != nil {
			j.State = "canceled"
			j.ErrorCode = "canceled"
			j.ErrorMessage = "任务已取消"
		}
		return w.complete(j, nil, nil, lost.Load())
	}
	m, err := w.st.GetModel(ctx, j.ModelID)
	if err != nil {
		return failBeforeDispatch("model_unavailable", "模型配置不存在")
	}
	c, err := w.st.GetChannel(ctx, j.ChannelID)
	if err != nil {
		return failBeforeDispatch("channel_unavailable", "渠道配置不存在")
	}
	p, err := w.st.GetPlatform(ctx, c.PlatformID)
	if err != nil {
		return failBeforeDispatch("platform_unavailable", "平台配置不存在")
	}
	if m.ChannelID != c.ID || j.ChannelRevision != c.Revision {
		return failBeforeDispatch("configuration_changed", "渠道配置已变更，请重新创建任务")
	}
	if m.Archived || c.Archived || p.Archived {
		return failBeforeDispatch("archived", "平台、渠道或模型已归档")
	}
	if !c.Enabled || m.Missing {
		return failBeforeDispatch("disabled", "模型已缺失或分组已停用，请检查配置")
	}
	if j.ScheduleID != "" {
		enabled := c.Enabled && !m.Missing && ((j.Kind == "probe" && m.ProbeEnabled) || (j.Kind == "artifact" && c.ScheduledArtifactEnabled(*m, j.TemplateID)))
		if !enabled {
			return failBeforeDispatch("schedule_disabled", "定时任务已停用")
		}
	}
	settings, err := w.st.GetSettings(ctx)
	if err != nil {
		return failBeforeDispatch("settings_unavailable", "读取设置失败")
	}
	protocol := m.Protocol
	if protocol == "" {
		protocol = c.Protocol
	}
	if protocol == "" {
		protocol = "chat"
	}
	path := m.GeneratePath
	if path == "" {
		path = c.GeneratePath
	}
	maxTokens, timeoutSeconds := 1024, settings.ProbeTimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	prompt := "Reply with exactly OK."
	template := domain.PromptTemplate{}
	if j.Kind == "artifact" {
		maxTokens = 8192
		timeoutSeconds = settings.ArtifactTimeoutSeconds
		if timeoutSeconds <= 0 {
			timeoutSeconds = 600
		}
		t, err := w.st.GetTemplate(ctx, j.TemplateID)
		if err != nil {
			return failBeforeDispatch("template_unavailable", "提示词模板不存在")
		}
		template = *t
		prompt = t.Prompt
	}
	if j.Kind == "artifact" && m.MaxTokens > 0 {
		maxTokens = m.MaxTokens
	}
	if j.Kind == "artifact" && m.TimeoutSeconds > 0 {
		timeoutSeconds = m.TimeoutSeconds
	}
	effort, thinkingMode, thinkingBudget := "", "", 0
	if j.Kind == "artifact" {
		effort, thinkingMode, thinkingBudget = upstream.ResolveReasoning(*m, *c)
		if err = upstream.ValidateReasoning(protocol, effort, thinkingMode, thinkingBudget, maxTokens); err != nil {
			code, message := errorDetails(err)
			return failBeforeDispatch(code, message)
		}
	}
	key, err := w.vault.Decrypt(c.EncryptedKey)
	if err != nil {
		return failBeforeDispatch("key_unavailable", err.Error())
	}
	snapshot := requestSnapshot{ModelID: m.ID, RequestedModel: m.UpstreamID, ChannelID: c.ID, ChannelRevision: c.Revision, Protocol: protocol, TemplateID: template.ID, TemplateVersion: template.Version, Prompt: prompt, Parameters: requestParameters{ContextPolicy: "independent", MaxTokens: maxTokens, TimeoutSeconds: timeoutSeconds, ReasoningEffort: effort, ThinkingMode: thinkingMode, ThinkingBudget: thinkingBudget}}
	if c.GroupVerificationRevision == c.Revision {
		snapshot.GroupVerificationStatus = c.GroupVerificationStatus
		snapshot.GroupVerificationRevision = c.GroupVerificationRevision
		if c.GroupVerificationStatus == "verified" {
			snapshot.VerifiedGroup = c.VerifiedGroup
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return failBeforeDispatch("snapshot_error", "请求快照创建失败")
	}
	latest, err := w.st.GetChannel(ctx, c.ID)
	if err != nil || latest.Revision != j.ChannelRevision {
		return failBeforeDispatch("configuration_changed", "渠道配置已变更，请重新创建任务")
	}
	started := time.Now().UTC()
	deadline := started.Add(time.Duration(timeoutSeconds) * time.Second)
	if err = w.st.MarkDispatching(ctx, j.ID, j.LeaseToken, deadline, string(encoded)); err != nil {
		if errors.Is(err, store.ErrConfigurationChanged) {
			return failBeforeDispatch("configuration_changed", "平台、渠道或计划配置已变更，请重新创建任务")
		}
		current, getErr := w.st.GetJob(context.Background(), j.ID)
		if getErr == nil && current.CancelRequested {
			return failBeforeDispatch("canceled", "任务已取消")
		}
		if ctx.Err() != nil {
			return failBeforeDispatch("canceled", "任务已取消")
		}
		if errors.Is(err, store.ErrConflict) {
			// A claim can expire or be recovered before dispatch while this
			// process still owns the worker lease. Leave that unsent job to
			// normal recovery instead of stopping every execution pool.
			if w.running.Load() {
				leaseCtx, leaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
				leaseErr := w.st.CheckWorkerLease(leaseCtx, w.owner)
				leaseCancel()
				if leaseErr != nil {
					return fmt.Errorf("%w: dispatch %s: %v", ErrLeaseLost, j.ID, leaseErr)
				}
			}
			return fmt.Errorf("worker: unsent job %s: %w", j.ID, err)
		}
		return err
	}
	j.State = "dispatching"
	j.Snapshot = string(encoded)
	j.Deadline = &deadline
	requestCtx, requestCancel := context.WithDeadline(ctx, deadline)
	defer requestCancel()
	var lastProgress time.Time
	result, requestErr := w.client.Generate(requestCtx, upstream.Request{Config: configFor(*c, key, protocol, path), Model: m.UpstreamID, Prompt: prompt, MaxTokens: maxTokens, Timeout: time.Duration(timeoutSeconds) * time.Second, ReasoningEffort: effort, ThinkingMode: thinkingMode, ThinkingBudget: thinkingBudget}, func(event upstream.Event) error {
		if event.Type == "text" {
			j.ProgressChars += int64(utf8.RuneCountInString(event.Text))
		}
		if event.ReturnedModel != "" {
			j.ReturnedModel = event.ReturnedModel
		}
		n := time.Now().UTC()
		if j.ProgressChars == 0 || !lastProgress.IsZero() && n.Sub(lastProgress) < time.Second {
			return nil
		}
		if err := w.st.UpdateJobProgress(requestCtx, j.ID, j.LeaseToken, j.ProgressChars, j.ReturnedModel); err != nil {
			if errors.Is(err, store.ErrConflict) {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				current, getErr := w.st.GetJob(ctx, j.ID)
				if getErr == nil && current.CancelRequested {
					cancel()
					return context.Canceled
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				lost.Store(true)
				return ErrLeaseLost
			}
			return err
		}
		j.ProgressAt = &n
		lastProgress = n
		return nil
	})
	if chars := int64(utf8.RuneCountInString(result.Text)); chars > j.ProgressChars {
		j.ProgressChars = chars
	}
	if result.ReturnedModel != "" {
		j.ReturnedModel = result.ReturnedModel
	}
	if j.ProgressChars > 0 {
		n := time.Now().UTC()
		j.ProgressAt = &n
	}
	if lost.Load() {
		return ErrLeaseLost
	}
	if ctx.Err() != nil {
		j.State = "canceled"
		j.ErrorCode = "canceled"
		j.ErrorMessage = "任务已取消"
		return w.complete(j, nil, nil, false)
	}
	j.State = "succeeded"
	var code, message string
	if requestErr != nil {
		j.State = "failed"
		code, message = errorDetails(requestErr)
	} else if !result.Complete {
		j.State = "failed"
		code, message = "incomplete_response", "上游响应未完整结束"
	}
	j.ErrorCode = code
	j.ErrorMessage = message
	if j.Kind == "probe" {
		status := "ok"
		if j.State != "succeeded" {
			status = "failed"
		} else if settings.SlowThresholdMS > 0 && result.FirstTextMS != nil && *result.FirstTextMS >= settings.SlowThresholdMS {
			status = "slow"
		}
		probe := &domain.ProbeResult{ModelID: m.ID, Status: status, Protocol: protocol, ReturnedModel: result.ReturnedModel, HTTPStatus: result.HTTPStatus, FirstTextMS: result.FirstTextMS, DurationMS: result.DurationMS, Usage: result.Usage, FinishReason: result.FinishReason, ErrorCode: code, ErrorMessage: message, CreatedAt: time.Now().UTC()}
		return w.complete(j, probe, nil, false)
	}
	if j.State != "succeeded" {
		return w.complete(j, nil, nil, false)
	}
	extracted, actualKind, err := artifact.Extract(result.Text, template.Kind)
	if err != nil {
		j.State = "failed"
		j.ErrorCode = "invalid_artifact"
		j.ErrorMessage = "返回内容没有完整可预览的HTML/SVG"
		return w.complete(j, nil, nil, false)
	}
	parameters, _ := json.Marshal(snapshot.Parameters)
	art := &domain.Artifact{ModelID: m.ID, ChannelID: c.ID, TemplateID: template.ID, TemplateVersion: template.Version, Prompt: prompt, Protocol: protocol, RequestedModel: m.UpstreamID, ReturnedModel: result.ReturnedModel, Parameters: string(parameters), ReasoningEffort: effort, ThinkingMode: thinkingMode, ThinkingBudget: thinkingBudget, VerifiedGroup: snapshot.VerifiedGroup, GroupVerificationStatus: snapshot.GroupVerificationStatus, RawResponse: result.Text, Code: extracted, Kind: actualKind, FirstTextMS: result.FirstTextMS, DurationMS: result.DurationMS, Usage: result.Usage, FinishReason: result.FinishReason, StartedAt: started, CreatedAt: time.Now().UTC()}
	return w.complete(j, nil, art, false)
}
func errorDetails(err error) (string, string) {
	var upstreamErr *upstream.Error
	if errors.As(err, &upstreamErr) {
		return upstreamErr.Code, upstreamErr.Message
	}
	return "upstream_error", "上游请求失败"
}
func (w *Worker) complete(j domain.Job, probe *domain.ProbeResult, art *domain.Artifact, lost bool) error {
	if lost {
		return ErrLeaseLost
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.st.FinishJob(ctx, j, probe, art); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return fmt.Errorf("%w: finish %s: %v", ErrLeaseLost, j.ID, err)
		}
		return fmt.Errorf("worker: finish %s: %w", j.ID, err)
	}
	return nil
}
