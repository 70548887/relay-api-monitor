package server

import (
	"github.com/gin-gonic/gin"
	"relaylab/internal/domain"
	"relaylab/internal/fingerprint"
)

// fingerprintRoundsByModel groups stored rounds by model, keeping the order the store lists them in.
func fingerprintRoundsByModel(rounds []domain.FingerprintResult) map[string][]domain.FingerprintResult {
	out := map[string][]domain.FingerprintResult{}
	for _, round := range rounds {
		out[round.ModelID] = append(out[round.ModelID], round)
	}
	return out
}

// visitorFingerprintRound is a stored round as visitors see it. What an upstream says when a request fails can name
// hosts and accounts, so a failure keeps its code and loses its message, as a probe does on the monitor page. The
// job of the round is of no use to a visitor.
func visitorFingerprintRound(round domain.FingerprintResult) domain.FingerprintResult {
	round.JobID = ""
	failures := make([]domain.FingerprintRequestError, len(round.Errors))
	for i, failure := range round.Errors {
		failures[i] = domain.FingerprintRequestError{Question: failure.Question, Code: failure.Code, Message: "上游请求失败"}
	}
	round.Errors = failures
	return round
}

// fingerprintResults lists every public model with the conclusion drawn from its newest rounds.
func (s *Server) fingerprintResults(c *gin.Context) {
	ctx := c.Request.Context()
	catalog, err := s.publicCatalog(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	rounds, err := s.Store.ListLatestFingerprintResults(ctx, fingerprint.SummaryRounds)
	if err != nil {
		dbError(c, err)
		return
	}
	recent := fingerprintRoundsByModel(rounds)
	channels := map[string][]domain.Channel{}
	for _, ch := range catalog.channels {
		channels[ch.PlatformID] = append(channels[ch.PlatformID], ch)
	}
	models := map[string][]domain.Model{}
	for _, m := range catalog.models {
		models[m.ChannelID] = append(models[m.ChannelID], m)
	}
	out := domain.FingerprintPublicResults{Models: []domain.FingerprintPublicModel{}}
	for _, p := range catalog.platforms {
		for _, ch := range channels[p.ID] {
			for _, m := range models[ch.ID] {
				row := domain.FingerprintPublicModel{ID: m.ID, UpstreamID: m.UpstreamID, DisplayName: m.DisplayName, Protocol: modelProtocol(m, ch),
					ChannelID: ch.ID, ChannelName: ch.Name, PlatformID: p.ID, PlatformName: p.Name}
				if stored := recent[m.ID]; len(stored) > 0 {
					row.Summary, row.CheckedAt = fingerprint.Summarize(stored), &stored[0].CreatedAt
					for _, round := range stored {
						if round.ID == row.Summary.RoundID {
							round = visitorFingerprintRound(round)
							row.Round = &round
						}
					}
				}
				out.Models = append(out.Models, row)
			}
		}
	}
	c.JSON(200, out)
}

// fingerprintModelResults lists the stored rounds of one public model, newest first.
func (s *Server) fingerprintModelResults(c *gin.Context) {
	ctx := c.Request.Context()
	catalog, err := s.publicCatalog(ctx)
	if err != nil {
		dbError(c, err)
		return
	}
	m, ok := catalog.modelByID[c.Param("id")]
	if !ok {
		fail(c, 404, "not_found", "模型不存在")
		return
	}
	rounds, err := s.Store.ListFingerprintResults(ctx, m.ID)
	if err != nil {
		dbError(c, err)
		return
	}
	out := make([]domain.FingerprintResult, len(rounds))
	for i, round := range rounds {
		out[i] = visitorFingerprintRound(round)
	}
	c.JSON(200, out)
}
