package bg

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// settleBatch 数据源切换的门（审计 §9.43）。
//
// # 这道门在防什么
//
// §9.43 把三条腿（outcome join / LATERAL / loadTaskBaselines）接到同一个
// `settleSourceSpec` 上，按 S4 写门在 `request_logs_hot` 与 `session_turns_hot`
// 之间切换。三个具体风险：
//
//  1. **三条腿不同源**。outcome 读会话族而 baseline 读 v1（或反过来）时，
//     p95/p75 基线与被它归一化的 latency 不在同一批行上算——那比缺数据更隐蔽，
//     因为数字都"有值"。
//  2. **默认值方向搞反**。`settings.GetPlatformBool` 在存储未初始化时默认 true
//     （写门=开着）。若代码默认走会话族，就会在**任何**配置下悄悄改源。
//  3. **切换不可见**。运维需要能在 /metrics 上立刻看到源从 v1 变成 session。

// TestSettleSourceForDefaultsToV1 钉住 2：默认必须是 v1（与今天逐字相同）。
func TestSettleSourceForDefaultsToV1(t *testing.T) {
	src := settleSourceFor(true)
	if src.Family != settleFamilyV1 {
		t.Errorf("写门开着时 family = %q，期望 %q", src.Family, settleFamilyV1)
	}
	if src.TurnsTable != "request_logs_hot" {
		t.Errorf("写门开着时 TurnsTable = %q，期望 request_logs_hot（与移植前逐字相同）", src.TurnsTable)
	}
	if src.SessionKeyCol != "gw_session_id" {
		t.Errorf("写门开着时 SessionKeyCol = %q，期望 gw_session_id", src.SessionKeyCol)
	}
}

// TestSettleSourceForSessionSide 钉住停写后的目标族与会话键列名。
func TestSettleSourceForSessionSide(t *testing.T) {
	src := settleSourceFor(false)
	if src.Family != settleFamilySession {
		t.Errorf("写门关闭时 family = %q，期望 %q", src.Family, settleFamilySession)
	}
	if src.TurnsTable != "session_turns_hot" {
		t.Errorf("写门关闭时 TurnsTable = %q，期望 session_turns_hot", src.TurnsTable)
	}
	// v1 叫 gw_session_id，会话族叫 session_id。写错会得到恒不匹配的 LATERAL
	// ——而那不会报错，只会让 retry 信号变成 unavailable（§9.42 的三态能看见，
	// 但根因在这里就该挡住）。
	if src.SessionKeyCol != "session_id" {
		t.Errorf("写门关闭时 SessionKeyCol = %q，期望 session_id", src.SessionKeyCol)
	}
}

// TestSettleLegsAllUseTheSameSource 钉住 1：三条腿必须共用同一个规格。
//
// 判据是「settleBatch / loadTaskBaselines 里出现的是 src.TurnsTable 而不是
// 字面量表名」。字面量表名出现在任何一条腿上，就说明有人绕过规格直接写了表名
// ——那正是三条腿不同源的起点。
func TestSettleLegsAllUseTheSameSource(t *testing.T) {
	raw := readFileForSettleGate(t, "auto_route_settle_worker.go")
	lits := sqlLiteralsOfSettleFile(t, "auto_route_settle_worker.go")
	var sql strings.Builder
	for _, l := range lits {
		sql.WriteString(l)
		sql.WriteString("\n")
	}

	// 硬编码的表名（作为 SQL 字面量文本出现）= 绕过规格。
	for _, banned := range []string{"FROM request_logs_hot r2", "JOIN request_logs_hot rl"} {
		if strings.Contains(sql.String(), banned) {
			t.Errorf("SQL 里出现了硬编码表名 %q。三条腿必须共用 settleSourceSpec，\n"+
				"  否则会出现「outcome 读会话族、baseline 读 v1」——基线与被归一化的\n"+
				"  latency 不在同一批行上算，数字都有值但毫无意义。", banned)
		}
	}
	// 三处都必须出现：outcome join、LATERAL、baseline。
	if n := strings.Count(raw, "src.TurnsTable"); n < 3 {
		t.Errorf("src.TurnsTable 只出现 %d 次，期望至少 3 次"+
			"（outcome join / LATERAL / loadTaskBaselines 各一处）", n)
	}
	// LATERAL 的会话键必须也来自规格。
	if !strings.Contains(raw, "src.SessionKeyCol") {
		t.Error("LATERAL 没有用 src.SessionKeyCol —— 会话键列名必须随族切换")
	}
}

// TestSettleSourceMetricIsRegistered 钉住 3：切换在 /metrics 上可见。
func TestSettleSourceMetricIsRegistered(t *testing.T) {
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var found *dto.MetricFamily
	for _, mf := range families {
		if mf.GetName() == "llmgw_autoroute_settle_source_total" {
			found = mf
			break
		}
	}
	if found == nil {
		t.Fatal("/metrics 里没有 llmgw_autoroute_settle_source_total —— " +
			"源族切换将不可见，运维只能在「settle 变慢了」之后才发现")
	}
	got := map[string]bool{}
	for _, m := range found.GetMetric() {
		for _, lp := range m.GetLabel() {
			if lp.GetName() == "family" {
				got[lp.GetValue()] = true
			}
		}
	}
	for _, f := range []string{settleFamilyV1, settleFamilySession} {
		if !got[f] {
			t.Errorf("指标里缺少 family=%q 序列（预初始化没做，切换前会缺一条）", f)
		}
	}
	if len(got) != 2 {
		t.Errorf("family 标签值集合 = %v，期望恰好两个闭集值", got)
	}
}
