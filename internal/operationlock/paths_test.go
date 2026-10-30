package operationlock

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSiblingDataDirectoriesHaveIndependentNamespacesAndLocks(t *testing.T) {
	parent := t.TempDir()
	first, second := filepath.Join(parent, "first"), filepath.Join(parent, "second")
	if BackupRoot(first) == BackupRoot(second) || Path(first) == Path(second) {
		t.Fatal("sibling instances share state")
	}
	release, e := Acquire(first)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	other, e := Acquire(second)
	if e != nil {
		t.Fatal("sibling lock interferes", e)
	}
	defer other()
	if _, e = Acquire(filepath.Join(first, "..", "first")); e == nil {
		t.Fatal("same canonical directory received second lock")
	}
}
func TestCanonicalAliasesPreserveIdentityBeforeAndAfterCreation(t *testing.T) {
	parent := t.TempDir()
	data := filepath.Join(parent, "future", "data")
	before := InstanceID(data)
	if e := os.MkdirAll(data, 0700); e != nil {
		t.Fatal(e)
	}
	if before != InstanceID(data) {
		t.Fatal("namespace changed on directory creation")
	}
	if runtime.GOOS == "windows" && before != InstanceID(strings.ToUpper(data)) {
		t.Fatal("Windows case alias changed instance")
	}
	link := filepath.Join(t.TempDir(), "alias")
	if e := os.Symlink(parent, link); e != nil {
		t.Log("directory symlink unavailable", e)
		return
	}
	if before != InstanceID(filepath.Join(link, "future", "data")) {
		t.Fatal("symlink alias changed instance")
	}
}
