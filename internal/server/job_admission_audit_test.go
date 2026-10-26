package server

import (
	"context"
	"testing"
)

func TestManualJobUnavailableCatalogTargetRejectedBeforeEnqueue(t *testing.T) {
	for _, scenario := range []string{"archived-platform", "missing-model"} {
		t.Run(scenario, func(t *testing.T) {
			s, p, _, m := publicFixture(t)
			h := s.Handler()
			cookie, csrf := loginForTest(t, h)
			if scenario == "archived-platform" {
				p.Archived = true
				if err := s.Store.SavePlatform(context.Background(), p); err != nil {
					t.Fatal(err)
				}
			} else {
				m.Missing = true
				if err := s.Store.SaveModel(context.Background(), m); err != nil {
					t.Fatal(err)
				}
			}
			w := request(t, h, "POST", "/api/v1/admin/jobs", map[string]string{"model_id": m.ID, "kind": "artifact", "template_id": "cn_html"}, cookie, csrf)
			if w.Code != 400 {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			jobs, err := s.Store.ListJobs(context.Background())
			if err != nil || len(jobs) != 0 {
				t.Fatalf("invalid task persisted: %v", err)
			}
		})
	}
}
