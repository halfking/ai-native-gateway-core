package deploygate_test

// deploy_gate_guard_domain_selection_gate_test.go —— 门：部署闸门的**选包判据**
// 必须覆盖「改动的**任何**文件」，★ 而不只是「改动的 .go 文件」
// （★ 2026-11-23 N10 根因收口）。
//
// # 这道门守什么
//
// `scripts/deploy-test-gate.sh` 原本按【改动的 .go 文件所在目录】选包跑测试。
//
// ⇒ ★ **纯 .sql / 纯 .sh 改动一个包都不触发** ⇒ ★ 回落 FALLBACK_PACKAGES。
//
// ★ 而**门不住 .go 文件里**：
//   ★ 「canonical ≡ embeddata 副本逐字节一致」「物化视图别名列闭合」
//     「迁移预算」「自登记」「DDL 形态」⇒ 全在 `./sql/migrations/startup`
//     （★ **全是 _test.go** ⇒ ★ .go 规则**永远选不到**）；
//   ★ 「部署脚本不得用相对 $(dirname "$0") 解析同目录脚本」⇒ 在
//     `./scripts/deploygate`（★ **也只有 _test.go** ⇒ ★ 同样选不到）。
//
// ★ 实测（211 seq 3230 前后，★ 一天内两次）：★ 漏改 embeddata 副本两次
//   （★ `761e641ed` D2 去重改写、★ `d587edc47` 加关并行 SET）⇒ ★ 两次
//   「副本身份门」都是**红的**、★ ★ 却没有任何一步在部署里看见它。
//
// # 判据（四条，★ 前两条行为、★ 后两条形态）
//
// ① 【行为】★ 把 `dd_guard_domain_packages` **原样抽出来**在 bash 里跑：
//    · 改动含 `sql/migrations/startup/*.sql` ⇒ 必须选中 `./sql/migrations/startup`；
//    · 改动含 `installer/**/embeddata/startup/*.sql` ⇒ 同样必须选中；
//    · 改动含 `scripts/deploy-*.sh` ⇒ 必须选中 `./scripts/deploygate`；
//    · 改动含 `scripts/deploy-lib/*.sh` ⇒ 同样必须选中；
//    · 改动只有前端 / 其它文件 ⇒ 必须**什么都不输出**（★ 不许误伤）。
// ② 【对照臂】★ 把映射表换成**永不匹配**的形态 ⇒ 必须什么都不输出
//    ⇒ ⇒ 证明判据看得见这条规则（★ 不是恒绿）。
// ③ 【死规则】★ 映射表里**每条规则都必须命中仓内至少一个真实文件**
//    ⇒ 否则那条规则是死的（★ 它承诺的门永远不会因为对应改动而入选）。
// ④ 【可指向性】★ 映射到的**每个包都必须存在且含 _test.go**
//    ⇒ 否则闸门会选中一个不存在的包 ⇒ `go test` 不产出包级结果行
//    ⇒ ⇒ assert_full_coverage 判「读数不全」⇒ 把部署**判死**。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const gateScriptRel = "../../scripts/deploy-test-gate.sh"

// extractBlock —— 从闸门脚本里**原样**抽出从 startMarker 起、到以 endMarker 结尾的块。
func extractBlock(t *testing.T, startMarker, endMarker string) string {
	t.Helper()
	b, err := os.ReadFile(gateScriptRel)
	if err != nil {
		t.Fatalf("读闸门脚本 %s: %v", gateScriptRel, err)
	}
	lines := strings.Split(string(b), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), startMarker) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("闸门脚本里找不到 %s（★ 接线被删了？★ 见文件头「缺口」段）", startMarker)
	}
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == endMarker {
			return strings.Join(lines[start:i+1], "\n")
		}
	}
	t.Fatalf("%s 没有正常闭合", startMarker)
	return ""
}

// gateDomainTable —— 抽出映射表（★ 一行一条 "正则|门包"）。
func gateDomainTable(t *testing.T) []string {
	t.Helper()
	block := extractBlock(t, "DD_GUARDED_DOMAINS=(", ")")
	var rows []string
	for _, ln := range strings.Split(block, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || ln == "DD_GUARDED_DOMAINS=(" || ln == ")" {
			continue
		}
		// ★ bash 语义：★ 数组字面量里以 # 开头的行就是**注释**、★ 不参与取值。
		if strings.HasPrefix(ln, "#") {
			continue
		}
		rows = append(rows, strings.Trim(ln, `"`))
	}
	return rows
}

