package bg

import (
	"bytes"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// goCodeWithoutComments 返回 Go 源码的**去注释**形态。
//
// 为什么不能按行剥 "//"：Go 字符串字面量里可以含 "//"（URL、SQL 里的 `--`
// 注释），按行剥会把字面量截断。仓库既有做法（bg/auto_route_settle_source_gate_test.go）
// 是用 go/parser 解析 AST；本门更进一步：用 go/printer 在 f.Comments=nil 的前提下
// 重打源码 ⇒ 判据打在**去掉注释后**的生产源码上 —— 退役说明里出现的函数名不会
// 把门变成永绿。
func goCodeWithoutComments(t *testing.T, path string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 %s 失败 %v —— 这道门不能在没有源码的情况下判定为通过", path, err)
	}
	f.Comments = nil
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, f); err != nil {
		t.Fatalf("打印 %s 失败 %v", path, err)
	}
	return buf.String()
}

// 本文件是**迁移 890（v1 族真删）**在分区管理器侧的退役门。
//
// 旧门（2026-10-13 / 迁移 860 轮）守的是「停写 ⇒ v1 族生命周期作业下线」：
// 前提是**族还在、只是被冻结**，所以门是可逆的（回写 ⇒ 自动恢复维护）。
// 890 把族（表 + 分区 + 序列 + 只服务它的分区管理函数）**物理删除** ⇒ 前提消失 ⇒
// 维护侧必须**无条件**退役：恢复维护没有对象可维护，继续调用必然 42P01。
//
// ⇒ 本文件的门从「条件退役」改成「**无条件退役**」，并且每条负向断言都配一条
// 阳性对照（扫描器本身能看见东西 / 活着的生命周期路径仍在），避免「什么都没查」
// 也能全绿。

// v1StopWriteFakeKV 是只读 settings_kv 桩：platform scope 对停写键返回固定值。
type v1StopWriteFakeKV struct{ enabled string }

func (f *v1StopWriteFakeKV) Get(scope settings.Scope, key string) ([]byte, error) {
	if scope == settings.ScopePlatform && key == settings.KeyRequestLogsWriteEnabled {
		return []byte(f.enabled), nil
	}
	return nil, nil
}
func (f *v1StopWriteFakeKV) Set(settings.Scope, string, any) ([]byte, error) { return nil, nil }
func (f *v1StopWriteFakeKV) GetTenant(string, string) ([]byte, error)        { return nil, nil }
func (f *v1StopWriteFakeKV) SetTenant(string, string, any) ([]byte, error)   { return nil, nil }

func withV1WriteEnabled(t *testing.T, enabled string) {
	t.Helper()
	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &v1StopWriteFakeKV{enabled: enabled})
	for _, spec := range settings.StorageSpecs() {
		if err := registry.RegisterSpec(spec); err != nil {
			t.Fatalf("register spec %s: %v", spec.Key, err)
		}
	}
	settings.Global = registry
}

// v1LifecycleFnNames 是**已随 890 退役**的四个 v1 族生命周期条目。
//
// 这四个函数（ensure_request_logs_partition / ensure_request_logs_bodies_partition /
// promote_request_logs_hot_to_partition / promote_request_logs_bodies_hot_to_partition）
// 都被 890 的 DROP 列表删除 ⇒ 890 之后调用必然 42P01（未定义函数）。
var v1LifecycleFnNames = map[string]bool{
	"ensure_request_logs_partition":                true,
	"ensure_request_logs_bodies_partition":         true,
	"promote_request_logs_hot_to_partition":        true,
	"promote_request_logs_bodies_hot_to_partition": true,
}

