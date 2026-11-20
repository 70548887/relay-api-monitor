package worker

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/fingerprint"
	"relaylab/internal/upstream"
)

// fingerprintQuestionTimeout bounds the request for one question. A reply is about three hundred numbers, which a
// model writes in seconds, or in a minute or two when it reasons first.
const fingerprintQuestionTimeout = 3 * time.Minute

const fingerprintBankUnavailable = "指纹参考库不可用，请在后台「指纹匹对」更新参考库后重试"

// errReplyExceeds stops the request of a question whose reply already holds more numbers than asked.
var errReplyExceeds = errors.New("worker: the reply holds more numbers than asked")

// FingerprintBank is the part of the reference bank that detection rounds use. *fingerprint.Bank implements it.
type FingerprintBank interface {
	// Expected returns the bank ID of the reference model a channel model is compared with, or "" when the bank
	// lists no such model.
	Expected(ctx context.Context, source string, m domain.Model) (string, error)
	// Judge scores the answers of one round.
	Judge(ctx context.Context, source string, answers []fingerprint.Answer, expected string) (domain.FingerprintAnalysis, error)
}

// SetFingerprintBank gives the worker the reference bank that fingerprint jobs are scored against. Without one,
// those jobs fail before any request is sent.
func (w *Worker) SetFingerprintBank(bank FingerprintBank) { w.bank = bank }

// fingerprintRound is the plan of one detection round, made before any request is sent.
type fingerprintRound struct {
	// source is the reference bank setting the round was planned with.
	source     string
	expected   string
	challenges []domain.FingerprintChallenge
}

// prompt joins the questions of the round for the request snapshot of the job.
func (r fingerprintRound) prompt() string {
	prompts := make([]string, len(r.challenges))
	for i, challenge := range r.challenges {
		prompts[i] = challenge.Prompt
	}
	return strings.Join(prompts, "\n\n")
}

// planFingerprint prepares the round of a fingerprint job. Without a usable reference bank it returns no round but
// the error code and message of the job, so that no request is paid for answers that cannot be scored.
func (w *Worker) planFingerprint(ctx context.Context, m domain.Model, settings domain.Settings) (*fingerprintRound, string, string) {
	if w.bank == nil {
		return nil, "fingerprint_bank_unavailable", fingerprintBankUnavailable
	}
	expected, err := w.bank.Expected(ctx, settings.FingerprintBankDir, m)
	if err != nil {
		return nil, "fingerprint_bank_unavailable", fingerprintBankUnavailable
	}
	challenges, err := fingerprint.NewChallenges(fingerprint.Questions(m))
	if err != nil {
		return nil, "fingerprint_challenge_error", "指纹题目生成失败"
	}
	return &fingerprintRound{source: settings.FingerprintBankDir, expected: expected, challenges: challenges}, "", ""
}

