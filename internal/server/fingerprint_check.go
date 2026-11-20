package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"relaylab/internal/domain"
	"relaylab/internal/fingerprint"
	"relaylab/internal/upstream"
)

// fingerprintCheckWait bounds each request of a round that a member runs through their own connection. A reply is
// about three hundred numbers, which a model writes in seconds, or in a minute or two when it reasons first.
const fingerprintCheckWait = 3 * time.Minute

// fingerprintCheck runs one detection round against a connection the member saved and returns its outcome, which is
// not stored. The questions are asked at the same time, and the round is scored on the replies that arrived.
//
// A round counts against the hourly budget of the member once it passes the checks and its questions are asked,
// whether or not the connection answers them; a request refused before that costs nothing.
func (s *Server) fingerprintCheck(c *gin.Context) {
	settings, release, ok := s.visitorAdmission(c, false)
	if !ok {
		return
	}
	defer release()
	var b domain.FingerprintCheckRequest
	if !bind(c, &b) {
		return
	}
	b.Model, b.Mode, b.ExpectedModel = strings.TrimSpace(b.Model), strings.TrimSpace(b.Mode), strings.TrimSpace(b.ExpectedModel)
	if b.Mode == "" {
		b.Mode = domain.FingerprintModeAuto
	}
	if b.Questions == 0 {
		b.Questions = fingerprint.MaxChallenges
	}
	if b.ConnectionID == "" {
		fail(c, 400, "connection_required", "请选择一个已保存的连接")
		return
	}
	if b.Model == "" || len(b.Model) > 256 {
		fail(c, 400, "invalid_model", "请填写要检测的模型")
		return
	}
	if (b.Mode != domain.FingerprintModeAuto && b.Mode != domain.FingerprintModeManual && b.Mode != domain.FingerprintModeUnlisted) || b.Questions < 1 || b.Questions > fingerprint.MaxChallenges {
		fail(c, 400, "invalid_fingerprint_settings", "期望模型的方式或题数不正确")
		return
	}
	if b.Mode != domain.FingerprintModeManual {
		b.ExpectedModel = ""
	} else if !s.acceptFingerprintReference(c, b.ExpectedModel) {
		return
	}
	connection := visitorRequest{ConnectionID: b.ConnectionID, ConnectionRevision: b.ConnectionRevision}
	defer func() { connection.APIKey = "" }()
	if _, ok := s.resolveTestConnection(c, &connection); !ok {
		return
	}
	if !safeProtocol(connection.Protocol) {
		fail(c, 400, "invalid_protocol", "连接的生成协议不正确")
		return
	}
	if !validateVisitor(c, connection) {
		return
	}
	if s.Fingerprint == nil {
		fingerprintUnavailable(c)
		return
	}
	planCtx, cancel := context.WithTimeout(c.Request.Context(), fingerprintJudgeWait)
	expected, err := s.Fingerprint.Expected(planCtx, settings.FingerprintBankDir, domain.Model{UpstreamID: b.Model, FingerprintMode: b.Mode, FingerprintModel: b.ExpectedModel})
	cancel()
	if err != nil {
		// Without a reference bank the replies could not be scored, so none is paid for.
		fingerprintUnavailable(c)
		return
	}
	challenges, err := fingerprint.NewChallenges(b.Questions)
	if err != nil {
		fail(c, 500, "fingerprint_failed", "题目生成失败，请稍后重试")
		return
	}
	if allowed, wait := s.allowedWithin("fingerprint-api:"+memberID(c), max(settings.FingerprintAPIPerHour, 1), time.Hour); !allowed {
		c.Header("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		fail(c, 429, "rate_limited", fmt.Sprintf("本小时的检测次数已用完，请 %d 分钟后再试", int(math.Ceil(wait.Minutes()))))
		return
	}

	maxTokens := domain.DefaultArtifactMaxTokens(connection.Protocol)
	request := upstream.Request{Config: connection.config(), Model: b.Model, MaxTokens: maxTokens, Timeout: fingerprintCheckWait}
	replies := make([]struct {
		response upstream.Result
		answer   fingerprint.Answer
		err      error
	}, len(challenges))
	started := time.Now()
	var pending sync.WaitGroup
	for i, challenge := range challenges {
		pending.Go(func() {
			replies[i].response, replies[i].answer, replies[i].err = fingerprint.Ask(c.Request.Context(), s.Client, request, challenge, nil)
		})
	}
	pending.Wait()
	if c.Request.Context().Err() != nil {
		return
	}

	check := domain.FingerprintCheck{Questions: len(challenges), Errors: []domain.FingerprintRequestError{}, QuestionMS: make([]int64, len(challenges)), ExpectedCounts: make([]int, len(challenges)), DurationMS: time.Since(started).Milliseconds(), CreatedAt: time.Now().UTC()}
	answers := make([]fingerprint.Answer, len(challenges))
	for i, reply := range replies {
		answers[i], check.QuestionMS[i], check.ExpectedCounts[i] = reply.answer, reply.response.DurationMS, challenges[i].ExpectedCount
		check.Usage.Add(reply.response.Usage)
		if check.ReturnedModel == "" {
			check.ReturnedModel = redactPrivate(reply.response.ReturnedModel, connection.APIKey)
		}
		if reply.err != nil {
			code, message := fingerprintFailure(reply.err, connection.APIKey, maxTokens)
			check.Errors = append(check.Errors, domain.FingerprintRequestError{Question: i + 1, Code: code, Message: message})
		}
	}
	judgeCtx, cancel := context.WithTimeout(c.Request.Context(), fingerprintJudgeWait)
	defer cancel()
	check.Analysis, err = s.Fingerprint.Judge(judgeCtx, settings.FingerprintBankDir, answers, expected)
	switch {
	case errors.Is(err, fingerprint.ErrBankUnavailable):
		fingerprintUnavailable(c)
	case err != nil:
		fail(c, 500, "fingerprint_failed", "评分失败，请稍后重试")
	default:
		c.JSON(200, check)
	}
}

// fingerprintFailure describes the failed request of one question to the member who owns the connection: its code
// and the reason the connection gave, without the key.
func fingerprintFailure(err error, key string, maxTokens int) (string, string) {
	code := "upstream_error"
	var upstreamErr *upstream.Error
	if errors.As(err, &upstreamErr) && upstreamErr.Code != "" {
		code = upstreamErr.Code
	}
	if code == "truncated" {
		return code, fmt.Sprintf("上游输出达到本次 %d Tokens 上限后被截断；模型可能先思考了较长时间", maxTokens)
	}
	return code, redactPrivate(publicError(err), key)
}
