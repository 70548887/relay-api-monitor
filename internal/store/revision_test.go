package store

import "testing"

func TestConfigurationRevisionIgnoresCallerIncrementAndSyncHealth(t *testing.T) {
	s, ctx, c, _ := fixture(t)
	before, err := s.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	edited := *before
	edited.Revision += 100
	edited.Name = "changed"
	if err = s.SaveChannel(ctx, edited); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision+1 {
		t.Fatalf("save revision=%d expected %d", after.Revision, before.Revision+1)
	}
	if err = s.SyncModels(ctx, c.ID, []string{"upstream"}); err != nil {
		t.Fatal(err)
	}
	synced, err := s.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if synced.Revision != after.Revision || synced.LastSyncAt == nil || synced.SyncError != "" {
		t.Fatal("successful discovery changed configuration revision", synced)
	}
	if err = s.SetChannelSyncError(ctx, c.ID, "temporarily unavailable"); err != nil {
		t.Fatal(err)
	}
	failed, err := s.GetChannel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Revision != after.Revision || failed.SyncError == "" {
		t.Fatal("discovery health changed revision", failed)
	}
}
