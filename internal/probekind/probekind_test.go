// internal/probekind/probekind_test.go — 单一真值源的自检门
//
// probekind 的规范数据是**字面量**（不能从代码推导，否则就成了"推导公式
// 错了但没人知道"）。因此它的正确性必须由本文件持续证明：
//
//	① TestProbeActorsCoverEveryRealWriter
//	   扫全仓 `X-LLM-Origin-Actor` 的字面量取值 + telemetry 层的 actor 赋值，
//	   验证**没有任何真实写入者**漏在 Writers 表外。
//	   ⇒ 新增一个探测器却忘了登记 ⇒ 本门红。
//
//	② TestProbeStagesMatchMigrationCheck
//	   读 sql/migrations/domain/341_probe_origin_and_node_probe.sql 的
//	   CHECK 约束，验证 ProbeStages ∪ {business} 与它**逐字一致**。
//	   ⇒ 改了 ProbeStages 却没写迁移 ⇒ 本门红。
//
//	③ TestWritersStagesAreProbeStages
//   Writers 里每个 actor 声明的 stage 都必须在 ProbeStages 里。
//
//	④ TestProbeActorSetIsSortedAndUnique
//   ProbeActors 必须有序且无重复 —— 它会被渲染进 SQL，顺序漂移会让
//   diff 噪声变大，重复会让 NOT IN 列表出现冗余项。
//
//	⑤ TestSQLActorListQuoting
//   SQL 渲染的引号/分隔符形态。
package probekind

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// repoRoot 从本包目录往上找 go.mod 定位仓库根。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("go.mod not found walking up from package dir")
	return ""
}

// TestProbeActorsCoverEveryRealWriter 验证 Writers 表覆盖全仓所有真实
// 探测写入者。
//
// 扫两种写入形态：
//   - `X-LLM-Origin-Actor", <expr>`（HTTP 头声明）
//   - `OriginActor: <expr>`（telemetry 直写）
//
// ⚠ 量具教训（2026-10-23）：第一版门只认**字面量**参数，于是漏掉了
//   - `X-LLM-Origin-Actor", OriginActorMockProbeClient`（常量引用）
//   - `OriginActor: strPtrTelemetry(actor)`（变量引用，actor 来自上文
//     `actor = "active-probe-worker"`）
// 两个**真实**写入点被判成"凭想象登记" ⇒ 门红了但原因在量具。
// 现在的门对每处写入同时解两种形态：字面量直接取；标识符回溯到同文件里
// 的常量定义 / 变量赋值。
//
// 排除项：
//   - `admin/auto_title_generator.go` 的 "auto-title-generator" —— 那是
//     业务侧的自动标题生成器，**不是**探测。
//   - 测试文件。
func TestProbeActorsCoverEveryRealWriter(t *testing.T) {
	root := repoRoot(t)

	// 非探测的 actor（业务侧）。
	nonProbe := map[string]string{
		"auto-title-generator": "admin/auto_title_generator.go — 业务侧自动标题生成，不是探测",
	}

	// 抓 "X-LLM-Origin-Actor" / "OriginActor:" 之后的参数表达式。
	headerRe := regexp.MustCompile(`X-LLM-Origin-Actor"\s*,\s*([^\n]+)`)
	directRe := regexp.MustCompile(`OriginActor:\s*([^\n]+)`)
	// 参数里的字面量。
	literalRe := regexp.MustCompile(`"([^"]+)"`)
	// 参数里的标识符（常量名 / 变量名）。
	identRe := regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\b`)

	found := map[string]string{} // actor → 第一个发现它的文件
	scanErrs := 0

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == "vendor" || base == ".git" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			scanErrs++
			return nil
		}
		src := string(data)
		rel, _ := filepath.Rel(root, path)

		for _, re := range []*regexp.Regexp{headerRe, directRe} {
			for _, m := range re.FindAllStringSubmatch(src, -1) {
				arg := m[1]
				// 形态 A：参数里直接带字面量。
				for _, lit := range literalRe.FindAllStringSubmatch(arg, -1) {
					checkActor(t, lit[1], rel, nonProbe, found)
				}
				// 形态 B：参数是标识符（常量 / 变量）⇒ 回溯同文件里的赋值。
				for _, id := range identRe.FindAllStringSubmatch(arg, -1) {
					name := id[1]
					if name == "strPtrTelemetry" || name == "strPtr" || name == "string" {
						continue
					}
					// 常量：const X = "v"  /  X = "v"
					assignRe := regexp.MustCompile(`(?m)\b` + regexp.QuoteMeta(name) + `\s*(?::\s*string\s*)?=\s*"([^"]+)"`)
					for _, as := range assignRe.FindAllStringSubmatch(src, -1) {
						checkActor(t, as[1], rel, nonProbe, found)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	if scanErrs > 0 {
		t.Errorf("walk skipped %d unreadable file(s); the scan is incomplete", scanErrs)
	}

	// 反向：Writers 里每个 actor 都必须**真的**被写到 —— 否则它是一个
	// 凭想象登记的 actor（会让 SQL 谓词多出无意义的排除项）。
	for actor := range Writers {
		if _, ok := found[actor]; !ok {
			t.Errorf("Writers 里的 actor %q 在全仓找不到任何写入点 —— 它是凭想象登记的。\n"+
				"  请删除它，或补上真实的写入点。（扫描范围：非测试 .go 文件）", actor)
		}
	}
}

