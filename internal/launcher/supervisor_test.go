package launcher

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlHandlerRequiresLoopbackAndToken(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Options{RelayLabPath: os.Args[0], DataDir: dir, ControlToken: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/status", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	s.controlHandler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("non-loopback status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/status", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	response = httptest.NewRecorder()
	s.controlHandler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d body=%s", response.Code, response.Body.String())
	}
	request.Header.Set("X-RelayLab-Launcher-Token", "secret")
	response = httptest.NewRecorder()
	s.controlHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"stopped"`) {
		t.Fatalf("authorized status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestNewRejectsNonLoopbackControlAddress(t *testing.T) {
	_, err := New(Options{RelayLabPath: os.Args[0], DataDir: t.TempDir(), ControlAddress: "0.0.0.0:42784"})
	if !errors.Is(err, ErrNonLoopback) {
		t.Fatalf("error=%v, want ErrNonLoopback", err)
	}
}

func TestWriteTokenAndStateAreAtomic(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Options{RelayLabPath: os.Args[0], DataDir: dir, ControlToken: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	tokenPath, err := s.WriteTokenFile()
	if err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(tokenPath)
	if err != nil || string(token) != "secret\n" {
		t.Fatalf("token file=%q err=%v", token, err)
	}
	if err := s.WriteStateFile(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "launcher.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "launcher.token.tmp")); !os.IsNotExist(err) {
		t.Fatalf("temporary token file remains: %v", err)
	}
}

func TestSHA256File(t *testing.T) {
	path := filepath.Join(t.TempDir(), "asset.bin")
	if err := os.WriteFile(path, []byte("relaylab"), 0600); err != nil {
		t.Fatal(err)
	}
	hash, size, err := SHA256File(path)
	if err != nil {
		t.Fatal(err)
	}
	if size != 8 || hash != "7d8b4199f4d1b8a7e84b7185e619f86e8e09fbf82076d359e9916212166907fa" {
		t.Fatalf("unexpected digest %s size=%d", hash, size)
	}
}

func TestReplaceFileCopiesAtomically(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	dest := filepath.Join(dir, "dest")
	if err := os.WriteFile(source, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(source, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "new" {
		t.Fatalf("dest=%q err=%v", got, err)
	}
	if _, err := os.Stat(dest + ".incoming.tmp"); !os.IsNotExist(err) {
		t.Fatalf("replacement temporary file remains: %v", err)
	}
}
