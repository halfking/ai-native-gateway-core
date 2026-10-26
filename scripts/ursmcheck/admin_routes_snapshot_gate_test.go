package ursm_test

// admin_routes_snapshot_gate_test.go —— 「仓内路由清单快照」的**防漂移门**。
//
// # 这道门守的是哪件东西
//
// 本仓有一条长期目标：「逐个 API 验证 + 效率检查」。
// 巡检器 `scripts/audit/api-sweep.sh` 是仓内的，但它吃的**路由清单**此前只在
// **仓外**（/tmp/kx-*.txt）⇒ 别人复现不了。
//
// 而「**计数是活事实**」（§400 通用式）：本仓路由一直在长，
// 旧清单 444 条 ⇒ 当前源码重抽 503 条 ⇒ **新增 59、消失 0**
// （新增里含整族 `/api/admin/session-analytics/*`，此前从未被覆盖过）。
//
// ⇒ 所以路由清单必须变成**仓内快照 + 防漂移门**，否则它会静默腐化：
//
//	scripts/audit/extract-admin-routes.sh           ← 生成器（口径见它头部）
//	scripts/audit/asset/admin-routes.txt            ← 快照（生成产物，不要手改）
//	scripts/ursmcheck/admin_routes_snapshot_gate_test.go  ← 本门
//
// # ★ 核心断言只有一条：**快照 == 当前源码**
//
// 本门**自己调生成器**（不是复述一份拷贝），把生成器的输出与快照
// （去掉那一行 `#` 头）**逐行比对** ⇒ 不一致就红。
//
// ★ 为什么不能只做「关键字在不在」的形状门：
// 本仓反复吃过这个亏（「门绿 ≠ 事实成立」）。如果本门只断言
// 「快照文件里有 `/api/admin/session-analytics/`」，那么：
//
//	① 生成器口径被改坏（方法前缀被吞、注释里的注册被算进来、
//	   扫的范围被缩小）⇒ 门**依然绿**，而清单已经不准了。
//
// ⇒ 所以本门分三层：
//
//	★ **一致性层**（动态）：**自己跑生成器**，逐行比对快照。
//	★★ **量具层**（动态）：行数下限（> 400）+ 与快照行数相等
//	   + 快照头里自述的行数与实际相等 ⇒ 防「生成器突然只吐几条」。
//	★★★ **形状层**（静态）：已知新增族在不在、方法前缀形态拆出来没有、
//	   生成器把「已知不注册」偏差登记在注释里没有。
//
// # ★ 诚实边界（★ 这道门保证不了什么）
//
// 本门保证的是「**快照 == 当前源码**」。
//
// ★ 它**不能**保证「线上真的注册了这条路由」——
// 「源码声明」≠「实际注册」。已知至少 3 条反例：
//
//	admin/modules.go:1393-1397 的 `registerFeishuRoutes`
//	（/api/admin/feishubot/routing-rules、.../routing-rules/、/send-log）
//	源码里有 `mux.HandleFunc`，但**那个注册函数故意不被调用**
//	（源码 `routemount: disabled` 注释自证：恢复调用点需要三件一起的事，
//	  缺一件生产会从 404「诚实的 404」变成 500「relation does not exist」）。
//
//	⇒ 这 3 条**在快照里**、★ 但线上不注册 ⇒ 巡检时看到 404 **不是缺陷**。
//	⇒ 门**只**保证快照跟着源码走；它**不**保证线上行为。
//
// ★ 本门也**不是**运行时探测：它**没有**任何 404/500/延迟读数。

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// adminRoutesRelPath 是三个被守护文件**在仓内的相对路径**（正斜杠）。
//
// ★ 三条常量同时被「文件存在性」「门自己调生成器」「快照比对」几处引用 ⇒
//
//	门指错文件 ⇒ 红；文件被挪走 ⇒ 红。
const (
	adminRoutesScriptRelPath = "scripts/audit/extract-admin-routes.sh"
	adminRoutesAssetRelPath  = "scripts/audit/asset/admin-routes.txt"
	// adminRoutesRelPathSelf 是**本门自身**的仓内相对路径。
	//
	// ★ 用它做「门不许指向别人」的断言：门被搬走/改名 ⇒ 红。
	adminRoutesRelPathSelf = "scripts/ursmcheck/admin_routes_snapshot_gate_test.go"
)

