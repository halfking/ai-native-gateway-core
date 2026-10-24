package extpointledger

// wiring.go —— 「**是否接了台账**」的**静态**真值源（P2-2 续）。
//
// # 为什么「接线」必须是静态事实，不能从运行期推
//
// ★ 初版把 `Unwired` 定义成 `Production && !Recorded`（`Recorded` = 运行期
// 「本次进程内被记到过」）⇒ ★★ **这是错的**：
//
//	① 一个**已经接了台账**、但**本次进程内还没有流量**的机制
//	   （比如刚启动还没来请求的网关）⇒ `Recorded=false` ⇒ `Unwired=true`
//	   ⇒ ★ 读端会显示「**尚未接入台账**」⇒ ★★ **而它其实接了**；
//	② ⇒ 也就是说我原本声称要解决的误读（「分不清『没接线』与『没流量』」），
//	   ★ **自己又犯了一遍** —— 只是把它从「没流量」挪到了「没接线」。
//
// ⇒ ⇒ **修法：把两个事实彻底分开**：
//
//	┌──────────────────┬────────────────────┬──────────────────────────┐
//	│ 事实             │ 来源               │ 字段                     │
//	├──────────────────┼────────────────────┼──────────────────────────┤
//	│ **接没接台账**   │ ★ **静态**（源码）  │ `Wired` / `Wiring`       │
//	│ 有没有流量       │ ★ **运行期**（计数）│ `Recorded` / `Calls`     │
//	│ 「在不在生产」   │ ★ **静态**（裁决表）│ `Production`             │
//	└──────────────────┴────────────────────┴──────────────────────────┘
//
// ⇒ `Unwired` = `Production && !Wired` ⇒ ★ 语义终于对上了：
// **「这个在产的扩展点，还没接台账」** —— 一个**与流量无关**的静态结论。
//
// # 这张表是**谁**在维护
//
// ★ 它由**门**（`scripts/ursmcheck/magpie_p2_extension_ledger_test.go`）
// 与**源码**对拍：表中每个机制的**接线文件**必须真的存在、且真的**用到**
// 该机制的常量（`extpoints.ID…`）⇒ ★ 两份独立来源互相印证：
//
//	静态半 = 本表（声明「接在哪」）；
//	源码半 = 真实的文件内容（证明「真的接了」）。
//
// ⇒ ★ 因此：**改了接线位置忘了改本表 ⇒ 门会红**（而不是让读端默默说假话）。

import "github.com/kaixuan/llm-gateway-go/internal/extpoints"

// Wiring 是「机制 ID ⇒ 记账接线所在的**仓库相对文件路径**」的静态表。
//
// ★ 值的形状刻意选成**文件路径**（而不是一句自由文本）⇒
//
//	门可以**真的去读那个文件**、确认它**真的用到了**该机制的常量
//	⇒ ⇒ 静态表与源码**两份来源对拍**，而不是靠人写一句注释。
//
// ★ 只列**真的接了台账**的机制；`Production=false` 的机制不在表里
//
//	（判死 / 骨架 / 别名 ⇒ 本来就不该有流量计数）。
var Wiring = map[string]string{
	extpoints.IDHTTPMiddlewareChain: "middleware/ledger_mw.go",
	extpoints.IDResponseIntercept:   "domains/hooks/response/chain.go",
}

// IsWired 报告该机制**是否接了台账**（静态事实，与运行期流量无关）。
func IsWired(mechanismID string) bool {
	_, ok := Wiring[mechanismID]
	return ok
}

// WiringLocation 返回该机制的接线文件路径（没接则空串）。
func WiringLocation(mechanismID string) string {
	return Wiring[mechanismID]
}
