//go:build integration

package settings

import (
	"context"
	"testing"
)

func TestOutputSwitchFirstSaveRollback(t *testing.T) {
	store, db := sessionSettingsFixture(t)
	for _, sp := range ModuleSpecs() {
		if Global.Spec(sp.Key) == nil {
			Global.MustRegisterSpec(sp)
		}
	}
	const key = "sanitize.output_check_enabled"
	for _, tc := range []struct {
		env          string
		first, prior bool
	}{{"", false, true}, {"false", true, false}} {
		t.Setenv(EnvNameAuto(key), tc.env)
		if _, err := db.Exec(context.Background(), "DELETE FROM settings_kv WHERE key=$1", key); err != nil {
			t.Fatal(err)
		}
		previous, err := store.Set(ScopePlatform, key, tc.first)
		if err != nil || string(previous) != boolJSON(tc.prior) {
			t.Fatalf("first save previous=%s err=%v", previous, err)
		}
		got, err := store.Rollback(ScopePlatform, key, "")
		if err != nil || string(got) != boolJSON(tc.prior) {
			t.Fatalf("first rollback=%s err=%v", got, err)
		}
		got, err = store.Rollback(ScopePlatform, key, "")
		if err != nil || string(got) != boolJSON(tc.first) {
			t.Fatalf("redo=%s err=%v", got, err)
		}
		if _, err = store.Set(ScopePlatform, key, tc.prior); err != nil {
			t.Fatal(err)
		}
		got, err = store.Rollback(ScopePlatform, key, "")
		if err != nil || string(got) != boolJSON(tc.first) {
			t.Fatalf("second update rollback=%s err=%v", got, err)
		}
	}
}

func boolJSON(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
