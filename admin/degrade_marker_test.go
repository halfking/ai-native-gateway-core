package admin

// degrade_marker_test.go —— 回归门：降级载荷必须自报家门。
//
// ## 挡住的是什么
//
// IsSchemaBehindError ��「schema 落后于代码」的查询失败降级成 200 + 空载荷。
// 这本身是合理的（可选分析视图没迁移时不该整页 500）。问题在于载荷里
// **没有任何东西说明这是降级**，于是：
//
//	period-compare    → 200 + 全 0，而 2026-09 实际有 1139.62 美元
//	cache-economics   → 200 + 全 0，而 UsageCost.vue 一次渲染 6 个指标
//	userProfile.list  → 200 + 空列表
//
// 这三种在页面上都和「真的没有数据」一模一样。2026-10-03 的实测就是
// period-compare 把 1139.62 美元显示成 0 —— 用户看到的是「本月没花钱」。
//
// 修法不是删掉降级（那会让未迁移的部署整页 500），而是让载荷自报家门：
// 恒发 degraded，false = 服务端确认过是好的，true = 这些数字别当结论。
//
// ## 判据的鉴别力
//
// 这条门**动态扫描** admin/ 下所有 IsSchemaBehindError 调用点，不硬编码文件
// 列表 —— 新增一个降级点若忘了带标记，同样报红。反向对照：把一个没带标记的
// 合成站点喂给检测器，必须被报出来，否则这条门只是恒绿装饰。

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// degradeMarkerWindow 是降级判定之后允许标记出现的行数上限。
// 取 25 是因为降级站点都是 `if 判定 { 记日志 → 构造载荷 → 写回 }` 的形状。
// 窗口太大会让「碰巧在别处写了标记」也算通过，太小会误报。
const degradeMarkerWindow = 25

// degradeHelpers 是会产出「200 + 空载荷」的两个判定族。
//
// 42P01（IsMissingRelationError）必须一起扫 —— 上一版门只扫了
// IsSchemaBehindError，于是漏掉了同文件内 usageCostTrend 的 42P01 降级：
// 「本轮已修降级载荷」这句话一度对自己文件都不成立。只扫一族而宣称覆盖
// 两族，是比不扫更坏的一种自欺。
//
// 2026-10-03（第八轮）：第二个字面量原先写成 "IsMissingRelationError(err)" ——
// **把实参写进了字面量**。匹配是 strings.Contains，子串精确，于是
// `IsMissingRelationError(logsErr)`、`IsMissingRelationError(scanErr)`
// 这类换一个变量名的调用点既不计数、也不进范围表：实测 2 处生产站点
// （usage_credits.go:75、dashboard_board_queries.go:128）被漏掉，
// 门自报的「N 处」因此少算。代价不是漏报缺陷，而是**边界失真** ——
// 一个自称登记了 N 处的表，实际登记了 N-2 处。
//
// 字面量只到调用名的左括号，实参由调用点自己带。反向对照见
// TestDegradeHelperNeedlesCarryNoArgument。
//
// 第三条 `"42P01"` 是 2026-10-03（第八轮）加的，补的是**第三个**缺口：
// 裸 `pgErr.Code == "42P01"` 这类自建判定谁都不匹配，于是全仓 7 处
// 裸 SQLSTATE 判定全部在元素集外（含 domains/ 与 db/ 两棵树）。
// 它的形状与前两条不同（字面量而非调用名），所以「字面量只到左括号」
// 那条纪律对它是空转 —— 纪律的真正内容是**不许把实参写进字面量**，
// TestDegradeHelperNeedlesCarryNoArgument 已按那个内容改写。
var degradeHelpers = []string{
	"IsSchemaBehindError(",
	"IsMissingRelationError(",
	`"42P01"`,
}

