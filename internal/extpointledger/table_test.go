package extpointledger

// AUDIT-GUARD: 本文件的门**必须随包在 CI 上执行** ⇒ 该包已登记进 Makefile 的 GUARD_PACKAGES

// table_test.go —— P2-2「扩展点台账」的**合并视图**门。
//
// # 判据清单
//
//	① **静态半与运行期半合并**：一行一机制，静态字段来自裁决表、运行期字段来自计数器
//	② ★ **未被记录过的机制也在表里**（调用次数 0）——★ 「0」本身就是要看的信息
//	③ ★ **两份来源可对拍**：裁决表说 `Production=false` ⇒ 台账调用次数必须为 0
//	④ ★ **反向对拍**：裁决表说在生产的机制，台账里**必须有**记录
//	⑤ 载荷形状：口径（`ledger_scope`）必须写在载荷里

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/extpoints"
)

// TestBuildRowsMergesStaticAndRuntime ① + ②。
func TestBuildRowsMergesStaticAndRuntime(t *testing.T) {
	ledger := New()
	ledger.Record(extpoints.IDResponseIntercept, OutcomeOK, 5_000_000, nil) // 5ms
	ledger.Record(extpoints.IDResponseIntercept, OutcomeFailed, 7_000_000, errBoom{})

	rows := BuildRows(ledger)
	mechanisms := extpoints.Mechanisms()
	if len(rows) != len(mechanisms) {
		t.Fatalf("台账行数 = %d，want %d（必须一行一机制）", len(rows), len(mechanisms))
	}

	byID := map[string]Row{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	// ① 静态半逐条与裁决表一致。
	for _, m := range mechanisms {
		r, ok := byID[m.ID]
		if !ok {
			t.Fatalf("台账缺少机制 %s", m.ID)
		}
		if r.Title != m.Title || r.Verdict != string(m.Verdict) ||
			r.Production != m.Production || r.Phase != m.Phase || r.Location != m.Location {
			t.Errorf("机制 %s 的静态半与裁决表不一致：\n台账: %+v\n裁决: %+v", m.ID, r, m)
		}
	}
	// ① 运行期半。
	r := byID[extpoints.IDResponseIntercept]
	if r.Calls != 2 {
		t.Errorf("calls = %d, want 2", r.Calls)
	}
	if r.Failures != 1 {
		t.Errorf("failures = %d, want 1", r.Failures)
	}
	if r.P99Millis != 7 {
		t.Errorf("p99_ms = %d, want 7", r.P99Millis)
	}
	if r.LastError == "" {
		t.Error("last_error 应留得住")
	}
	if !r.Recorded {
		t.Error("recorded 应为 true")
	}
	// ② 未记录过的机制**也在表里**，次数为 0。
	for _, m := range mechanisms {
		if m.ID == extpoints.IDResponseIntercept {
			continue
		}
		row := byID[m.ID]
		if row.Calls != 0 {
			t.Errorf("机制 %s 未被记录，calls 应为 0，实际 %d", m.ID, row.Calls)
		}
		if row.Recorded {
			t.Errorf("机制 %s 未被记录，recorded 应为 false", m.ID)
		}
		// ★ 「裁决表说在生产、但**没接台账**」必须是**显式可见**的状态
		//   （否则读端分不清「没接线」与「没流量」）。
		//
		// ★★ 注意判据用的是 `Wired`（**静态**）而不是 `Recorded`（运行期）：
		//   一个**接好了**、只是本次进程内**还没流量**的机制
		//   ⇒ `Recorded=false` ⇒ ★ 但 `Unwired` 必须是 **false**
		//   ⇒ 否则「刚启动还没来请求」会被误标成「未接线」。
		if want := m.Production && !row.Wired; row.Unwired != want {
			t.Errorf("机制 %s：unwired = %v，want %v（production=%v wired=%v recorded=%v）",
				m.ID, row.Unwired, want, m.Production, row.Wired, row.Recorded)
		}
	}
	// ★ 已接台账的机制不该被标成「未接线」。
	if r := byID[extpoints.IDResponseIntercept]; r.Unwired {
		t.Error("已接台账的机制不该是 unwired")
	}
}

// TestBuildRowsCrossChecksProductionFlag ③ + ④：
// ★ **两份独立来源对拍**。
func TestBuildRowsCrossChecksProductionFlag(t *testing.T) {
	ledger := New()
	produced := 0
	for _, m := range extpoints.Mechanisms() {
		if m.Production {
			produced++
			ledger.Record(m.ID, OutcomeOK, 1_000_000, nil)
		}
	}
	if produced == 0 {
		t.Fatal("裁决表里没有任何在生产的机制 ⇒ 判据失效")
	}
	rows := BuildRows(ledger)
	for _, r := range rows {
		if !r.Production && r.Calls != 0 {
			t.Errorf("机制 %s：裁决表说不在生产（Production=false），台账却有 %d 次调用 ⇒ "+
				"两份来源分家", r.ID, r.Calls)
		}
		if r.Production && !r.Recorded {
			t.Errorf("机制 %s：裁决表说在生产，台账却没有任何记录 ⇒ 接线可能没通", r.ID)
		}
	}
}

// TestBuildPayloadCarriesScope ⑤：口径必须写在载荷里。
func TestBuildPayloadCarriesScope(t *testing.T) {
	payload := BuildPayload(New())
	if len(payload.ExtensionPoints) != len(extpoints.Mechanisms()) {
		t.Fatalf("载荷行数 = %d，want %d", len(payload.ExtensionPoints), len(extpoints.Mechanisms()))
	}
	for _, k := range []string{"p99_window_samples", "p99_definition", "ledger_lifetime", "source",
		"outcome_caliber"} {
		if _, ok := payload.LedgerScope[k]; !ok {
			t.Errorf("ledger_scope 缺 %q（口径必须写在载荷里）", k)
		}
	}
	// ★ 载荷必须能被 JSON 序列化（读端直接用它）。
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("载荷不能 JSON 序列化：%v", err)
	}
	var back struct {
		ExtensionPoints []map[string]any `json:"extension_points"`
		LedgerScope     map[string]any   `json:"ledger_scope"`
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("载荷往返失败：%v", err)
	}
	for _, row := range back.ExtensionPoints {
		for _, k := range []string{"id", "title", "location", "verdict", "production",
			"calls", "failures", "timeouts", "dropped", "rejected", "p99_ms", "p99_window",
			"recorded", "wired", "unwired", "phase"} {
			if _, ok := row[k]; !ok {
				t.Errorf("行 %v 缺字段 %q", row["id"], k)
			}
		}
	}
	// ★ 载荷必须把「裁决表说在生产、但本次进程内没记账」显式标出来
	//   ⇒ 否则读端分不清「没接线」与「没流量」。
	// ★ 载荷必须把「**接没接台账**」显式标出来（静态事实 ⇒ 与流量无关）。
	wired := 0
	for _, m := range extpoints.Mechanisms() {
		if !m.Production {
			continue
		}
		for _, row := range back.ExtensionPoints {
			if row["id"] == m.ID {
				if v, _ := row["wired"].(bool); v {
					wired++
				}
			}
		}
	}
	if wired == 0 {
		t.Error("载荷里没有任何机制被标为 wired ⇒ ★「接没接台账」这个状态在读端不可见")
	}
}

