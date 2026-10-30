package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupAuditContractHasServerFiltersHealthAndRetentionPreview(t *testing.T) {
	b, e := os.ReadFile(filepath.Join("..", "..", "docs", "openapi.json"))
	if e != nil {
		t.Fatal(e)
	}
	var doc object
	if e = json.Unmarshal(b, &doc); e != nil {
		t.Fatal(e)
	}
	paths := doc["paths"].(map[string]any)
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	list := paths["/admin/backups/records"].(map[string]any)["get"].(map[string]any)
	names := map[string]bool{}
	for _, raw := range list["parameters"].([]any) {
		p := raw.(map[string]any)
		names[p["name"].(string)] = true
	}
	for _, name := range []string{"state", "trigger", "protection", "from", "to", "limit", "offset"} {
		if !names[name] {
			t.Fatal("filter missing", name)
		}
	}
	preview := paths["/admin/backups/schedule/retention-preview"].(map[string]any)["post"].(map[string]any)
	if len(preview["security"].([]any)) == 0 {
		t.Fatal("retention preview not authenticated")
	}
	props := schemas["BackupRetentionPreview"].(map[string]any)["properties"].(map[string]any)
	for _, name := range []string{"count", "local_copies", "remote_copies", "total_bytes", "items"} {
		if props[name] == nil {
			t.Fatal("preview response missing", name)
		}
	}
	health := schemas["BackupStorageHealth"].(map[string]any)["properties"].(map[string]any)
	for _, name := range []string{"status", "checked_at", "message"} {
		if health[name] == nil {
			t.Fatal("health missing", name)
		}
	}
}

func TestBackupCloudImportContractKeepsSourceKeysPrivate(t *testing.T) {
	b, e := os.ReadFile(filepath.Join("..", "..", "docs", "openapi.json"))
	if e != nil {
		t.Fatal(e)
	}
	var doc object
	if e = json.Unmarshal(b, &doc); e != nil {
		t.Fatal(e)
	}
	paths := doc["paths"].(map[string]any)
	for _, path := range []string{"/admin/backups/cloud/scan", "/admin/backups/cloud/import"} {
		op := paths[path].(map[string]any)["post"].(map[string]any)
		if len(op["security"].([]any)) == 0 {
			t.Fatal("cloud operation not authenticated", path)
		}
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	candidate := schemas["BackupCloudCandidate"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"object_key", "api_key", "access_key_id", "secret_access_key"} {
		if candidate[field] != nil {
			t.Fatal("private source property exposed", field)
		}
	}
	input := schemas["BackupCloudImportInput"].(map[string]any)["properties"].(map[string]any)
	if input["identity"].(map[string]any)["writeOnly"] != true {
		t.Fatal("private identity must be write-only")
	}
}
