package store

import (
	"errors"
	"testing"
)

func TestPasswordChangeCannotOverwriteConcurrentReset(t *testing.T) {
	s, ctx, _, _ := fixture(t)
	u, err := s.CreateMember(ctx, "member-reset-race", "original-hash")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateMemberPassword(ctx, u.ID, "admin-reset-hash"); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateMemberPassword(ctx, u.ID, "stale-change-hash", "original-hash"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale member password accepted: %v", err)
	}
	current, _ := s.GetMemberByID(ctx, u.ID)
	if current.PasswordHash != "admin-reset-hash" {
		t.Fatal("admin reset was overwritten")
	}
	a, err := s.CreateAdmin(ctx, "admin-reset-race", "original-admin-hash")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.UpdatePassword(ctx, a.ID, "new-admin-hash"); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdatePassword(ctx, a.ID, "stale-admin-hash", "original-admin-hash"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale admin password accepted: %v", err)
	}
	admin, _ := s.GetAdminByID(ctx, a.ID)
	if admin.PasswordHash != "new-admin-hash" {
		t.Fatal("admin change was overwritten")
	}
}
