package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/security"
	"relaylab/internal/store"
)

// Explicitly opt-in and refuse ordinary data directories. This fixture is never
// included in the application binary and never calls an upstream service.
func TestSeedBrowserPreviewFixture(t *testing.T) {
	dir := os.Getenv("RELAYLAB_BROWSER_FIXTURE_DIR")
	if dir == "" {
		t.Skip("isolated browser fixture not requested")
	}
	if !strings.HasPrefix(filepath.Base(dir), "relaylab-browser-") {
		t.Fatal("refusing to seed non-test directory")
	}
	st, e := store.OpenExisting(filepath.Join(dir, "relaylab.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	v, e := security.LoadVault(dir, true)
	if e != nil {
		t.Fatal(e)
	}
	cipher, e := v.Encrypt("test-fixture-no-network")
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	p := domain.Platform{ID: domain.NewID(), Name: "浏览器测试夹具", Public: true, CreatedAt: now}
	if e = st.SavePlatform(ctx, p); e != nil {
		t.Fatal(e)
	}
	ch := domain.Channel{ID: domain.NewID(), PlatformID: p.ID, Name: "无上游调用", BaseURL: "https://example.com/v1", EncryptedKey: cipher, Protocol: "chat", Enabled: true, Public: true, Revision: 1, CreatedAt: now}
	if e = st.SaveChannel(ctx, ch); e != nil {
		t.Fatal(e)
	}
	m := domain.Model{ID: domain.NewID(), ChannelID: ch.ID, UpstreamID: "browser-fixture-only", DisplayName: "预览隔离测试", Public: true, CreatedAt: now}
	if e = st.SaveModel(ctx, m); e != nil {
		t.Fatal(e)
	}
	j, e := st.EnqueueJob(ctx, domain.Job{ID: domain.NewID(), ModelID: m.ID, ChannelID: ch.ID, Kind: "artifact", TemplateID: "cn_html", ChannelRevision: 1, CreatedAt: now, ScheduledAt: now})
	if e != nil {
		t.Fatal(e)
	}
	claim, e := st.ClaimJobForChannels(ctx, "artifact", "browser-fixture", now, []string{ch.ID})
	if e != nil || claim == nil || claim.ID != j.ID {
		t.Fatal(e)
	}
	if e = st.MarkDispatching(ctx, claim.ID, claim.LeaseToken, now.Add(time.Minute), `{"fixture":true}`); e != nil {
		t.Fatal(e)
	}
	claim.State = "succeeded"
	claim.FinishedAt = &now
	code := `<!doctype html><html><head><style>@keyframes spin{to{transform:rotate(360deg)}}svg{width:100%;max-width:500px}circle{transform-origin:100px 100px;animation:spin 2s linear infinite}</style></head><body><h2>浏览器测试夹具 · 无上游调用</h2><svg viewBox="0 0 200 160" xmlns="http://www.w3.org/2000/svg"><circle cx="65" cy="115" r="30" fill="none" stroke="#168878" stroke-width="5"/><circle cx="145" cy="115" r="30" fill="none" stroke="#168878" stroke-width="5"/><path d="M65 115L95 70L120 115H65L110 70L145 115M83 60h32" fill="none" stroke="#21434a" stroke-width="6"/><path d="M80 60Q60 20 105 20Q145 23 132 50L112 43L102 65Z" fill="#e1ad5d"/></svg><script>document.body.dataset.relayPreview='ready';document.body.dataset.parentAccess='blocked';try{parent.document.body.dataset.leaked='yes';document.body.dataset.parentAccess='allowed'}catch(e){}document.body.dataset.cookieAccess='blocked';try{document.body.dataset.cookieAccess=document.cookie||'empty'}catch(e){}</script></body></html>`
	a := domain.Artifact{ID: domain.NewID(), JobID: j.ID, ModelID: m.ID, ChannelID: ch.ID, TemplateID: "cn_html", TemplateVersion: 1, Prompt: domain.ChinesePrompt, Protocol: "chat", RequestedModel: m.UpstreamID, ReturnedModel: m.UpstreamID, Parameters: `{"fixture":true}`, RawResponse: code, Code: code, Kind: "html", FinishReason: "stop", StartedAt: now, CreatedAt: now}
	if e = st.FinishJob(ctx, *claim, nil, &a); e != nil {
		t.Fatal(e)
	}
	fmt.Println("BROWSER_ARTIFACT_ID=" + a.ID)
}
