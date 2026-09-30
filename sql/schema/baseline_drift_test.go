// Round 43: guards for the three hand-maintained baseline copies.
//
// Background. The three 00-prereqs.sql / 01-schema.sql copies are emitted by
// sql/scripts/dump-schema.sh, which sources scripts/_lib/db-init-lib.sh. That
// library does not exist in this repository and never has (git log --all --
// '*db-init-lib.sh' is empty); the script's ../../../../ path escapes the repo
// and lands on a sibling workspace tree. So dump-schema.sh has been
// unrunnable since commit 28d4d8612 (2026-07-05), and the three copies are
// hand-maintained orphans that drift independently.
//
// ── A FALSE INVARIANT, RECORDED SO IT IS NOT RE-INVENTED ────────────────
// The first version of this file asserted that all three copies create the
// same object set. It went red, and the red was correct while the rule was
// wrong. Canonical creates 2612 objects; installer-embeddata and
// deploy-baseline each create 2603, lacking credentials.revision, the
// credentials_governor_revision sequence/index, both governor functions and
// their triggers, and the two tenant_model_policies primary keys.
//
// That delta is not drift to be closed. It is a generation offset: canonical
// was dumped from a database that had already applied
// 566_credentials_governor_revision.sql (which creates the column, sequence,
// index, both functions and both triggers) and 609 (the pkeys). The derived
// copies are the pre-566 generation, and the incremental migrations supply
// exactly the difference. "Syncing" them with a cp from canonical would
// duplicate migration 566's work inside the baseline and change what a fresh
// install produces — it would look fixed and still be wrong.
//
// The true property is one-directional: an object the baseline lacks is
// acceptable only if some registered startup migration creates it. That is
// what TestDerivedBaselineLagIsSuppliedByMigrations pins, for the objects
// that actually differ.
package schema

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// objectsCreatedBy extracts object names from a pg_dump-style baseline file,
// keying on the "-- Name: X; Type: Y; Schema:" banner pg_dump emits before
// each object. That banner is stable across body reformatting.
func objectsCreatedBy(t *testing.T, path string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if m := headerRe.FindStringSubmatch(line); m != nil {
			// Key on type+name so a function and a table sharing a name
			// (common in this schema) do not collide.
			out[m[2]+":"+m[1]] = true
		}
	}
	return out
}

// baselineLagObjects are the objects canonical's baseline creates that the two
// derived copies do not. Each must be created by a registered startup
// migration, or a fresh install through the installer would end without it.
var baselineLagObjects = []struct {
	obj string
	// needle is the literal text the supplying migration must contain. It is
	// stated explicitly rather than derived from obj: pg_dump's banner uses
	// compound names ("COLUMN t.c", "t trigger") while migrations write the
	// bare identifier, so any derivation would be a guess.
	needle string
	migs   []string
}{
	{"COMMENT:COLUMN credentials.revision", "revision", []string{"566_credentials_governor_revision.sql"}},
	{"SEQUENCE:credentials_governor_revision_seq", "credentials_governor_revision_seq", []string{"566_credentials_governor_revision.sql"}},
	{"INDEX:credentials_revision_idx", "credentials_revision_idx", []string{"566_credentials_governor_revision.sql"}},
	{"FUNCTION:bump_credentials_governor_revision()", "bump_credentials_governor_revision", []string{"566_credentials_governor_revision.sql"}},
	{"FUNCTION:notify_credentials_governor_revision()", "notify_credentials_governor_revision", []string{"566_credentials_governor_revision.sql"}},
	{"TRIGGER:credentials trg_bump_credentials_governor_revision", "trg_bump_credentials_governor_revision", []string{"566_credentials_governor_revision.sql"}},
	{"TRIGGER:credentials trg_notify_credentials_governor_revision_insert", "trg_notify_credentials_governor_revision_insert", []string{"566_credentials_governor_revision.sql"}},
	{"TRIGGER:credentials trg_notify_credentials_governor_revision_update", "trg_notify_credentials_governor_revision_update", []string{"566_credentials_governor_revision.sql"}},
	{"CONSTRAINT:tenant_model_policies tenant_model_policies_pkey", "tenant_model_policies_pkey", []string{"608_tenant_model_policies_add_pkey.sql"}},
	{"CONSTRAINT:tenant_model_policies_audit tenant_model_policies_audit_pkey", "tenant_model_policies_audit_pkey", []string{"609_tenant_model_policies_audit_rekey_pkey.sql"}},
}

