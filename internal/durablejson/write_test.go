package durablejson

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicReplacementAndEncodingFailurePreserveMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "operations", "journal.json")
	for _, state := range []string{"accepted", "replacing", "committed"} {
		if e := Write(path, map[string]string{"state": state}); e != nil {
			t.Fatal(e)
		}
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		var got map[string]string
		if e = json.Unmarshal(b, &got); e != nil || got["state"] != state {
			t.Fatal("non-atomic metadata", string(b), e)
		}
	}
	if e := Write(path, make(chan int)); e == nil {
		t.Fatal("unsupported value accepted")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var got map[string]string
	_ = json.Unmarshal(b, &got)
	if got["state"] != "committed" {
		t.Fatal("failed write changed committed journal")
	}
	entries, e := os.ReadDir(filepath.Dir(path))
	if e != nil || len(entries) != 1 {
		t.Fatal("temporary metadata leaked", entries, e)
	}
}