// adminRoutesSessionAnalyticsPrefix 是**已知新增族**的前缀。
//
// ★ 用途：防「快照回退到旧清单」（旧清单 444 条里**没有**这一族）。
// 任取其一即可 ⇒ 这里要求**至少一条**登记命中。
const adminRoutesSessionAnalyticsPrefix = "/api/admin/session-analytics/"

// adminRoutesMinRows 是行数下限。
//
// ★ 为什么要有下限：生成器若因口径被改坏而**只吐几条**（而不是 0 条，
//
//	0 条会被生成器自己判死并 exit 70），快照会跟着变短 ⇒
//	「快照 == 生成器」这条断言**照样绿** ⇒ 恒绿门。
//
// ⇒ 用一个**与历史规模同量级**的下限（400）把这种静默缩水挡住。
const adminRoutesMinRows = 400

// adminRoutesRepoRoot 返回仓根。
//
// ★ 必须用 runtime.Caller：写死相对路径（"../.."）或依赖 os.Getwd()，
//
//	在测试的工作目录变化时会扫不到文件 ⇒ 静态守卫变成恒绿门。
func adminRoutesRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	// file = <repo>/scripts/ursmcheck/<this>.go
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓根定位可疑：%s 下没有 go.mod", root)
	}
	return root
}

// adminRoutesAbs 返回仓内相对路径的绝对路径。
func adminRoutesAbs(t *testing.T, rel string) string {
	t.Helper()
	return filepath.Join(adminRoutesRepoRoot(t), filepath.FromSlash(rel))
}

// adminRoutesReadAll 读文件全部字节。
func adminRoutesReadAll(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(adminRoutesAbs(t, rel))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", rel, err)
	}
	return string(b)
}

// adminRoutesSnapshotLines 读快照，返回**去掉 `#` 头之后**的数据行。
//
// ★ 只去**开头**那一行 `#` 头：快照的其余内容必须与生成器输出逐字一致，
//
//	所以「除了那一行，别的都不许有」这条由「数据行 == 生成器输出」保证。
func adminRoutesSnapshotLines(t *testing.T) []string {
	t.Helper()
	raw := adminRoutesReadAll(t, adminRoutesAssetRelPath)
	lines := strings.Split(raw, "\n")
	// 末尾换行 ⇒ 最后一段是空串，去掉它（但只去一个）。
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if len(lines) == 0 {
		t.Fatalf("%s 是空文件 ⇒ 快照不存在", adminRoutesAssetRelPath)
	}
	if !strings.HasPrefix(lines[0], "#") {
		t.Fatalf("%s 的第一行不是 `#` 开头的口径说明头：\n%s", adminRoutesAssetRelPath, lines[0])
	}
	return lines[1:]
}

// adminRoutesSnapshotHeader 返回快照的 `#` 头行。
func adminRoutesSnapshotHeader(t *testing.T) string {
	t.Helper()
	raw := adminRoutesReadAll(t, adminRoutesAssetRelPath)
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "#") {
		t.Fatalf("%s 的第一行不是 `#` 开头的口径说明头", adminRoutesAssetRelPath)
	}
	return lines[0]
}

