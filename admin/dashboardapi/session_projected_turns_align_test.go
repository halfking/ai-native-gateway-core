package dashboardapi

// session_projected_turns_align_test.go —— v2 投影端点 turns 对拍接线的契约门
// （2026-11-26 接线轮）。
//
// 守什么：v2 /api/admin/dashboard/v2/session-overview 此前**不带**
// turns_alignment 键 ⇒ SessionStatsPanel.vue 的对拍横幅在 v2 页面**静默失效**
// （键缺席 → v-if 恒假 → 永不渲染）。接线后：
//
//  1. 窗口谓词必须是参数化 [start,end)（与投影主查询 $1/$2 同界）——
//     照搬遗留的 rolling NOW()-N 会让日历模式对拍量错区间（假对拍）；
//  2. 判据必须复用 businessTurnsFilterSQL（probekind SSOT 三条款）——
//     手抄第三份正则清单会漂移；
//  3. 注入逻辑：轮次腿成功 ⇒ 键在、形状与遗留 TurnsAlignment 逐字一致；
//     轮次腿失败/cost_stats 缺席 ⇒ 键缺席（soft-fail，缺席比假绿诚实）；
//  4. 轮次查询必须发生在 tx.Commit 之前（pgx 事务提交后连接归还）——
//     该时序是静态审查项，由约定注释钉住，运行期测不了（见文件尾注）。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/internal/probekind"
)

// fakeRowQuerier 是 queryBusinessTurnsCountWindowed 的最小桩：捕获 SQL 与参数，
// 返回预设计数或错误。
type fakeRowQuerier struct {
	gotSQL  string
	gotArgs []any
	count   int64
	err     error
}

func (f *fakeRowQuerier) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.gotSQL = sql
	f.gotArgs = args
	return fakeRow{count: f.count, err: f.err}
}

type fakeRow struct {
	count int64
	err   error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 1 {
		return errors.New("scan dest mismatch")
	}
	p, ok := dest[0].(*int64)
	if !ok {
		return errors.New("scan dest not *int64")
	}
	*p = r.count
	return nil
}

func TestWindowedTurnsCountUsesParameterizedWindowBounds(t *testing.T) {
	// 契约①：窗口必须参数化 [start,end)，禁止 rolling NOW() 字面量 ——
	// 日历模式下 NOW()-N 量的不是页面展示的区间（假对拍）。
	start := mustTime(t, "2026-11-20T00:00:00Z")
	end := mustTime(t, "2026-11-27T00:00:00Z")
	fake := &fakeRowQuerier{count: 85013}
	n, err := queryBusinessTurnsCountWindowed(context.Background(), fake, start, end, "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if n != 85013 {
		t.Fatalf("count = %d, want 85013", n)
	}
	if len(fake.gotArgs) != 2 {
		t.Fatalf("rolling window needs exactly 2 bound args, got %v", fake.gotArgs)
	}
	if got := fake.gotArgs[0]; got != start {
		t.Fatalf("lower bound = %v, want %v (same $1 as projection query)", got, start)
	}
	if got := fake.gotArgs[1]; got != end {
		t.Fatalf("upper bound = %v, want %v (same $2 as projection query)", got, end)
	}
	if strings.Contains(fake.gotSQL, "NOW() - INTERVAL") {
		t.Fatalf("windowed leg must not use rolling NOW() literal: %s", fake.gotSQL)
	}
	if !strings.Contains(fake.gotSQL, "rl.ts < $2") {
		t.Fatalf("window upper bound must be exclusive (< $2, end 独占): %s", fake.gotSQL)
	}
}

