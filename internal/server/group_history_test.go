package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func TestBusyManualJobNamesTheBlockingJob(t *testing.T) {
	s, _, _, m := publicFixture(t)
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	body := map[string]string{"model_id": m.ID, "kind": "artifact", "template_id": "cn_html"}
	w := request(t, h, "POST", "/api/v1/admin/jobs", body, cookie, csrf)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var first domain.Job
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil || len(first.ID) < 8 {
		t.Fatal(err, w.Body.String())
	}
	// A second run of the same kind is fenced; the answer must say by what.
	body["template_id"] = "en_svg"
	w = request(t, h, "POST", "/api/v1/admin/jobs", body, cookie, csrf)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	var failure struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
		t.Fatal(err, w.Body.String())
	}
	if failure.Error.Code != "model_busy" || !strings.Contains(failure.Error.Message, first.ID[:8]) || !strings.Contains(failure.Error.Message, "中文 HTML") || !strings.Contains(failure.Error.Message, "正在排队") {
		t.Fatalf("unhelpful conflict: %+v", failure.Error)
	}
	if jobs, err := s.Store.ListJobs(context.Background()); err != nil || len(jobs) != 1 {
		t.Fatal(err, jobs)
	}
}

func TestAdminJobsCanBeFilteredByGroup(t *testing.T) {
	s, p, c, m := publicFixture(t)
	ctx := context.Background()
	other := domain.Channel{ID: "other-channel", PlatformID: p.ID, Name: "Other Channel", Public: true, Enabled: true, Protocol: "chat", EncryptedKey: "encrypted-key-private", BaseURL: "https://internal-upstream-private.example/v1"}
	if err := s.Store.SaveChannel(ctx, other); err != nil {
		t.Fatal(err)
	}
	otherModel := domain.Model{ID: "other-model", ChannelID: other.ID, UpstreamID: "other-model-name", DisplayName: "Other model"}
	if err := s.Store.SaveModel(ctx, otherModel); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	for _, id := range []string{m.ID, otherModel.ID} {
		if w := request(t, h, "POST", "/api/v1/admin/jobs", map[string]string{"model_id": id, "kind": "probe"}, cookie, csrf); w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	list := func(path string) []domain.Job {
		t.Helper()
		w := request(t, h, "GET", path, nil, cookie, csrf)
		var jobs []domain.Job
		if err := json.Unmarshal(w.Body.Bytes(), &jobs); w.Code != 200 || err != nil {
			t.Fatal(w.Code, err, w.Body.String())
		}
		return jobs
	}
	if all := list("/api/v1/admin/jobs?limit=100"); len(all) != 2 {
		t.Fatalf("unfiltered jobs = %d", len(all))
	}
	for _, channel := range []domain.Channel{c, other} {
		jobs := list("/api/v1/admin/jobs?limit=100&channel_id=" + channel.ID)
		if len(jobs) != 1 || jobs[0].ChannelID != channel.ID {
			t.Fatalf("jobs for %s: %+v", channel.ID, jobs)
		}
	}
	w := request(t, h, "GET", "/api/v1/admin/artifacts?channel_id="+c.ID, nil, cookie, csrf)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestAdminGroupArtifactsCanBrowseBeyondFirstHundred(t *testing.T) {
	s, p, c, m := publicFixture(t)
	ctx := context.Background()
	other := domain.Channel{ID: "other-artifact-channel", PlatformID: p.ID, Name: "Other", Enabled: true}
	if err := s.Store.SaveChannel(ctx, other); err != nil {
		t.Fatal(err)
	}
	otherModel := domain.Model{ID: "other-artifact-model", ChannelID: other.ID, UpstreamID: "other-model"}
	if err := s.Store.SaveModel(ctx, otherModel); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 108; i++ {
		modelID, channelID := m.ID, c.ID
		if i == 107 {
			modelID, channelID = otherModel.ID, other.ID
		}
		a := domain.Artifact{ID: fmt.Sprintf("work-%03d", i), JobID: fmt.Sprintf("job-%03d", i), ModelID: modelID, ChannelID: channelID, TemplateID: "cn_html", Code: "private source", RawResponse: "private response", Hidden: i%10 == 0, CreatedAt: base.Add(time.Duration(i) * time.Second)}
		encoded, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Store.DB.ExecContext(ctx, "INSERT INTO artifacts(id,job_id,model_id,template_id,created_at,hidden,data) VALUES(?,?,?,?,?,?,?)", a.ID, a.JobID, a.ModelID, a.TemplateID, a.CreatedAt.Format(time.RFC3339Nano), a.Hidden, string(encoded)); err != nil {
			t.Fatal(err)
		}
	}
	h := s.Handler()
	cookie, csrf := loginForTest(t, h)
	getPage := func(page int) struct {
		Items    []domain.Artifact `json:"items"`
		Total    int               `json:"total"`
		Page     int               `json:"page"`
		PageSize int               `json:"page_size"`
	} {
		t.Helper()
		w := request(t, h, "GET", fmt.Sprintf("/api/v1/admin/artifacts?channel_id=%s&page=%d", c.ID, page), nil, cookie, csrf)
		var result struct {
			Items    []domain.Artifact `json:"items"`
			Total    int               `json:"total"`
			Page     int               `json:"page"`
			PageSize int               `json:"page_size"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); w.Code != 200 || err != nil {
			t.Fatal(w.Code, err, w.Body.String())
		}
		return result
	}
	first, last := getPage(1), getPage(5)
	if first.Total != 107 || first.Page != 1 || first.PageSize != 25 || len(first.Items) != 25 || first.Items[0].ID != "work-106" || last.Total != 107 || last.Page != 5 || len(last.Items) != 7 || last.Items[0].ID != "work-006" || last.Items[6].ID != "work-000" {
		t.Fatalf("incorrect grouped pages: first=%+v last=%+v", first, last)
	}
	if first.Items[0].Code != "" || first.Items[0].RawResponse != "" || !last.Items[6].Hidden {
		t.Fatal("page exposed source or omitted retained hidden work")
	}
	if out := getPage(6); out.Total != 107 || len(out.Items) != 0 {
		t.Fatalf("page beyond end: %+v", out)
	}
	w := request(t, h, "GET", "/api/v1/admin/artifacts?channel_id="+c.ID+"&page=0", nil, cookie, csrf)
	if w.Code != 400 {
		t.Fatal("invalid page accepted", w.Code, w.Body.String())
	}
}