// adminRoutesRunGenerator **自己调生成器**，返回它的输出行。
//
// ★ 这是本门的核心量具：门不复述一份拷贝，而是**现跑现比**。
//
// ★ 调法按契约：`bash scripts/audit/extract-admin-routes.sh --out <tmp>`，
//
//	且 cmd.Dir = 仓根 ⇒ 生成器里的**相对路径**（含它自己的 `--root` 默认值
//	与快照位置）都相对仓根解析。
func adminRoutesRunGenerator(t *testing.T) []string {
	t.Helper()
	root := adminRoutesRepoRoot(t)
	outFile := filepath.Join(t.TempDir(), "admin-routes.tsv")

	cmd := exec.Command("bash", adminRoutesScriptRelPath, "--out", outFile)
	cmd.Dir = root
	var stderr strings.Builder
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	if runErr != nil {
		t.Fatalf("生成器跑失败（%v）：\n%s", runErr, stderr.String())
	}
	b, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("读生成器产物失败：%v", err)
	}
	raw := string(b)
	if raw == "" {
		t.Fatalf("生成器产物是空的 ⇒ 量具静默失效")
	}
	lines := strings.Split(raw, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// adminRoutesDiffFirst 返回首个不一致的行号（1-based）与两侧内容。
//
// ★ 逐行比对（不是集合比对）：顺序也是快照的一部分
//
//	（生成器按 path 排序 ⇒ 排序被改坏也要红）。
func adminRoutesDiffFirst(got, want []string) (int, string, string) {
	n := len(got)
	if len(want) < n {
		n = len(want)
	}
	for i := 0; i < n; i++ {
		if got[i] != want[i] {
			return i + 1, got[i], want[i]
		}
	}
	if len(got) != len(want) {
		return n + 1, "<缺失>", "<缺失>"
	}
	return 0, "", ""
}

// TestAdminRoutesFilesExist —— 三个文件都在，脚本能解析、有可执行位。
func TestAdminRoutesFilesExist(t *testing.T) {
	for _, rel := range []string{adminRoutesScriptRelPath, adminRoutesAssetRelPath, adminRoutesRelPathSelf} {
		st, err := os.Stat(adminRoutesAbs(t, rel))
		if err != nil {
			t.Errorf("%s 不存在：%v", rel, err)
			continue
		}
		if st.IsDir() {
			t.Errorf("%s 是目录，不是文件", rel)
			continue
		}
		if rel == adminRoutesScriptRelPath {
			if st.Mode().Perm()&0111 == 0 {
				t.Errorf("%s 没有可执行位（%v）⇒ 用 `bash %s` 能跑，但直接执行会失败",
					rel, st.Mode().Perm(), rel)
			}
			// ★ `bash -n` 是最便宜的检查：能抓住绝大多数自伤（引号不闭合、heredoc 不完整）。
			out, err := exec.Command("bash", "-n", adminRoutesAbs(t, rel)).CombinedOutput()
			if err != nil {
				t.Errorf("bash -n 失败（%s）：\n%s", rel, out)
			}
		}
	}
}

// TestAdminRoutesSnapshotMatchesGenerator —— ★★ 核心断言：快照 == 当前源码。
//
// ★ 这道门**自己调生成器**，把输出与快照（去掉 `#` 头）**逐行比对**。
//
//	① 有人在源码里加了路由但没刷新快照 ⇒ 红（这正是要拦的漂移）；
//	② 有人在快照里手改了内容 ⇒ 红；
//	③ 生成器的口径被改坏（顺序、字段、注释剥离、方法前缀拆分）
//	   ⇒ 也会红（因为比对的是**现跑**的输出）。
func TestAdminRoutesSnapshotMatchesGenerator(t *testing.T) {
	got := adminRoutesRunGenerator(t)
	want := adminRoutesSnapshotLines(t)

	if len(got) != len(want) {
		t.Errorf("★ 行数不一致：生成器 %d 行 vs 快照 %d 行 ⇒ 快照已漂移（或生成器口径变了）",
			len(got), len(want))
	}
	line, g, w := adminRoutesDiffFirst(got, want)
	if line > 0 {
		t.Errorf("★ 第 %d 行不一致 ⇒ 快照已漂移（或生成器口径变了）\n生成器：%q\n快照　：%q",
			line, g, w)
	}
}

// TestAdminRoutesSnapshotRowCount —— ★ 量具层：行数下限 + 与快照相等 + 头部自述相等。
func TestAdminRoutesSnapshotRowCount(t *testing.T) {
	got := adminRoutesRunGenerator(t)
	snap := adminRoutesSnapshotLines(t)

	if len(got) <= adminRoutesMinRows {
		t.Errorf("★ 生成器只抽出 %d 条登记（<= %d）⇒ 量具静默失效（口径被改坏 / 扫描范围被缩小）",
			len(got), adminRoutesMinRows)
	}
	if len(got) != len(snap) {
		t.Errorf("★ 生成器 %d 行 vs 快照 %d 行 ⇒ 不相等", len(got), len(snap))
	}
	if len(snap) <= adminRoutesMinRows {
		t.Errorf("★ 快照只有 %d 行数据（<= %d）⇒ 快照回退成了旧清单/空清单",
			len(snap), adminRoutesMinRows)
	}

	// ★ 快照头里自述的「数据行数」必须与实际相等。
	//   （否则「头部写 576、实际 500」这种半更新也会绿。）
	header := adminRoutesSnapshotHeader(t)
	re := regexp.MustCompile(`数据行数=([0-9]+)`)
	m := re.FindStringSubmatch(header)
	if m == nil {
		t.Errorf("★ 快照头里找不到「数据行数=N」⇒ 头与数据可能各说各话：\n%s", header)
		return
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Errorf("快照头里的行数不是整数：%q", m[1])
		return
	}
	if n != len(snap) {
		t.Errorf("★ 快照头自述 %d 行，实际 %d 行 ⇒ 头与数据不一致", n, len(snap))
	}
}

// TestAdminRoutesSnapshotContainsSessionAnalyticsFamily —— ★ 防「快照回退到旧清单」。
//
// ★ 旧清单 444 条里**没有** `/api/admin/session-analytics/*` 这一族
//
//	（它是本轮新抽出来的）⇒ 快照里至少要有它的一条登记。
func TestAdminRoutesSnapshotContainsSessionAnalyticsFamily(t *testing.T) {
	snap := adminRoutesSnapshotLines(t)
	hits := 0
	for _, line := range snap {
		if strings.Contains(line, adminRoutesSessionAnalyticsPrefix) {
			hits++
		}
	}
	if hits == 0 {
		t.Errorf("★ 快照里没有 %q 的任何一条登记 ⇒ 快照回退到旧清单了",
			adminRoutesSessionAnalyticsPrefix)
	}
}

// TestAdminRoutesSnapshotHasMethodPrefixRows —— ★ 防「方法前缀形态被吞掉」。
//
// ★ 生成器要能把 `mux.HandleFunc("GET /api/x", …)` 的**方法前缀拆出来**单独成列。
//
//	没有方法前缀的登记 method 列 = `ANY`。
//	⇒ 快照里必须**至少有一行** method ≠ `ANY`；
//	若一条都没有 ⇒ 前缀拆分逻辑坏了（或源码里真的没有了 ⇒ 那也该红，因为
//	§routemount/scan.go 的注释自陈本仓有 11 处方法前缀形态）。
//
// ★★ 这里**同时**查**生成器的现跑输出**：只看快照的话，
// 「生成器坏了但快照还没重新生成」这一档只会红在一致性断言上；
// 而「生成器坏了 ⇒ 顺手把快照也重新生成」这一档，快照会变成全 ANY
// ⇒ **本断言也必须红**（否则快照可以静默退化）。
func TestAdminRoutesSnapshotHasMethodPrefixRows(t *testing.T) {
	snap := adminRoutesSnapshotLines(t)
	rows := 0
	for _, line := range snap {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			t.Errorf("快照行不是 3 字段 TSV：%q", line)
			continue
		}
		if parts[1] != "ANY" {
			rows++
		}
	}
	if rows == 0 {
		t.Errorf("★ 快照里没有任何一行 method ≠ ANY ⇒ 方法前缀形态被吞掉了")
	}

	// ★ 生成器的**现跑输出**里也必须有 method ≠ ANY 的行。
	gen := adminRoutesRunGenerator(t)
	genRows := 0
	for _, line := range gen {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			continue
		}
		if parts[1] != "ANY" {
			genRows++
		}
	}
	if genRows == 0 {
		t.Errorf("★ 生成器现跑输出里没有任何一行 method ≠ ANY ⇒ 方法前缀拆分逻辑坏了")
	}
}

