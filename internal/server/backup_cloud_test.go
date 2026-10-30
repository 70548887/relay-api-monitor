package server

import (
	"net/http"
	"testing"
)

func TestCloudBackupEndpointsRequireAdminCSRFAndRecentVerification(t *testing.T) {
	s, h, _, _ := backupTestApp(t)
	for _, path := range []string{"/cloud/scan", "/cloud/import"} {
		if w := request(t, h, "POST", "/api/v1/admin/backups"+path, map[string]any{}, nil, ""); w.Code != 401 {
			t.Fatal("anonymous admitted", w.Code)
		}
	}
	cookie, csrf := loginForTest(t, h)
	if w := request(t, h, "POST", "/api/v1/admin/backups/cloud/scan", map[string]any{}, cookie, ""); w.Code != 403 {
		t.Fatal("CSRF bypass", w.Code)
	}
	if w := request(t, h, "POST", "/api/v1/admin/backups/cloud/import", map[string]string{"candidate_id": "not-registered", "identity": "invalid"}, cookie, csrf); w.Code != 403 {
		t.Fatal("import bypassed reauth", w.Code)
	}
	member, token, _ := registerMemberForAuthTest(t, h, "cloud-member")
	if w := memberRequest(t, s, http.MethodPost, "/api/v1/admin/backups/cloud/scan", map[string]any{}, member, token); w.Code != 401 {
		t.Fatal("member admitted", w.Code)
	}
}
