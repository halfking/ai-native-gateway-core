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
			case ".git", "vendor", "node_modules", "docs", "installer",
				".audit", ".audit-validation", ".audit-workspace":
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

// TestRequestLogsWriteEnabledDefaultsFalseWhenStoreUninitialised: a validator
// wired before settings.Global is populated must report the documented default.
//
// 2026-10-20 (audit D0): the documented default **changed** from true to
// false. The old contract is recorded here rather than deleted, because the
// test's purpose did not change:
//
//   - Old: "true (keep double-writing)". Its rationale was that false would
//     be read as "v1 is frozen" and s4GateVerdictOf would treat the S4
//     cutover gate as un-evaluable — voiding the gate for a reason that had
//     nothing to do with drift.
//   - New: "false (do not double-write)". The same reasoning now applies in
//     the opposite direction: the *code* default must not be "keep writing the
//     retired wide family". The S4 gate is voided by a false value either way
//     (there is no v1 side left to compare against), but now it is void
//     *because v1 is off on purpose*, which is a different operator message
//     than "the gate could not be evaluated".
//
// So the invariant that survived the change is the one this test still pins:
// the accessor must never invent a value. With an uninitialised store it must
// return the **declared** default — and the declared default is now false.
func TestRequestLogsWriteEnabledDefaultsFalseWhenStoreUninitialised(t *testing.T) {
	saved := Global
	t.Cleanup(func() { Global = saved })
	Global = nil
	if RequestLogsWriteEnabled() {
		t.Fatal("Global 未初始化时必须回落到默认 false（不双写）：" +
			"回退 true 会让未显式配置的环境（新装/克隆/灾备）继续写已退役的宽族，" +
			"使「双写已下线」只是某台机器的配置事实而不是代码事实")
	}
}

// TestRequestLogsWriteEnabledCodeDefaultIsOff pins the D0 fix from all three
// ends, because a default flip is exactly the change that can make a gate
// green from both sides at once (LESSONS: 改「默认值」会让既有判据同时从两端假绿).
//
//  1. **declared** — the spec table says Default: false. This is the value
//     the effective-read path falls back to.
//  2. **fallback** — the accessor with Global == nil returns false. This is
//     the value read before the registry exists.
//  3. **effective** — with a registry that has the spec registered, an env
//     backend, and no DB row, the accessor returns false. Without this leg the
//     test would pass even if the effective-read path silently resolved to
//     something else (e.g. a stale hard-coded true inside getPlatformBool).
//
// Leg 3 is the one that can go red for a reason that has nothing to do with
// the declared default (an env var set in the ambient environment), so the
// env var is explicitly cleared rather than assumed unset.
func TestRequestLogsWriteEnabledCodeDefaultIsOff(t *testing.T) {
	// Leg 1 — declared default.
	var spec *Spec
	for _, candidate := range StorageSpecs() {
		if candidate.Key == KeyRequestLogsWriteEnabled {
			spec = candidate
			break
		}
	}
	if spec == nil {
		t.Fatalf("spec 表未登记键 %q", KeyRequestLogsWriteEnabled)
	}
	if spec.Default != false {
		t.Fatalf("storage.request_logs_write_enabled 的声明默认值 = %v，必须是 false："+
			"「未显式配置 ⇒ 不双写」必须是代码事实，而不是每台机器各自配置的事实",
			spec.Default)
	}

	saved := Global
	t.Cleanup(func() { Global = saved })

	// Leg 2 — accessor fallback with no registry at all.
	Global = nil
	if RequestLogsWriteEnabled() {
		t.Fatal("Global == nil 时访问器必须回退 false")
	}

	// Leg 3 — effective read through an initialised registry (no DB row, no env).
	t.Setenv(EnvNameAuto(KeyRequestLogsWriteEnabled), "")
	registry := NewRegistry()
	for _, candidate := range StorageSpecs() {
		if err := registry.RegisterSpec(candidate); err != nil {
			t.Fatalf("register spec %s: %v", candidate.Key, err)
		}
	}
	registry.RegisterBackend(EnvBackendScope, NewStoreEnv())
	Global = registry
	if RequestLogsWriteEnabled() {
		t.Fatal("已初始化但未配置（无 settings_kv 行、无环境变量）时必须读到 false")
	}
}