// TestAdminRoutesSnapshotRowsAreWellFormed —— 每行 3 字段、path 以 / 开头、method 合法。
//
// ★ 防止「快照里混进了生成器不产出的东西」或字段被手工改坏。
func TestAdminRoutesSnapshotRowsAreWellFormed(t *testing.T) {
	snap := adminRoutesSnapshotLines(t)
	methods := map[string]bool{
		"ANY": true, "GET": true, "POST": true, "PUT": true, "DELETE": true,
		"PATCH": true, "HEAD": true, "OPTIONS": true, "CONNECT": true, "TRACE": true,
	}
	for i, line := range snap {
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			t.Errorf("第 %d 行不是 3 字段 TSV：%q", i+1, line)
			continue
		}
		if !strings.HasPrefix(parts[0], "/") {
			t.Errorf("第 %d 行的 path 不以 / 开头：%q", i+1, parts[0])
		}
		if !methods[parts[1]] {
			t.Errorf("第 %d 行的 method 不是已知方法/ANY：%q", i+1, parts[1])
		}
		if !strings.Contains(parts[2], ".go:") {
			t.Errorf("第 %d 行的来源不是 file:line 形态：%q", i+1, parts[2])
		}
	}
}

// TestAdminRoutesGeneratorDocumentsKnownUnregisteredDeviation —— ★
// 「已知不注册」偏差必须**留在生成器的注释里**。
//
// ★ 为什么这道门有意义：飞书那 3 条（/api/admin/feishubot/routing-rules、
//
//	.../routing-rules/、/send-log）**源码里有、线上不注册**
//	（admin/modules.go 的 `registerFeishuRoutes` 故意没有调用点，
//	  源码 `routemount: disabled` 注释自证）。
//
// ⇒ 「源码声明」≠「实际注册」这个事实**必须**写在生成器的注释里，
//
//	否则读快照的人会把 404 读成缺陷。
//
// ★ 门不判「飞书路由真的没被注册」（那是运行时事实，本门不探测），
//
//	只判「这个已知偏差**被登记在注释里**」⇒ 防止注释被悄悄删掉。
func TestAdminRoutesGeneratorDocumentsKnownUnregisteredDeviation(t *testing.T) {
	src := adminRoutesReadAll(t, adminRoutesScriptRelPath)

	for _, needle := range []string{
		"/api/admin/feishubot/routing-rules",
		"/api/admin/feishubot/send-log",
		"registerFeishuRoutes",
		"已知不注册",
	} {
		if !strings.Contains(src, needle) {
			t.Errorf("★ 生成器注释里没有登记「已知不注册」偏差的关键字 %q ⇒ 这个偏差被吞掉了", needle)
		}
	}

	// ★ 生成器自己也要说明「口径是源码声明、不是运行时注册表」。
	for _, needle := range []string{"源码声明", "运行时"} {
		if !strings.Contains(src, needle) {
			t.Errorf("★ 生成器注释里没有说明 %q ⇒ 读快照的人会误以为这是运行时注册表", needle)
		}
	}

	// ★ 生成器必须写清「怎么复跑」。
	for _, needle := range []string{"--out", "--root"} {
		if !strings.Contains(src, needle) {
			t.Errorf("★ 生成器注释/用法里没有 %q ⇒ 别人复跑不了", needle)
		}
	}
}
