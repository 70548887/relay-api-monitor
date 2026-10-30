package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUpdateContractMatchesDeploymentCapabilitiesAndOperationIdentity(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	properties := func(name string) map[string]any {
		return schemas[name].(map[string]any)["properties"].(map[string]any)
	}
	update := properties("UpdateSnapshot")
	if update["install_enabled"].(map[string]any)["type"] != "boolean" {
		t.Fatal("installation capability must be a boolean")
	}
	mode := update["install_mode"].(map[string]any)
	if mode["type"] != "string" || !reflect.DeepEqual(mode["enum"], []any{"binary", "container"}) {
		t.Fatalf("deployment mode contract mismatch: %+v", mode)
	}
	if _, exists := update["staged_path"]; exists {
		t.Fatal("update contract exposes a server filesystem path")
	}
	kind := properties("UpdateOperation")["kind"].(map[string]any)
	if kind["type"] != "string" || kind["description"] == "" {
		t.Fatal("operation kind is not documented")
	}
	if _, exists := kind["enum"]; exists {
		t.Fatal("operation kind must allow launcher and legacy variants")
	}
	for _, name := range []string{"UpdateInfo", "UpdateRelease"} {
		guard := properties(name)["usage_guard"].(map[string]any)
		if _, exists := guard["maximum"]; exists {
			t.Fatal("container discovery contract excludes future usage guards", name)
		}
	}
}