// TestWiringTableMatchesVerdictTable —— **静态接线表**与裁决表同源。
//
// ★ 两个方向都要查：
//
//	① 裁决表里 `Production=true` 的机制 ⇒ **必须**在接线表里
//	   （在产的扩展点不接台账 ⇒ 读端那一行永远是空的 ⇒ 等于没做）；
//	② 接线表里的机制 ⇒ **必须**在裁决表里（不许有孤儿行）。
func TestWiringTableMatchesVerdictTable(t *testing.T) {
	for _, m := range extpoints.Mechanisms() {
		if !m.Production {
			continue
		}
		if !IsWired(m.ID) {
			t.Errorf("机制 %q 在生产（Production=true）却**没接台账** ⇒ "+
				"读端那一行的计数恒为 0 ⇒ ★ 要么给它接上，要么把它改判为不在生产",
				m.ID)
		}
	}
	for id, loc := range Wiring {
		if _, ok := extpoints.MechanismByID(id); !ok {
			t.Errorf("接线表里有裁决表之外的机制 %q（位置 %s）⇒ 孤儿行", id, loc)
		}
		if loc == "" {
			t.Errorf("机制 %q 的接线位置为空 ⇒ 门没法与源码对拍", id)
		}
	}
}

// errBoom 是测试用的错误。
type errBoom struct{}

func (errBoom) Error() string { return "boom" }
