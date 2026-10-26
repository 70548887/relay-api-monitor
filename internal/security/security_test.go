package security

import "testing"

func TestVaultAndMissingKey(t *testing.T) {
	d := t.TempDir()
	v, e := LoadVault(d, false)
	if e != nil {
		t.Fatal(e)
	}
	c, e := v.Encrypt("secret-platform-key")
	if e != nil {
		t.Fatal(e)
	}
	if c == "secret-platform-key" {
		t.Fatal("unencrypted")
	}
	p, e := v.Decrypt(c)
	if e != nil || p != "secret-platform-key" {
		t.Fatal(p, e)
	}
	other, e := LoadVault(t.TempDir(), false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = other.Decrypt(c); e == nil {
		t.Fatal("wrong key accepted")
	}
	if _, e = LoadVault(t.TempDir(), true); e == nil {
		t.Fatal("missing existing key accepted")
	}
}
func TestPasswordAndSession(t *testing.T) {
	h, e := PasswordHash("correct-horse-12345")
	if e != nil {
		t.Fatal(e)
	}
	if !VerifyPassword(h, "correct-horse-12345") || VerifyPassword(h, "incorrect-password") {
		t.Fatal("password verification")
	}
	if HashToken("session") == "session" {
		t.Fatal("plaintext token")
	}
}
