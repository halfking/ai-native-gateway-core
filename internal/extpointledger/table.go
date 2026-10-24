package extpointledger

// table.go —— **台账的合并视图**：静态裁决表 × 运行期计数。
//
// # 为什么合并逻辑住在台账包而不是 admin
//
// ① **可测性**：合并与载荷形状是**纯逻辑**（无 HTTP、无 DB）⇒
// 放在 `internal/` 里 ⇒ 门可以住在 `scripts/ursmcheck`（★ CI **一定**跑）。
// ⇒ ★ 反例：`admin` 包的测试套件实测 **65.8s**（逼近 CI 快门的 `-timeout=120s`）
// ⇒ ⇒ 把门放进 `admin/` ⇒ 它在 CI 上**跑不到**（或把 CI 拖到超时）⇒ ★ **门不能放那儿**。
// ② **admin 只做薄壳**：序列化 + 鉴权，不含业务判断。
//
// # ★ 两半是**独立来源**，合并后可以互相印证
//
//	静态半：`internal/extpoints` 的裁决表（`Production` 来自**源码扫描**）；
//	运行期半：本包的计数器（来自**运行期计数**）。
//
// ⇒ 「裁决表说不在生产」⇒ 「台账调用次数为 0」⇒ 两份来源一致；
// ⇒ 「裁决表说在生产、但台账恒为 0」⇒ ★ **有人把扩展点悄悄摘了**（值得看一眼）。

import (
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/extpoints"
)

// Row 是台账的一行（静态半 + 运行期半）。
type Row struct {
	// ── 静态半（来自裁决表）──
	ID         string `json:"id"`
	Title      string `json:"title"`
	Location   string `json:"location"`
	Verdict    string `json:"verdict"`
	Production bool   `json:"production"`
	// Phase 是该机制挂的骨架 phase。
	//
	// ★ **不用 `omitempty`** ⇒ 键**恒在** ⇒ 读端字段集稳定、可断言。
	// ★ 值为空是**合法的**：裁决表里只有「挂在某个 phase 上」的机制
	//   （`VerdictPhaseBound`）才有 phase；主链 / 骨架 / 别名 / 判死这四类
	//   **本来就不属于任何单一 phase** ⇒ 不编造。
	Phase string `json:"phase"`

	// ── 运行期半（来自计数器）──
	Calls       uint64    `json:"calls"`
	Failures    uint64    `json:"failures"`
	Timeouts    uint64    `json:"timeouts"`
	Dropped     uint64    `json:"dropped"`
	P99Millis   uint64    `json:"p99_ms"`
	P99Window   int       `json:"p99_window"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitempty"`
	// Recorded 报告这个机制**在台账里被记录过**。
	Recorded bool `json:"recorded"`
	// ★ Unwired 报告这个机制「**裁决表说在生产，但本次进程内还没有任何记账**」。
	//
	// ★ 为什么需要这个字段（诚实边界）：
	// 台账是进程内的 ⇒ 恒为 0 有**两种**完全不同的成因——
	//
	//	(a) ★ **这个扩展点还没接台账**（本轮的真实形态：6 个机制里只有
	//	    `response_intercept` 接了记账）；
	//	(b) 它**没流量**（本次进程启动以来没人调它）。
	//
	// ⇒ 而 `Production=false` 的机制（判死 / 别名 / 骨架）calls=0 是**正常的**。
	// ⇒ ⇒ 不加这个字段，读端看到的「0」**分不清 (a) 与 (b)** ⇒ admin 页面会
	//     把「还没接线」误读成「这个扩展点没人用」⇒ ★ 因此显式暴露。
	//
	// ⚠ 口径提醒：进程重启即清零 ⇒ `Unwired=true` **不蕴含**「它真的没流量」，
	//   只蕴含「本次进程内没有记账记录」。
	Unwired bool `json:"unwired"`
}

// BuildRows 把裁决表与台账合成一行一机制。
//
// ★ **未被记录过的机制也出现在表里**（调用次数 0）——
// 因为「0」本身就是要看的信息（它与裁决表的 `Production` 对拍）。
func BuildRows(ledger *Ledger) []Row {
	mechanisms := extpoints.Mechanisms()
	rows := make([]Row, 0, len(mechanisms))
	for _, m := range mechanisms {
		row := Row{
			ID:         m.ID,
			Title:      m.Title,
			Location:   m.Location,
			Verdict:    string(m.Verdict),
			Production: m.Production,
			Phase:      m.Phase,
			P99Window:  DefaultSampleCapacity,
		}
		if e, ok := ledger.Entry(m.ID); ok {
			row.Calls = e.Calls
			row.Failures = e.Failures
			row.Timeouts = e.Timeouts
			row.Dropped = e.Dropped
			row.P99Millis = e.P99Millis
			row.LastError = e.LastError
			row.LastErrorAt = e.LastErrorAt
			row.Recorded = true
		}
		// ★ 「裁决表说在生产、但本次进程内没记账」⇒ 显式标记（见 Row.Unwired）。
		row.Unwired = row.Production && !row.Recorded
		rows = append(rows, row)
	}
	return rows
}

// Payload 是台账读端的完整载荷。
type Payload struct {
	ExtensionPoints []Row `json:"extension_points"`
	// LedgerScope 把**口径**写在响应里（否则读者只能猜 p99 是全量还是窗口）。
	LedgerScope map[string]any `json:"ledger_scope"`
}

// BuildPayload 造出读端载荷（静态 + 运行期 + 口径）。
func BuildPayload(ledger *Ledger) Payload {
	return Payload{
		ExtensionPoints: BuildRows(ledger),
		LedgerScope: map[string]any{
			"p99_window_samples": DefaultSampleCapacity,
			"p99_definition":     "nearest-rank over the most recent N samples (process-local, in-memory)",
			"ledger_lifetime":    "process-local; resets on restart (observation surface, not a ledger)",
			"source":             "internal/extpoints (static verdict) merged with internal/extpointledger (runtime counters)",
		},
	}
}
