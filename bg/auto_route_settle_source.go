package bg

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// settleBatch 的数据源选择（审计 §9.43）。
//
// # 背景
//
// 停写后 v1 热表停止增长，而 selection 行仍由
// domains/hooks/observability/telemetry/selection_writer.go 持续写入
// （该写方**不咨询** S4 写门）⇒ v1 臂对每条新 selection 恒不命中 ⇒
// `p.success == nil` ⇒ 过 settleAbandonAfter 即 abandon，reward 永久为 NULL。
//
// # 为什么是「按门切换」而不是「直接换成会话族」
//
// 直接换会让**停写之前**的行为也变：会话臂对同一批 request_id 的覆盖率实测是
// 99.3%（§9.40），另外 0.7% 会当场失去 outcome；`loadTaskBaselines` 的
// `is_auto_request` 覆盖率只有 83.0%，基线 cohort 会缩水约 17%。
//
// 目标要求「确保数据在更改前后一致」。所以这里按 **settings.RequestLogsWriteEnabled**
// 切换源：写门开着时读 v1（**与今天逐字相同**），关掉后读会话族。
//
// 注意这与 §9.35 的「不门控 worker」不矛盾：那条说的是**不要门控 worker 的执行**
// （门控只会让数字不再变化，而正确修法是改读会话族）；这里门控的是**读哪个族**，
// 目的是在切换发生前保持行为不变。
//
// # ★ 890（v1 族退役）：v1 臂整条删除，本函数恒返回会话族
//
// 迁移 890 把 v1 族（`_request_logs*` 及其分区）整体删掉 ⇒ **v1 臂不再是
// 「写门开着时才走的分支」，而是「永远走不到的分支」**。留着它有两重害处：
//
//  1. `settings.RequestLogsWriteEnabled()` 在 **settings 存储未初始化**时回落
//     **代码默认**（见 settings/key_request_logs_write_enabled.go 的
//     GetPlatformBool 默认值）⇒ 新装/早启进程会读 v1 ⇒ 42P01 ⇒ 结算 worker
//     每轮失败，reward 永久 NULL。**「写门生产上是关的」不等于「写门读出来是
//     关的」**。
//     ★ 2026-10-20 订正：G-0 已把**代码默认**翻成 `false` ⇒ 这条隐患的
//     **前提已不成立**（新装/早启现在读到 false ⇒ 不会读 v1）。
//     v1 臂删除**仍然是对的**——它是纵深防御，且不依赖任何默认值；
//     但上面这条理由现在只是「历史动机」，不再是「当前风险」。
//  2. 运维把写门翻回 true（本意是回滚）会直接踩 42P01。
//
// ⇒ v1 臂删除，`settleSourceFor` 恒返回会话族规格。**参数保留**是因为它的
// 调用点（bg/auto_route_settle_sql.go / bg/auto_route_settle_worker.go /
// admin 侧探针）不在本轮改动范围内，签名变更会波及其它写者。
//
// # 三条腿必须同源
//
// outcome join、LATERAL、loadTaskBaselines 若各读各的族，基线与结果就会来自
// 不同总体（p95/p75 与被归一化的 latency 不在同一批行上算），那是比缺数据更
// 隐蔽的错误。所以三者由**同一个** settleSourceSpec 驱动。

// settleSourceSpec 描述一轮结算该读哪个族。
type settleSourceSpec struct {
	// TurnsTable 是读行的那张表（outcome join / baseline / LATERAL 共用）。
	TurnsTable string
	// SessionKeyCol 是该族里承载会话身份的列名。
	// v1 叫 gw_session_id；会话族叫 session_id（710 视图的会话臂也是这么
	// 投影 gw_session_id 的：`CASE WHEN t.session_id ~~ 'sys:%' THEN NULL
	// ELSE t.session_id END`）。
	SessionKeyCol string
	// Family 是闭集标签值，供指标使用。
	Family string
}

const (
	settleFamilyV1      = "v1"
	settleFamilySession = "session"
)

