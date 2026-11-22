package deploygate_test

// deploy_gate_migration_package_selection_gate_test.go —— 门：部署闸门**必须**
// 在改动触及**迁移 SQL** 时选中迁移包（★ 2026-11-23 补）。
//
// # 这道门守什么
//
// `scripts/deploy-test-gate.sh` 按【改动的 .go 文件所在目录】选包跑测试
// （`git diff --name-only` → 取 .go 的 dirname → 该目录有 *_test.go 就整包跑）。
//
// ⇒ ★ **纯 .sql 改动**（★ 典型形态：给某条迁移加一行 `SET`、改一条注释）
//   **一个包都不触发** ⇒ ★ 回落 FALLBACK_PACKAGES（admin / sanitizerules / …）。
//
// ★ 而迁移族的门全住在 `./sql/migrations/startup`（★ 177 个测试文件）：
// ★ 「canonical ≡ embeddata 副本逐字节一致」「物化视图别名列闭合」
// ★ 「迁移预算」「自登记」「DDL 形态」…… ⇒ ★ 它们**永远不被跑**。
//
// # 缺口是实测出来的（★ 不是推演）
//
// ★ 2026-11-23 一天内**两次**踩到同一个形态：
//   ① 并行车 761e641ed「D2 去重修复」改写迁移 994 的 JOIN ⇒ **只改 canonical**
//      ⇒ embeddata 副本没跟 ⇒ 新装通道跑旧字节；
//   ② 并行车 d587edc47「994 迁移关并行」加 `SET max_parallel_workers_per_gather = 0`
//      ⇒ **又只改 canonical** ⇒ 新装通道仍缺这行 ⇒ 仍会撞 211 的 /dev/shm 上限。
//
// ★ 两次「副本身份门」都是**红的**，★ ★ 但没有任何一步在部署里看见它
// ⇒ ⇒ 判据与「这次改动该被谁看」之间有**盲区**。
//
// # 判据形态（★ 行为判据、★ 真跑 bash）
//
// ★ 把 `deploy-test-gate.sh` 里的 `dd_migration_gate_packages` **原样抽出来**
//   放进一个临时脚本里跑（★ 仓内真文件零改动）：
//   ① 改动含 `sql/migrations/startup/*.sql` ⇒ 必须输出 `./sql/migrations/startup`；
//   ② 改动含 `installer/**/embeddata/startup/*.sql` ⇒ 同样必须输出；
//   ③ 改动只有 `.go` / `.ts` 等 ⇒ 必须**什么都不输出**（★ 不许误伤、★ 不许把
//      整个迁移包拖进每次部署）。
// ★ 对照臂：★ 把抽出来的函数里的匹配规则**换成永不匹配**的形态 ⇒ 必须什么都不输出
//   ⇒ ⇒ 证明判据真的看得见这条规则（★ 不是恒绿）。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const gateScriptRel = "../../scripts/deploy-test-gate.sh"

// extractMigrationGateFunc —— 从闸门脚本里**原样**抽出 dd_migration_gate_packages 函数体。
func extractMigrationGateFunc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(gateScriptRel)
	if err != nil {
		t.Fatalf("读闸门脚本 %s: %v", gateScriptRel, err)
	}
	lines := strings.Split(string(b), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "dd_migration_gate_packages() {") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("闸门脚本里找不到 dd_migration_gate_packages 函数 ⇒ 接线被删了（★ 见文件头「缺口」段）")
	}
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "}" {
			return strings.Join(lines[start:i+1], "\n")
		}
	}
	t.Fatalf("dd_migration_gate_packages 函数没有正常闭合")
	return ""
}

// runGateFunc —— 在 bash 里跑抽出来的函数，★ 返回它的标准输出。
func runGateFunc(t *testing.T, funcBody string, changed string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "gate.sh")
	body := "set -euo pipefail\n" + funcBody + "\n" +
		"dd_migration_gate_packages \"$1\"\n"
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		t.Fatalf("写临时脚本: %v", err)
	}
	out, err := exec.Command("bash", script, changed).CombinedOutput()
	if err != nil {
		t.Fatalf("跑判据函数失败（changed=%q）: %v\n%s", changed, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestDeployGateSelectsMigrationPackageOnSqlChange(t *testing.T) {
	funcBody := extractMigrationGateFunc(t)

	cases := []struct {
		name    string
		changed string
		want    string
	}{
		{
			name:    "迁移 SQL 改动 ⇒ 必须选中迁移包",
			changed: "sql/migrations/startup/994_session_analytics_owner_key_and_close_path.sql",
			want:    "./sql/migrations/startup",
		},
		{
			name:    "embeddata 副本改动 ⇒ 必须选中迁移包",
			changed: "installer/cmd/llm-gw-installer/embeddata/startup/994_session_analytics_owner_key_and_close_path.sql",
			want:    "./sql/migrations/startup",
		},
		{
			name:    "多文件混合 ⇒ 仍必须选中迁移包",
			changed: "web/src/foo.ts\nsql/migrations/startup/999_session_summary_layers.sql",
			want:    "./sql/migrations/startup",
		},
		{
			name:    "非迁移改动 ⇒ 不得输出（★ 不许把重包拖进每次部署）",
			changed: "web/src/components/live-topology/TopologyCanvas.vue\nadmin/logs.go",
			want:    "",
		},
		{
			name:    "空改动 ⇒ 不得输出",
			changed: "",
			want:    "",
		},
	}

	for _, tc := range cases {
		got := runGateFunc(t, funcBody, tc.changed)
		if got != tc.want {
			t.Errorf("%s：判据输出 %q，期望 %q", tc.name, got, tc.want)
		}
	}
}

func TestDeployGateMigrationSelectionHasTeeth(t *testing.T) {
	funcBody := extractMigrationGateFunc(t)

	// ★ 对照臂：★ 把匹配规则换成**永不匹配**的形态 ⇒ 必须什么都不输出。
	broken := strings.Replace(funcBody,
		`'^(sql/migrations/startup/.*\.sql|installer/.*/embeddata/startup/.*\.sql)$'`,
		`'^__never_match__$'`, 1)
	if broken == funcBody {
		t.Fatalf("对照臂没改到匹配规则（★ 规则形态变了？★ 请同步更新本测试）")
	}
	got := runGateFunc(t, broken, "sql/migrations/startup/994_session_analytics_owner_key_and_close_path.sql")
	if got != "" {
		t.Errorf("对照臂判据仍输出 %q ⇒ 这道门恒绿 ⇒ 接线被删也发现不了", got)
	}

	// ★ 正面确认：★ 原函数对同一条输入**必须**输出（★ 否则上面那条 PASS 是假的）。
	real := runGateFunc(t, funcBody, "sql/migrations/startup/994_session_analytics_owner_key_and_close_path.sql")
	if real != "./sql/migrations/startup" {
		t.Errorf("原函数对迁移改动输出 %q，期望 ./sql/migrations/startup", real)
	}
}
