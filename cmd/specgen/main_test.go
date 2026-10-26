package main

import "testing"

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