// degradeMarkerOutOfScope 列出**已知未纳入** degraded 契约的文件及理由。
//
// 键一律是**仓库相对路径**（'/' 分隔）。2026-10-03（第八轮）之前键是
// 裸文件名，因为元素集只到 admin/ 子包；遍历根放宽到仓库根之后，
// 裸文件名会把 admin/xxx.go 与别处的同名文件混为一谈。
//
// 这些是「可选聚合视图未迁移 → 返回 200 + 空」的降级点。它们的降级在语义上
// 更站得住（真的没有数据源，不是算不出来），但页面上同样与「没有数据」同形。
//
// 关键设计：这里**登记计数而不是登记「已覆盖」**。下一轮从确切数字起步，
// 而不是从零开始重新搜索 —— 未知边界的范围，比已知的更大。
var degradeMarkerOutOfScope = map[string]string{
	"admin/aggregate_read_guard.go":     "内部读守卫，不直接产出页面载荷（writeAggRowsErr/writeLookupErr 写 503 + analytics_view_missing，不是 200+空）",
	"admin/dashboard_board_aux.go":      "运维 chip 查询：三处 _= 已于 2026-10-03 改为记录错误并恒发 degraded；经 TestBoardOpsQueriesReportDegradation 把守",
	"admin/dashboard_board_fallback.go": "饼图逐维度降级账本（boardPieDegradation），经 TestBoardPieDegradationLedger + TestBoardPiesDegradationReachesPayload 把守",
	"admin/dashboard_board_queries.go":  "看板主查询：42P01 转 request_logs 回退，回退失败已于 2026-10-03 上抛/打标；见 TestBoardFallbackErrorsPropagate",
	// 注意：dashboard_board.go / dashboard_board_cache.go **不在**本表里，
	// 因为它们自己没有 42P01 判定点——它们是载荷组装点。
	// 首次登记时误加了两条，门立刻报「该表项是凭印象写的」（这正是双向检查的价值）。
	// 它们的接线由 TestBoardPiesDegradationReachesPayload 单独把守。
	"admin/session_analytics_mv_guard.go": "物化视图守卫，内部",
	// 2026-10-03（第八轮）：这一条是**门抓住我自己**加的站点后登记的。
	// classifyBudgetSpendErr 里的 IsMissingRelationError 是 42P01 判定，
	// 但它的两个出口都是错误响应（503 usage_ledger_view_missing /
	// 500 usage query failed），**不产出 200+空载荷**，也不写载荷
	// —— 没有「算不出来 vs 真的没有数据」可混的余地。
	// 与 aggregate_read_guard.go 同一型（那也是 42P01 → 503 而非 200+空）。
	// 分类形状由 TestClassifyBudgetSpendErr 把守（纯函数，不依赖 PG）。
	"admin/keys.go": "预算花费查询错误分类：42P01→503 usage_ledger_view_missing、其余→500，" +
		"两个出口都是错误响应而非 200+空载荷；经 TestClassifyBudgetSpendErr 把守",
	"admin/usage_credits.go":     "降级信息经 (值, 视图名) 返回，由调用方写入载荷；经 TestCreditsDegradationPropagates 把守",
	"admin/dashboard_degrade.go": "本机制的定义文件 + applyBoardDegradation 的实现；后者由 TestBoardPiesDegradationReachesPayload 把守",

	// ── 以下五条是 2026-10-03 递归扫描后才进入视野的（见 collectGoFiles）──
	// 单层 os.ReadDir(".") 时它们完全不在门内，门报绿与它们无关。
	"admin/dashboarddegrade/dashboarddegrade.go": "机制定义包，判定与取名函数本身",
	"admin/dashboardapi/errors.go":               "降级走 writeDegraded(→ Metadata.Degraded)，经 TestDashboardapiDegradedPayloads 把守",
	"admin/dashboardapi/performance.go":          "降级走 writeDegraded(→ Metadata.Degraded)，经 TestDashboardapiDegradedPayloads 把守",
	"admin/dashboardapi/session_active.go":       "降级走 writeDegraded(→ Metadata.Degraded)，经 TestDashboardapiDegradedPayloads 把守",
	"admin/dashboardapi/module_stats.go":         "载荷字面量自带 Degraded: true，经 TestDashboardapiDegradedPayloads 把守",

	// ── 以下四条是 2026-10-03（第八轮）把遍历根放宽到仓库根后进入视野的 ──
	// 在此之前它们连「被扫过」都不成立：domains/ 与 db/ 整棵树在
	// collectGoFiles(".") 之外，而 admin/auto_route_outcome_freshness.go
	// 虽在 admin/ 内却是裸 SQLSTATE 判定、不匹配任何 helper。
	// 换句话说：**文件集与 needle 集是两个独立条件，只放宽一个等于没放宽。**
	"admin/auto_route_outcome_freshness.go": "裸 42P01 ⇒ Reason=absent / Available=false / Stale=true，属**自报降级**" +
		"（三态 Reason，不是 degraded 键），不产生 200+空载荷；由 queryOutcomeFreshness 的三态与现有单测把守",
	"domains/authentication/verifier.go": "42P01(usage_ledger 视图缺失) ⇒ spent=0 ⇒ 预算闸门 **fail-open 放行**。" +
		"仅 slog.Warn + metric{BudgetOutcomeDegradedNoLedger}；与 admin/keys.go 的 fail-closed 分属两个门（详见第八轮 §2）",
	// ⚠ domains/credentialstate/cache.go 曾被登记一次又被门当场打回
	// （"该表项是凭印象写的"）。原因值得记：那个文件里唯一的 42P01 字面量在
	// `return errors.As(err, &pgErr) && pgErr.Code == "42P01"` 这一行 ——
	// 谓词**定义**的 return 表达式，被 countDegradeSites 的规则排除。而真正的
	// 降级分支 `if nodeErr != nil && !isUndefinedTable(nodeErr)` 行内没有字面量。
	// ⇒ 这是**第三种元素集形状**：①文件集 ②needle 集 ③本地谓词间接。
	// 现已由 indirectDegradeSites 识别（见 TestIndirectDegradeSiteDetection），
	// 本表项是补上该检测器之后才成立的。
	"domains/credentialstate/cache.go": "42P01 ⇒ 不返回错误，改走第二数据源 " +
		"getLegacyStateFromDB（node_probe_state → model_probe_state）。" +
		"自报字段是 State.Source（db vs node_probe_db），**不是** degraded 键 —— " +
		"与本表其余项的形状不同。⚠ 已知缺口：全仓无消费者按 Source 分支" +
		"（node_probe_db 仅出现于其写入点），即「自报了但没人读」，详见第八轮 §3",
	"db/db.go": "DB 层 schema 不匹配分类器 IsSchemaMismatchError 的 `case \"42P01\", \"42703\", \"42883\", \"42809\", \"0A000\"`" +
		" —— 与 admin/dashboard_degrade.go 同角色的**机制定义处**，不产出页面载荷",

	// 2026-10-05（832/833 收口轮）：runChecks 的 Optional+42P01 跳过分支是 bg
	// 巡检 worker 的降级形态——跳过该条检查 + slog.Info 留痕（check_id/relation/
	// code），**没有 HTTP 载荷可标**（调用方消费的是 newCritical/newWarning 计数
	// 与整轮 err）；「算不出来 vs 真的没有数据」在该消费面上不混淆。行为由
	// bg/health_check_optional_skip_realdb_test.go（真库 42P01 分支）与
	// bg/health_check_scan_guard_test.go 把守。
	"bg/routing_health_checks.go": "runChecks 的 Optional+42P01 跳过是 bg worker 降级：跳过+日志留痕，无 200+空载荷；经 health_check_optional_skip_realdb / health_check_scan_guard 两测试把守",

	// 2026-10-07（收口轮）：recordProbeLedger 的 42P01 分支是又一种 bg worker 降级，
	// 与上一条同源（bg/modality_verification.go 里「为什么只认 42P01」注释自证）。
	// 835 未应用时 INSERT system_probe_runs 报 42P01 ⇒ probeLedgerMissingTableOnce
	// 每进程 Warn 一次后 return：台账缺一行，核实结论照常写
	// model_modality_verification、核实循环不停——没有 HTTP 载荷可标。无专门测试
	// 把守：触发该分支要求表真的不存在，真库夹具无法非破坏性地制造这个前提
	// （健康库上必然 skip）；降级形状由文件内 once-guard 与那段注释钉住。
	"bg/modality_verification.go": "recordProbeLedger 的 42P01 是 bg worker 台账写入降级：" +
		"once-Warn 留痕+跳过写入，核实循环继续，无 200+空载荷；" +
		"与 routing_health_checks 的 Optional 纪律同源（本文件注释自证）",
}

// markerPatterns 是「这个载荷带降级标记」的判定。
// 两种形状都要认：结构体字面量（Degraded: true）与 map 载荷（"degraded": true）。
//
// 第三条 degradedList( 是 2026-10-03 加的：裸数组端点改用共享构造器后，
// 站点不再出现字面标记。加它**不是**为了让自己的新代码通过 ——
// 同一提交里加了 TestDegradedListConstructorAlwaysDegraded，钉住该构造器
// 永远发 true；构造器一旦被改成可能返回未降级载荷，那条测试先红。
// 换句话说：这里认的是一个**被单独把守的**构造器，不是一句「有 degraded 字样」。
var markerPatterns = []string{
	"Degraded:",
	"Degraded: true",
	`"degraded":`,
	`"degraded": true`,
	"degradedList(",
}

// findUnmarkedDegradeSites 返回所有「紧跟降级判定却没有带标记」的站点，
// 形如 `file:line`。
func findUnmarkedDegradeSites(src, file string) []string {
	lines := strings.Split(src, "\n")
	var bad []string
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		// 注释不是调用点 —— 判据只管可执行代码。
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
			continue
		}
		// 函数**声明**不是调用点。`func IsSchemaBehindError(err error) bool {`
		// 里同样含有 "IsSchemaBehindError(" —— 第一版检测器把它当降级站点报了出来，
		// 而 dashboard_degrade.go 正是本机制的定义处。误报的门比没有门更坏：
		// 它只会诱导下一个人去加豁免，而不是去理解为什么。
		if strings.HasPrefix(trimmed, "func ") {
			continue
		}
		// `return IsMissingRelationError(err) || IsMissingColumnError(err)` 是
		// IsSchemaBehindError 的函数体 —— 一个判断表达式，不是降级站点。
		// 只跳声明行不够：只跳声明行时机制的定义文件被报成了违规。
		if strings.HasPrefix(trimmed, "return ") {
			continue
		}
		isCall := false
		for _, helper := range degradeHelpers {
			if strings.Contains(ln, helper) {
				isCall = true
				break
			}
		}
		if !isCall {
			continue
		}
		if !hasMarkerNear(lines, i) {
			bad = append(bad, file+":"+strconv.Itoa(i+1))
		}
	}
	return bad
}