// settleSourceFor 返回一轮结算要读的数据源。
//
// 抽成纯函数（不碰 DB、不读全局设置）是为了能单测：这一层如果只能靠集成测试
// 验证，本地库的 selection 表是空的，永远证明不了任何事（§9.34）。
//
// ★ 890（v1 族退役）：**恒返回会话族**。v1 族已删，写门再翻回 true 也读不到
// 任何 v1 行（42P01），所以「按门切换」这件事本身已经不存在了。参数
// logsWriteEnabled 保留只为不波及其它写者的调用点（见文件头「890」一节）。
//
// ⚠ 合并订正（2026-10-20，与上游 R59 轮的「默认 v1 ⇒ 缺省 session」订正并存）：
// 上游曾把本判定写成「默认 v1、只有明确读到写门关闭才切会话族」，并在
// 2026-10-21 订正为「缺省 ⇒ session」（依据：`GetPlatformBool` 的缺省值被上游
// 从 true 改成 false，见 settings/key_request_logs_write_enabled.go）。
// **本分支不再需要这场选择**：v1 族已被迁移 890 物理删除 ⇒ 两种缺省都会指向
// 一张不存在的表 ⇒ 恒返回会话族是唯一安全解。上游那段注释里仍然有效的只有
// 「D0 把缺省翻成 false」这一事实，其余「按门切族」的语义随 v1 族一并退役。
func settleSourceFor(logsWriteEnabled bool) settleSourceSpec {
	_ = logsWriteEnabled // v1 族已随 890 退役；写门不再决定读哪一族。
	return settleSourceSpec{
		TurnsTable:    "session_turns_hot",
		SessionKeyCol: "session_id",
		Family:        settleFamilySession,
	}
}

// ActiveSettleTurnsTable 返回当前 settle worker 实际读取的行表名。
//
// 为什么导出：admin 的 outcome-freshness 探针
// （admin/auto_route_outcome_freshness.go）要回答的是「结算 worker 现在还能不能
// 拿到 outcome」。它此前硬编码读 v1 热表，于是停写之后探针盯着的是
// worker 早已不再读取的冻结表，每次都报 stale —— 一条与真实状态相反的告警，
// 比没有告警更糟（运维会以为 auto-route 结算坏了，实际它在正常读会话族）。
//
// 复用 settleSourceFor 而不是各写一份判定：这个文件开头「三条腿必须同源」说的
// 就是这类分叉。
//
// ★ 890：v1 族退役后本函数恒返回会话族热表名，写门不再影响读源。
func ActiveSettleTurnsTable() string {
	return settleSourceFor(settings.RequestLogsWriteEnabled()).TurnsTable
}

var (
	// autoRouteSettleSource 暴露实际使用的源族。
	//
	// 为什么必须有：切换的那一刻，运维需要能在 /metrics 上**立刻**看到
	// 结算源从 v1 变成 session。没有它，唯一信号是「settle 突然变慢了」
	// 或「reward 分布变了」——都太晚也太含糊。
	//
	// 同时它让「源已切换但三条腿没同步切换」这类半吊子状态暴露成一条
	// 只有两个取值的曲线，而不是一堆诡异的 reward。
	//
	// ★ 890：v1 族退役后只有 session 一族会被计数；v1 标签值仍在（预置序列
	// + 标签基数闭集），但恒为 0 —— 保留它是为了不改变标签基数（告警表达式
	// 与基数纪律都按闭集写）。
	autoRouteSettleSource = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_autoroute_settle_source_total",
			Help: "Settled selections by the request-row family the outcome/baseline/LATERAL legs read (session = session_turns_hot; the v1 family was retired with migration 890, so the v1 label is always 0).",
		},
		[]string{"family"},
	)
)

func init() {
	// 预置两条序列，使 family 维度在首次运行前就出现在 /metrics 里。
	for _, f := range []string{settleFamilyV1, settleFamilySession} {
		_ = autoRouteSettleSource.WithLabelValues(f)
	}
}

// currentSettleSource 读取当前生效的源规格。
func currentSettleSource() settleSourceSpec {
	return settleSourceFor(settings.RequestLogsWriteEnabled())
}

// R33（2026-10-02 12h 审计）：这里曾有一个 logSettleSourceSwitch（源族变化时
// 打 slog），自 §9.43 引入起就从未被调用过——「切换时打日志」的承诺一直没
// 生效，切换的可观测性实际由 deploy/prometheus/rules/auto-route-settle-
// baseline.yml 的 AutoRouteSettleSourceSwitched 告警承担（该告警的求值语义
// 由 rule_tests/auto-route-settle-baseline_test.yml 的 promtool 单测钉住）。
// 死代码已删；若将来需要进程内日志，重新接线时别再留成无调用方的孤儿。