// checkActor 校验一个扫出来的 actor 值是否在 Writers 里。
func checkActor(t *testing.T, actor, rel string, nonProbe map[string]string, found map[string]string) {
	t.Helper()
	if actor == "" {
		return
	}
	if _, skip := nonProbe[actor]; skip {
		return
	}
	if _, ok := Writers[actor]; !ok {
		t.Errorf("origin_actor=%q 在 %s 里被写入，但不在 internal/probekind.Writers 里。\n"+
			"  ⇒ 该探测器的行会被 stats/board 端当成业务流量（板上数字虚高），\n"+
			"    也会被 dashboard 的 actor 兜底路径漏掉。\n"+
			"  修法：把它登记进 internal/probekind.Writers（Stage 填它声明的 origin_stage）。",
			actor, rel)
		return
	}
	if _, dup := found[actor]; !dup {
		found[actor] = rel
	}
}

// TestProbeStagesMatchMigrationCheck 验证 ProbeStages ∪ {business} 与
// migration 341 的 DB CHECK 约束逐字一致。
//
// 这是"中间件认、DB 拒"的守卫：isValidOriginStage（中间件闸门）与
// migration 341（落库约束）必须是同一个集合，否则会出现写库失败的行。
func TestProbeStagesMatchMigrationCheck(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "sql", "migrations", "domain", "341_probe_origin_and_node_probe.sql")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration 341: %v", err)
	}
	text := string(src)

	// 取 CHECK 约束里 origin_stage IN (...) 的那段。
	marker := "origin_stage IS NULL OR origin_stage IN ("
	start := strings.Index(text, marker)
	if start < 0 {
		t.Fatal("origin_stage CHECK constraint not found in migration 341")
	}
	// 从 "IN (" 之后开始取，直到配平的 ")"。
	rest := text[start+len(marker):]
	end := strings.Index(rest, ")")
	if end < 0 {
		t.Fatal("could not delimit the origin_stage IN list")
	}
	list := rest[:end]

	got := map[string]bool{}
	for _, part := range strings.Split(list, ",") {
		v := strings.Trim(strings.TrimSpace(part), "'")
		if v != "" {
			got[v] = true
		}
	}

	want := map[string]bool{}
	for _, s := range ValidStages() {
		want[s] = true
	}

	for s := range want {
		if !got[s] {
			t.Errorf("stage %q 在 internal/probekind.ProbeStages 里，但 migration 341 的 CHECK 不接受它。\n"+
				"  ⇒ 写库会被 DB 拒（23514）。要么把 %q 加进 migration 341（并写新迁移扩 CHECK），\n"+
				"  要么从 probekind.ProbeStages 里拿掉。", s, s)
		}
	}
	for s := range got {
		if !want[s] {
			t.Errorf("stage %q 被 migration 341 的 CHECK 接受，但不在 internal/probekind.ProbeStages 里。\n"+
				"  ⇒ 中间件闸门会把它降级成 business，落库值与 schema 语义不符。\n"+
				"  请把它加进 probekind.ProbeStages。", s)
		}
	}
}

// TestWritersStagesAreProbeStages 验证 Writers 声明的 stage 都是探测 stage。
func TestWritersStagesAreProbeStages(t *testing.T) {
	for actor, w := range Writers {
		if !IsProbeStage(w.Stage) {
			t.Errorf("Writers[%q].Stage = %q，不在 ProbeStages 里。\n"+
				"  ⇒ 该 actor 写出的行会被 dashboard/侧表判为非探测，actor 兜底路径也失效。", actor, w.Stage)
		}
	}
}

// TestProbeActorSetIsSortedAndUnique 验证 ProbeActors 有序且无重复。
// 它会被渲染进 SQL ⇒ 顺序漂移会让 diff 噪声变大。
func TestProbeActorSetIsSortedAndUnique(t *testing.T) {
	if !sort.StringsAreSorted(ProbeActors) {
		t.Errorf("ProbeActors 必须有序（当前 %v）—— 它会被渲染进 SQL 谓词。", ProbeActors)
	}
	seen := map[string]bool{}
	for _, a := range ProbeActors {
		if seen[a] {
			t.Errorf("ProbeActors 有重复项 %q", a)
		}
		seen[a] = true
	}
}

// TestSQLActorListQuoting 验证 SQL 渲染形态：单引号 + 逗号分隔。
func TestSQLActorListQuoting(t *testing.T) {
	got := SQLActorList()
	if got == "" {
		t.Fatal("SQLActorList is empty")
	}
	parts := strings.Split(got, ",")
	if len(parts) != len(ProbeActors) {
		t.Fatalf("SQLActorList produced %d items, want %d (%q)", len(parts), len(ProbeActors), got)
	}
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if !strings.HasPrefix(p, "'") || !strings.HasSuffix(p, "'") {
			t.Errorf("item %d = %q, want single-quoted", i, p)
		}
		if unq := strings.Trim(p, "'"); unq != ProbeActors[i] {
			t.Errorf("item %d = %q, want %q", i, unq, ProbeActors[i])
		}
	}
}

// TestIsProbeCoversStageAndMarkers 验证组合判定的两条路径各自成立。
func TestIsProbeCoversStageAndMarkers(t *testing.T) {
	// stage 路径
	if !IsProbe("self_check", "", nil, "") {
		t.Error("origin_stage='self_check' 必须是探测")
	}
	// actor 路径（stage 丢失时的兜底）
	if !IsProbe("", "", nil, "self-check-worker") {
		t.Error("origin_actor='self-check-worker' 必须是探测（stage 丢失时的兜底）")
	}
	// task_type 路径
	if !IsProbe("", "probe_triggered", nil, "") {
		t.Error("task_type='probe_triggered' 必须是探测")
	}
	// quality_flags 路径
	if !IsProbe("", "", []string{"probe"}, "") {
		t.Error("quality_flags 含 'probe' 必须是探测")
	}
	// 业务行
	if IsProbe("business", "chat", []string{"empty_tool_name"}, "auto-title-generator") {
		t.Error("普通业务行不得被判为探测")
	}
}