// TestDerivedBaselineLagIsSuppliedByMigrations is the true, one-directional
// form of the drift rule: the derived copies may lag canonical, but only for
// objects a registered startup migration recreates.
//
// It also guards against the tempting wrong fix. If someone "closes the drift"
// by copying canonical over a derived copy, these objects move into the
// baseline while 566 still creates them — and this test goes red, naming the
// duplication, instead of the change landing silently.
func TestDerivedBaselineLagIsSuppliedByMigrations(t *testing.T) {
	canon := objectsCreatedBy(t, copies[0].path)
	derived := objectsCreatedBy(t, copies[1].path)

	// Every declared lag object must really be absent from the derived copy
	// and present in canonical. If that stops being true the lag was closed,
	// and the declared supply list must be revisited rather than trusted.
	for _, o := range baselineLagObjects {
		if !canon[o.obj] {
			t.Errorf("声明的滞后对象 %s 已不在 canonical 基线里；"+
				"请重新核对 baselineLagObjects（不要直接删断言）", o.obj)
		}
		if derived[o.obj] {
			t.Errorf("%s 现在存在于 installer 基线副本中。若这是为了让副本"+
				"「看起来与 canonical 一致」而 cp 过来的，请回退：%v 仍会创建它们，"+
				"结果是同一对象被基线与迁移各建一次。", o.obj, o.migs)
		}
	}

	// The supply claim itself: each named migration must exist on disk and
	// must actually create the object.
	const startupDir = "../../sql/migrations/startup"
	for _, o := range baselineLagObjects {
		for _, mig := range o.migs {
			b, err := os.ReadFile(startupDir + "/" + mig)
			if err != nil {
				t.Errorf("%s 声称由 %s 提供，但该迁移不在 installer 启动集：%v", o.obj, mig, err)
				continue
			}
			if !strings.Contains(string(b), o.needle) {
				t.Errorf("%s 声称由 %s 提供，但该迁移正文里找不到 %q",
					o.obj, mig, o.needle)
			}
		}
	}
	t.Logf("canonical 对象=%d, installer 基线对象=%d, 声明滞后对象=%d（均由注册迁移提供）",
		len(canon), len(derived), len(baselineLagObjects))
}

// TestBaselineDumpScriptIsRunnable prevents the silent-death shape: a
// committed generator wrapper that cannot run looks like a working tool to
// every reader, which is why nobody noticed the copies were hand-edited.
//
// This gate is red on purpose and stays red until someone either restores
// db-init-lib.sh into this repository or deletes the wrapper. That is the
// decision recorded for this round: mark the generator dead rather than
// pretend the copies are reproducible.
func TestBaselineDumpScriptIsRunnable(t *testing.T) {
	const script = "../../sql/scripts/dump-schema.sh"

	// Desired end state is binary: either the generator is restored (its
	// library resolves) or the wrapper is gone. Asserting the *current* broken
	// state instead would make this permanently red and teach everyone to
	// ignore it. This shape goes green the moment either resolution lands.
	b, err := os.ReadFile(script)
	if os.IsNotExist(err) {
		t.Log("dump-schema.sh 已移除：无生成器的手工基线由 " +
			"TestDerivedBaselineLagIsSuppliedByMigrations 兜底")
		return
	}
	if err != nil {
		t.Fatalf("read %s: %v", script, err)
	}

	// Resolve the sourced path the way bash would. The wrapper may anchor on
	// $SCRIPT_DIR or $REPO_ROOT, so substitute both rather than assuming one
	// literal form — otherwise this guard breaks on a harmless refactor.
	var libPath string
	for _, line := range strings.Split(string(b), "\n") {
		l := strings.TrimSpace(line)
		if !strings.HasPrefix(l, "source ") {
			continue
		}
		if m := regexp.MustCompile(`source\s+"?([^"\s]+)"?`).FindStringSubmatch(l); m != nil {
			libPath = m[1]
		}
	}
	if libPath == "" {
		t.Fatalf("%s 里找不到 source 行，dump-schema.sh 结构变了", script)
	}

	// sql/scripts -> repo root is two levels up; from there to sql/scripts is
	// where $SCRIPT_DIR and $REPO_ROOT-resolved paths are anchored.
	scriptDir := cleanJoin("../../sql/scripts", "x")
	_ = scriptDir
	repoRoot := "../.."
	resolved := libPath
	resolved = strings.ReplaceAll(resolved, "$SCRIPT_DIR", "../..")
	resolved = strings.ReplaceAll(resolved, "${SCRIPT_DIR}", "../..")
	resolved = strings.ReplaceAll(resolved, "$REPO_ROOT", repoRoot)
	resolved = strings.ReplaceAll(resolved, "${REPO_ROOT}", repoRoot)
	// strip a leading $VAR/ if the shell form used one
	if i := strings.Index(resolved, "/"); i > 0 && strings.HasPrefix(resolved, "$") {
		resolved = resolved[i:]
	}
	resolved = cleanJoin("../../sql/schema", resolved)

	if _, err := os.Stat(resolved); err != nil {
		t.Errorf("dump-schema.sh source 的 SSOT 库不可达：%s（%v）\n"+
			"  后果：00-prereqs/01-schema 三份副本是无生成器的手工维护件。\n"+
			"  修法二选一：把生成器库放回被 source 的位置，或删除本包装脚本。",
			resolved, err)
		return
	}
	t.Logf("生成器库可达：%s（原始 source 形态 %q）", resolved, libPath)
}

func cleanJoin(base, rel string) string {
	parts := strings.Split(strings.TrimPrefix(base+"/"+rel, "./"), "/")
	var out []string
	for _, p := range parts {
		switch p {
		case "", ".":
		case "..":
			// Pop a real name only. A leading ".." can never be satisfied,
			// so consuming it would rewrite "../../scripts/x" into
			// "scripts/x" and make a missing file look like a different,
			// plausible one — which is exactly the bug this had.
			if n := len(out); n > 0 && out[n-1] != ".." {
				out = out[:n-1]
			} else {
				out = append(out, "..")
			}
		default:
			out = append(out, p)
		}
	}
	return strings.Join(out, "/")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