// runFingerprint asks the questions of a dispatched round one after another, scores the replies and finishes the
// job. Each question is asked once. A question whose request fails does not stop the round: the round is scored on
// the replies that arrived, and the job fails only when none did. request is the request of the job without its
// prompt, and progress is the stream callback that tracks the received text of the job.
func (w *Worker) runFingerprint(ctx, requestCtx context.Context, j *domain.Job, lost *atomic.Bool, round fingerprintRound, request upstream.Request, progress func(upstream.Event) error) error {
	started := time.Now()
	canceled := func() error {
		j.State, j.ErrorCode, j.ErrorMessage = "canceled", "canceled", "任务已取消"
		return w.completeFingerprint(*j, nil)
	}
	failures := []domain.FingerprintRequestError{}
	answers := make([]fingerprint.Answer, len(round.challenges))
	answered := false
	for i, challenge := range round.challenges {
		var reply strings.Builder
		request.Prompt = challenge.Prompt
		response, err := w.client.Generate(requestCtx, request, func(event upstream.Event) error {
			if err := progress(event); err != nil {
				return err
			}
			if event.Type != "text" {
				return nil
			}
			reply.WriteString(event.Text)
			if fingerprint.Exceeds(reply.String(), challenge.ExpectedCount) {
				return errReplyExceeds
			}
			return nil
		})
		j.ReceivedBytes += response.ReceivedBytes
		j.TextBytes += response.TextBytes
		if i == 0 {
			j.FirstTextMS = response.FirstTextMS
		}
		if response.HTTPStatus != 0 {
			j.HTTPStatus = response.HTTPStatus
		}
		if response.FinishReason != "" {
			j.FinishReason = response.FinishReason
		}
		if response.ReturnedModel != "" {
			j.ReturnedModel = response.ReturnedModel
		}
		addUsage(&j.Usage, response.Usage)
		j.DurationMS = time.Since(started).Milliseconds()
		if lost.Load() {
			return ErrLeaseLost
		}
		if ctx.Err() != nil {
			return canceled()
		}
		answers[i] = fingerprint.NewAnswer(response.Text, challenge.ExpectedCount)
		answered = answered || strings.TrimSpace(response.Text) != ""
		switch {
		case errors.Is(err, errReplyExceeds):
		case err != nil:
			code, message := errorDetails(err)
			if code == "truncated" {
				message = truncatedMessage(j.Kind, request.MaxTokens)
			}
			failures = append(failures, domain.FingerprintRequestError{Question: i + 1, Code: code, Message: message})
		case !response.Complete:
			failures = append(failures, domain.FingerprintRequestError{Question: i + 1, Code: "incomplete_response", Message: "上游响应未完整结束"})
		}
	}
	if j.ProgressChars > 0 {
		n := time.Now().UTC()
		j.ProgressAt = &n
	}
	if !answered {
		j.State, j.ErrorCode, j.ErrorMessage = "failed", "upstream_error", "上游请求失败"
		if len(failures) > 0 {
			j.ErrorCode, j.ErrorMessage = failures[0].Code, failures[0].Message
		}
		return w.completeFingerprint(*j, nil)
	}
	analysis, err := w.bank.Judge(ctx, round.source, answers, round.expected)
	if ctx.Err() != nil {
		return canceled()
	}
	if err != nil {
		j.State, j.ErrorCode, j.ErrorMessage = "failed", "fingerprint_scoring_failed", "指纹评分失败"
		if errors.Is(err, fingerprint.ErrBankUnavailable) {
			j.ErrorCode, j.ErrorMessage = "fingerprint_bank_unavailable", fingerprintBankUnavailable
		}
		return w.completeFingerprint(*j, nil)
	}
	j.State = "succeeded"
	return w.completeFingerprint(*j, &domain.FingerprintResult{ModelID: j.ModelID, Questions: len(round.challenges), Analysis: analysis, Errors: failures,
		ReturnedModel: j.ReturnedModel, Usage: j.Usage, DurationMS: j.DurationMS, CreatedAt: time.Now().UTC()})
}

func (w *Worker) completeFingerprint(j domain.Job, round *domain.FingerprintResult) error {
	return w.finish(j, func(ctx context.Context) error { return w.st.FinishFingerprintJob(ctx, j, round) })
}

// addUsage adds the token counts that one response reported to a running total. A count that no response reported
// stays unknown.
func addUsage(total *domain.Usage, usage domain.Usage) {
	add := func(sum **int64, value *int64) {
		if value == nil {
			return
		}
		next := *value
		if *sum != nil {
			next += **sum
		}
		*sum = &next
	}
	add(&total.InputTokens, usage.InputTokens)
	add(&total.OutputTokens, usage.OutputTokens)
	add(&total.TotalTokens, usage.TotalTokens)
	add(&total.CachedInputTokens, usage.CachedInputTokens)
	add(&total.CacheCreationInputTokens, usage.CacheCreationInputTokens)
}
