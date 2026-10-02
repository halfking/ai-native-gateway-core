package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRequestLogsWriteEnabledKeyIsSpelledOnce pins the S4 stop-write key.
//
// The failure this prevents is specific and already documented in the code it
// replaces: a gate spelled in several places is a gate where one place can be
// flipped without the others, and "stop-write on one path, keep writing on
// another" is exactly the split that makes the cutover unsafe. Before the
// constant there were four production spellings of this key across four
// packages; admin's own comment named the hazard while de-duplicating only
// within admin.
//
// The scan covers production .go files only. Test files legitimately spell the
// key to seed settings_kv — a test that reads its fixture from the constant
// would assert nothing about the real key. They are listed below so the
// exemption is a decision on the record rather than a blind skip.
func TestRequestLogsWriteEnabledKeyIsSpelledOnce(t *testing.T) {
	// Files allowed to contain the literal, with why. Keys are repo-relative,
	// because that is what rel below produces.
	allowed := map[string]string{
		"settings/key_request_logs_write_enabled.go": "the constant's own declaration",
		"settings/spec_storage.go":                   "the spec table that defines the key",
	}

	// Scan the repo root, which is the package dir's parent. (Two levels up
	// would be the *workspace* — an earlier draft walked there and duly
	// reported sibling checkouts as offenders, which is how the wrong base
	// surfaced at all.)
	repoRoot := ".."
	var offenders []string
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "docs", "installer":
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(repoRoot, path)
		rel = filepath.ToSlash(rel)
		if _, ok := allowed[rel]; ok {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if strings.Contains(string(raw), `"`+KeyRequestLogsWriteEnabled+`"`) {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("S4 停写键在生产代码中被字面量拼写 %d 处：%v\n"+
			"请改用 settings.KeyRequestLogsWriteEnabled / settings.RequestLogsWriteEnabled()。"+
			"一个键有几种拼法，就有可能只停写了其中一条路径。",
			len(offenders), offenders)
	}
}

// TestRequestLogsWriteEnabledMatchesSpec keeps the constant and the spec table
// from drifting apart: a renamed key in the spec that the constant does not
// follow would silently turn every reader back onto its default (true), which
// for a stop-write gate reads as "writes continue" — the safe direction, but
// also the direction that makes the cutover never happen, unnoticed.
func TestRequestLogsWriteEnabledMatchesSpec(t *testing.T) {
	raw, err := os.ReadFile("spec_storage.go")
	if err != nil {
		t.Fatalf("read spec_storage.go: %v", err)
	}
	if !strings.Contains(string(raw), "Key:             \""+KeyRequestLogsWriteEnabled+"\"") {
		t.Fatalf("spec_storage.go 未登记键 %q——常量与 spec 已漂移", KeyRequestLogsWriteEnabled)
	}
}

// TestRequestLogsWriteEnabledDefaultsTrueWhenStoreUninitialised: a validator
// wired before settings.Global is populated must report the documented default
// (writes continue) rather than false.
//
// The direction matters: false would mean "v1 is frozen", and s4GateVerdictOf
// reads a false here as "the S4 gate cannot be evaluated" — so an
// uninitialised store would void the cutover gate for a reason that has
// nothing to do with drift, and would do so silently.
func TestRequestLogsWriteEnabledDefaultsTrueWhenStoreUninitialised(t *testing.T) {
	saved := Global
	t.Cleanup(func() { Global = saved })
	Global = nil
	if !RequestLogsWriteEnabled() {
		t.Fatal("Global 未初始化时必须回落到默认 true（继续双写）：" +
			"返回 false 会被 dual-read 门读成「v1 已冻结」，从而无理由地作废 S4 判据")
	}
}
