package bg

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestCandidateFailureMonitorClassifyStale 是 2026-10-20 24h 审计轮的 S4
// 停写假阳性修复的门。
//
// 修复前：candidate_failure_monitor 读到「failure log 冻结 + request log
// 活跃」就一律 slog.Error，而该形态在 S4 退役 / 主动停写场景下是**预期内**
// 的（failure writer 不再写，gateway 仍服务用户流量）⇒ ERROR 把预期内状态
// 当成故障告警，反复触发直到停写期结束。
//
// 修复后：分类器按 source_max_failure_id 是否 > 0 区分两类：
//   - > 0 ⇒ failure writer 曾活跃过 ⇒ 视作 S4 退役 / 主动停写 ⇒ WARN
//   - = 0 ⇒ failure writer 从未写入过 ⇒ 视作 writer 真坏 ⇒ ERROR
//
// 此门钉死「两类形态各自的判定方向」，并用一个**真实 S4 停写复现**的端到端
// 例子（lastFailure=2026-10-17 02:48 / lastRequest=2026-10-19 10:05，
// failureSourceMaxID=40 > 0）走全部分类器分支。
func TestCandidateFailureMonitorClassifyStale(t *testing.T) {
	tests := []struct {
		name string
		fid  int64
		want stalenessVerdict
	}{
		{
			name: "S4 停写场景：failure writer 曾经活跃（source_max=40），现在冻结 ⇒ 分类为 likelyStopWrite",
			fid:  40,
			want: stalenessLikelyStopWrite,
		},
		{
			name: "writer 长期运行后冻结：source_max=1048576 ⇒ 分类为 likelyStopWrite",
			fid:  1048576,
			want: stalenessLikelyStopWrite,
		},
		{
			name: "writer 从未写入：source_max=0 ⇒ 分类为 writerNeverWrote（真坏）",
			fid:  0,
			want: stalenessWriterNeverWrote,
		},
		{
			name: "writer max_id 落在 bigint 极小值：source_max=1 ⇒ 仍然判定曾活跃 ⇒ likelyStopWrite",
			fid:  1,
			want: stalenessLikelyStopWrite,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := candidateFailureMonitorClassifyStale(tt.fid); got != tt.want {
				t.Fatalf("candidateFailureMonitorClassifyStale(%d) = %v, want %v",
					tt.fid, got, tt.want)
			}
		})
	}
}

