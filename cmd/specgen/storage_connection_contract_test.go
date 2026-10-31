package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStorageConnectionContractPreservesSafeResultOnFailure(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	props := schemas["BackupStorageTestResult"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"status", "stage", "error_code", "message", "duration_ms", "http_status", "cleanup_error_code", "cleanup_message", "request_id", "test_scope", "storage_id", "storage_revision"} {
		if props[field] == nil {
			t.Fatal("missing diagnostic field", field)
		}
	}
	for _, field := range []string{"access_key_id", "secret_access_key", "endpoint", "object_key", "upstream_body"} {
		if props[field] != nil {
			t.Fatal("private diagnostic field", field)
		}
	}
	path := doc["paths"].(map[string]any)["/admin/backups/storage/test"].(map[string]any)["post"].(map[string]any)
	responses := path["responses"].(map[string]any)
	if len(path["security"].([]any)) == 0 {
		t.Fatal("test no longer requires administrator authentication")
	}
	for _, status := range []string{"200", "400", "408", "502", "504"} {
		response := responses[status].(map[string]any)
		schema := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		want := "#/components/schemas/BackupStorageTestError"
		if status == "200" {
			want = "#/components/schemas/BackupStorageTestResponse"
		}
		if status == "400" {
			choices, ok := schema["oneOf"].([]any)
			if !ok || len(choices) != 2 || choices[0].(map[string]any)["$ref"] != want || choices[1].(map[string]any)["$ref"] != "#/components/schemas/Error" {
				t.Fatal("malformed requests and diagnostic failures need distinct contracts")
			}
			continue
		}
		if schema["$ref"] != want {
			t.Fatal("incorrect connection diagnostic envelope", status)
		}
	}
}
