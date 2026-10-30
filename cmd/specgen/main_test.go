package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSchemasDoNotRequireServerGeneratedFields(t *testing.T) {
	original := object{"type": "object", "properties": object{"id": object{"type": "string"}, "created_at": object{"type": "string"}, "name": object{"type": "string"}, "key_mask": object{"type": "string"}, "revision": object{"type": "integer"}}, "required": []string{"id", "created_at", "name"}}
	write := writeSchema(original, []string{"name"})
	props := write["properties"].(object)
	for _, key := range []string{"id", "created_at", "key_mask", "revision"} {
		if _, exists := props[key]; exists {
			t.Fatal("server-only property remains in write schema", key)
		}
	}
	if len(write["required"].([]string)) != 1 || write["required"].([]string)[0] != "name" {
		t.Fatal("write schema requires server fields")
	}
	if _, exists := original["properties"].(object)["id"]; !exists {
		t.Fatal("response schema mutated")
	}
}

func TestBackupContractKeepsCredentialsWriteOnlyAndStatusTokenReadOnly(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"BackupStorage", "BackupRun"} {
		props := schemas[name].(map[string]any)["properties"].(map[string]any)
		for _, secret := range []string{"secret_access_key", "access_key_id", "encrypted_credentials", "identity", "local_path", "object_key"} {
			if _, ok := props[secret]; ok {
				t.Fatalf("%s exposes %s", name, secret)
			}
		}
	}
	input := schemas["BackupStorageInput"].(map[string]any)["properties"].(map[string]any)
	for _, key := range []string{"secret_access_key", "access_key_id"} {
		if input[key].(map[string]any)["writeOnly"] != true {
			t.Fatal("credential not writeOnly", key)
		}
	}
	count := 0
	for path, raw := range paths {
		if !strings.HasPrefix(path, "/admin/backups") {
			continue
		}
		count++
		for method, v := range raw.(map[string]any) {
			op := v.(map[string]any)
			security := op["security"].([]any)
			if len(security) == 0 {
				t.Fatal("unsecured backup API", path)
			}
			if method != "get" {
				found := false
				for _, p := range op["parameters"].([]any) {
					if p.(map[string]any)["name"] == "X-CSRF-Token" {
						found = true
					}
				}
				if !found {
					t.Fatal("backup write missing CSRF", path)
				}
				if strings.Contains(stringMustMarshal(t, security), "restoreStatusToken") {
					t.Fatal("read-only token authorizes write", path)
				}
			}
		}
	}
	if count < 15 {
		t.Fatal("backup routes missing", count)
	}
	preflight := paths["/admin/backups/restore/preflight"].(map[string]any)["post"].(map[string]any)
	if _, ok := preflight["requestBody"].(map[string]any)["content"].(map[string]any)["multipart/form-data"]; !ok {
		t.Fatal("stream import contract missing")
	}
	if !strings.Contains(stringMustMarshal(t, preflight["responses"]), "3GiB") {
		t.Fatal("stream import limit wrong")
	}
}

func stringMustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
