package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"relaylab/internal/security"
	"relaylab/internal/upstream"
)

func TestModelSyncReturnsFullCatalogueOverClosingTCPConnection(t *testing.T) {
	s, _, channel, _ := publicFixture(t)
	var err error
	s.Vault, err = security.LoadVault(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := s.Vault.Encrypt("fixture-sync-key")
	if err != nil {
		t.Fatal(err)
	}
	channel.EncryptedKey = secret
	if err := s.Store.SaveChannel(context.Background(), channel); err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	cookie, csrf := loginForTest(t, handler)
	data := make([]map[string]string, 17)
	for index := range data {
		data[index] = map[string]string{"id": fmt.Sprintf("anthropic-model-%02d", index)}
	}
	payload, _ := json.Marshal(map[string]any{"data": data})
	s.Client = upstream.NewTestClient(visitorDoer(func(*http.Request) (*http.Response, error) {
		return visitorResponse(string(payload), "application/json"), nil
	}))
	server := httptest.NewServer(handler)
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/admin/channels/"+channel.ID+"/sync-models", bytes.NewBufferString("{}"))
	request.Close = true
	request.AddCookie(cookie)
	request.Header.Set("Origin", server.URL)
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("model catalogue response interrupted: %v", err)
	}
	var result struct {
		Count  int               `json:"count"`
		Models []json.RawMessage `json:"models"`
	}
	if response.StatusCode != 200 || json.Unmarshal(encoded, &result) != nil || result.Count != 17 || len(result.Models) < 17 {
		t.Fatalf("incomplete sync response: status=%d body=%s", response.StatusCode, encoded)
	}
}

func TestModelSyncRejectsInvalidBodyBeforeDiscovery(t *testing.T) {
	s, handler := testServer(t)
	cookie, csrf := loginForTest(t, handler)
	calls := 0
	s.Client = upstream.NewTestClient(visitorDoer(func(*http.Request) (*http.Response, error) {
		calls++
		return visitorResponse(`{"data":[]}`, "application/json"), nil
	}))
	for _, body := range []string{`{`, `{} {}`, `[]`} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/channels/unused/sync-models", bytes.NewBufferString(body))
		req.AddCookie(cookie)
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set("X-CSRF-Token", csrf)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid sync body was not rejected: status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if calls != 0 {
		t.Fatalf("invalid sync body sent %d upstream calls", calls)
	}
}