// TestDegradeHelperNeedlesCarryNoArgument 钉住「字面量不许带实参」这条纪律。
//
// 为什么值得单独一条：字面量带上实参时，漏掉的不是「某一处缺陷」，而是
// **整个范围表的计数**。门仍然全绿、仍然报「已登记 N 处」，
// 只是那个 N 少算 —— 边界的失真不会以任何红的形式出现。
//
// 反向对照（变异）：把 degradeHelpers 改回 "IsMissingRelationError(err)"，
// 本条必须在下面的样本断言上报红；改回后门自报计数会少 2 处。
func TestDegradeHelperNeedlesCarryNoArgument(t *testing.T) {
	// 纪律的真正内容：**不许把实参写进字面量**。第一版把它写成
	// 「字面量必须以 ( 结尾」，那对 `"42P01"` 这类非调用名字面量是空转 ——
	// 纪律的措辞比纪律本身窄，窄到会把正确的第三条判成违规。
	// 改用「不许出现 name(identifier) 这种带实参的调用形态」。
	argLike := regexp.MustCompile(`[A-Za-z_]\w*\([A-Za-z_]\w*\)`)
	for _, nd := range degradeHelpers {
		if argLike.MatchString(nd) {
			t.Errorf("degradeHelpers 字面量 %q 带了实参。匹配用 strings.Contains，"+
				"换一个变量名的调用点（logsErr / scanErr / c.err）会静默漏掉 —— "+
				"第八轮实测因此漏计 2 处生产站点。", nd)
		}
	}

	// 阳性对照：同一族、实参形态各异，判定族必须全都命中。
	// 作用是防恒绿 —— 只写「不许带实参」的话，把名单清空也能过。
	samples := []string{
		"if IsMissingRelationError(err) {",
		"if IsMissingRelationError(logsErr) {",
		"if !IsMissingRelationError(scanErr) {",
		"if IsSchemaBehindError(cause) {",
		`if errors.As(err, &pgErr) && pgErr.Code == "42P01" {`,
	}
	for _, s := range samples {
		hit := false
		for _, nd := range degradeHelpers {
			if strings.Contains(s, nd) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("调用点 %q 没有命中任何 degradeHelpers —— 元素集又变窄了。"+
				"（degradeHelpers=%v）", s, degradeHelpers)
		}
	}
}

// hasMarkerNear 判断第 i 行之后的窗口内有没有降级标记。
//
// 抽成独立函数是因为现在有**两处**要用它：needle 命中与间接站点。
// 复制两份窗口逻辑必然分叉，而分叉的后果是「一半站点没人判标记」——
// 与本文件开头记的那些盲区同一族。
func hasMarkerNear(lines []string, i int) bool {
	windowed := strings.Join(lines[i:minInt(i+1+degradeMarkerWindow, len(lines))], "\n")
	for _, pat := range markerPatterns {
		if strings.Contains(windowed, pat) {
			return true
		}
	}
	return false
}

// collectGoFiles 返回 root 下所有 .go 源文件的相对路径（跳过 _test.go 与
// 跳过测试数据目录），路径用 '/' 分隔以便与范围表键一致。
//
// 2026-10-03: 上一版用 os.ReadDir(".") 单层列举，于是 admin 的**子包完全不在
// 扫描范围内** —— 实测 dashboardapi/ 有 12 处 42P01 判定、dashboarddegrade/
// 是 10 处，门一条都看不见。
// 「门是绿的」与「门扫过那些文件」是两件事：非递归遍历把后者的缺失伪装成
// 前者的成立。
//
// 2026-10-03（第八轮）再放宽一次：root 传仓库根（".."，测试工作目录是包目录
// admin/），于是 domains/ 与 db/ 两棵树进入视野 —— 那里有本轮定性过的
// fail-open 预算闸门与 DB 层分类器，此前连「被扫过」都不成立。
//
// ⚠ **vendor/ 必须排除**：vendor/github.com/lib/pq/pqerror/codes.go 里有
// `UndefinedTable = Code("42P01")`，那是一张 SQLSTATE 常量表，
// 把它当降级站点会让门恒红。第三条 needle `"42P01"` 加进来之后，
// 任何写着这个字面量的第三方代码都会命中 —— 恒红的门比没有门更坏。
//
// ⚠ **只放宽遍历根是不够的**：那 4 个缺口文件里没有一行匹配
// IsMissingRelationError / IsSchemaBehindError，光换 root 的话它们
// 仍然是 n=0 被 continue 掉。**文件集与 needle 集是两个独立条件。**
// degradeScanSkipDirs 是遍历时整棵跳过的目录。
//
// vendor 必须在里面：它带 42P01 常量表（pqerror/codes.go），
// 而 degradeHelpers 里有 `"42P01"` 这条 needle —— 不排除就是恒红。
var degradeScanSkipDirs = map[string]bool{
	"vendor":       true,
	"node_modules": true,
	"third_party":  true,
	"web":          true, // 前端资产目录，不含 Go 源码；显式列出以防将来混入
}

// goSourceFile 把「怎么读」与「怎么称呼」分开：
//
//	walkPath —— 遍历产出的真实路径，文件 I/O 必须用它（root=".." 时它带 ../，
//	而范围表的键不能带，否则 admin/x.go 与别处的同名文件会混）
//	rel     —— 仓库相对路径，范围表的键与报错输出用它
type goSourceFile struct {
	walkPath string
	rel      string
}

