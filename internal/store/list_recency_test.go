package store

import (
	"reflect"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func TestAccountListsShowNewAccountsBeforeLegacyUnknownDates(t *testing.T) {
	s, ctx, _, _ := fixture(t)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	adminIDs := make(map[string]string)
	memberIDs := make(map[string]string)
	for _, name := range []string{"z-older", "a-newer", "0-legacy"} {
		admin, err := s.CreateAdmin(ctx, name, "test-hash")
		if err != nil {
			t.Fatal(err)
		}
		member, err := s.CreateMember(ctx, name, "test-hash")
		if err != nil {
			t.Fatal(err)
		}
		adminIDs[name], memberIDs[name] = admin.ID, member.ID
		created := ts(base)
		if name == "a-newer" {
			created = ts(base.Add(time.Hour))
		} else if name == "0-legacy" {
			created = "" // Existing migrated admin accounts can have unknown creation dates.
		}
		if _, err = s.DB.ExecContext(ctx, "UPDATE admins SET created_at=? WHERE id=?", created, admin.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = s.DB.ExecContext(ctx, "UPDATE users SET created_at=? WHERE id=?", created, member.ID); err != nil {
			t.Fatal(err)
		}
	}
	wantAdmins := []string{adminIDs["a-newer"], adminIDs["z-older"], adminIDs["0-legacy"]}
	wantMembers := []string{memberIDs["a-newer"], memberIDs["z-older"], memberIDs["0-legacy"]}
	admins, err := s.ListAdmins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotAdmins := make([]string, 0, len(admins))
	for _, account := range admins {
		gotAdmins = append(gotAdmins, account.ID)
	}
	if !reflect.DeepEqual(gotAdmins, wantAdmins) {
		t.Fatalf("admin recency = %v, want %v", gotAdmins, wantAdmins)
	}
	members, err := s.ListMembers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotMembers := make([]string, 0, len(members))
	for _, account := range members {
		gotMembers = append(gotMembers, account.ID)
	}
	if !reflect.DeepEqual(gotMembers, wantMembers) {
		t.Fatalf("member recency = %v, want %v", gotMembers, wantMembers)
	}
}

func TestPrivateConnectionsShowPreciseRecencyAndStableTiesWithinOwner(t *testing.T) {
	s, ctx, _, _ := fixture(t)
	first, err := s.CreateMember(ctx, "first-owner", "test-hash")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateMember(ctx, "second-owner", "test-hash")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entries := []struct {
		id, owner string
		created   time.Time
	}{
		{"old-whole", first.ID, base},
		{"fraction-1", first.ID, base.Add(100 * time.Millisecond)},
		{"fraction-11", first.ID, base.Add(110 * time.Millisecond)},
		{"a-tie", first.ID, base.Add(110 * time.Millisecond)},
		{"z-tie", first.ID, base.Add(110 * time.Millisecond)},
		{"other-new", second.ID, base.Add(200 * time.Millisecond)},
	}
	for _, entry := range entries {
		connection := domain.UserConnection{ID: entry.id, UserID: entry.owner, Name: entry.id, EncryptedKey: "test-cipher", CredentialFingerprint: entry.id, Revision: 1, CreatedAt: entry.created, UpdatedAt: entry.created}
		if err := s.SaveUserConnection(ctx, connection); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(owner string) []string {
		t.Helper()
		connections, err := s.ListUserConnections(ctx, owner)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(connections))
		for _, connection := range connections {
			out = append(out, connection.ID)
		}
		return out
	}
	wantFirst := []string{"z-tie", "fraction-11", "a-tie", "fraction-1", "old-whole"}
	if got := ids(first.ID); !reflect.DeepEqual(got, wantFirst) {
		t.Fatalf("owner connections = %v, want %v", got, wantFirst)
	}
	wantAll := append([]string{"other-new"}, wantFirst...)
	if got := ids(""); !reflect.DeepEqual(got, wantAll) {
		t.Fatalf("admin connections = %v, want %v", got, wantAll)
	}
	if got := ids(second.ID); !reflect.DeepEqual(got, []string{"other-new"}) {
		t.Fatalf("other owner's connections = %v", got)
	}
}

func TestProbeAndArtworkListsAgreeOnNewerFirstAndStableTies(t *testing.T) {
	s, ctx, channel, model := fixture(t)
	other := model
	other.ID, other.UpstreamID = "sibling-model", "sibling-upstream"
	if err := s.SaveModel(ctx, other); err != nil {
		t.Fatal(err)
	}
	foreignChannel := channel
	foreignChannel.ID = "foreign-channel"
	if err := s.SaveChannel(ctx, foreignChannel); err != nil {
		t.Fatal(err)
	}
	foreign := model
	foreign.ID, foreign.ChannelID, foreign.UpstreamID = "foreign-model", foreignChannel.ID, "foreign-upstream"
	if err := s.SaveModel(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entries := []struct {
		id    string
		model domain.Model
		at    time.Time
	}{
		{"a", model, base},
		{"z", model, base},
		{"middle", other, base},
		{"new", foreign, base.Add(time.Second)},
	}
	for _, entry := range entries {
		probe := domain.ProbeResult{ID: "probe-" + entry.id, JobID: "probe-job-" + entry.id, ModelID: entry.model.ID, Status: "ok", CreatedAt: entry.at}
		probeJSON, err := data(probe)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.DB.ExecContext(ctx, "INSERT INTO probe_results(id,job_id,model_id,created_at,data) VALUES(?,?,?,?,?)", probe.ID, probe.JobID, probe.ModelID, ts(probe.CreatedAt), probeJSON); err != nil {
			t.Fatal(err)
		}
		art := domain.Artifact{ID: "art-" + entry.id, JobID: "art-job-" + entry.id, ModelID: entry.model.ID, ChannelID: entry.model.ChannelID, TemplateID: "cn_html", Code: "private source", RawResponse: "private response", CreatedAt: entry.at}
		artJSON, err := data(art)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.DB.ExecContext(ctx, "INSERT INTO artifacts(id,job_id,model_id,template_id,created_at,data) VALUES(?,?,?,?,?,?)", art.ID, art.JobID, art.ModelID, art.TemplateID, ts(art.CreatedAt), artJSON); err != nil {
			t.Fatal(err)
		}
	}
	probeIDs := func(results []domain.ProbeResult) []string {
		out := make([]string, 0, len(results))
		for _, result := range results {
			out = append(out, result.ID)
		}
		return out
	}
	artIDs := func(results []domain.Artifact) []string {
		out := make([]string, 0, len(results))
		for _, result := range results {
			out = append(out, result.ID)
		}
		return out
	}
	checkProbes := func(label string, got []domain.ProbeResult, err error, want []string) {
		t.Helper()
		if err != nil || !reflect.DeepEqual(probeIDs(got), want) {
			t.Fatalf("%s = %v, want %v: %v", label, probeIDs(got), want, err)
		}
	}
	checkArt := func(label string, got []domain.Artifact, err error, want []string) {
		t.Helper()
		if err != nil || !reflect.DeepEqual(artIDs(got), want) {
			t.Fatalf("%s = %v, want %v: %v", label, artIDs(got), want, err)
		}
	}
	probes, err := s.ListAllProbeResults(ctx, time.Time{})
	checkProbes("all probes", probes, err, []string{"probe-new", "probe-z", "probe-middle", "probe-a"})
	probes, err = s.ListProbeResults(ctx, model.ID, time.Time{})
	checkProbes("one model probes", probes, err, []string{"probe-z", "probe-a"})
	probes, err = s.ListProbeResultsLimit(ctx, "", time.Time{}, 2)
	checkProbes("limited probes", probes, err, []string{"probe-new", "probe-z"})
	probes, err = s.ListProbeResultsLimit(ctx, model.ID, time.Time{}, 1)
	checkProbes("limited model probes", probes, err, []string{"probe-z"})
	arts, err := s.ListArtifacts(ctx, "")
	checkArt("all art", arts, err, []string{"art-new", "art-z", "art-middle", "art-a"})
	arts, err = s.ListArtifacts(ctx, model.ID)
	checkArt("one model art", arts, err, []string{"art-z", "art-a"})
	arts, err = s.ListArtifactsSummary(ctx, "")
	checkArt("all art summaries", arts, err, []string{"art-new", "art-z", "art-middle", "art-a"})
	arts, err = s.ListArtifactsSummary(ctx, model.ID)
	checkArt("model summary", arts, err, []string{"art-z", "art-a"})
	if arts[0].Code != "" || arts[0].RawResponse != "" {
		t.Fatal("summary exposed source or raw response")
	}
	arts, err = s.ListChannelArtifactsSummary(ctx, channel.ID, "cn_html", 5)
	checkArt("group summary", arts, err, []string{"art-z", "art-middle", "art-a"})
	arts, total, err := s.ListChannelArtifactsSummaryPage(ctx, channel.ID, 1, 0)
	if total != 3 {
		t.Fatalf("group page total = %d, want 3", total)
	}
	checkArt("first group page", arts, err, []string{"art-z"})
	arts, total, err = s.ListChannelArtifactsSummaryPage(ctx, channel.ID, 1, 1)
	if total != 3 {
		t.Fatalf("group page total = %d, want 3", total)
	}
	checkArt("second group page", arts, err, []string{"art-middle"})
	arts, total, err = s.ListChannelArtifactsSummaryPage(ctx, channel.ID, 1, 2)
	if total != 3 {
		t.Fatalf("group page total = %d, want 3", total)
	}
	checkArt("last group page", arts, err, []string{"art-a"})
}
