package server

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"relaylab/internal/domain"
	"relaylab/internal/fingerprint"
)

// FingerprintBank is what the handlers need from the fingerprint reference bank. source is the configured local
// bank directory, or empty for the online bank.
type FingerprintBank interface {
	Catalog(ctx context.Context, source string) (domain.FingerprintBank, []domain.FingerprintModel)
	Judge(ctx context.Context, source string, answers []fingerprint.Answer, expected string) (domain.FingerprintAnalysis, error)
}

const (
	// A full answer is about 300 short numbers; the limit leaves room for the prose some models add around them.
	fingerprintAnswerBytes = 32 << 10
	// The page must not wait for a first download of the bank, which continues in the background.
	fingerprintPageWait  = 3 * time.Second
	fingerprintJudgeWait = 20 * time.Second
)

// acceptFingerprintSettings validates the fingerprint settings of a settings update. A zero is a setting that a
// client older than the fingerprint feature did not send; it takes the default.
func acceptFingerprintSettings(c *gin.Context, b *domain.Settings) bool {
	normalized := domain.NormalizeFingerprintSettings(*b)
	for _, pair := range [][2]int{
		{b.FingerprintConcurrency, normalized.FingerprintConcurrency}, {b.FingerprintIntervalSeconds, normalized.FingerprintIntervalSeconds},
		{b.FingerprintManualPerMinute, normalized.FingerprintManualPerMinute}, {b.FingerprintAPIPerHour, normalized.FingerprintAPIPerHour},
	} {
		if pair[0] != 0 && pair[0] != pair[1] {
			fail(c, 400, "invalid_settings", "设置超出允许范围")
			return false
		}
	}
	dir := strings.TrimSpace(b.FingerprintBankDir)
	if dir != "" && (!filepath.IsAbs(dir) || len(dir) > 1024 || strings.ContainsAny(dir, "\r\n\x00")) {
		fail(c, 400, "invalid_settings", "参考库本地目录须为绝对路径")
		return false
	}
	*b = normalized
	b.FingerprintBankDir = dir
	return true
}

func (s *Server) fingerprintOverview(c *gin.Context) {
	settings, err := s.Store.GetSettings(c.Request.Context())
	if err != nil {
		dbError(c, err)
		return
	}
	out := domain.FingerprintOverview{Bank: domain.FingerprintBank{Source: "online"}, Models: []domain.FingerprintModel{}}
	if s.Fingerprint != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), fingerprintPageWait)
		defer cancel()
		bank, models := s.Fingerprint.Catalog(ctx, settings.FingerprintBankDir)
		// The error names local paths and download hosts, which only administrators need.
		bank.Error = ""
		out.Bank = bank
		if models != nil {
			out.Models = models
		}
	}
	c.JSON(200, out)
}

func (s *Server) fingerprintChallenges(c *gin.Context) {
	count := fingerprint.MaxChallenges
	if raw := c.Query("count"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			value = 0
		}
		count = value
	}
	challenges, err := fingerprint.NewChallenges(count)
	if err != nil {
		fail(c, 400, "invalid_count", "题数须为 1 到 3")
		return
	}
	c.JSON(200, domain.FingerprintChallengeSet{Challenges: challenges})
}

// fingerprintAnalyze scores pasted answers for a visitor. Nothing is stored. Admission precedes decoding, so
// malformed bodies consume the same rate budget as valid ones.
func (s *Server) fingerprintAnalyze(c *gin.Context) {
	settings, err := s.Store.GetSettings(c.Request.Context())
	if err != nil {
		dbError(c, err)
		return
	}
	if !s.allowedRate("fingerprint-manual:"+c.ClientIP(), max(settings.FingerprintManualPerMinute, 1)) {
		c.Header("Retry-After", "60")
		fail(c, 429, "rate_limited", "请求过于频繁，请一分钟后重试")
		return
	}
	var b domain.FingerprintAnalyzeRequest
	if !bind(c, &b) {
		return
	}
	if len(b.Answers) < 1 || len(b.Answers) > fingerprint.MaxChallenges {
		fail(c, 400, "invalid_answers", "请提交 1 到 3 个回答")
		return
	}
	answers := make([]fingerprint.Answer, len(b.Answers))
	for i, answer := range b.Answers {
		if answer.ExpectedCount < fingerprint.MinExpectedCount || answer.ExpectedCount > fingerprint.MaxExpectedCount {
			fail(c, 400, "invalid_answers", "回答对应的题目不正确，请重新取题")
			return
		}
		if len(answer.Text) > fingerprintAnswerBytes {
			fail(c, 400, "answer_too_long", "单个回答不能超过 32 KB")
			return
		}
		answers[i] = fingerprint.Answer{Text: answer.Text, ExpectedCount: answer.ExpectedCount}
	}
	expected := strings.TrimSpace(b.ExpectedModel)
	if len(expected) > 200 {
		fail(c, 400, "invalid_model", "期望模型不正确")
		return
	}
	if s.Fingerprint == nil {
		fingerprintUnavailable(c)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), fingerprintJudgeWait)
	defer cancel()
	analysis, err := s.Fingerprint.Judge(ctx, settings.FingerprintBankDir, answers, expected)
	switch {
	case errors.Is(err, fingerprint.ErrBankUnavailable):
		fingerprintUnavailable(c)
	case err != nil:
		fail(c, 500, "fingerprint_failed", "评分失败，请稍后重试")
	default:
		c.JSON(200, analysis)
	}
}

func fingerprintUnavailable(c *gin.Context) {
	c.Header("Retry-After", "30")
	fail(c, 503, "fingerprint_unavailable", "参考库暂不可用，请稍后重试")
}