// runGuardFunc —— 在 bash 里跑抽出来的判据函数（★ 仓内真文件零改动）。
func runGuardFunc(t *testing.T, tableBlock, funcBody string, changed string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "gate.sh")
	body := "set -euo pipefail\n" + tableBlock + "\n" + funcBody + "\n" +
		"dd_guard_domain_packages \"$1\"\n"
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		t.Fatalf("写临时脚本: %v", err)
	}
	out, err := exec.Command("bash", script, changed).CombinedOutput()
	if err != nil {
		t.Fatalf("跑判据函数失败（changed=%q）: %v\n%s", changed, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestDeployGateSelectsGuardDomainPackages(t *testing.T) {
	funcBody := extractBlock(t, "dd_guard_domain_packages() {", "}")

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
			// ★ 这条是「正则里自带 '|'」的活体样本：★ 若按**第一个** '|' 切分
			//   ⇒ 正则被劈成 `^installer/migrations/.*/(up` ⇒ 编译不过 ⇒ 这条规则
			//   **永远不生效** ⇒ 而「改了离线迁移副本却没人跑三方门」正是 P1 的根因。
			name:    "离线副本改动 ⇒ 必须选中三方门（★ 规则正则自带 '|'）",
			changed: "installer/migrations/994_session_analytics_owner_key_and_close_path/up.sql",
			want:    "./internal/sqlguard\n./sql/migrations/startup",
		},
		{
			name:    "离线副本 down.sql 改动 ⇒ 必须选中三方门",
			changed: "installer/migrations/994_session_analytics_owner_key_and_close_path/down.sql",
			want:    "./internal/sqlguard",
		},
		{
			name:    "部署脚本改动 ⇒ 必须选中部署脚本门（★ 该包只有 _test.go ⇒ .go 规则选不到）",
			changed: "scripts/deploy-seamless-docker.sh",
			want:    "./scripts/deploygate",
		},
		{
			name:    "多文件混合 ⇒ 两个门都要选中",
			changed: "scripts/deploy-211.sh\nsql/migrations/startup/999_session_summary_layers.sql",
			want:    "./sql/migrations/startup\n./scripts/deploygate",
		},
		{
			name:    "非守卫域改动 ⇒ 不得输出（★ 不许把重包拖进每次部署）",
			changed: "web/src/components/live-topology/TopologyCanvas.vue\nadmin/logs.go",
			want:    "",
		},
		{
			name:    "空改动 ⇒ 不得输出",
			changed: "",
			want:    "",
		},
	}

	tableBlock := extractBlock(t, "DD_GUARDED_DOMAINS=(", ")")
	for _, tc := range cases {
		got := runGuardFunc(t, tableBlock, funcBody, tc.changed)
		if got != tc.want {
			t.Errorf("%s：判据输出 %q，期望 %q", tc.name, got, tc.want)
		}
	}
}

func TestDeployGateGuardDomainSelectionHasTeeth(t *testing.T) {
	tableBlock := extractBlock(t, "DD_GUARDED_DOMAINS=(", ")")
	funcBody := extractBlock(t, "dd_guard_domain_packages() {", "}")

	// ★ 对照臂：★ 把**每条**规则换成永不匹配的形态 ⇒ 必须什么都不输出。
	brokenTable := regexp.MustCompile(`"[^"]*\|[^"]*"`).ReplaceAllString(tableBlock, `"^__never_match__$|./__none__"`)
	brokenFunc := strings.Replace(funcBody,
		`printf '%s\n' "${DD_GUARDED_DOMAINS[@]}"`,
		`printf '%s\n' "${DD_GUARDED_DOMAINS[@]}"`, 1)
	if brokenTable == tableBlock {
		t.Fatalf("对照臂没改到映射表（★ 表的形态变了？★ 请同步更新本测试）")
	}
	got := runGuardFunc(t, brokenTable, brokenFunc,
		"sql/migrations/startup/994_session_analytics_owner_key_and_close_path.sql")
	if got != "" {
		t.Errorf("对照臂判据仍输出 %q ⇒ 这道门恒绿 ⇒ 接线被删也发现不了", got)
	}

	// ★ 正面确认：★ 原表对同一条输入**必须**输出（★ 否则上面那条 PASS 是假的）。
	real := runGuardFunc(t, tableBlock, funcBody,
		"sql/migrations/startup/994_session_analytics_owner_key_and_close_path.sql")
	if real != "./sql/migrations/startup" {
		t.Errorf("原表对迁移改动输出 %q，期望 ./sql/migrations/startup", real)
	}
}

// TestGuardDomainTableHasNoDeadRules —— ★ 每条规则都必须命中**git 跟踪**的真实文件。
//
// ★ 为什么以 `git ls-files` 为基准（★ 而不是扫文件系统）：
//
//	★ 闸门的改动清单来自 `git diff --name-only` ⇒ ★ **只有 git 跟踪的文件**
//	  才可能出现在清单里；
//	★ 而仓内 `scripts/deploy-lib` 是**指向仓外的符号链接**
//	  （★ `../../../deploy-lib`）⇒ ★ 它的内容**不在本仓的跟踪集里**
//	  ⇒ ★ 为它写规则 = 写一条**永远不触发**的死规则
//	  ⇒ ★ （★ 本轮实测：★ 守卫当场把这条规则判成死规则、★ 照此删掉了它。）
func TestGuardDomainTableHasNoDeadRules(t *testing.T) {
	rows := gateDomainTable(t)
	if len(rows) == 0 {
		t.Fatal("映射表是空的 ⇒ 选包判据退回「只看 .go 文件」的盲区")
	}
	tracked := gitTrackedFiles(t)
	for _, row := range rows {
		re, _, ok := splitDomainRow(row)
		if !ok {
			t.Errorf("映射表条目格式不对（期望 \"<正则>|<门包>\"）: %q", row)
			continue
		}
		compiled, err := regexp.Compile(re)
		if err != nil {
			t.Errorf("映射表里的正则编译不过: %q（%v）", re, err)
			continue
		}
		hits := 0
		for _, f := range tracked {
			if compiled.MatchString(f) {
				hits++
			}
		}
		if hits == 0 {
			t.Errorf("映射表条目是【死规则】（★ git 跟踪的文件里没有任何一个命中它）: %q ⇒ "+
				"它承诺的门永远不会因为对应改动而入选", row)
		}
	}
}

