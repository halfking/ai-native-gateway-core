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
//
// ── R32 口径更新（2026-10-02，7d55159b4）────────────────────────────────
// d5932d26c 的快照收敛把 566/609 的对象 cp 进了副本，上面的「滞后态」前提
// 被翻掉：副本**出现**这些对象（收敛态）现在同样是合法形态，测试只计数不
// 打红。供给链 needle 块（每个对象必须有在册迁移且正文含对象标识）在两种
// 形态下都继续承重——这是本文件现在的牙齿，不是滞后断言。
package schema

import (
	"os"
	"regexp"
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
// derived copies historically did not; each was required to be created by a
// registered startup migration so a fresh install through the installer still
// ended with it.
//
// R32（2026-10-02）口径更新：d5932d26c（轮 31 采纳的「快照收敛」）把三份
// 基线副本 cp 成与 canonical 逐字节一致，本清单里的 10 个对象随之**进入**
// 副本——本测试因此转红，而它自己的注释正预言了这一幕。收敛是显式决策，
// 不回退：快照哲学下副本=终态，566/608/609 在 fresh-install 上退化为
// no-op（IF NOT EXISTS / OR REPLACE），仍注册以服务升级链。清单保留的
// 价值随之翻转——从「滞后必须由迁移供给」变成**双源对账**：对象仍须在
// canonical 里；供给迁移仍须在启动集且正文含对象标识（迁移被改名/瘦身
// 时红）；副本里出现与否两种状态都合法（出现=收敛态，缺席=滞后态）。
// 「副本与 canonical 定义漂移」由逐文件 md5 镜像门（installer
// TestStatsStartupMigrationsMatchCanonicalSources 家族）承担，不在本门重复。
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
// R32 口径（7d55159b4）：副本**收敛**（cp 过来）不再打红——那是合法形态，
// 测试按 converged/lagging 计数留痕。仍然打红的是供给链断裂：声明的对象
// 不在 canonical、或声称供给它的迁移不在启动集/正文不含对象标识。历史版本
// 曾断言「cp 收敛 = 重复供给 = 假修复」，该前提被快照收敛推翻后由 R32 轮
// 翻转（见文件头 R32 口径更新）。
func TestDerivedBaselineLagIsSuppliedByMigrations(t *testing.T) {
	canon := objectsCreatedBy(t, copies[0].path)
	derived := objectsCreatedBy(t, copies[1].path)

	// R32 口径（见 baselineLagObjects 注释）：对象必须在 canonical 里；
	// 副本里出现（收敛态）或缺席（滞后态）都合法——两种状态下「供给迁移
	// 仍能提供该对象」都由下方 needle 块承重。历史上这里曾断言副本必须
	// 缺席（滞后前提），快照收敛把前提翻掉后，若原样保留会永远红且指示
	// 一个错误的方向（回退收敛）。
	converged, lagging := 0, 0
	for _, o := range baselineLagObjects {
		if !canon[o.obj] {
			t.Errorf("声明的滞后对象 %s 已不在 canonical 基线里；"+
				"请重新核对 baselineLagObjects（不要直接删断言）", o.obj)
			continue
		}
		if derived[o.obj] {
			converged++
		} else {
			lagging++
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
	t.Logf("canonical 对象=%d, installer 基线对象=%d, 声明对象=%d（收敛态 %d / 滞后态 %d，供给链由 needle 块钉住）",
		len(canon), len(derived), len(baselineLagObjects), converged, lagging)
}

// TestBaselineGeneratorLibraryResolves prevents the silent-death shape: a
// committed generator wrapper whose sourced library is missing looks like a
// working tool to every reader, which is why nobody noticed the copies were
// hand-edited.
//
// WHAT THIS DOES NOT TEST, stated plainly because the previous name claimed
// otherwise: it does not run the generator. Running it needs a live SSOT
// database whose schema is known-good, and no such database exists in this
// repository — the local dev database was found to be a half-migrated
// intermediate state (migration 434's four indexes are three present,
// handoff_logs has no primary key). So "runnable" is not a property this
// repository can currently assert, and naming the test that way turned an
// unverified claim into a passing result.
//
// The split, stated so the next reader does not have to rediscover it:
//   - resolvable  — asserted here;
//   - produces a baseline that applies to an empty database — verified
//     manually on 2026-10-01 (generated baseline applied exit=0, 660
//     relations, matching the source database), NOT asserted by any test.
func TestBaselineGeneratorLibraryResolves(t *testing.T) {
	const script = "../../sql/scripts/dump-schema.sh"

	// Desired end state is binary: either the generator library is present (its
	// sourced path resolves) or the wrapper is gone. Asserting the *current*
	// broken state instead would make this permanently red and teach everyone
	// to ignore it. This shape goes green the moment either resolution lands.
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

	// sql/scripts -> repo root is two levels up; from there to sql/schema is
	// where $SCRIPT_DIR and $REPO_ROOT-resolved paths are anchored.
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