// TestV1LifecycleSpecsAreUnconditionallyRetired 钉住 890 之后的**无条件**退役：
// 无论写开关开还是关，ensure/promote 清单里都**不得**再出现 v1 族条目。
//
// 为什么必须是「无条件」：旧实现靠 filterRetiredV1Lifecycle 过滤，而过滤器本身
// 由 v1LifecycleRetired()（= !写开关）决定 ⇒ 写开关一翻回 true，四条 v1 条目
// 就会重新出现在清单里，而它们对应的 SQL 函数已被 890 删除 ⇒ 每 tick 一次
// 42P01。这条门把「过滤器恒滤 / 过滤器被删 / 条目被删」三种实现都钉成同一个
// 可观测事实：清单里没有它们。
//
// 阳性对照：写开关两种取值都被显式跑一遍（若实现仍是设置驱动的，两侧结果会
// 不同 ⇒ 门红）。
func TestV1LifecycleSpecsAreUnconditionallyRetired(t *testing.T) {
	for _, writeEnabled := range []string{"true", "false"} {
		withV1WriteEnabled(t, writeEnabled)
		for _, spec := range ensureSpecs() {
			if v1LifecycleFnNames[spec.fnName] {
				t.Errorf("写开关=%s 时 ensureSpecs 仍含 v1 族条目 %q —— 890 已删该族与其函数，调用必然 42P01", writeEnabled, spec.fnName)
			}
		}
		for _, spec := range promoteSpecs() {
			if v1LifecycleFnNames[spec.fnName] {
				t.Errorf("写开关=%s 时 promoteSpecs 仍含 v1 族条目 %q —— 890 已删该族与其函数，调用必然 42P01", writeEnabled, spec.fnName)
			}
		}
	}
}

// TestV1LifecycleRetiredIsUnconditional 钉住退役闸本身：v1LifecycleRetired()
// **恒为 true**，不读写开关。
//
// 为什么单独一条：archiveOldRequestLogs 的归档调用（archive_request_logs_default，
// 同样被 890 删除）就靠这个闸挡在门外。旧实现里它是设置驱动的（!写开关）⇒
// 那只是**偶然**的护栏：写开关一翻就漏。这里把它钉成恒定，避免「靠设置挡
// 42P01」这种把故障概率绑在运维配置上的设计。
func TestV1LifecycleRetiredIsUnconditional(t *testing.T) {
	for _, writeEnabled := range []string{"true", "false"} {
		withV1WriteEnabled(t, writeEnabled)
		if !v1LifecycleRetired() {
			t.Errorf("写开关=%s 时 v1LifecycleRetired() = false —— 890 之后 v1 族已不存在，退役必须是无条件的", writeEnabled)
		}
	}
}

// TestV1BodyTTLCleanupCallIsGone 钉住「v1 正文族的 TTL 清理**不再发 SQL**」。
//
// 退役了什么：每 tick 调用 `drop_old_request_logs_bodies_partitions($1)` 的那一步
// （Go 侧函数 dropOldRequestLogsBodiesPartitions + 它在 runCleanup 里的调用点）。
// 890 把该函数 DROP ⇒ 890 之后每次 tick 都会 42P01。
//
// 判据打在**去掉注释后**的源码上（stripLineComments）：否则本文件自己写的退役
// 说明（里面就出现了这个函数名）会把门变成永绿。
//
// 阳性对照：同一条扫描必须能看见**活着**的会话族正文排水函数
// （promote_session_bodies_hot_to_partition）⇒ 扫描器没坏、这条负向断言不是
// 「什么都没查」。
func TestV1BodyTTLCleanupCallIsGone(t *testing.T) {
	code := goCodeWithoutComments(t, "partition_manager.go")

	if strings.Contains(code, "drop_old_request_logs_bodies_partitions") {
		t.Error("partition_manager.go 仍在调用 drop_old_request_logs_bodies_partitions —— 890 已 DROP 该函数，890 之后每 tick 必然 42P01")
	}
	if strings.Contains(code, "lifecycle.request_logs_bodies_ttl_days") {
		t.Error("partition_manager.go 仍在读 lifecycle.request_logs_bodies_ttl_days —— 该键服务的对象（v1 正文族）已被 890 删除")
	}
	// 阳性对照：扫描器必须看得见活着的路径。
	if !strings.Contains(code, "promote_session_bodies_hot_to_partition") {
		t.Error("阳性对照失败：扫描器在去掉注释后的源码里找不到 promote_session_bodies_hot_to_partition —— 说明判据本身失效（负向断言会假绿）")
	}
}