// splitDomainRow —— 把映射表的一行拆成「正则 | 门包」。
//
// ★★ 必须按**最后一个** '|' 拆（★ 不是第一个）：★ 正则里**自带** '|' 是合法的
//
//	（★ 如 `^installer/migrations/.*/(up|down)\.sql$`）⇒ 按第一个拆会把正则
//	从中间劈开（★ 实测：★ 正则变成 `^installer/migrations/.*/(up` ⇒ 编译不过
//	⇒ ★ 那条规则**永远不生效**、★ 门包名也变成错的 ⇒ 会选中一个不存在的包）。
//	⇒ ★ 门包路径里不会出现 '|' ⇒ 按最后一个拆必然拆对。
func splitDomainRow(row string) (re string, pkg string, ok bool) {
	i := strings.LastIndex(row, "|")
	if i <= 0 || i == len(row)-1 {
		return "", "", false
	}
	return row[:i], row[i+1:], true
}

// gitTrackedFiles —— 本仓 git 跟踪的文件清单（★ 改动清单的真正来源域）。
func gitTrackedFiles(t *testing.T) []string {
	t.Helper()
	// ★ 必须在**仓根**跑：★ git ls-files 默认只列【当前目录】下的文件
	//   （★ 测试的 cwd 是包目录 scripts/deploygate ⇒ 不加 -C 会只列这一个小目录）。
	// ★ -z 化（2026-11-25 12h 修订审计轮）：★ `--name-only` 形态默认受
	// ★ core.quotePath 转义 ⇒ 非 ASCII 路径被转成八进制＋外层双引号 ⇒ ★
	// ★ 而这里要把路径**逐字**与仓内真实路径比对（⇒ 门选中的包路径必须真实存在）
	// ★ ⇒ 必须走 `-z` 拿原始 UTF-8 字节 ★
	out, err := exec.Command("git", "-C", "../..", "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, ln := range strings.Split(string(out), "\x00") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			files = append(files, filepath.ToSlash(ln))
		}
	}
	return files
}

// TestGuardDomainPackagesExistAndHaveTests —— ★ 映射到的包必须存在且含 _test.go。
//
// ★ 否则闸门会选中一个不存在的包 ⇒ `go test` 不产出包级结果行
// ⇒ ⇒ assert_full_coverage 判「读数不全」⇒ 把部署**判死**。
func TestGuardDomainPackagesExistAndHaveTests(t *testing.T) {
	rows := gateDomainTable(t)
	seen := map[string]bool{}
	for _, row := range rows {
		_, pkg, ok := splitDomainRow(row)
		if !ok {
			continue
		}
		dir := strings.TrimPrefix(pkg, "./")
		if seen[pkg] {
			continue
		}
		seen[pkg] = true
		info, err := os.Stat(filepath.Join("../../", dir))
		if err != nil || !info.IsDir() {
			t.Errorf("映射表指向的包不存在: %s（★ 闸门会选中它 ⇒ go test 无结果行 ⇒ 部署判死）", pkg)
			continue
		}
		entries, err := os.ReadDir(filepath.Join("../../", dir))
		if err != nil {
			t.Errorf("读包目录 %s: %v", dir, err)
			continue
		}
		hasTest := false
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), "_test.go") {
				hasTest = true
				break
			}
		}
		if !hasTest {
			t.Errorf("映射表指向的包里没有 _test.go: %s（★ 选它没有意义）", pkg)
		}
	}
}

// TestDeployGateSkipsPackagesMissingFromBaselineTree —— ★ 基线树缺包时必须跳过。
//
// ★ 场景：★ 基线树 = `git merge-base HEAD origin/main` ⇒ ★ 若某个门包是本轮才
// ★ 新建的，★ 基线树里没有它 ⇒ ★ `go test ./该包` 不产出包级结果行
// ⇒ ★ assert_full_coverage 会判「读数不全」⇒ ★ **把基线跑判死**
// ⇒ ★ 而基线红只是**告警**、★ 不该变成拦路。
func TestDeployGateSkipsPackagesMissingFromBaselineTree(t *testing.T) {
	block := extractBlock(t, "run_tree() {", "}")
	if !strings.Contains(block, `[[ -d "$tree/$d" ]]`) {
		t.Errorf("run_tree 没有按「该树里是否存在这个包」过滤 ⇒ " +
			"基线树早于某个门包时会把基线跑判死（★ 读数不全 ⇒ 拒绝上线）")
	}
}