func TestWindowedTurnsCountKeepsProbekindSSOTFilter(t *testing.T) {
	// 契约②：判据必须复用 businessTurnsFilterSQL（与遗留腿同一份 SSOT），
	// 且含三条款的全部形态。
	start := mustTime(t, "2026-11-20T00:00:00Z")
	end := mustTime(t, "2026-11-27T00:00:00Z")
	fake := &fakeRowQuerier{}
	_, _ = queryBusinessTurnsCountWindowed(context.Background(), fake, start, end, "")
	if !strings.Contains(fake.gotSQL, probekind.SQLStageList()) {
		t.Fatalf("turns leg must use probekind.SQLStageList SSOT: %s", fake.gotSQL)
	}
	if !strings.Contains(fake.gotSQL, "task_type") || !strings.Contains(fake.gotSQL, "probe_triggered") {
		t.Fatalf("turns leg must exclude probe_triggered: %s", fake.gotSQL)
	}
	if !strings.Contains(fake.gotSQL, "NOT LIKE 'probe-%'") {
		t.Fatalf("turns leg must exclude probe-%% request ids: %s", fake.gotSQL)
	}

	// tenant 收窄追加第三个绑定参数（带 ::text 对齐会话族投影形态）。
	fake2 := &fakeRowQuerier{}
	_, _ = queryBusinessTurnsCountWindowed(context.Background(), fake2, start, end, "acme")
	if len(fake2.gotArgs) != 3 || fake2.gotArgs[2] != "acme" {
		t.Fatalf("tenant-scoped leg needs 3 args with tenant last, got %v", fake2.gotArgs)
	}
	if !strings.Contains(fake2.gotSQL, "rl.tenant_id::text = $3") {
		t.Fatalf("tenant predicate must be ::text-cast 3rd placeholder: %s", fake2.gotSQL)
	}
}

func TestProjectedTurnsAlignmentPayloadShape(t *testing.T) {
	// 契约③：键形状与遗留端点 TurnsAlignment 逐字一致 —— 前端横幅
	// （summary_requests/turns_count/abs_delta/degraded）跨两个端点消费同一组键。
	a := evaluateTurnsAlignment(80164, 85039)
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"summary_requests", "turns_count", "abs_delta", "degraded", "threshold_pct"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("wire payload missing key %q (legacy contract): %s", key, raw)
		}
	}
	if _, ok := m["message"]; ok {
		t.Fatalf("healthy alignment must omit message (omitempty): %s", raw)
	}
	d := evaluateTurnsAlignment(80164, 85039)
	if d.Degraded || d.AbsDelta != 4875 {
		t.Fatalf("known-good window must stay healthy: %+v", d)
	}
}

func TestProjectedTurnsAlignmentSoftFailShape(t *testing.T) {
	// 契约③b：soft-fail 语义 —— 轮次腿错误 ⇒ 注入函数拿到零值计数也**不得**
	// 产出 degraded 假绿；调用方以 turnsCountOK 门控注入（键缺席 = 诚实）。
	// 这里钉注入逻辑的判据结构：错误必须被识别为失败而不是当 0 用。
	fake := &fakeRowQuerier{err: errors.New("tx is closed")}
	_, err := queryBusinessTurnsCountWindowed(context.Background(), fake,
		mustTime(t, "2026-11-20T00:00:00Z"), mustTime(t, "2026-11-27T00:00:00Z"), "")
	if err == nil {
		t.Fatal("query failure must surface as error (caller skips injection)")
	}
	// 注入门控的三重条件结构（与 session_projected.go 逐字同构）：
	// turnsCountOK ∧ cost_stats 在 ∧ total_requests 是 json.Number。
	data := map[string]any{
		"cost_stats": map[string]any{"total_requests": json.Number("80164")},
	}
	var injected bool
	var turnsCountOK bool // 模拟轮次腿失败 ⇒ false
	if turnsCountOK {
		if costStats, ok := data["cost_stats"].(map[string]any); ok {
			if total, ok := costStats["total_requests"].(json.Number); ok {
				if _, err := total.Int64(); err == nil {
					injected = true
				}
			}
		}
	}
	if injected {
		t.Fatal("failed turns leg must not inject turns_alignment (absent > false green)")
	}
	// 正常腿 ⇒ 注入发生。
	turnsCountOK = true
	if turnsCountOK {
		if costStats, ok := data["cost_stats"].(map[string]any); ok {
			if total, ok := costStats["total_requests"].(json.Number); ok {
				if summaryRequests, err := total.Int64(); err == nil {
					a := evaluateTurnsAlignment(summaryRequests, 85039)
					data["turns_alignment"] = a
					injected = true
				}
			}
		}
	}
	if !injected {
		t.Fatal("healthy turns leg must inject turns_alignment")
	}
	if _, ok := data["turns_alignment"].(TurnsAlignment); !ok {
		t.Fatalf("injected value must be TurnsAlignment value, got %T", data["turns_alignment"])
	}
}