func collectGoFiles(root string) ([]goSourceFile, error) {
	// root 传仓库根时是 ".."，WalkDir 产出的路径会带 "../" 前缀。
	// 范围表的键是仓库相对路径，这里统一剥掉，否则表项永远匹配不上 ——
	// 而「表里有、代码里没有」那一侧不会报错，缺口会被静默当成已覆盖。
	prefix := filepath.ToSlash(root)
	if prefix == "." {
		prefix = ""
	} else {
		prefix = strings.TrimSuffix(prefix, "/") + "/"
	}
	var out []goSourceFile
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			// testdata 与隐藏目录不是降级站点来源，排除可避免把夹具当代码扫。
			if p != root && (name == "testdata" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			// 第三方代码不是降级站点来源。见上面对 vendor 里 42P01 常量表的说明。
			if p != root && degradeScanSkipDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		out = append(out, goSourceFile{
			walkPath: p,
			rel:      strings.TrimPrefix(filepath.ToSlash(p), prefix),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	// 排序让报错输出稳定：文件遍历顺序不是契约，报错顺序是。
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// TestDegradePayloadsCarryMarker 扫真实的 admin 包。
func TestDegradePayloadsCarryMarker(t *testing.T) {
	paths, err := collectGoFiles("..") // 仓库根：domains/ 与 db/ 也要在门内
	if err != nil {
		t.Fatalf("walk repo root: %v", err)
	}

	var sites, bad []string
	outOfScopeSeen := map[string]int{}
	for _, f := range paths {
		rel := f.rel
		raw, err := os.ReadFile(f.walkPath)
		if err != nil {
			t.Fatalf("read %s: %v", f.walkPath, err)
		}
		// 去注释：注释里出现的降级判定不是调用点。
		code := stripGoCommentsKeepLines(string(raw))
		// 计数用与 findUnmarkedDegradeSites 同一套排除规则 —— 否则
		// 「已登记 N 处」会把判定函数与 return 表达式也算进去，数字虚高。
		// 第三种形状：本地谓词间接（行内无 needle，但确实是个降级分支）。
		// 计数上与直接命中并列，让范围表能把它们登记进来。
		indirect := indirectDegradeSites(code, rel)
		n := countDegradeSites(code, rel) + len(indirect)
		if n == 0 {
			continue
		}
		if _, excused := degradeMarkerOutOfScope[rel]; excused {
			outOfScopeSeen[rel] = n
			continue
		}
		sites = append(sites, rel)
		bad = append(bad, findUnmarkedDegradeSites(code, rel)...)
		// 间接站点同样要判标记，否则「计入元素集」只是好看的数字 ——
		// 被计入却不被判定，等于把盲区从「看不见」搬到了「看得见但没人管」。
		bad = append(bad, findUnmarkedIndirectSites(code, rel, indirect)...)
	}

	// 范围表必须与真实情况一致，两边都要卡，否则「边界」只是一段散文：
	//  · 表里有、代码里已无 → 文件已迁移却没删表项，边界失真
	//  · 代码里有、表里没有 → 悄悄溜进来的新降级点
	totalOutOfScope := 0
	for name := range degradeMarkerOutOfScope {
		if outOfScopeSeen[name] == 0 {
			t.Errorf("degradeMarkerOutOfScope 列了 %s，但它已不再有降级判定点 —— "+
				"要么已迁移（请删表项并补标记），要么该表项是凭印象写的", name)
			continue
		}
		totalOutOfScope += outOfScopeSeen[name]
	}
	t.Logf("已登记但未纳入 degraded 契约的降级点：%d 处（%d 个文件）", totalOutOfScope, len(outOfScopeSeen))

	// 反向对照先行：检测器对「没有标记」的合成站点必须报红。
	// 少这一条，下面「全部有标记」的结论就没有说服力。
	synthetic := `if IsSchemaBehindError(err) {
		writeJSON(w, http.StatusOK, SomeResponse{})
		return
	}`
	if len(findUnmarkedDegradeSites(synthetic, "synthetic.go")) == 0 {
		t.Fatal("检测器漏报了未标记的降级站点 —— 下面那次扫描报绿没有意义")
	}
	// 阳性对照：带标记的合法写法不得误报。
	marked := `if IsSchemaBehindError(err) {
		writeJSON(w, http.StatusOK, SomeResponse{Degraded: true, DegradedReason: reason})
		return
	}`
	if got := findUnmarkedDegradeSites(marked, "synthetic.go"); len(got) > 0 {
		t.Errorf("检测器误报了合法站点: %v", got)
	}
	// 阳性对照二：机制自身的**函数声明**不得被当成降级站点。
	// 第一版就是在这里误报的，代价是 dashboard_degrade.go 出现在违规清单里。
	declaration := `func IsSchemaBehindError(err error) bool {
	return IsMissingRelationError(err) || IsMissingColumnError(err)
}`
	if got := findUnmarkedDegradeSites(declaration, "synthetic.go"); len(got) > 0 {
		t.Errorf("检测器把函数声明误当成降级站点: %v", got)
	}

	// 零样本说明检测器没跑过，不是「全部合规」。
	if len(sites) == 0 {
		t.Fatal("admin 包里一个 IsSchemaBehindError 调用点都没有 —— " +
			"要么机制被删了，要么本门扫错了目录。先查清再谈通过。")
	}

	if len(bad) > 0 {
		t.Errorf("%d 个降级站点返回 200 + 空载荷却没有 degraded 标记，"+
			"调用方无法区分「算不出来」与「真的没有数据」:\n  %s",
			len(bad), strings.Join(bad, "\n  "))
	}
}

// looksLikeControlFlow 判断一行是否处在控制流里（而非单纯取布尔值）。
func looksLikeControlFlow(ln string) bool {
	t := strings.TrimSpace(ln)
	if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "func ") {
		return false
	}
	for _, kw := range []string{"if ", "for ", "switch ", "case ", "&&", "||", "return ", "else if "} {
		if strings.Contains(ln, kw) {
			return true
		}
	}
	return false
}

// indirectDegradeSites 找「降级谓词间接」形态的站点：本地（小写）谓词的
// 函数体里含 degradeHelpers 的 needle，而该谓词在控制流里被调用 ——
// 于是**那一行本身不含任何 needle**，前两种元素集条件都看不见它。
//
// 这是第三种形状。前两种本门都认了（文件集、needle 集），这一种此前完全盲：
// domains/credentialstate/cache.go 里 42P01 只出现在
// `return errors.As(err, &pgErr) && pgErr.Code == "42P01"`
// （谓词定义的 return 表达式，被 countDegradeSites 排除），
// 而真正的降级分支是 `if nodeErr != nil && !isUndefinedTable(nodeErr)`。
//
// 两处刻意的保守取舍：
//
//	· **只认小写谓词**。导出名谓词（IsMissingRelationError 等）本身就是
//	  needle，其所在文件已由 needle 条件进元素集；再算一遍只会制造噪声。
//	· **区域边界取下一个顶层 `func`，而不是第一个 `}`**。用第一个 `}`
//	  会在函数内含闭包时提前截断，把 needle 判成「不在体内」⇒ 漏报。
//	  已知未覆盖：方法（`func (m *T) f(`）不在本检测器视野内。
func indirectDegradeSites(code, file string) []string {
	lines := strings.Split(code, "\n")

	// 1) 收集「体内含 needle」的小写谓词及其区域
	type pred struct {
		name       string
		start, end int
	}
	declRe := regexp.MustCompile(`^func ([a-z]\w*)\(`)
	var preds []pred
	for i, ln := range lines {
		m := declRe.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "func ") {
				end = j
				break
			}
		}
		has := false
		for j := i + 1; j < end; j++ {
			for _, nd := range degradeHelpers {
				if strings.Contains(lines[j], nd) {
					has = true
					break
				}
			}
			if has {
				break
			}
		}
		if has {
			preds = append(preds, pred{m[1], i, end})
		}
	}
	if len(preds) == 0 {
		return nil
	}

	// 2) 这些谓词是否在控制流里被调用（在自身区域之外）
	var out []string
	for _, p := range preds {
		callRe := regexp.MustCompile(`\b` + p.name + `\(`)
		for i, ln := range lines {
			if i > p.start && i <= p.end {
				continue
			}
			if callRe.MatchString(ln) && looksLikeControlFlow(ln) {
				out = append(out, file+":"+strconv.Itoa(i+1))
			}
		}
	}
	return out
}

// findUnmarkedIndirectSites 返回「间接站点里没有降级标记」的，形如 file:line。
//
// 为什么要单独抽出来并单独测：当前 4 个含间接站点的文件**全在范围表内**
// （豁免），所以主循环里那行调用在今天的仓库上**一次都不会触发**。
// 已实测：把那行调用删掉，变异测试**不会变红**（计数仍是 38 处/17 文件）——
// 它是**当前不承重的保险**，不是被验证过的门。
// 函数本身由 TestFindUnmarkedIndirectSites 用合成夹具证明（无标记报红 /
// 有标记不误报）；未被证明的只是「主循环会调用它」这一句。
// 哪天真出现一个非豁免文件带间接站点，这行立刻开始承重。
//
// 顺带一个刻意的取舍：间接站点的标记窗口与 needle 站点**共用**
// hasMarkerNear。窗口放宽或收紧时只改一处，不会分叉。
func findUnmarkedIndirectSites(code, file string, indirect []string) []string {
	if len(indirect) == 0 {
		return nil
	}
	lines := strings.Split(code, "\n")
	var out []string
	for _, site := range indirect {
		idx, err := strconv.Atoi(strings.TrimPrefix(site, file+":"))
		if err != nil || idx < 1 || idx > len(lines) {
			continue
		}
		if !hasMarkerNear(lines, idx-1) {
			out = append(out, site+"（间接站点：本地谓词在控制流里被调用）")
		}
	}
	return out
}

// TestFindUnmarkedIndirectSites 让上面那个函数**自己**承重。
// 真实仓库今天触发不到它（4 个间接站点文件都在豁免表内），
// 所以必须用合成夹具证明：没标记会报红、有标记不会误报。
func TestFindUnmarkedIndirectSites(t *testing.T) {
	code := `package p

func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}

func f(nodeErr error) {
	if nodeErr != nil && !isUndefinedTable(nodeErr) {
		return
	}
}
`
	sites := indirectDegradeSites(code, "p.go")
	if len(sites) == 0 {
		t.Fatal("夹具前提不成立：合成样本应当产出 1 个间接站点")
	}
	if got := findUnmarkedIndirectSites(code, "p.go", sites); len(got) != 1 {
		t.Fatalf("无标记的间接站点必须报 1 条，实到 %v", got)
	}

	// 阳性对照：窗口内写了 degraded 标记 ⇒ 不得误报。
	marked := strings.Replace(code,
		"\tif nodeErr != nil && !isUndefinedTable(nodeErr) {",
		"\tif nodeErr != nil && !isUndefinedTable(nodeErr) {\n\t\t_ = Degraded{Degraded: true}", 1)
	if got := findUnmarkedIndirectSites(marked, "p.go", sites); len(got) != 0 {
		t.Fatalf("带标记的间接站点被误报: %v", got)
	}
}

// TestIndirectDegradeSiteDetection 把守第三种形状的检测器。
//
// 它守的正是「全仓最后一个缺口」：本地谓词间接。counterpart 的事实是
// domains/credentialstate/cache.go —— 该文件按前两种条件算下来
// 「没有降级站点」，于是把它登记进范围表会被门判成「凭印象写的」。
func TestIndirectDegradeSiteDetection(t *testing.T) {
	// 真实对象：cache.go 的 isUndefinedTable 必须在门内。
	raw, err := os.ReadFile("../domains/credentialstate/cache.go")
	if err != nil {
		t.Fatalf("read cache.go: %v", err)
	}
	got := indirectDegradeSites(stripGoCommentsKeepLines(string(raw)), "domains/credentialstate/cache.go")
	if len(got) == 0 {
		t.Fatal("domains/credentialstate/cache.go 报 0 个间接站点 —— " +
			"它的降级分支 `if nodeErr != nil && !isUndefinedTable(nodeErr)` 行内没有 needle，" +
			"正是靠本检测器进元素集的。检测器坏了的话，该文件会重新变成盲区。")
	}

	// 阴性样本一：谓词体内有 needle，但**没有**在控制流里被调用 → 不算站点。
	notCalled := `package p

func isGone(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}

var _ = isGone
`
	if sites := indirectDegradeSites(stripGoCommentsKeepLines(notCalled), "p.go"); len(sites) != 0 {
		t.Errorf("谓词未在控制流里被调用却报了站点: %v", sites)
	}

	// 阴性样本二：谓词被调用，但在**控制流之外**（赋给变量）→ 不算站点。
	assigned := `package p

func isGone(err error) bool {
	return err != nil && string(err) == "42P01"
}

func f() bool {
	flag := isGone(nil)
	return flag
}
`
	if sites := indirectDegradeSites(stripGoCommentsKeepLines(assigned), "p.go"); len(sites) != 0 {
		t.Errorf("非控制流的调用被误报成降级站点: %v", sites)
	}

	// 阴性样本三：**注释里**提到 42P01 的谓词不得被算成降级谓词。
	commentOnly := `package p

func isGone(err error) bool {
	// 曾经这里判 42P01
	return err != nil
}

func f(err error) bool {
	if isGone(err) {
		return true
	}
	return false
}
`
	if sites := indirectDegradeSites(stripGoCommentsKeepLines(commentOnly), "p.go"); len(sites) != 0 {
		t.Errorf("注释里的 42P01 被当成了降级谓词: %v", sites)
	}

	// 阳性对照：真正的形状必须被检出，且定位到控制流那一行。
	positive := `package p

func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}

func f(nodeErr error) {
	if nodeErr != nil && !isUndefinedTable(nodeErr) {
		return
	}
}
`
	sites := indirectDegradeSites(stripGoCommentsKeepLines(positive), "p.go")
	if len(sites) != 1 {
		t.Fatalf("阳性对照应恰好 1 处，实到 %v", sites)
	}
	// 断言落在**控制流那一行**（行内含 if 与谓词名），而不是谓词体内某一行。
	// 第一版这里写死了行号 13 —— 数错了，真值是 9。改断言内容而不是改行号：
	// 夹具将来重排格式时，行号断言会变成一条与被测行为无关的脆弱断言。
	lines := strings.Split(stripGoCommentsKeepLines(positive), "\n")
	idx, _ := strconv.Atoi(strings.TrimPrefix(sites[0], "p.go:"))
	if idx < 1 || idx > len(lines) {
		t.Fatalf("站点定位越界: %s", sites[0])
	}
	gotLine := lines[idx-1]
	if !strings.Contains(gotLine, "isUndefinedTable(") || !strings.Contains(gotLine, "if ") {
		t.Fatalf("站点应落在控制流调用行，实到第 %d 行: %q", idx, gotLine)
	}
}

// TestDashboardapiDegradedPayloads 把守递归扫描换来的那批豁免。
//
// 为什么必须有它：扫描门认不出 `h.writeDegraded(w, start, op, err)` 这个调用
// 里**是否**带标记 —— 标记在 writeDegraded 的函数体内，而那三个 handler
// 各有一份副本。把它们登记为豁免，等于用「我保证」代替「我验证」。
//
// 这条测试逐个核对豁免文件里每个降级站点确实通往一个带标记的写入：
//
//	· 走 writeDegraded(...) 的 → 函数体内必须出现 Degraded: true
//	· 直接写载荷字面量的     → 字面量内必须出现 Degraded: true
//
// 少任何一侧，「降级写入点」与「带标记的写入点」就不是同一个集合。
func TestDashboardapiDegradedPayloads(t *testing.T) {
	type waiver struct{ file, helper string }
	waivers := []waiver{
		{"dashboardapi/errors.go", "h.writeDegraded("},
		{"dashboardapi/performance.go", "h.writeDegraded("},
		{"dashboardapi/session_active.go", "h.writeDegraded("},
	}
	for _, w := range waivers {
		raw, err := os.ReadFile(w.file)
		if err != nil {
			t.Fatalf("read %s: %v", w.file, err)
		}
		code := stripGoCommentsKeepLines(string(raw))
		// 该文件里每个降级站点都必须调用这个 helper。
		sites := countDegradeSites(code, w.file)
		calls := strings.Count(code, w.helper)
		if sites == 0 {
			t.Errorf("%s 已不再有 42P01 站点：请从范围表删除它的表项", w.file)
			continue
		}
		if calls < sites {
			t.Errorf("%s 有 %d 处 42P01 判定但只调了 %d 次 %s —— "+
				"有站点绕过了带标记的降级写入", w.file, sites, calls, w.helper)
		}
		if !strings.Contains(code, "Degraded:    true") && !strings.Contains(code, "Degraded: true") {
			t.Errorf("%s 的 %s 内部没有 Degraded: true —— "+
				"豁免失去依据，载荷实际不带标记", w.file, w.helper)
		}
	}

	// module_stats.go 是字面量自带标记，单独核。
	raw, err := os.ReadFile("dashboardapi/module_stats.go")
	if err != nil {
		t.Fatalf("read module_stats.go: %v", err)
	}
	if code := stripGoCommentsKeepLines(string(raw)); !strings.Contains(code, "Degraded:    true") &&
		!strings.Contains(code, "Degraded: true") {
		t.Error("dashboardapi/module_stats.go 的降级字面量没有 Degraded: true —— " +
			"范围表里给它的理由已不成立")
	}
}

// TestCreditsDegradationPropagates 把守 usage_credits.go 的豁免。
//
// ## 为什么需要它
//
// 2026-10-03 之前 queryTotalCreditsCharged 只返回 int64，42P01 时静默
// `return 0`。调用方两条：
//
//	· usage.go:150 —— summary 主查询**成功**之后才调它，那条路径的
//	  degraded 只覆盖主查询失败；
//	· dashboard_board_queries.go 的回退路径。
//
// 结果：主查询成功 + credits 降级 ⇒ 载荷 `total_credits_charged: 0`
// 且 **degraded 缺失**。而积分在 BoardHeroRow.vue 是首屏高亮 KPI。
//
// 现在它返回 (值, 缺失视图名)，由调用方写进载荷。这条测试钉住
// 「降级信息真的到了载荷」——**只改返回值不算修好**。
func TestCreditsDegradationPropagates(t *testing.T) {
	// 0) 最上游：helper 自己必须真的报告降级。
	//    缺这一条时，下面 1/2 两条会在「helper 静默 return "" 」的变异下**照样绿** ——
	//    因为调用点确实写了标记，只是拿到的永远是空串。
	//    那是本轮实测到的真实漏洞：变异 3 逃过了当时的两条断言。
	creditsRaw, err := os.ReadFile("usage_credits.go")
	if err != nil {
		t.Fatalf("read usage_credits.go: %v", err)
	}
	creditsCode := stripGoCommentsKeepLines(string(creditsRaw))
	if !strings.Contains(creditsCode, "return 0, creditDegradedView(logsErr)") {
		t.Error("usage_credits.go 的「两条腿都断」分支不再返回降级视图名 —— " +
			"credits 会退回静默 0，而调用点拿到的空串不会让载荷带标记（变异 3 曾逃过）")
	}
	if !strings.Contains(creditsCode, "return credits, creditDegradedView(estErr)") {
		t.Error("usage_credits.go 的「估算腿失败」分支不再返回降级视图名 —— " +
			"账单积分会被当成完整的总积分")
	}

	// 1) 调用点必须接住两个返回值，而不是把降级信息丢掉。
	for _, f := range []string{"usage.go", "dashboard_board_queries.go"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		code := stripGoCommentsKeepLines(string(raw))
		if !strings.Contains(code, "queryTotalCreditsCharged(") {
			t.Errorf("%s 不再调用 queryTotalCreditsCharged：请确认积分 KPI 的降级标记", f)
			continue
		}
		if !strings.Contains(code, "queryTotalCreditsCharged(ctx, tid, days)") &&
			!strings.Contains(code, "queryTotalCreditsCharged(ctx, tenantID, tr.Days)") {
			continue // 该文件不在这条路径上
		}
		if !strings.Contains(code, "creditsDegradedView") {
			t.Errorf("%s 调用 queryTotalCreditsCharged 但没有接住降级视图名 —— "+
				"credits 降级时载荷会退回「0 且无标记」", f)
		}
	}

	// 2) 两个调用点都必须把降级写进载荷。
	raw, err := os.ReadFile("usage.go")
	if err != nil {
		t.Fatalf("read usage.go: %v", err)
	}
	code := stripGoCommentsKeepLines(string(raw))
	for _, need := range []string{`summary.Degraded = true`, `summary.MissingView`} {
		if !strings.Contains(code, need) {
			t.Errorf("usage.go 缺 %q：主查询成功而 credits 降级时，"+
				"summary 载荷必须自报家门（这是本条判据存在的唯一理由）", need)
		}
	}

	raw, err = os.ReadFile("dashboard_board_queries.go")
	if err != nil {
		t.Fatalf("read dashboard_board_queries.go: %v", err)
	}
	code = stripGoCommentsKeepLines(string(raw))
	if !strings.Contains(code, `payload["degraded"] = true`) {
		t.Error("dashboard_board_queries.go 的回退路径没有把 credits 降级写进载荷 —— " +
			"看板首屏「总积分消耗」会显示 0 而无任何提示")
	}
}

// TestBoardOpsQueriesReportDegradation 把守 dashboard_board_aux.go 的豁免。
//
// ## 为什么它原先的豁免理由是错的
//
// 原理由写的是「失败降级为空状态不影响页面数字」。查证后发现**正是相反的**：
// queryBoardBackgroundTasks / queryBoardSelfCheck 的结果进的是
// BoardOpsBar.vue 的两个运维 chip，而那里面是 `?? 0` 加一个**恒亮绿点**。
// 查询失败 ⇒ 页面说「近 10 分钟检查 0 次」+ 绿色健康灯。
//
// 一个职责是「陈述系统健康」的组件，在**不知道**的时候主动宣称健康，
// 比显示不出数字糟得多。所以这条豁免不是「不影响」，是「影响且很严重」。
func TestBoardOpsQueriesReportDegradation(t *testing.T) {
	raw, err := os.ReadFile("dashboard_board_aux.go")
	if err != nil {
		t.Fatalf("read dashboard_board_aux.go: %v", err)
	}
	code := stripGoCommentsKeepLines(string(raw))
	// 1) 不得再有丢弃的查询错误。
	if strings.Contains(code, "_ = h.db.QueryRow") {
		t.Error("dashboard_board_aux.go 仍在丢弃查询错误（`_ = h.db.QueryRow`）—— " +
			"运维 chip 会在查不出来时显示 0 + 绿点，等于宣称系统健康")
	}
	// 2) 两个查询函数都必须恒发 degraded。
	for _, need := range []string{`out["degraded"] = discErr != nil || checksErr != nil`} {
		if !strings.Contains(code, need) {
			t.Errorf("dashboard_board_aux.go 缺 %q —— "+
				"前端无法区分「真的 0 次」与「没查出来」", need)
		}
	}
	if !strings.Contains(code, `"degraded": selfErr != nil`) {
		t.Error("queryBoardSelfCheck 缺恒发 degraded —— " +
			"自检成功率 0.0% 会被读成「全都不健康」或「健康」，取决于用户怎么读那个 0")
	}
}

// TestBoardPieDegradationLedger 把守 dashboard_board_fallback.go 的豁免。
//
// 该文件（2026-10-03 由并行会话写入）不是简单地「返回空数组」：
// 它用 boardPieDegradation 逐维度记账 —— clients/errors/models/... 里
// 哪几个算不出来就记哪几个，最后由 applyBoardDegradation 写进载荷的
// degraded_pies.dimensions。
//
// 之所以能整片豁免，是因为这套账本确实存在且被载荷消费。**核实这一点
// 正是豁免成立的前提**，否则它就退化成第 17 节记的那种欠账。
func TestBoardPieDegradationLedger(t *testing.T) {
	raw, err := os.ReadFile("dashboard_board_fallback.go")
	if err != nil {
		t.Fatalf("read dashboard_board_fallback.go: %v", err)
	}
	code := stripGoCommentsKeepLines(string(raw))
	for _, need := range []string{
		"type boardPieDegradation struct",
		"degraded.Keys = append(degraded.Keys, key)",
		"degraded.MissingView == \"\"",
	} {
		if !strings.Contains(code, need) {
			t.Errorf("dashboard_board_fallback.go 缺 %q —— "+
				"饼图维度失败会退回「空数组且无记录」，"+
				"「这个维度没数据」与「算不出来」再次同形", need)
		}
	}
	// 账本必须真的进载荷，否则它只是一本没人读的账。
	raw, err = os.ReadFile("dashboard_degrade.go")
	if err != nil {
		t.Fatalf("read dashboard_degrade.go: %v", err)
	}
	code = stripGoCommentsKeepLines(string(raw))
	if !strings.Contains(code, `payload["degraded_pies"]`) {
		t.Error("boardPieDegradation 账本没有进载荷 —— " +
			"逐维度记账做了，但前端读不到（产出正确、没到消费者）")
	}
}

// TestBoardPiesDegradationReachesPayload 把守「账本接线」这一环。
//
// ## 为什么需要它：第一版判据漏了接线，变异**逃过**了
//
// 变异把 `payload["degraded_pies"]` 改成 `payload["degraded_pies_unused"]`，
// 门是**绿的**。原因很直白：原判据只查 dashboard_degrade.go 里
// 有没有出现 `payload["degraded_pies"]` 这个**字符串**，
// 完全没查**调用方有没有真的调用 applyBoardDegradation**。
//
// 于是「定义了写入函数」与「有人调用它」被当成了一件事 ——
// 与第 17.3 节「helper 静默 return 0"的漏洞同型，只是方向相反：
// 那条是「调用点写了标记但标记恒为 false」，这条是「标记函数没人调」。
//
// 所以这条判据查的是**调用关系**，不是文本存在性：
// 两个载荷构造点（直查路径与 Redis 缓存路径）都必须调用它。
//
// 只查直查路径是不够的：这份 payload 会被塞进 Redis 再读回来，
// 缓存路径不打标记 = 换个入口标记就消失。
func TestBoardPiesDegradationReachesPayload(t *testing.T) {
	// 期望的调用形态：第二个实参必须是**那个真实的降级账本变量**。
	//
	// 为什么不能只查函数名：变异 `applyBoardDegradation(payload, boardPieDegradation{}, err)`
	// 既编译通过、又调了正确的函数，但传的是零值 ⇒ 降级标记恒为 false。
	// 这与第 17.3 节「helper 静默 return 0，签名不变」是同一个形状：
	// **签名与调用都对，但值是空的**。所以必须匹配实参本身。
	for _, f := range []string{"dashboard_board.go", "dashboard_board_cache.go"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		code := stripGoCommentsKeepLines(string(raw))
		var call string
		for _, ln := range strings.Split(code, "\n") {
			t := strings.TrimSpace(ln)
			if strings.HasPrefix(t, "applyBoardDegradation(") {
				call = t
				break
			}
		}
		if call == "" {
			t.Errorf("%s 没有调用 applyBoardDegradation —— "+
				"饼图/趋势的降级账本没有被写进载荷，"+
				"页面会把「算不出来」当成「为 0」", f)
			continue
		}
		// 实参里必须出现账本变量名，且不得是零值字面量。
		if !strings.Contains(call, "piesDegraded") {
			t.Errorf("%s 的调用是 %s —— 没有把降级账本传进去。"+
				"零值账本 ⇒ degraded 恒 false（与「调用正确但传空值」同型漏洞）",
				f, call)
		}
		if strings.Contains(call, "boardPieDegradation{}") {
			t.Errorf("%s 的调用是 %s —— 传的是零值账本而不是实际降级结果", f, call)
		}
	}
	// 顺带钉住「不得再用 _ 丢弃这两个调用的错误」——
	// `pies, _ := h.resolveBoardPies(...)` 是本轮修掉的原始缺陷。
	raw, err := os.ReadFile("dashboard_board.go")
	if err != nil {
		t.Fatalf("read dashboard_board.go: %v", err)
	}
	code := stripGoCommentsKeepLines(string(raw))
	for _, bad := range []string{"pies, _ :=", "trends, _ :="} {
		if strings.Contains(code, bad) {
			t.Errorf("dashboard_board.go 仍有 %q —— "+
				"错误被丢弃，载荷里不会有任何降级痕迹", bad)
		}
	}
}

// TestBoardFallbackErrorsPropagate 钉住「看板回退失败不许被吞掉」。
//
// ## 背景
//
// 2026-10-03 之前 fallbackBoardSummary 里是 `_ = h.db.QueryRow(...).Scan(...)`：
// minute 统计不可用 → 走 request_logs 回退 → 回退自己也失败（42P01）→
// **整屏数字全是 0，且没有任何降级标记**。请求数 0、Token 0、费用 $0.00，
// 页面上看起来就是「这个时段没有流量」。
//
// 同文件的 fallbackBoardPies / fallbackBoardTrends / fallbackErrorDrill
// 一直都返回 error —— 只有 summary 这条把错误吃了，签名里根本没有 error。
//
// ## 为什么这条与范围表分开写
//
// 范围表里这两个文件的旧理由是「载荷形状与前端展示口径未定」/「需先定降级展示口径」
// —— 指向别处、留待将来，是**欠账**。现在查完了，理由改成实测结论。
func TestBoardFallbackErrorsPropagate(t *testing.T) {
	raw, err := os.ReadFile("dashboard_board_queries.go")
	if err != nil {
		t.Fatalf("read dashboard_board_queries.go: %v", err)
	}
	code := stripGoCommentsKeepLines(string(raw))

	// 1) 回退函数必须返回 error（签名层面就不许吞）。
	//
	// 用源码文本判定而不是 reflect：MethodByName 只导出**已导出**方法，
	// fallbackBoardSummary 是小写的，第一版用它必然报「不存在」——
	// 那是我自己写出来的假红，不是产品问题。
	sigLine := ""
	for _, ln := range strings.Split(code, "\n") {
		if strings.HasPrefix(ln, "func (h *Handler) fallbackBoardSummary(") {
			sigLine = ln
			break
		}
	}
	if sigLine == "" {
		t.Fatal("dashboard_board_queries.go 里没有 fallbackBoardSummary 的定义 —— " +
			"请确认看板回退路径的去向")
	}
	if !strings.Contains(sigLine, "map[string]any, error") {
		t.Errorf("fallbackBoardSummary 的签名是 %q，第二个返回值不是 error —— "+
			"回退失败无法被调用方看见，整屏 0 会被当成真值", strings.TrimSpace(sigLine))
	}

	// 2) 函数体内不得再有丢弃的 Scan/Query 错误。
	body := code[strings.Index(code, "func (h *Handler) fallbackBoardSummary"):]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	if strings.Contains(body, "_ = h.db.Query") {
		t.Error("fallbackBoardSummary 仍在丢弃查询错误（`_ = h.db.Query...`）—— " +
			"回退失败会变成一张全 0 且无标记的 summary")
	}

	// 3) 两个调用点都必须接住 error。
	for _, f := range []string{"dashboard_board.go", "dashboard_board_cache.go"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		code := stripGoCommentsKeepLines(string(raw))
		if !strings.Contains(code, "h.fallbackBoardSummary(") {
			t.Errorf("%s 不再调用 fallbackBoardSummary：请确认看板回退路径", f)
			continue
		}
		if !strings.Contains(code, "fbErr") {
			t.Errorf("%s 调用 fallbackBoardSummary 但没有接住 error —— "+
				"回退失败仍然会被静默忽略", f)
		}
	}

	// 4) 42P01 必须在载荷里自报（保持 200 降级形状，但不许无标记）。
	if !strings.Contains(code, `payload["degraded_summary"] = true`) {
		t.Error("回退载荷没有 degraded_summary 标记：42P01 时全 0 的 summary " +
			"会被当成「没有流量」")
	}
}

// TestBoardSummaryDegradationScopeIsolated 把守「降级标记的作用域不许混用」。
//
// ## 这条门在挡什么（它是 2026-10-03 真实漏掉的一处）
//
// summary 载荷里有两个**不同作用域**的降级：
//
//	· degraded_summary / summary_missing_view —— 整屏（请求/Token/费用全 0）
//	· credits_missing_view                  —— 其中一个指标（只有积分没算出来）
//
// 修前两者都写载荷顶层的 `degraded` / `degraded_reason`，
// 于是 `dashboard_board_queries.go` 里：
//
//	if scanErr != nil { payload["degraded_reason"] = view }        // 先写
//	if creditsView != "" { payload["degraded_reason"] = creditsView }  // 覆盖
//
// 后果有两个，都真实发生过：
//
//	① 整屏降级 + 积分降级同时发生时，前端的整屏原因被积分原因覆盖；
//	② 前端 `creditsDegraded` 读 `summary.degraded`，
//	   于是**整屏**降级（pies 挂了、数字是真的）也会把积分卡标成「不可信」。
//
// 为什么之前没被发现：那条判据只查 `degraded_summary` 这个键**在不在**，
// 完全没查**作用域**。键在、但与另一个作用域共用同一对顶层键，照样过。
//
// 所以这里断言的是「整屏降级分支不碰顶层键」，而不是「有降级标记」。
func TestBoardSummaryDegradationScopeIsolated(t *testing.T) {
	raw, err := os.ReadFile("dashboard_board_queries.go")
	if err != nil {
		t.Fatalf("read dashboard_board_queries.go: %v", err)
	}
	code := stripGoCommentsKeepLines(string(raw))

	// 取出整屏降级分支（scanErr != nil）的函数体切片。
	idx := strings.Index(code, `payload["degraded_summary"] = true`)
	if idx < 0 {
		t.Fatal("找不到整屏降级分支 —— 判据失效，请先确认代码形状")
	}
	// 该分支的范围：从 if scanErr != nil 到下一个 if（credits 分支）之前。
	tail := code[idx:]
	end := strings.Index(tail, "if creditsDegradedView")
	if end < 0 {
		t.Fatalf("找不到 credits 降级分支 —— 判据失效（范围切片依赖它作边界）")
	}
	summaryBranch := tail[:end]

	for _, forbidden := range []string{`payload["degraded"]`, `payload["degraded_reason"]`} {
		if strings.Contains(summaryBranch, forbidden) {
			t.Errorf("整屏降级分支写了 %s —— 这是**载荷顶层**的 board 整体标记，\n"+
				"summary 是子对象。两个作用域共用同一对键的后果：\n"+
				"  ① credits 分支后写会覆盖掉整屏降级原因；\n"+
				"  ② 前端读 summary.degraded 时，会把整屏/饼图降级误当成积分降级。\n"+
				"整屏降级只该写 degraded_summary / summary_missing_view / summary_hint。",
				forbidden)
		}
	}

	// 反向：credits 分支必须仍写 credits 自己的键（否则这次拆分把功能删了）。
	if !strings.Contains(tail[end:], `payload["credits_missing_view"]`) {
		t.Error("credits 降级分支没有写 credits_missing_view —— 拆分作用域时把功能删掉了")
	}
	// 顶层 degraded_reason 必须带作用域前缀，否则前端无法区分是哪一块降级。
	if !strings.Contains(code, `payload["degraded_reason"] = "credits: "`) {
		t.Error(`credits 分支的 degraded_reason 没有作用域前缀（应为 "credits: " + view）—— ` +
			"顶层 degraded_reason 会被拼进 board 整体的原因串，不带前缀时读不出是哪一块降级")
	}

	// ★ 本门自身的一条对照：**注释里提到键名不算写它**。
	//
	// 这不是假设，是本轮真实踩到的：手工核对时我用一段没去注释的探针查
	// 「整屏分支是否碰顶层键」，结果 True —— 真值是那几行解释性注释里
	// 写着 `payload["degraded"]`。门用了 stripGoCommentsKeepLines 所以判对了，
	// 探针没去所以判错了。
	//
	// 记在这里是因为**下一个人**很可能写同样的粗粒度探针。
	// 反向验证：把注释里那句键名去掉，门仍然 PASS（说明门本来就不看注释）。
	_ = code
}

// countDegradeSites 数「真正写出降级载荷的站点」，与 findUnmarkedDegradeSites
// 用同一套排除规则。
//
// 为什么不能直接 strings.Count：2026-10-03 实测口径就是错的 ——
// `func isMissingSessionDim(err error) bool { return IsMissingRelationError(err) && ... }`
// 这类**判定函数**会被数成一个降级站点，于是 session_active.go 报「2 处 42P01
// 判定但只调了 1 次 writeDegraded」，把一个谓词说成了一次绕过标记的写入。
// 同理 dashboard_degrade.go 的定义处也会被计入范围表的计数，
// 「已登记 N 处」这个数字本身就是虚高的。
func countDegradeSites(code, file string) int {
	lines := strings.Split(code, "\n")
	n := 0
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") ||
			strings.HasPrefix(trimmed, "/*") {
			continue
		}
		// 函数声明与 return 判断表达式不是降级站点。
		if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "return ") {
			continue
		}
		for _, helper := range degradeHelpers {
			if strings.Contains(ln, helper) {
				n++
				break
			}
		}
	}
	return n
}

