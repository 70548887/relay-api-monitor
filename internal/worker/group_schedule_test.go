package worker

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func TestGroupScheduleRunsWithLegacyModelFlagsDisabled(t *testing.T) {
	calls := 0
	w, st, ctx, c, m := workerFixture(t, func(rw http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) != 1 || body.Messages[0].Content != domain.EnglishPrompt {
			t.Error("selected template was not sent verbatim", err)
		}
		chatResponse(rw, `<svg xmlns="http://www.w3.org/2000/svg"><text>pelican</text></svg>`)
	})
	if m.ArtifactChinese || m.ArtifactEnglish {
		t.Fatal("fixture must have legacy schedules disabled")
	}
	if _, err := st.SetIntelligenceSchedule(ctx, c.ID, domain.IntelligenceSchedule{Enabled: true, ModelID: m.ID, TemplateIDs: []string{"en_svg"}, IntervalSeconds: 1800}); err != nil {
		t.Fatal(err)
	}
	if count, err := st.TickSchedules(ctx, time.Now().UTC().Add(time.Hour)); err != nil || count != 1 {
		t.Fatalf("scheduled count=%d err=%v", count, err)
	}
	if worked, err := w.WorkOne(ctx, "artifact"); err != nil || !worked {
		t.Fatalf("execute worked=%v err=%v", worked, err)
	}
	jobs, err := st.ListJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].State != "succeeded" || calls != 1 {
		t.Fatalf("jobs=%+v calls=%d err=%v", jobs, calls, err)
	}
	arts, err := st.ListArtifacts(ctx, m.ID)
	if err != nil || len(arts) != 1 || arts[0].TemplateID != "en_svg" || arts[0].ModelID != m.ID {
		t.Fatalf("artifacts=%+v err=%v", arts, err)
	}
}