// TestCheckStalenessLogsWarnOnS4StopScenario 复现 S4 停写 + request 写活的
// 现场，断言日志走 WARN 而不是 ERROR。
//
// 2026-10-19/20 生产上观察到的形态（failures 完全冻结、requests 仍有）：
//
//	last_insert_at:   2026-10-17 02:48 (failure writer 已 72h+ 没写)
//	last_request_at:  2026-10-19 10:05 (request 端 5 分钟内仍有行)
//	source_max_failure_id: 40 (failure writer 曾写入 ⇒ 视作 S4 退役)
//	source_max_request_id: 任意正值
//
// 期望：slog.Warn（不是 slog.Error），并把 source_max_failure_id /
// source_max_request_id 两个字段都透传出来。
func TestCheckStalenessLogsWarnOnS4StopScenario(t *testing.T) {
	monSrc := mustReadSourceBg(t, "candidate_failure_monitor.go")
	// 提取 checkStaleness 函数体（到下一个 `func ` 之前）。
	const anchor = "func (m *CandidateFailureMonitor) checkStaleness("
	i := strings.Index(monSrc, anchor)
	if i < 0 {
		t.Fatal("checkStaleness not found in candidate_failure_monitor.go")
	}
	body := monSrc[i:]
	if j := strings.Index(body, "\nfunc "); j >= 0 {
		body = body[:j]
	}
	// ★★ 必须先**剥注释**再断言（2026-10-21 实测出的判据缺陷）：
	//   checkStaleness 的**注释里**就写着「source_max_failure_id / source_max_request_id
	//   两个诊断字段透传」⇒ 原来那两条 `strings.Contains(body, ...)` 断言
	//   **可以被注释满足** ⇒ 把日志里的字段真删掉，这道门**仍然全绿**
	//   （实测证据：scripts/mutate-candidate-failure-monitor-classify-gate.py 的
	//   M5 变异「把两个诊断字段换成不含目标词的形状」⇒ 变异后判据全绿）。
	//   ⇒ 「判据一旦能在注释里被满足，就不是判据」。
	clean := stripGoSourceComments(body)
	// ⚠ 第二重陷阱：被提取的函数体里**还包含** `const sourceMaxRequestIDKey =
	//   "source_max_request_id"` 这一行（它在 checkStaleness 之后、下一个 `func `
	//   之前）⇒ 光靠「这个字面量出现过」仍然会被**常量声明**满足 ⇒
	//   下面两条断言钉的是**键值对形状**（key + value 变量），那个形状只出现在
	//   日志调用里。
	// ★★ 2026-10-21（批判式审计轮）：断言不再只判「形状」，还要判**关系**——
	//   日志透传的标识符必须与分类器 switch 的实参**是同一个**（见下面 ③）。
	//   值标识符**不钉死** ⇒ 语义不变的改名必须仍然绿（反向变异 R1）。
	failureVals := cfmFailureIDFieldRE.FindAllStringSubmatch(clean, -1)
	if len(failureVals) == 0 {
		t.Fatalf("checkStaleness 必须透传 source_max_failure_id 字段（键值对形状）；got:\n%s", clean)
	}
	requestVals := cfmRequestIDFieldRE.FindAllStringSubmatch(clean, -1)
	if len(requestVals) == 0 {
		t.Fatalf("checkStaleness 必须透传 %s 字段（键值对形状）；got:\n%s", sourceMaxRequestIDKey, clean)
	}
	// 分类器必须被调用（不直接写 if-else，必须走纯函数，否则单测覆盖不到）。
	callVals := cfmClassifierCallRE.FindAllStringSubmatch(clean, -1)
	if len(callVals) == 0 {
		t.Fatalf("checkStaleness 必须调用 candidateFailureMonitorClassifyStale(<某个标识符>)；got:\n%s", clean)
	}
	// switch 必须落在分类器调用上（而不是别的表达式）。
	switchVals := cfmClassifierSwitchRE.FindAllStringSubmatch(clean, -1)
	if len(switchVals) == 0 {
		t.Fatalf("checkStaleness 必须用 switch <classifier>(<某个标识符>)；got:\n%s", clean)
	}
	// ③ ★ 关系不变式（2026-10-21 新增）：日志里透传的 failure id 标识符必须与
	//    分类器 switch 的实参**是同一个标识符** —— 否则日志透传的根本不是
	//    「被分类的那个值」（例如把 request 侧的 max id 当成 failure 侧透传）。
	//    ⚠ 两处日志（WARN 分支 + ERROR 分支）透传的必须是**同一个**标识符。
	classifierArg := switchVals[0][1]
	for _, m := range failureVals {
		if m[1] != classifierArg {
			t.Fatalf("日志里的 source_max_failure_id 透传的标识符 %q 与分类器实参 %q 不是同一个 ⇒ 透传的不是被分类的那个值；got:\n%s",
				m[1], classifierArg, clean)
		}
	}
	for _, m := range callVals {
		if m[1] != classifierArg {
			t.Fatalf("分类器调用点的实参 %q 与 switch 的实参 %q 不是同一个；got:\n%s", m[1], classifierArg, clean)
		}
	}

	// 2. SQL 必须同时读 max(aggregation_id)（failure 侧）与 max(id)（request 侧）。
	//    只有 max(ts) 不能区分 S4 退役与 writer 真坏。
	if !strings.Contains(clean, "max(aggregation_id) FROM candidate_failure_logs_with_current_month") {
		t.Fatalf("checkStaleness 必须读 max(aggregation_id) FROM candidate_failure_logs_with_current_month；got:\n%s", clean)
	}
	if !strings.Contains(clean, "max(id) FROM") {
		t.Fatalf("checkStaleness 必须读 request 侧的 max(id) 用于 source_max_request_id；got:\n%s", clean)
	}

	// 3. S4 停写形态走 WARN，不是 ERROR：函数体里必须出现 slog.Warn，且
	//    该 slog.Warn 的 msg 文案必须可被搜得到「failure writer previously
	//    active; likely intentional stop-write」——这是判定方向的钉子。
	//    ⚠ 防回归：若有人改回 slog.Error 而不删 slog.Warn，这一条会把 warning
	//    判定为「存在但 msg 文案变了」⇒ 不会误放过去。
	if !strings.Contains(clean, `slog.Warn("candidate_failure_monitor: failure log is stale (failure writer previously active; likely intentional stop-write)"`) {
		t.Fatalf("checkStaleness 的 S4 停写分支必须 slog.Warn(msg with 'failure writer previously active; likely intentional stop-write')；got:\n%s", clean)
	}

	// 4. writer 真坏走 ERROR：必须保留一条 slog.Error 路径（msg 文案以
	//    「failure writer never wrote any rows」为特征）。这一条是 negative
	//    space：删了它就会让 S4 停写和 writer 真坏共用同一形态，告警面再次混淆。
	if !strings.Contains(clean, `slog.Error("candidate_failure_monitor: failure log is stale (failure writer never wrote any rows)"`) {
		t.Fatalf("checkStaleness 的 writer 真坏分支必须 slog.Error(msg with 'failure writer never wrote any rows')；got:\n%s", clean)
	}

	// 5. 分类器必须被调用（不直接写 if-else，必须走纯函数，否则单测覆盖不到）：
	//    ★ 2026-10-21：这一条已由上面的 cfmClassifierCallRE（callVals）承担 ——
	//    旧写法 `strings.Contains(clean, "candidateFailureMonitorClassifyStale(fid)")`
	//    把实参名 `fid` 写死 ⇒ 语义不变的改名会假红 ⇒ 改成「捕获实参标识符」。

	// 6. S4 停写判定方向正确：分类器把 fid>0 映射为 stalenessLikelyStopWrite，
	//    该 case 标签必须驱动 slog.Warn(「failure writer previously active…」)
	//    这一行，而不是 slog.Error。换言之：scan 整个 checkStaleness 函数体，
	//    ① 找到 switch 语句块（最近的 `switch … {` 到匹配 `}`），② 在块内
	//    同时含 `case stalenessLikelyStopWrite:` 与 slog.Warn(…stop-write…) 文案，
	//    ③ 含 `default:` 且 default 出现在 slog.Warn 之后（默认分支走 ERROR）。
	//
	// ⚠ 不再用「slog.Warn 与 slog.Error 之间的子串」那种位置断言：switch 的写法
	// 可以把 case 标签放在 slog.Warn 之前也可以放在之后，单测不该把实现绑死到
	// 某一种排版。改为「switch 块内文本满足三个不变式」即可。
	//
	// ★ 2026-10-21：定位 switch 块也**不钉实参名**（旧写法钉的是
	//   "switch candidateFailureMonitorClassifyStale(fid)"）⇒ 改用正则定位
	//   「switch <任意函数名>(<标识符>)」这一段，取其起始下标。
	switchLoc := cfmClassifierSwitchRE.FindStringIndex(clean)
	if switchLoc == nil {
		t.Fatalf("checkStaleness 必须用 switch <classifier>(<某个标识符>)；got:\n%s", clean)
	}
	switchStart := switchLoc[0]
	switchEnd := strings.Index(clean[switchStart:], "\n\t}")
	if switchEnd < 0 {
		t.Fatalf("switch 块找不到收尾 \\n\\t}；got:\n%s", clean[switchStart:])
	}
	switchBody := clean[switchStart : switchStart+switchEnd]

	// slog.Warn / slog.Error 都是多行调用，msg 在单独一行 ⇒ 只断言开括号
	// 一行（含 msg 字面量），别断言收 `)` —— 收括号在另一行，含进去会把
	// 单测绑死到排版，将来谁把同一调用换成单行写法就误报。
	stopWriteMsg := `slog.Warn("candidate_failure_monitor: failure log is stale (failure writer previously active; likely intentional stop-write)"`
	neverWroteMsg := `slog.Error("candidate_failure_monitor: failure log is stale (failure writer never wrote any rows)"`

	if !strings.Contains(switchBody, "case stalenessLikelyStopWrite:") {
		t.Fatalf("switch 块必须 case stalenessLikelyStopWrite:；got:\n%s", switchBody)
	}
	if !strings.Contains(switchBody, stopWriteMsg) {
		t.Fatalf("switch 块必须 slog.Warn(…stop-write…)；got:\n%s", switchBody)
	}
	if !strings.Contains(switchBody, neverWroteMsg) {
		t.Fatalf("switch 块必须 slog.Error(…never wrote…)；got:\n%s", switchBody)
	}
	if !strings.Contains(switchBody, "default:") {
		t.Fatalf("switch 块必须 default: 兜底分支；got:\n%s", switchBody)
	}
	// 顺序：slog.Warn(…stop-write…) 必须在 default: 之前出现（因为它挂在
	// case stalenessLikelyStopWrite: 之下），slog.Error(…never wrote…) 必须在
	// default: 之后出现。
	warnPos := strings.Index(switchBody, stopWriteMsg)
	defPos := strings.Index(switchBody, "default:")
	errPos := strings.Index(switchBody, neverWroteMsg)
	if !(warnPos >= 0 && defPos >= 0 && errPos >= 0 && warnPos < defPos && defPos < errPos) {
		t.Fatalf("switch 块顺序应为 slog.Warn(stop-write) → default: → slog.Error(never wrote)；got warn=%d default=%d err=%d", warnPos, defPos, errPos)
	}
}

