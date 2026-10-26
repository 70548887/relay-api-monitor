package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type zipPart struct {
	name string
	data []byte
}

func writeTestZIP(t *testing.T, parts []zipPart, corrupt bool) string {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, p := range parts {
		f, err := w.CreateHeader(&zip.FileHeader{Name: p.name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write(p.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if corrupt {
		needle := []byte("unique-crc-fixture-payload")
		i := bytes.Index(b, needle)
		if i < 0 {
			t.Fatal("CRC fixture not found")
		}
		b[i] ^= 1
	}
	file := filepath.Join(t.TempDir(), "test.zip")
	if err := os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestRestoreRejectsMalformedMembersBeforePublishing(t *testing.T) {
	cases := []struct {
		name    string
		parts   []zipPart
		corrupt bool
		want    string
	}{
		{"oversized key", []zipPart{{"master.key", make([]byte, 33)}, {"relaylab.db", []byte("db")}}, false, "32"},
		{"oversized manifest", []zipPart{{"manifest.json", make([]byte, 65537)}, {"master.key", make([]byte, 32)}, {"relaylab.db", []byte("db")}}, false, "大小限制"},
		{"future manifest", []zipPart{{"manifest.json", []byte(`{"schema":999}`)}, {"master.key", make([]byte, 32)}, {"relaylab.db", []byte("db")}}, false, "版本"},
		{"duplicate entry", []zipPart{{"master.key", make([]byte, 32)}, {"master.key", make([]byte, 32)}}, false, "重复"},
		{"path traversal", []zipPart{{"../master.key", make([]byte, 32)}}, false, "未知"},
		{"CRC corruption", []zipPart{{"relaylab.db", []byte("unique-crc-fixture-payload")}, {"master.key", make([]byte, 32)}}, true, "checksum"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := writeTestZIP(t, tc.parts, tc.corrupt)
			target := filepath.Join(t.TempDir(), "restored")
			err := restore(target, input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s got %v", tc.want, err)
			}
			if _, e := os.Stat(target); !os.IsNotExist(e) {
				t.Fatal("invalid archive published a target directory")
			}
		})
	}
}
