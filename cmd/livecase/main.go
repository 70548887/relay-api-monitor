// Command livecase configures and observes a deliberately bounded live test.
// It never enables recurring billable jobs and never accepts a key in argv.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
	"relaylab/internal/domain"
	"relaylab/internal/security"
	"relaylab/internal/store"
	"relaylab/internal/upstream"
)

type liveCase struct {
	PlatformID  string            `json:"platform_id"`
	ChannelID   string            `json:"channel_id"`
	ModelID     string            `json:"model_id"`
	Model       string            `json:"model"`
	Protocol    string            `json:"protocol"`
	ModelCount  int               `json:"model_count"`
	GroupStatus string            `json:"group_status"`
	Jobs        map[string]string `json:"jobs"`
}

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	mode := flag.String("mode", "report", "configure, enqueue, wait or report")
	dir := flag.String("data-dir", "data", "application data directory")
	base := flag.String("base-url", "", "user supplied endpoint")
	model := flag.String("model", "gpt-6.1-sol", "explicit model")
	protocol := flag.String("protocol", "responses", "explicit protocol")
	kind := flag.String("kind", "artifact", "probe or artifact")
	template := flag.String("template", "cn_html", "cn_html or en_svg")
	output := flag.String("output", "", "redacted JSON report file")
	flag.Parse()
	st, e := store.OpenExisting(filepath.Join(*dir, "relaylab.db"))
	if e != nil {
		return e
	}
	defer st.Close()
	ctx := context.Background()
	casePath := filepath.Join(*dir, "live-case.json")
	lc := liveCase{Jobs: map[string]string{}}
	if b, err := os.ReadFile(casePath); err == nil {
		if err = json.Unmarshal(b, &lc); err != nil {
			return err
		}
		if lc.Jobs == nil {
			lc.Jobs = map[string]string{}
		}
	}
	switch *mode {
	case "configure":
		if *base == "" {
			return errors.New("base-url required")
		}
		key, e := readKey()
		if e != nil {
			return e
		}
		vault, e := security.LoadVault(*dir, true)
		if e != nil {
			return e
		}
		client := upstream.New()
		cfg := upstream.Config{BaseURL: *base, APIKey: key, Protocol: *protocol, DiscoveryProtocol: "openai", ChatTokenField: "max_completion_tokens"}
		discoveryCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		ids, e := client.ListModels(discoveryCtx, cfg)
		cancel()
		if e != nil {
			return e
		}
		found := false
		for _, id := range ids {
			if id == *model {
				found = true
				break
			}
		}
		if !found {
			return errors.New("chosen model absent from actual model directory")
		}
		now := time.Now().UTC()
		p := domain.Platform{ID: lc.PlatformID, Name: "实测中转平台", Description: "使用实际模型请求验证；结果与用量均来自上游返回。", Public: true, CreatedAt: now}
		if p.ID == "" {
			p.ID = domain.NewID()
		}
		if e = st.SavePlatform(ctx, p); e != nil {
			return e
		}
		cipher, e := vault.Encrypt(key)
		if e != nil {
			return e
		}
		ch := domain.Channel{ID: lc.ChannelID, PlatformID: p.ID, Name: "实测 High 线路", BaseURL: *base, EncryptedKey: cipher, KeyMask: security.Mask(key), Protocol: *protocol, DiscoveryProtocol: "openai", ChatTokenField: "max_completion_tokens", ReasoningEffort: "high", GroupProvider: "newapi", Enabled: true, Public: true, Revision: 1, CreatedAt: now}
		if ch.ID == "" {
			ch.ID = domain.NewID()
		}
		if e = st.SaveChannelWithSchedules(ctx, ch); e != nil {
			return e
		}
		chPtr, e := st.GetChannel(ctx, ch.ID)
		if e != nil {
			return e
		}
		ch = *chPtr
		if e = st.SyncModels(ctx, ch.ID, ids, ch.Revision); e != nil {
			return e
		}
		models, e := st.ListModels(ctx, ch.ID)
		if e != nil {
			return e
		}
		var chosen domain.Model
		for _, m := range models {
			if m.UpstreamID == *model {
				chosen = m
				break
			}
		}
		if chosen.ID == "" {
			return errors.New("synced model missing")
		}
		chosen.Public = true
		chosen.Archived = false
		chosen.Protocol = *protocol
		chosen.ReasoningEffort = "high"
		chosen.MaxTokens = 16384
		chosen.TimeoutSeconds = 600
		chosen.ProbeEnabled = false
		chosen.ArtifactChinese = false
		chosen.ArtifactEnglish = false
		if e = st.SaveModelWithSchedules(ctx, chosen); e != nil {
			return e
		}
		ch.IntelligenceModelID = chosen.ID
		if e = st.SaveChannelWithSchedules(ctx, ch); e != nil {
			return e
		}
		chPtr, e = st.GetChannel(ctx, ch.ID)
		if e != nil {
			return e
		}
		ch = *chPtr
		verificationCtx, vcancel := context.WithTimeout(ctx, 25*time.Second)
		verification, verifyErr := client.VerifyGroup(verificationCtx, cfg, "newapi", "", "")
		vcancel()
		if verifyErr != nil {
			verification.Status = "failed"
			verification.Message = "上游分组元信息验证未能完成"
		}
		if e = st.SetGroupVerification(ctx, ch.ID, ch.Revision, verification.Status, verification.Group, verification.Provider, verification.Message, verification.CheckedAt); e != nil {
			return e
		}
		settings, e := st.GetSettings(ctx)
		if e != nil {
			return e
		}
		settings.ArtifactTimeoutSeconds = 600
		settings.ArtifactHistoryScope = "group"
		if e = st.SaveSettingsAndReconcile(ctx, settings); e != nil {
			return e
		}
		lc.PlatformID = p.ID
		lc.ChannelID = ch.ID
		lc.ModelID = chosen.ID
		lc.Model = *model
		lc.Protocol = *protocol
		lc.ModelCount = len(ids)
		lc.GroupStatus = verification.Status
		if e = saveJSON(casePath, lc); e != nil {
			return e
		}
		return emit(*output, map[string]any{"configured": true, "channel_id": ch.ID, "model_id": chosen.ID, "model": chosen.UpstreamID, "model_count": len(ids), "reasoning_effort": "high", "timeout_seconds": 600, "group_status": verification.Status, "group_message": verification.Message, "automatic_enabled": false})
	case "enqueue":
		if lc.ModelID == "" {
			return errors.New("live case not configured")
		}
		if *kind != "probe" && *kind != "artifact" {
			return errors.New("invalid kind")
		}
		templateID := *template
		if *kind == "probe" {
			templateID = ""
		} else if templateID != "cn_html" && templateID != "en_svg" {
			return errors.New("invalid template")
		}
		key := *kind + ":" + templateID
		if previous := lc.Jobs[key]; previous != "" {
			return errors.New("this bounded live case already has a job for this kind/template; inspect it rather than submitting twice")
		}
		ch, e := st.GetChannel(ctx, lc.ChannelID)
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		j, e := st.EnqueueJob(ctx, domain.Job{ID: domain.NewID(), ModelID: lc.ModelID, ChannelID: lc.ChannelID, Kind: *kind, TemplateID: templateID, ChannelRevision: ch.Revision, ScheduledAt: now, CreatedAt: now})
		if e != nil {
			return e
		}
		lc.Jobs[key] = j.ID
		if e = saveJSON(casePath, lc); e != nil {
			return e
		}
		return emit(*output, map[string]any{"job_id": j.ID, "state": j.State, "kind": j.Kind, "template_id": j.TemplateID})
	case "wait":
		key := *kind + ":" + *template
		if *kind == "probe" {
			key = "probe:"
		}
		id := lc.Jobs[key]
		if id == "" {
			return errors.New("no recorded job")
		}
		deadline := time.Now().Add(660 * time.Second)
		lastState := ""
		var lastChars int64
		for time.Now().Before(deadline) {
			j, e := st.GetJob(ctx, id)
			if e != nil {
				return e
			}
			if j.State != lastState || j.ProgressChars-lastChars >= 1500 {
				fmt.Printf("job=%s state=%s chars=%d\n", j.ID, j.State, j.ProgressChars)
				lastState = j.State
				lastChars = j.ProgressChars
			}
			if j.State != "queued" && j.State != "claimed" && j.State != "dispatching" {
				result, e := jobReport(ctx, st, *j)
				if e != nil {
					return e
				}
				if e = emit(*output, result); e != nil {
					return e
				}
				if j.State != "succeeded" {
					return errors.New("live job did not succeed; it will not be resubmitted automatically")
				}
				return nil
			}
			time.Sleep(time.Second)
		}
		return errors.New("observation timeout; job may still be running, inspect its same id")
	case "report":
		result := map[string]any{"case": lc}
		for kind, id := range lc.Jobs {
			j, e := st.GetJob(ctx, id)
			if e != nil {
				return e
			}
			item, e := jobReport(ctx, st, *j)
			if e != nil {
				return e
			}
			result[kind] = item
		}
		return emit(*output, result)
	default:
		return errors.New("invalid mode")
	}
}
func readKey() (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "API Key: ")
		b, e := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		s := string(b)
		clear(b)
		return s, e
	}
	b, e := bufio.NewReader(io.LimitReader(os.Stdin, 4097)).ReadString('\n')
	if e != nil && e != io.EOF {
		return "", e
	}
	s := strings.TrimSuffix(strings.TrimSuffix(b, "\n"), "\r")
	if len(s) == 0 || len(s) > 4096 {
		return "", errors.New("invalid stdin key length")
	}
	return s, nil
}
func jobReport(ctx context.Context, st *store.Store, j domain.Job) (map[string]any, error) {
	r := map[string]any{"job_id": j.ID, "kind": j.Kind, "template_id": j.TemplateID, "state": j.State, "progress_chars": j.ProgressChars, "started_at": j.StartedAt, "finished_at": j.FinishedAt, "error_code": j.ErrorCode, "error_message": j.ErrorMessage}
	if j.Kind == "artifact" {
		arts, e := st.ListArtifactsSummary(ctx, j.ModelID)
		if e != nil {
			return nil, e
		}
		for _, a := range arts {
			if a.JobID == j.ID {
				r["artifact_id"] = a.ID
				r["requested_model"] = a.RequestedModel
				r["returned_model"] = a.ReturnedModel
				r["reasoning_effort"] = a.ReasoningEffort
				r["duration_ms"] = a.DurationMS
				r["first_text_ms"] = a.FirstTextMS
				r["usage"] = a.Usage
				r["parameters"] = a.Parameters
				r["group_status"] = a.GroupVerificationStatus
				break
			}
		}
	} else {
		rows, e := st.ListProbeResults(ctx, j.ModelID, time.Now().Add(-24*time.Hour))
		if e != nil {
			return nil, e
		}
		for _, p := range rows {
			if p.JobID == j.ID {
				r["http_status"] = p.HTTPStatus
				r["duration_ms"] = p.DurationMS
				r["first_text_ms"] = p.FirstTextMS
				r["returned_model"] = p.ReturnedModel
				r["usage"] = p.Usage
				break
			}
		}
	}
	return r, nil
}
func saveJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	tmp := path + ".tmp"
	if e = os.WriteFile(tmp, b, 0600); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}
func emit(path string, v any) error {
	if path != "" {
		if e := saveJSON(path, v); e != nil {
			return e
		}
	}
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	fmt.Println(string(b))
	return nil
}