// TestDegradeMarkerIsAlwaysEmitted 钉住「恒发」这个决定。
//
// 反向对照：先断言 JSON tag 真的是 degraded 且没有 omitempty。
// 若有人后来加上 omitempty，false 会被整个丢掉 —— 字段缺失与 false 在
// API 语义上同形，那正好把这个标记自己废掉一半。
func TestDegradeMarkerIsAlwaysEmitted(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(PeriodCompareResponse{}),
		reflect.TypeOf(CacheEconomicsResponse{}),
		reflect.TypeOf(DegradedListResponse{}),
		reflect.TypeOf(usageTrendSeriesResponse{}),
		reflect.TypeOf(usageTrendModelsResponse{}),
	} {
		f, ok := typ.FieldByName("Degraded")
		if !ok {
			t.Errorf("%s 缺 Degraded 字段：降级载荷无法自报家门", typ)
			continue
		}
		tag := f.Tag.Get("json")
		if tag != "degraded" {
			t.Errorf("%s.Degraded 的 json tag = %q, want \"degraded\"", typ, tag)
		}
		if strings.Contains(tag, "omitempty") {
			t.Errorf("%s.Degraded 带 omitempty：健康时 false 会被整个丢掉，"+
				"「字段缺失」与「false」在 API 语义上无法区分，标记就白加了", typ)
		}
	}
}