// TestFailureLogIsStaleKeepsQuietGatewayBaseline 保护原有「网关无流量时不告警」
// 的行为不被 S4 修复连带改坏。这一条失败就意味着有人误改了
// failureLogIsStale 的语义（lastRequest=nil 应返回 false）。
func TestFailureLogIsStaleKeepsQuietGatewayBaseline(t *testing.T) {
	now := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	if failureLogIsStale(&old, nil, now, 30*time.Minute) {
		t.Fatal("quiet gateway（lastRequest=nil）必须不告警；现行为被 S4 修复连带改坏了")
	}
	// 同步核：连 lastFailure 也不存在时也应不告警（网关从未有过失败）。
	if failureLogIsStale(nil, nil, now, 30*time.Minute) {
		t.Fatal("网关从未写过失败 + 当前无请求活动 ⇒ 必须不告警")
	}
}

// mustReadSourceBg 在 pkg 内 bg/credential_selfcheck_pick_test.go:128 已
// 定义（§9.26 提取助手），本文件直接复用，避免双份提取逻辑漂移。

// ─────────────────────────────────────────────────────────────────────────────
// 2026-10-21：判据自身的修复（实测发现的**判据缺陷**，不是产品缺陷）
//
// 现象：把 checkStaleness 日志里的两个诊断字段换成不含目标词的形状
//   （`"src_max_failure_id"` / `"src_max_request_id"`）⇒ 这道门**仍然全绿**。
//
// 根因（两重）：
//   ① checkStaleness 的**注释**里就写着「source_max_failure_id / source_max_request_id
//      两个诊断字段透传」⇒ 任何对**原始文本**做的 `strings.Contains` 断言
//      **可以被注释满足** ⇒ 「判据一旦能在注释里被满足，就不是判据」。
//   ② 被提取的函数体里**还包含** `const sourceMaxRequestIDKey = "source_max_request_id"`
//      这一行（它在 checkStaleness 之后、下一个 `func ` 之前）
//      ⇒ 光靠「这个字面量出现过」连**常量声明**都能满足 ⇒ 必须钉「键值对形状」。
//
// 修法：① 先剥注释（下面的 stripGoSourceComments）；② needle 改成 key+value 的
//   **键值对形状**（只可能出现在日志调用里）。两处都改完，M5 变异转红。
// ─────────────────────────────────────────────────────────────────────────────
//
// 2026-10-21（批判式审计轮 · 第二处判据缺陷）：**needle 钉死了变量名** ⇒ 假红
//
// 现象：把 checkStaleness 里的局部变量 `fid` **改名**成 `failureID`
//   （语义完全不变的重构）⇒ 这道门**变红**。
//
// 根因：needle 写的是 `"source_max_failure_id"\s*,\s*fid` ⇒ 判据把「实现的
//   命名」当成了「行为不变式」⇒ ① 任何人做一次无害改名就得先改判据；
//   ② 更坏的是：改判据比改实现「省事」⇒ 判据会开始**反向保护命名**，
//   而命名不是行为 ⇒ 判据的**信噪比**在下降（假红会让人习惯性地忽略这道门）。
//
// ★ 通用式：**判据要钉「关系」，不要钉「名字」**。名字是实现的自由度，
//   关系（「透传的必须是**被分类的那个值**」）才是行为不变式。
//
// 修法：needle 改成**捕获标识符**（见下面 var 块），断言改为：
//   ① 日志透传的标识符（所有出现处）**彼此一致**；
//   ② 分类器 switch 的实参标识符；
//   ③ ①与②**相等**。
//   ⇒ 改名 ⇒ 三者一起变 ⇒ 仍然一致 ⇒ **门必须仍绿**（反向变异 R1 钉这条）；
//   ⇒ 换成别的 key（M5）/ 透传另一个变量（M10）⇒ 必红。
// ─────────────────────────────────────────────────────────────────────────────

