package main

import (
	"bytes"
	"context"
	"crypto/sha256"
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
)

// This opt-in test compiles and runs real serve/worker/launcher executables.
// All databases and ports are isolated; no real model API is configured.
func TestActualBinaryUpgradeAndRecovery(t *testing.T) {
	if os.Getenv("RELAYLAB_UPGRADE_INTEGRATION") != "1" {
		t.Skip("set RELAYLAB_UPGRADE_INTEGRATION=1 to run real binary lifecycle tests")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "install with spaces")
	data := filepath.Join(dir, "data")
	bin := filepath.Join(dir, "bin")
	if err = os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	app := filepath.Join(bin, "relaylab"+ext)
	launcher := filepath.Join(bin, "relaylab-launcher"+ext)
	target := filepath.Join(dir, "candidate"+ext)
	oldVersion, newVersion := "1.2.1", "1.2.2"
	oldCommit, newCommit := strings.Repeat("a", 40), strings.Repeat("b", 40)
	build := func(out, pkg, version, commit string) {
		t.Helper()
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-X main.version="+version+" -X main.commit="+commit+" -X main.buildTime=2026-10-26T00:00:00Z", "-o", out, pkg)
		cmd.Dir = root
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("build: %v\n%s", e, b)
		}
	}
	build(app, "./cmd/relaylab", oldVersion, oldCommit)
	build(target, "./cmd/relaylab", newVersion, newCommit)
	build(launcher, "./cmd/relaylab-launcher", newVersion, newCommit)
	configPath := filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(configPath, []byte("update:\n  enabled: true\n  check_interval: 0\n  auto_restart: true\n  backup_dir: ./data/backups\n"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := listener.Addr().String()
	listener.Close()
	logPath := filepath.Join(dir, "launcher.log")
	logfile, e := os.Create(logPath)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, launcher, "--relaylab", app, "--data-dir", data, "--addr", address, "--control-addr", "127.0.0.1:0", "--config", configPath)
	cmd.Dir = dir
	cmd.Stdout = logfile
	cmd.Stderr = logfile
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	client := &http.Client{Timeout: 5 * time.Second}
	var control, token string
	request := func(method, url string, body any, auth string) (int, map[string]any, error) {
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		req, e := http.NewRequest(method, url, bytes.NewReader(b))
		if e != nil {
			return 0, nil, e
		}
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("X-RelayLab-Launcher-Token", auth)
		}
		res, e := client.Do(req)
		if e != nil {
			return 0, nil, e
		}
		defer res.Body.Close()
		var value map[string]any
		e = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&value)
		return res.StatusCode, value, e
	}
	defer func() {
		accepted := false
		if control != "" {
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				select {
				case <-done:
					accepted = true
				default:
				}
				if accepted {
					break
				}
				status, _, err := request("POST", control+"/control/shutdown", map[string]any{}, token)
				if err == nil && (status == http.StatusAccepted || status == http.StatusOK) {
					accepted = true
					break
				}
				// A durable terminal journal can be visible just before the
				// operation lock is released. A 409 is a retryable refusal.
				time.Sleep(200 * time.Millisecond)
			}
		}
		if !accepted {
			t.Error("test launcher did not acknowledge shutdown; stopping owned fixture children")
			_ = os.WriteFile(filepath.Join(data, "stop.request"), []byte("integration cleanup"), 0600)
			time.Sleep(time.Second)
		}
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("test launcher did not exit after shutdown")
			_ = os.WriteFile(filepath.Join(data, "stop.request"), []byte("integration cleanup"), 0600)
			time.Sleep(time.Second)
			cancel()
			<-done
		}
		logfile.Close()
		if t.Failed() {
			b, _ := os.ReadFile(logPath)
			t.Logf("launcher log: %s", b)
		}
	}()
	wait := func(label string, check func() bool) {
		t.Helper()
		deadline := time.Now().Add(50 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("timed out: %s (logs %s)", label, logPath)
	}
	wait("launcher ready", func() bool {
		b, e := os.ReadFile(filepath.Join(data, "launcher.json"))
		if e != nil {
			return false
		}
		var st struct {
			ControlAddress string `json:"control_address"`
		}
		if json.Unmarshal(b, &st) != nil || st.ControlAddress == "" {
			return false
		}
		tb, e := os.ReadFile(filepath.Join(data, "launcher.token"))
		if e != nil {
			return false
		}
		control = "http://" + st.ControlAddress
		token = strings.TrimSpace(string(tb))
		status, v, e := request("GET", control+"/status", nil, token)
		return e == nil && status == 200 && v["state"] == "running"
	})
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	api := func(method, path string, body any, csrf string) (int, map[string]any) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, "http://"+address+"/api/v1"+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://"+address)
		req.Header.Set("X-CSRF-Token", csrf)
		res, e := browser.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		var v map[string]any
		_ = json.NewDecoder(res.Body).Decode(&v)
		return res.StatusCode, v
	}
	status, setup := api("POST", "/setup", map[string]any{"username": "upgrade-test", "password": "Upgrade_Integration_Only_2026"}, "")
	if status != 201 {
		t.Fatal("fresh installation failed", status, setup)
	}
	keyBefore, e := os.ReadFile(filepath.Join(data, "master.key"))
	if e != nil {
		t.Fatal(e)
	}
	t.Log("PASS empty data directory first launch: web initialized DB before worker, browser admin setup succeeds")
	status, session := api("POST", "/auth/login", map[string]any{"username": "upgrade-test", "password": "Upgrade_Integration_Only_2026"}, "")
	if status != 200 {
		t.Fatal(status, session)
	}
	csrf, _ := session["csrf_token"].(string)
	status, p := api("POST", "/admin/platforms", map[string]any{"name": "must-survive-upgrade", "public": false}, csrf)
	if status != 200 {
		t.Fatal(status, p)
	}
	status, snapshot := api("GET", "/admin/updates", nil, "")
	if status != 200 || snapshot["launcher_managed"] != true {
		t.Fatalf("bridge unavailable: %d %#v", status, snapshot)
	}
	stagedDir := filepath.Join(data, "updates", "v1.2.2")
	if err = os.MkdirAll(stagedDir, 0700); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(stagedDir, "relaylab-"+runtime.GOOS+"-"+runtime.GOARCH+ext)
	payload, e := os.ReadFile(target)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(staged, payload, 0700); e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256(payload)
	op := "integration-upgrade-001"
	install := map[string]any{"operation_id": op, "staged_path": staged, "sha256": fmt.Sprintf("%x", hash), "size": len(payload), "version": newVersion, "commit": newCommit, "launcher_protocol": 2, "usage_guard": 1, "database_schema": 2, "min_database_schema": 1}
	status, reply, e := request("POST", control+"/control/replace", install, token)
	if e != nil || status != 202 {
		t.Fatalf("install: %d %#v %v", status, reply, e)
	}
	readTransaction := func() map[string]any {
		b, _ := os.ReadFile(filepath.Join(data, "launcher-update.json"))
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		return v
	}
	wait("upgrade succeeded with both children", func() bool {
		v := readTransaction()
		if v["operation_id"] != op {
			return false
		}
		if v["state"] == "failed" || v["state"] == "rolled_back" {
			t.Fatalf("upgrade failed: %#v", v)
		}
		return v["state"] == "succeeded"
	})
	status, snapshot = api("GET", "/admin/updates", nil, "")
	if status != 200 {
		t.Fatal(status)
	}
	current, _ := snapshot["current"].(map[string]any)
	if current["version"] != newVersion || current["commit"] != newCommit {
		t.Fatal("wrong running version", current)
	}
	keyAfter, e := os.ReadFile(filepath.Join(data, "master.key"))
	if e != nil || !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("key changed", e)
	}
	t.Log("PASS real binaries: authenticated admin, launcher2 handoff, both children target version, key and session preserved")
	// Manual rollback must keep data written after the successful upgrade.
	status, _ = api("POST", "/admin/platforms", map[string]any{"name": "created-after-upgrade", "public": false}, csrf)
	if status != 200 {
		t.Fatal(status)
	}
	status, reply, e = request("POST", control+"/control/rollback", map[string]any{"operation_id": "integration-rollback-002"}, token)
	if e != nil || status != 202 {
		t.Fatalf("rollback: %d %#v %v", status, reply, e)
	}
	wait("manual rollback", func() bool {
		v := readTransaction()
		return v["operation_id"] == "integration-rollback-002" && (v["state"] == "rolled_back" || v["state"] == "succeeded" && v["kind"] == "rollback")
	})
	status, snapshot = api("GET", "/admin/updates", nil, "")
	if status != 200 {
		t.Fatal(status)
	}
	current, _ = snapshot["current"].(map[string]any)
	if current["version"] != oldVersion {
		t.Fatal("rollback version", current)
	}
	res, e := browser.Get("http://" + address + "/api/v1/admin/platforms")
	if e != nil {
		t.Fatal(e)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !bytes.Contains(body, []byte("created-after-upgrade")) || !bytes.Contains(body, []byte("must-survive-upgrade")) {
		t.Fatal("rollback discarded current configuration")
	}
	t.Log("PASS real binary compatible rollback: new data and authentication retained")
	// A candidate that changes the schema and then dies exercises restoration
	// of the actual database, rather than merely swapping an executable back.
	brokenSource := filepath.Join(dir, "broken.go")
	brokenCode := `package main
import ("database/sql";"encoding/json";"os";"path/filepath";"runtime"; _ "modernc.org/sqlite")
func main(){
 if len(os.Args)>1&&os.Args[1]=="version"{json.NewEncoder(os.Stdout).Encode(map[string]any{"version":"1.2.3","commit":"cccccccccccccccccccccccccccccccccccccccc","build_time":"2026-10-26T00:00:00Z","os":runtime.GOOS,"arch":runtime.GOARCH,"launcher_protocol":2,"usage_guard":1,"database_schema":2,"min_database_schema":1});return}
 var data string;for i,a:=range os.Args{if a=="--data-dir"&&i+1<len(os.Args){data=os.Args[i+1]}}
 if data!=""{db,e:=sql.Open("sqlite",filepath.Join(data,"relaylab.db"));if e==nil{db.Exec("PRAGMA user_version=999");db.Exec("DELETE FROM platforms");db.Close()}}
 os.Exit(17)
}`
	if e = os.WriteFile(brokenSource, []byte(brokenCode), 0600); e != nil {
		t.Fatal(e)
	}
	brokenDir := filepath.Join(data, "updates", "v1.2.3")
	if e = os.MkdirAll(brokenDir, 0700); e != nil {
		t.Fatal(e)
	}
	broken := filepath.Join(brokenDir, "relaylab-"+runtime.GOOS+"-"+runtime.GOARCH+ext)
	build(broken, brokenSource, "1.2.3", strings.Repeat("c", 40))
	brokenPayload, e := os.ReadFile(broken)
	if e != nil {
		t.Fatal(e)
	}
	brokenHash := sha256.Sum256(brokenPayload)
	failureID := "integration-failed-start-003"
	install = map[string]any{"operation_id": failureID, "staged_path": broken, "sha256": fmt.Sprintf("%x", brokenHash), "size": len(brokenPayload), "version": "1.2.3", "commit": strings.Repeat("c", 40), "launcher_protocol": 2, "usage_guard": 1, "database_schema": 2, "min_database_schema": 1}
	status, reply, e = request("POST", control+"/control/replace", install, token)
	if e != nil || status != 202 {
		t.Fatalf("bad candidate request: %d %#v %v", status, reply, e)
	}
	wait("failed candidate restores program AND database", func() bool {
		v := readTransaction()
		if v["operation_id"] != failureID {
			return false
		}
		if v["state"] == "failed" {
			t.Fatalf("failed rollback: %#v", v)
		}
		return v["state"] == "rolled_back"
	})
	status, snapshot = api("GET", "/admin/updates", nil, "")
	if status != 200 {
		t.Fatal("database/session was not restored", status, snapshot)
	}
	current, _ = snapshot["current"].(map[string]any)
	if current["version"] != oldVersion {
		t.Fatal("recovery version", current)
	}
	res, e = browser.Get("http://" + address + "/api/v1/admin/platforms")
	if e != nil {
		t.Fatal(e)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !bytes.Contains(body, []byte("created-after-upgrade")) {
		t.Fatal("failed installation lost saved data")
	}
	t.Log("PASS real failed startup: schema and saved data restored; old serve and worker running")
}