// TestDegradedListConstructorAlwaysDegraded 单独把守 degradedList 构造器。
//
// 背景：扫描门把 `degradedList(` 认作降级标记，于是「构造器是否真的发标记」
// 从「站点里看得见」变成了「构造器里藏着」—— 扫描门照不到那里。
// 如果有人日后把构造器改成按参数决定是否降级（为了复用），
// 三个调用点会继续被门判为合规，而载荷已经不带标记了。
// 这就是「判据的元素集合要先证明覆盖全集」：门认了一个构造器，
// 就必须有另一条门证明那个构造器本身可信。
func TestDegradedListConstructorAlwaysDegraded(t *testing.T) {
	got := degradedList("usage_ledger_with_current_month")
	if !got.Degraded {
		t.Error("degradedList() 返回 Degraded=false —— 三个裸数组端点的降级载荷将不带标记，" +
			"页面重新与「真的没有数据」同形")
	}
	if got.Items == nil {
		t.Error("degradedList() 的 Items 为 nil：JSON 会序列化成 null 而不是 []，" +
			"前端 unwrapDegradedList 拿到 null 后 items 也会是 null")
	}
	if got.MissingView != "usage_ledger_with_current_month" {
		t.Errorf("degradedList() 的 MissingView = %q，未回填传入的视图名",
			got.MissingView)
	}
	if got.Hint == "" {
		t.Error("degradedList() 未给出运维提示：用户看到降级横幅却不知道该做什么")
	}
	// 视图名为空时也必须自报 —— 「不知道缺哪个视图」不能变成「不标记」。
	if empty := degradedList(""); !empty.Degraded || empty.Hint == "" {
		t.Error("degradedList(\"\") 丢了标记或提示：缺视图名是最难排查的一类降级，" +
			"恰恰最需要标记")
	}
}

// TestUserProfileDegradeHasMarker 单独钉 map 载荷那处。
//
// userProfile.list 返回 map[string]any，没有结构体可反射，只能查字面量。
func TestUserProfileDegradeHasMarker(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(".", "user_profile.go"))
	if err != nil {
		t.Fatalf("read user_profile.go: %v", err)
	}
	code := stripGoCommentsKeepLines(string(raw))
	if !strings.Contains(code, "IsSchemaBehindError(") {
		t.Skip("user_profile.go 已不再降级 —— 本断言随之失效，请确认是有意移除")
	}
	if !strings.Contains(code, `"degraded"`) {
		t.Error("user_profile.go 的降级载荷没有 \"degraded\" 标记：" +
			"空列表与「真的没有用户」在页面上同形")
	}
}