// cfmFailureIDFieldRE / cfmRequestIDFieldRE 钉「诊断字段以**键值对**形式出现在日志里」；
// cfmClassifierCallRE / cfmClassifierSwitchRE 捕获「分类器被调用」与「switch 的实参」。
//
// ⚠ 为什么不用裸字面量：裸字面量会被**注释**（根因①）与**常量声明**（根因②）满足；
//
//	而键值对形状（key 紧跟 value 变量）只出现在 slog 调用里 ⇒ 删掉字段必红。
//
// ★★ 2026-10-21（批判式审计轮）：**这四条 needle 都不钉变量名**，只把值标识符
//
//	**捕获出来**。旧版写的是 `"source_max_failure_id"\s*,\s*fid` ⇒ 把局部变量名
//	`fid` **写死进判据** ⇒ 一次**语义不变**的重命名（fid → failureID）就会让这道门
//	**假红** ⇒ 判据把「实现命名」当成了「行为不变式」⇒ 早晚会有人为了「让门绿」
//	而把改名改回去 ⇒ 判据开始**反向保护命名**（坏味道）。
//
// ✅ 改成「**捕获标识符 + 断言它们彼此一致**」——这才是真正的不变式：
//
//	① 日志里 source_max_failure_id 后面跟的标识符（**所有出现处**必须同一个）；
//	② 分类器 switch 的实参标识符；
//	③ ①与②必须**相等**（日志透传的必须是**被分类的那个值**）。
//
//	⇒ 改名 ⇒ ①②③**一起变** ⇒ 仍然一致 ⇒ 门**必须仍绿**（反向变异 R1 钉这条）。
//	⇒ 日志换成别的 key ⇒ ①无匹配 ⇒ 必红（M5 钉这条）。
//	⇒ 日志透传**另一个**变量 / switch 换了实参 ⇒ ①②不相等 ⇒ 必红（M10 钉这条）。
var (
	cfmFailureIDFieldRE   = regexp.MustCompile(`"source_max_failure_id"\s*,\s*([A-Za-z_][A-Za-z0-9_]*)`)
	cfmRequestIDFieldRE   = regexp.MustCompile(`sourceMaxRequestIDKey\s*,\s*([A-Za-z_][A-Za-z0-9_]*)`)
	cfmClassifierCallRE   = regexp.MustCompile(`candidateFailureMonitorClassifyStale\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)`)
	cfmClassifierSwitchRE = regexp.MustCompile(`switch\s+[A-Za-z_][A-Za-z0-9_]*\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)`)
)

