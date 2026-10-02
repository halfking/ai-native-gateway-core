//go:build s4audit

package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────
// TestControlPlaneGatedFlagAgreesWithCode
//
// # 这道门补的是哪一类洞
//
// requestLogsControlPlaneReaders 的每条登记有一个 `Gated bool`（「该消费方是否被
// S4 写门覆盖」）。**在 2026-10-02 之前，没有任何一道门把 Gated 与代码里的实际
// 护栏对照过** —— 既有那道 TestRequestLogsControlPlaneKnownEntriesAreReal 只核
// 三件事：Evidence 非空、Evidence 在登记文件里逐字存在、`Live && !Gated` 时
// BlastRadius 非空。Gated 自己**从不被验证**。
//
// 后果已经发生了一次，而且是可以完整复原的：
//
//	af4ef4b32  12:14  写入 discovery/discovery.go 的登记，Gated:false，
//	                  Note 描述「NOT EXISTS 恒真 ⇒ 主动禁用仍在工作的凭据模型」
//	e52687954  12:25  §9.12 给 discovery/discovery.go 加上护栏
//	                  （staleExpiryMayRun 在 :1091，消费点在 :1110）
//	此后        ——   门全绿，登记表一个字都没动。
//
// 护栏加对了，而记录仍然对外声明「门管不到它」。下一个读这张表的人会照着
// `Gated:false` 得出「停写会误下架凭据模型」的结论，并据此安排灰度 —— 而那件事
// 已经被修掉了。**这张表当时正在说假话，而且没有一道门会发现。**
//
// # 为什么是「默认拒绝 + 具名豁免」而不是一刀切禁止
//
// 方向不对称：**「文件里有护栏」不能推出「登记的那个读点被门控」**。一个文件
// 可能有多个读点，护栏只盖住其中一个；护栏也可能在**调用方**而不是文件内部。
// 所以一刀切禁止会再次误伤正确代码。
//
// 判据因此是：文件（**剥掉注释后**）出现 S4 门控标识符 ⇒ 该登记必须
// ① `Gated: true`，或 ② 在 gatedFlagExemption 里具名说明为什么不是。
//
// 剥注释是硬要求，理由与 sourceFamilyOf 相同：门控标识符出现在 doc comment 里
// 是常态（`// 见 settings.KeyRequestLogsWriteEnabled`），不剥的话这道门会在大量
// 没有护栏的文件上误报 —— 而**一个把「在」报成「不在」的门比没有门更坏**，
// 因为它训练读者忽略自己。
//
// ⚠ **还必须剥 SQL 注释，这是本门第一版漏掉的一层。** Go 层的 `//` 与 `/* */`
// 正则**剥不掉 raw string 里 SQL 的 `--`**。实测命中：`bg/auto_route_affinity_worker.go`
// 的 `aggregate()` 查询里有一段 SQL 注释在解释 S4 的影响
// （`-- settings.KeyRequestLogsWriteEnabled 声明的 request_logs 宽族`），
// 而那个文件**根本没有 Go 层护栏**。第一版门在它上面误报了 ——
// **一个要求为不存在的护栏写豁免的门，等于逼人写假豁免。**
//
// 只在这里剥 `--` 到行尾是安全的：Go 源码里 `--` 只可能出现在字符串字面量中，
// 而本正则的结果**只用于「有没有护栏」这一个判断**，不参与任何别的匹配。
//
// # 为什么不断言反方向
//
// 「Gated:false ⇒ 文件里不该有护栏」是**证伪不了的**：护栏完全可能在调用方、
// 在另一个文件、或通过接口注入。所以本门只断言能被证明的那一侧。
// ─────────────────────────────────────────────────────────────────────────

// stripCommentsForGatePresence 依次剥 Go 行注释、Go 块注释、SQL 行注释。
//
// SQL 那一段**复用包内已有的** stripSQLLineComments（view_source_column_contract_test.go:312），
// 不另起一个同名正则：两个包级 `sqlLineCommentRE` 会直接编译冲突，而重复定义本身
// 就是「同一个概念两处实现」的隐患。
func stripCommentsForGatePresence(code string) string {
	code = gateStopWriteLineCommentRE.ReplaceAllString(code, " ")
	code = gateStopWriteBlockCommentRE.ReplaceAllString(code, " ")
	return stripSQLLineComments(code)
}

// s4GateIdentifierRE 匹配 S4 停写门控的标识符。
//
// 刻意**不**匹配裸的 `request_logs_write_enabled` 键名字面量：那会命中
// 「本文件要门控别人」的注释与常量声明，而不是「本文件被门控」。
var s4GateIdentifierRE = regexp.MustCompile(
	`RequestLogsWriteEnabled|requestLogsWriteEnabled|` +
		`usageCreditComparability|lookbackComparability|` +
		`staleExpiryMayRun|comparabilityGuard`)

