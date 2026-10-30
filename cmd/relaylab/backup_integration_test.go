package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/security"
	"relaylab/internal/store"
)

// This fixture owns its binaries, processes, ports and DB. No model calls or real
// S3 credentials are configured; it exercises the public HTTP lifecycle end to end.
func TestActualBinaryBackupAndRestore(t *testing.T) {
	if os.Getenv("RELAYLAB_BACKUP_INTEGRATION") != "1" {
		t.Skip("set RELAYLAB_BACKUP_INTEGRATION=1 to run real backup/restore lifecycle")
	}
	root, e := filepath.Abs(filepath.Join("..", ".."))
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(t.TempDir(), "backup integration with spaces")
	data := filepath.Join(dir, "data")
	bin := filepath.Join(dir, "bin")
	if e = os.MkdirAll(bin, 0700); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	password := "Backup_Integration_Only_2026"
	hash, e := security.PasswordHash(password)
	if e != nil {
		t.Fatal(e)
	}
	db, e := store.Open(filepath.Join(data, "relaylab.db"))
	if e != nil {
		t.Fatal(e)
	}
	vault, e := security.LoadVault(data, false)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.EnsureAdmin(ctx, "backup-test", hash); e != nil {
		t.Fatal(e)
	}
	if e = db.SavePlatform(ctx, domain.Platform{ID: "fixture-platform", Name: "before-backup"}); e != nil {
		t.Fatal(e)
	}
	channelKey := "fixture-channel-key-never-sent"
	cipher, e := vault.Encrypt(channelKey)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.SaveChannel(ctx, domain.Channel{ID: "fixture-channel", PlatformID: "fixture-platform", Name: "fixture-disabled", BaseURL: "https://disabled.example.invalid", EncryptedKey: cipher, Enabled: false}); e != nil {
		t.Fatal(e)
	}
	member, e := db.CreateMember(ctx, "backup-member", hash)
	if e != nil {
		t.Fatal(e)
	}
	connectionID := "fixture-connection"
	userKey := "fixture-user-key-never-sent"
	cipher, e = vault.EncryptUserConnection(member.ID, connectionID, userKey)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	if e = db.SaveUserConnection(ctx, domain.UserConnection{ID: connectionID, UserID: member.ID, Name: "private-fixture", BaseURL: "https://disabled.example.invalid", Protocol: "responses", EncryptedKey: cipher, CredentialFingerprint: "fixture-fingerprint", Revision: 1, CreatedAt: now, UpdatedAt: now}); e != nil {
		t.Fatal(e)
	}
	history := domain.OnlineTestRun{ID: "fixture-private-history", UserID: member.ID, ConnectionID: connectionID, ConnectionRevision: 1, ClientRequestID: "fixture-history-request", RequestHash: "fixture-request-hash", State: "succeeded", Text: "private fixture survives restore", CreatedAt: now, StartedAt: now, FinishedAt: &now, ProcessID: "completed-fixture"}
	if _, _, e = db.CreateOnlineTestRun(ctx, history); e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	app := filepath.Join(bin, "relaylab"+ext)
	launcherPath := filepath.Join(bin, "relaylab-launcher"+ext)
	for _, target := range []struct{ out, pkg string }{{app, "./cmd/relaylab"}, {launcherPath, "./cmd/relaylab-launcher"}} {
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-X main.version=1.5.0 -X main.commit="+strings.Repeat("c", 40)+" -X main.buildTime=2026-10-29T00:00:00Z", "-o", target.out, target.pkg)
		cmd.Dir = root
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("build failed: %v %s", e, b)
		}
	}
	configPath := filepath.Join(dir, "config.yaml")
	if e = os.WriteFile(configPath, []byte("update:\n  enabled: false\n  check_interval: 0\n"), 0600); e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := listener.Addr().String()
	listener.Close()
	origin := "http://" + address
	logPath := filepath.Join(dir, "owned-launcher.log")
	logFile, e := os.Create(logPath)
	if e != nil {
		t.Fatal(e)
	}
	processCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(processCtx, launcherPath, "--relaylab", app, "--data-dir", data, "--addr", address, "--control-addr", "127.0.0.1:0", "--config", configPath)
	cmd.Dir = dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	controlClient := &http.Client{Timeout: 5 * time.Second}
	control, token := "", ""
	request := func(client *http.Client, method, url string, body any, headers map[string]string) (int, []byte, error) {
		b, _ := json.Marshal(body)
		req, e := http.NewRequest(method, url, bytes.NewReader(b))
		if e != nil {
			return 0, nil, e
		}
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, e := client.Do(req)
		if e != nil {
			return 0, nil, e
		}
		defer res.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		return res.StatusCode, raw, e
	}
	defer func() {
		if control != "" {
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				code, _, e := request(controlClient, "POST", control+"/control/shutdown", map[string]any{}, map[string]string{"X-RelayLab-Launcher-Token": token})
				if e == nil && (code == 200 || code == 202) {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
		}
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			_ = os.WriteFile(filepath.Join(data, "stop.request"), []byte("fixture cleanup"), 0600)
			time.Sleep(time.Second)
			cancel()
			<-done
			t.Error("fixture launcher required forced stop")
		}
		logFile.Close()
		if t.Failed() {
			b, _ := os.ReadFile(logPath)
			if len(b) > 12000 {
				b = b[len(b)-12000:]
			}
			t.Logf("owned fixture log: %s", b)
		}
	}()
	wait := func(label string, fn func() bool) {
		t.Helper()
		deadline := time.Now().Add(100 * time.Second)
		for time.Now().Before(deadline) {
			if fn() {
				return
			}
			time.Sleep(150 * time.Millisecond)
		}
		t.Fatalf("timeout waiting for %s", label)
	}
	statusMap := func() map[string]any {
		code, b, e := request(controlClient, "GET", control+"/status", nil, map[string]string{"X-RelayLab-Launcher-Token": token})
		if e != nil || code != 200 {
			return nil
		}
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		return v
	}
	wait("managed children ready", func() bool {
		b, e := os.ReadFile(filepath.Join(data, "launcher.json"))
		if e != nil {
			return false
		}
		var v struct {
			ControlAddress string `json:"control_address"`
		}
		if json.Unmarshal(b, &v) != nil || v.ControlAddress == "" {
			return false
		}
		b, e = os.ReadFile(filepath.Join(data, "launcher.token"))
		if e != nil {
			return false
		}
		control = "http://" + v.ControlAddress
		token = strings.TrimSpace(string(b))
		v2 := statusMap()
		return v2 != nil && v2["state"] == "running"
	})
	initial := statusMap()
	initialServe := initial["serve"].(map[string]any)["pid"]
	initialWorker := initial["worker"].(map[string]any)["pid"]
	csrf := ""
	api := func(method, path string, body any, want int) []byte {
		t.Helper()
		code, b, e := request(browser, method, origin+"/api/v1"+path, body, map[string]string{"Origin": origin, "X-CSRF-Token": csrf})
		if e != nil || code != want {
			t.Fatalf("%s %s got %d expected %d (%v) %s", method, path, code, want, e, b)
		}
		return b
	}
	login := func() {
		t.Helper()
		b := api("POST", "/auth/login", map[string]string{"username": "backup-test", "password": password}, 200)
		var v struct {
			CSRF string `json:"csrf_token"`
		}
		if json.Unmarshal(b, &v) != nil || v.CSRF == "" {
			t.Fatal("missing csrf")
		}
		csrf = v.CSRF
	}
	login()
	var preview struct {
		NextRuns []time.Time `json:"next_runs"`
	}
	if e = json.Unmarshal(api("POST", "/admin/backups/schedule/preview", domain.DefaultBackupSchedule(), 200), &preview); e != nil || len(preview.NextRuns) != 3 {
		t.Fatalf("standalone binary timezone preview failed: %v", e)
	}
	var keys struct {
		Identity  string `json:"identity"`
		Recipient string `json:"recipient"`
	}
	if e = json.Unmarshal(api("POST", "/admin/backups/keys/generate", map[string]any{}, 200), &keys); e != nil {
		t.Fatal(e)
	}
	api("PUT", "/admin/backups/keys/recipient", map[string]string{"recipient": keys.Recipient}, 200)
	api("POST", "/admin/backups/keys/verify", map[string]string{"identity": keys.Identity}, 200)
	var run domain.BackupRun
	if e = json.Unmarshal(api("POST", "/admin/backups/records", map[string]string{"request_id": "actual-binary-backup-test"}, 202), &run); e != nil {
		t.Fatal(e)
	}
	wait("encrypted backup terminal", func() bool {
		b := api("GET", "/admin/backups/records/"+run.ID, nil, 200)
		if json.Unmarshal(b, &run) != nil {
			return false
		}
		if run.State == "failed" {
			t.Fatalf("backup failed %s %s", run.ErrorCode, run.Message)
		}
		return run.State == "succeeded"
	})
	api("POST", "/admin/backups/reauth", map[string]string{"password": password}, 200)
	code, download, e := request(browser, "GET", origin+"/api/v1/admin/backups/records/"+run.ID+"/download", nil, nil)
	if e != nil || code != 200 {
		t.Fatalf("download failed %d %v", code, e)
	}
	digest := sha256.Sum256(download)
	if hex.EncodeToString(digest[:]) != run.SHA256 {
		t.Fatal("download hash mismatch")
	}
	t.Log("PASS actual binary encrypted backup, authenticated download and SHA-256")
	api("PUT", "/admin/platforms/fixture-platform", map[string]any{"name": "after-backup-change", "public": false}, 200)
	time.Sleep(200 * time.Millisecond)
	var preflight struct {
		ID string `json:"id"`
	}
	if e = json.Unmarshal(api("POST", "/admin/backups/restore/preflight", map[string]string{"record_id": run.ID, "identity": keys.Identity}, 200), &preflight); e != nil || preflight.ID == "" {
		t.Fatal("preflight id missing", e)
	}
	var operation struct {
		ID    string `json:"operation_id"`
		Token string `json:"status_token"`
	}
	if e = json.Unmarshal(api("POST", "/admin/backups/restore/apply", map[string]string{"preflight_id": preflight.ID}, 202), &operation); e != nil {
		t.Fatal(e)
	}
	wait("restore operation succeeded", func() bool {
		code, b, e := request(controlClient, "GET", origin+"/api/v1/admin/backups/operations/"+operation.ID, nil, map[string]string{"X-Restore-Status-Token": operation.Token})
		if e != nil || code != 200 {
			return false
		}
		var v map[string]any
		if json.Unmarshal(b, &v) != nil {
			return false
		}
		state := v["state"]
		if state == "failed" || state == "rolled_back" || state == "recovery_required" {
			t.Fatalf("restore failed %#v", v)
		}
		return state == "succeeded"
	})
	wait("both children restarted and resumed", func() bool {
		v := statusMap()
		if v == nil || v["state"] != "running" || v["maintenance"] != false {
			return false
		}
		s, w := v["serve"].(map[string]any), v["worker"].(map[string]any)
		return s["running"] == true && w["running"] == true && s["pid"] != initialServe && w["pid"] != initialWorker
	})
	api("GET", "/auth/me", nil, 401)
	login()
	var platforms []domain.Platform
	if e = json.Unmarshal(api("GET", "/admin/platforms", nil, 200), &platforms); e != nil || len(platforms) != 1 || platforms[0].Name != "before-backup" {
		t.Fatalf("platform not restored %+v %v", platforms, e)
	}
	var backupStatus struct {
		Schedule domain.BackupSchedule `json:"schedule"`
		Native   bool                  `json:"native_restore_supported"`
	}
	if e = json.Unmarshal(api("GET", "/admin/backups/status", nil, 200), &backupStatus); e != nil || !backupStatus.Schedule.ReconciliationRequired || backupStatus.Schedule.Enabled || !backupStatus.Native {
		t.Fatalf("restore state incorrect %+v %v", backupStatus, e)
	}
	restored, e := store.OpenExisting(filepath.Join(data, "relaylab.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	restoredVault, e := security.LoadVault(data, true)
	if e != nil {
		t.Fatal(e)
	}
	ch, e := restored.GetChannel(ctx, "fixture-channel")
	if e != nil {
		t.Fatal(e)
	}
	plain, e := restoredVault.Decrypt(ch.EncryptedKey)
	if e != nil || plain != channelKey {
		t.Fatal("channel key not restored")
	}
	connection, e := restored.GetUserConnection(ctx, member.ID, connectionID)
	if e != nil {
		t.Fatal(e)
	}
	plain, e = restoredVault.DecryptUserConnection(member.ID, connectionID, connection.EncryptedKey)
	if e != nil || plain != userKey {
		t.Fatal("user key not restored")
	}
	record, e := restored.GetOnlineTestRun(ctx, member.ID, history.ID)
	if e != nil || record.Text != history.Text {
		t.Fatal("private history not restored", e)
	}
	t.Log(fmt.Sprintf("PASS actual launcher restore: serve %v→%v, worker %v→%v, platform restored, old session rejected, credentials/private history readable, backup cleanup frozen", initialServe, statusMap()["serve"].(map[string]any)["pid"], initialWorker, statusMap()["worker"].(map[string]any)["pid"]))
}