// TestSessionFamilyLifecycleIsIntact 阳性对照：退役 v1 **不得**动到其它表的
// 生命周期行为（session_turns / session_bodies / usage_ledger / credit_ledger …）。
//
// 这条门存在的理由：把 v1 条目从清单里删掉时，最容易犯的错是顺手多删/少删一条
// 别的表（例如漏掉 session_bodies 的 promote ⇒ 会话正文永远停在 hot 表里）。
func TestSessionFamilyLifecycleIsIntact(t *testing.T) {
	wantEnsure := []string{
		"ensure_sessions_v2_partitions",           // 430：sessions / session_turns / session_bodies
		"ensure_session_family_partitions",        // 706：memora / censors / tools
		"ensure_usage_ledger_partition",           // 330
		"ensure_credit_ledger_partition",          // 334
		"ensure_tool_usage_stats_partition",       // 335
		"ensure_candidate_failure_logs_partition", // 689/694
	}
	ensureSeen := map[string]bool{}
	for _, s := range ensureSpecs() {
		ensureSeen[s.fnName] = true
	}
	for _, fn := range wantEnsure {
		if !ensureSeen[fn] {
			t.Errorf("ensureSpecs 缺 %q —— 退役 v1 不得动到其它表的生命周期", fn)
		}
	}

	wantPromote := []string{
		"promote_session_turns_hot_to_partition",          // 526
		"promote_session_turn_details_hot_to_partition",   // 733
		"promote_session_bodies_hot_to_partition",         // 614
		"promote_usage_ledger_hot_to_partition",           // 330
		"promote_credit_ledger_hot_to_partition",          // 334
		"promote_tool_usage_stats_hot_to_partition",       // 335
		"promote_candidate_failure_logs_hot_to_partition", // 392
	}
	promoteSeen := map[string]bool{}
	for _, s := range promoteSpecs() {
		promoteSeen[s.fnName] = true
	}
	for _, fn := range wantPromote {
		if !promoteSeen[fn] {
			t.Errorf("promoteSpecs 缺 %q —— 退役 v1 不得动到其它表的生命周期", fn)
		}
	}
}

// TestMonthlyPartitionRetentionSweepIsStillWired 钉住「现在由什么承担」这一侧：
// 会话族月分区的过期 DROP 由迁移 841 的通用机制承担
// （llm_gateway_drop_expired_month_partitions），它的调用点必须仍在促升循环里。
//
// 为什么不把它当成「v1 已有等价物」：841 的配置表 llm_gateway_partition_retention
// **出厂为空**（全仓无 INSERT）⇒ 默认不删任何月分区 ⇒ v1 那条 7 天正文 TTL
// **没有等价物**。这条门只保证「通用机制还在跑」，不保证「会话族正文会被删」。
func TestMonthlyPartitionRetentionSweepIsStillWired(t *testing.T) {
	code := goCodeWithoutComments(t, "partition_manager.go")
	if !strings.Contains(code, "pm.dropExpiredMonthlyPartitions(ctx)") {
		t.Error("促升循环里没有 pm.dropExpiredMonthlyPartitions(ctx) —— 迁移 841 的通用月度分区保留机制（会话族月分区过期的唯一承担者）没接上")
	}
	if !strings.Contains(code, "llm_gateway_drop_expired_month_partitions()") {
		t.Error("partition_manager.go 不再调用 llm_gateway_drop_expired_month_partitions() —— 通用月度分区保留机制被摘掉了")
	}
}