// stripGoSourceComments 去掉 Go 源码里的行注释与块注释，**保留字符串字面量**。
//
// ★ 为什么必须有它：源码断言一旦能在注释里被满足，就不是判据。
//
//	（仓内同款纪律见 cmd/gateway/dual_read_gate_test.go 的 stripGoComments、
//	  cmd/tools/validate_sessions_v2/loader_test.go、autoupdate/testschema_guard_test.go。）
//
// ⚠ 与仓内另几处实现的差别：那几个用正则（**不解析字面量**）⇒ 字符串里的 `//`
//
//	会被吃掉。本实现是**状态机**（认得解释字符串 / 原始字符串 / rune 字面量）
//	⇒ 不会剥过头（见 TestStripGoSourceCommentsKeepsCodeAndLiterals 的第二腿）。
func stripGoSourceComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	const (
		stCode = iota
		stLineComment
		stBlockComment
		stInterpretedString
		stRawString
		stRune
	)
	state := stCode
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch state {
		case stCode:
			switch {
			case c == '/' && i+1 < len(src) && src[i+1] == '/':
				state = stLineComment
				i++
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				state = stBlockComment
				i++
			case c == '"':
				state = stInterpretedString
				b.WriteByte(c)
			case c == '`':
				state = stRawString
				b.WriteByte(c)
			case c == '\'':
				state = stRune
				b.WriteByte(c)
			default:
				b.WriteByte(c)
			}
		case stLineComment:
			// 行注释的内容整体丢弃，但**保留换行** ⇒ 行结构不变（后续按行/缩进定位仍成立）
			if c == '\n' {
				state = stCode
				b.WriteByte(c)
			}
		case stBlockComment:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				state = stCode
				i++
			}
		case stInterpretedString:
			b.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				b.WriteByte(src[i])
			} else if c == '"' {
				state = stCode
			}
		case stRawString:
			b.WriteByte(c)
			if c == '`' {
				state = stCode
			}
		case stRune:
			b.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				b.WriteByte(src[i])
			} else if c == '\'' {
				state = stCode
			}
		}
	}
	return b.String()
}

