package bg

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// settleBatch 的数据源选择（审计 §9.43）。
//
// # 背景
//
// 停写后 `request_logs_hot` 停止增长，而 selection 行仍由
// domains/hooks/observability/telemetry/selection_writer.go 持续写入
// （该写方**不咨询** S4 写门）⇒ `LEFT JOIN request_logs_hot rl ON
// rl.request_id = s.request_id` 对每条新 selection 恒不命中 ⇒
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

// settleSourceFor 依据 S4 写门选择数据源。
//
// 抽成纯函数（不碰 DB、不读全局设置）是为了能单测：这一层如果只能靠集成测试
// 验证，本地库的 selection 表是空的，永远证明不了任何事（§9.34）。
//
// 判定刻意做成**默认 v1**：只有明确读到写门关闭才切到会话族。反过来默认会话族
// 会在 settings 存储未初始化时（GetPlatformBool 默认 true，见
// settings/key_request_logs_write_enabled.go）意外改源。
func settleSourceFor(logsWriteEnabled bool) settleSourceSpec {
	if !logsWriteEnabled {
		return settleSourceSpec{
			TurnsTable:    "session_turns_hot",
			SessionKeyCol: "session_id",
			Family:        settleFamilySession,
		}
	}
	return settleSourceSpec{
		TurnsTable:    "request_logs_hot",
		SessionKeyCol: "gw_session_id",
		Family:        settleFamilyV1,
	}
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
	autoRouteSettleSource = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_autoroute_settle_source_total",
			Help: "Settled selections by the request-row family the outcome/baseline/LATERAL legs read (v1 = request_logs_hot, session = session_turns_hot). Switches when S4 stop-write flips.",
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

// logSettleSourceSwitch 在源族变化时打一条日志。
//
// 用 slog 而不是每次都打：结算每 30 秒一轮，逐轮打日志会把真正的信号淹掉。
// 门控下源只会切换一次（或恢复一次），所以「变了才打」几乎不会触发。
func logSettleSourceSwitch(prev, cur settleSourceSpec) {
	if prev.Family == cur.Family {
		return
	}
	slog.Info("auto-route settle: 数据源已切换",
		"from", prev.Family, "to", cur.Family,
		"turns_table", cur.TurnsTable, "session_key_col", cur.SessionKeyCol,
		"detail", "S4 写门状态已变；outcome join / baseline / LATERAL 三条腿同源切换")
}