// gatedFlagExemption 登记「文件里有 S4 门控标识符、但本条登记的读点并未被门控」
// 的**具名豁免**。
//
// 判据的默认是拒绝：把文件放进这里等于公开声明「这个标识符不是这个读点的护栏」，
// 并写下它到底是什么。空缺项会被本门判红。
//
// 每条必须回答同一个问题：**该标识符在这个文件里，落在谁头上？**
var gatedFlagExemption = map[string]string{
	"internal/trace/trace.go": "文件里唯一的门控是 `if !settings.RequestLogsWriteEnabled()`（:473），" +
		"但它在 **`FlushToPG`（:463-525）** 里，护的是 Redis→PG 的 **UPDATE 写入**（:495-496），" +
		"**不是本条登记的读点**。登记的 Evidence `SELECT trace_events, ts FROM request_logs_hot WHERE request_id = $1` " +
		"在 `LoadFromPG`（:601-）——**独立函数**，挂在 Recorder 上、不在 RedisRecorder 的写路径里，" +
		":601 之前只有 `requestID == \"\"` / `db == nil` 两个 nil 检查。调用方 " +
		"`admin/request_trace.go:148-156` 也无门控（:149 先试 Redis，miss 后直接进 LoadFromPG）。" +
		"⇒ 该读点**确实未被门控**，Gated:false 是对的。判据的另一半是：它虽然 Live:false（dormant），" +
		"但 §9.36 batch7 已把它按读端 `errors_out` 登记（停写后 PG 腿 0 行 ⇒ handleTrace 显式 404，响亮失败）。",

	"domains/hooks/observability/telemetry/client.go": "本文件**有**门控，但**全部在写路径**：" +
		"`requestLogsWriteEnabled()`(:38-40) 的调用点是 :1140（mirrorRequestBodies）、:1284 与 :2067（写事务快照 " +
		"logsWrite）、:2014（CorrectEstimatedUsage）、:2126（`if logsWrite` 包住 `UPDATE request_logs_hot`）。" +
		"本条登记的**两个读点都不在其中**：① `FindRecentGatewaySession`（:593-625，SQL 在 :607，Evidence " +
		"`AND gw_session_id LIKE 'gw\\_%'`）体内只有 `c == nil` / `dbPool == nil` / `since <= 0` 三组 nil 检查，**零门控**，" +
		"调用方 `domains/streaming/session_assignment.go:177` 全文件亦无 S4 标识符（:160 的 Redis 索引是 miss 后" +
		"仍会落回 DB finder 的快路径）；② `lookupTurnNumber`（:3776-3814）经 :2506-2513 提交，而 :2506 只判 " +
		"`outboxWriter != nil && GwSessionID != \"\" && requestLogEntryTerminal`，**不含 logsWrite**——" +
		":2122-2125 的注释明确写「outbox request-completed 会话事件在门控外照常提交」。" +
		"⇒ 两个读点**确实未被门控**，Gated:false 是对的。⚠ 这一条比 trace.go 更隐蔽：**护栏与读点在同一文件、同一包**，" +
		"只看「文件有没有门」必然误判，所以它必须留在豁免表里由人具名承担。",
}

// TestControlPlaneGatedFlagAgreesWithCode 是那道「Gated 从不被验证」的补口。
func TestControlPlaneGatedFlagAgreesWithCode(t *testing.T) {
	root := repoRootFromCaller(t)
	for file, v := range requestLogsControlPlaneReaders {
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Errorf("%s: 读取失败 %v（登记表里的文件必须存在）", file, err)
			continue
		}
		// 剥 Go 注释**与 SQL 注释**是硬要求，见文件头。
		code := stripCommentsForGatePresence(string(raw))

		loc := s4GateIdentifierRE.FindString(code)
		if loc == "" {
			continue // 文件里没有门控标识符 ⇒ Gated:false 无从冲突
		}

		if v.Gated {
			continue // 已声明被门控，与代码一致
		}

		if reason, ok := gatedFlagExemption[file]; !ok || strings.TrimSpace(reason) == "" {
			t.Errorf("%s: 登记 Gated:false，但该文件（剥注释后）出现 S4 门控标识符 %q。\n"+
				"这不是「门坏了」，是**登记表可能正在说假话**——见本文件头 discovery/discovery.go 的案例：\n"+
				"  af4ef4b32(12:14) 登记 Gated:false → e52687954(12:25) 加了护栏 → 此后门全绿，登记表没动。\n"+
				"三选一：\n"+
				"  ① 确认该读点确实被门控 ⇒ 把 Gated 改成 true（并清空 BlastRadius，\n"+
				"     因为它授权的那次写入在停写期间不再发生）；\n"+
				"  ② 该标识符管的是**别的**读点/在调用方 ⇒ 在 gatedFlagExemption 里具名登记，\n"+
				"     写清它落在谁头上（空缺项或空串会被本门判红）；\n"+
				"  ③ 该标识符只出现在注释里 ⇒ 不该出现在剥注释后的代码中，"+
				"请检查 s4GateIdentifierRE。",
				file, loc)
		}
	}
}

// TestGatedFlagExemptionIsNotStale 双向查豁免表：多登记的一条会让这道门永久失效。
//
// 方向与正门相反但同样重要：豁免表里的文件若已不再出现门控标识符（护栏被删了、
// 或标识符改名了），那条豁免就在**掩盖**一件应该重新判定的事。空字符串同样判红。
func TestGatedFlagExemptionIsNotStale(t *testing.T) {
	root := repoRootFromCaller(t)

	var stale []string
	for file := range gatedFlagExemption {
		if _, registered := requestLogsControlPlaneReaders[file]; !registered {
			stale = append(stale, file+"（不在控制面登记表里）")
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Errorf("%s: 读取失败 %v", file, err)
			continue
		}
		if !s4GateIdentifierRE.MatchString(stripCommentsForGatePresence(string(raw))) {
			stale = append(stale, file+"（剥注释后已无门控标识符——护栏可能被删了，该重判）")
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("gatedFlagExemption 里有 %d 条已失效的豁免：\n  %s\n"+
			"失效的豁免比没有豁免更坏：它让本门对这些文件**永久失效**，\n"+
			"而护栏的增删正是本门要盯的那件事。请删除或重写。",
			len(stale), strings.Join(stale, "\n  "))
	}
}