// TestStripGoSourceCommentsKeepsCodeAndLiterals 是剥注释器自己的**两腿自证**：
//
//	① **该剥的剥掉了**（行注释 / 块注释里的字样不再出现，代码本身还在）；
//	② **不该剥的没剥**（字符串字面量里的 `//`、原始字符串里的 `/* */`、rune `'/'`）。
//
// ⚠ 只有第一腿 ⇒ 剥过头（把字面量吃掉）这类缺陷会隐形；只有第二腿 ⇒
//
//	「剥不掉」这类缺陷会隐形。两腿都在，剥注释器自己才是被量的。
func TestStripGoSourceCommentsKeepsCodeAndLiterals(t *testing.T) {
	// ① 该剥的剥掉了
	in := "codeA\n// source_max_failure_id only in a comment\ncodeB\n/* source_max_request_id */\ncodeC\n"
	out := stripGoSourceComments(in)
	if strings.Contains(out, "source_max_failure_id") {
		t.Fatalf("行注释没被剥掉 ⇒ 源码断言可被注释满足；got:\n%s", out)
	}
	if strings.Contains(out, "source_max_request_id") {
		t.Fatalf("块注释没被剥掉 ⇒ 源码断言可被注释满足；got:\n%s", out)
	}
	for _, keep := range []string{"codeA", "codeB", "codeC"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("剥注释把代码 %q 也吃掉了；got:\n%s", keep, out)
		}
	}
	// ② 不剥过头：字面量里的斜杠必须留着
	in2 := "url := \"http://x/y\"\nraw := `/* not a comment */`\nr := '/'\n"
	out2 := stripGoSourceComments(in2)
	for _, keep := range []string{"http://x/y", "/* not a comment */", "'/'"} {
		if !strings.Contains(out2, keep) {
			t.Fatalf("剥过头了：字面量 %q 被吃掉；got:\n%s", keep, out2)
		}
	}
}
