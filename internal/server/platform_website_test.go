package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"relaylab/internal/domain"
)

func assertPlatformWebsiteForTest(t *testing.T, s *Server, h http.Handler, cookie *http.Cookie, id, want string) {
	t.Helper()
	stored, err := s.Store.GetPlatform(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.WebsiteURL != want {
		t.Fatalf("stored website_url = %q, want %q", stored.WebsiteURL, want)
	}
	response := request(t, h, http.MethodGet, "/api/v1/admin/platforms", nil, cookie, "")
	if response.Code != http.StatusOK {
		t.Fatalf("list platforms: %d %s", response.Code, response.Body.String())
	}
	var platforms []domain.Platform
	if err := json.Unmarshal(response.Body.Bytes(), &platforms); err != nil {
		t.Fatal(err)
	}
	for _, platform := range platforms {
		if platform.ID == id {
			if platform.WebsiteURL != want {
				t.Fatalf("GET website_url = %q, want %q", platform.WebsiteURL, want)
			}
			return
		}
	}
	t.Fatalf("GET platforms did not include platform %q", id)
}

func TestPlatformWebsiteLifecycle(t *testing.T) {
	s, h := testServer(t)
	cookie, csrf := loginForTest(t, h)
	const website = "https://example.test/docs?lang=zh#start"
	response := request(t, h, http.MethodPost, "/api/v1/admin/platforms", map[string]any{
		"name": "Website platform", "website_url": "  " + website + "  ", "public": true,
	}, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatalf("create platform: %d %s", response.Code, response.Body.String())
	}
	var platform domain.Platform
	if err := json.Unmarshal(response.Body.Bytes(), &platform); err != nil {
		t.Fatal(err)
	}
	if platform.ID == "" || platform.WebsiteURL != website {
		t.Fatalf("create returned id=%q website_url=%q", platform.ID, platform.WebsiteURL)
	}
	assertPlatformWebsiteForTest(t, s, h, cookie, platform.ID, website)

	path := "/api/v1/admin/platforms/" + platform.ID
	response = request(t, h, http.MethodPut, path, map[string]any{"name": "Renamed platform", "public": true}, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatalf("update without website_url: %d %s", response.Code, response.Body.String())
	}
	assertPlatformWebsiteForTest(t, s, h, cookie, platform.ID, website)

	const httpWebsite = "http://example.test:8080/home"
	response = request(t, h, http.MethodPut, path, map[string]any{
		"name": "Renamed platform", "website_url": " " + httpWebsite + " ",
	}, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatalf("update HTTP website_url: %d %s", response.Code, response.Body.String())
	}
	assertPlatformWebsiteForTest(t, s, h, cookie, platform.ID, httpWebsite)

	response = request(t, h, http.MethodPut, path, map[string]any{
		"name": "Renamed platform", "website_url": "",
	}, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatalf("clear website_url: %d %s", response.Code, response.Body.String())
	}
	assertPlatformWebsiteForTest(t, s, h, cookie, platform.ID, "")
	var cleared map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &cleared); err != nil {
		t.Fatal(err)
	}
	if _, exists := cleared["website_url"]; exists {
		t.Fatal("empty website_url should be omitted from the JSON response")
	}

	response = request(t, h, http.MethodPost, "/api/v1/admin/platforms", map[string]string{"name": "No website"}, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatalf("create without optional website_url: %d %s", response.Code, response.Body.String())
	}
	var withoutWebsite domain.Platform
	if err := json.Unmarshal(response.Body.Bytes(), &withoutWebsite); err != nil {
		t.Fatal(err)
	}
	assertPlatformWebsiteForTest(t, s, h, cookie, withoutWebsite.ID, "")
}

func TestPlatformWebsiteRejectsUnsafeURLs(t *testing.T) {
	s, h := testServer(t)
	cookie, csrf := loginForTest(t, h)
	const originalWebsite = "https://example.test/"
	response := request(t, h, http.MethodPost, "/api/v1/admin/platforms", map[string]string{
		"name": "Existing platform", "website_url": originalWebsite,
	}, cookie, csrf)
	if response.Code != http.StatusOK {
		t.Fatalf("create baseline platform: %d %s", response.Code, response.Body.String())
	}
	var platform domain.Platform
	if err := json.Unmarshal(response.Body.Bytes(), &platform); err != nil {
		t.Fatal(err)
	}
	invalid := []struct {
		name string
		url  string
	}{
		{"javascript", "javascript:alert(1)"},
		{"data", "data:text/html,<script>alert(1)</script>"},
		{"userinfo", "https://user:password@example.test/"},
		{"username_only", "https://user@example.test/"},
		{"http_without_host", "http:///docs"},
		{"https_without_host", "https://"},
		{"newline", "https://example.test/a\nb"},
		{"carriage_return", "https://example.test/a\rb"},
		{"tab", "https://example.test/a\tb"},
		{"null", "https://example.test/a\x00b"},
		{"delete", "https://example.test/a\x7fb"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{http.MethodPost, http.MethodPut} {
				t.Run(method, func(t *testing.T) {
					path := "/api/v1/admin/platforms"
					if method == http.MethodPut {
						path += "/" + platform.ID
					}
					response := request(t, h, method, path, map[string]string{
						"name": "Rejected platform", "website_url": tc.url,
					}, cookie, csrf)
					if response.Code != http.StatusBadRequest {
						t.Fatalf("unsafe website_url %q: got %d, want 400; %s", tc.url, response.Code, response.Body.String())
					}
					stored, err := s.Store.ListPlatforms(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					if len(stored) != 1 || stored[0].ID != platform.ID || stored[0].Name != platform.Name || stored[0].WebsiteURL != originalWebsite {
						t.Fatalf("rejected %s changed stored platforms: %+v", method, stored)
					}
				})
			}
		})
	}
}
