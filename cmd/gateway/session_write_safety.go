package main

import (
	"fmt"
	"strings"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// sessionWritePGEnabled 记录启动期校验时探测到的部署形态（PG / Lite）。
//
// 为什么是包级变量而不是参数：校验与提示是同一件事的两面 —— 校验按形态
// 判定「有没有写面」，提示按形态给出**这一形态下真的能用的**修法。两者
// 必须共用同一份形态事实，把形态挂在包级可以让它们不可能读到不同的值
// （传两遍参数则可能一边传 true 一边传 false）。
//
// 生命周期：validateSessionWriteSafety 的**第一件事**就是记录它，而提示
// 只在启动期校验失败后才被读取 ⇒ 读到的一定是本次启动探测到的形态。
var sessionWritePGEnabled bool

// Flags cannot provide a V2 fact source in lite/no-PG mode: no writer is wired.
func validateSessionWriteSafety(pgEnabled bool) error {
	sessionWritePGEnabled = pgEnabled
	v1 := settings.RequestLogsWriteEnabled()
	if err := settings.ValidateSessionWriteState(v1,
		settings.GetPlatformBool("sessions_v2.enabled", false),
		settings.GetPlatformBool("sessions_v2.shadow_write", false)); err != nil {
		return err
	}
	if !v1 && !pgEnabled {
		return fmt.Errorf("%w: V2 persistence requires PostgreSQL", settings.ErrNoRequestWriteSource)
	}
	return nil
}

// validSessionWriteConfigExamples returns the canonical configurations that
// pass validateSessionWriteSafety **in the current deployment shape**. Returned
// to main() on hard-fail so ops see the exact keys to set, not just the error
// sentinel.
//
// 2026-10-20 (audit D0): the list used to name `REQUEST_LOGS_WRITE_ENABLED`
// — an env var **that does not exist**. The real name is derived from the
// key by settings.EnvNameAuto, and the old "V2-only" line omitted
// sessions_v2.shadow_write, i.e. it advertised a configuration the validator
// rejects. Both were operator-facing lies in the one message that an operator
// reads when the gateway refuses to start, so both are now generated from the
// real names.
//
// 2026-10-29（★ 本轮：提示必须按部署形态分说）：旧列表是**形态盲**的 ——
// 它同时列出「V1-only（no V2, no PG required）」和「PG enabled + …」两族，
// 于是每一族都在**另一半形态**里是错的：
//
//	· PG 模式：v1 宽表已随 890 **物理删除**（族表与它的 promote/写分支都
//	  没了）⇒ PG 模式**根本没有 v1 写面**。「V1-only: <ENV>=true (no V2,
//	  no PG required)」宣传的是一个**不存在的写面**：那个开关在 PG 侧
//	  只剩下 admin ingest 的**正文文件镜像**一个副作用，与「请求日志落库」
//	  无关。把一个不存在的写面当成修法广告出去，运维照做之后仍然没有
//	  会话事实可查 —— 而网关**不会报错**（该配置能被校验器接受）。
//	· Lite 模式（无 PG）：会话族根本无从写入，「PG enabled + …」那两行
//	  是**做不到**的修法。
//
// ⇒ 现在：提示按形态给。PG 模式只给会话族那一条真修法；Lite 模式只给
//   SQLite request_logs 那一条（lite 的 SQLite 表确实还在，
//   cmd/gateway/lite_telemetry_sink.go 按同一个键写它）。
//
// 判定依据是**事实**而不是偏好：
//   · PG 侧 v1 写分支整段删除（domains/hooks/observability/telemetry/client.go，
//     890 一轮）+ 890 物理删掉整个 v1 族 ⇒ PG 模式无 v1 写面；
//   · storage/sqlite/schema.go 仍建 request_logs 表，lite sink 按同一个键
//     写它 ⇒ Lite 模式有 v1 写面；
//   · PG 模式的会话事实写入恒为必需（entry.SessionV2WriteRequired 在 PG
//     持久化路径上**无条件**置 true）⇒ 「V2-only」是 PG 模式的真修法。
//
// 形态标记用 [PG] / [Lite] 前缀：既给人看，也让守卫测试能逐条**执行**
// 广告出来的配置（见 cmd/gateway/session_write_safety_test.go）—— 字符串
// 相等断言会把两句谎话永久钉死，这正是 D0 审计要避免的。
func validSessionWriteConfigExamples() string {
	v1Env := settings.EnvNameAuto(settings.KeyRequestLogsWriteEnabled)
	v2Env := "LLM_GATEWAY_SESSIONS_V2_ENABLED"
	shadowEnv := "LLM_GATEWAY_SESSIONS_V2_SHADOW_WRITE"
	if sessionWritePGEnabled {
		return strings.Join([]string{
			"  - [PG]    " + v2Env + "=true + " + shadowEnv + "=true" +
				"  （会话事实写入 PG 会话族；PG 模式的 v1 宽表已随 890 退役，没有 v1 写面可开）",
		}, "\n")
	}
	return strings.Join([]string{
		"  - [Lite]  " + v1Env + "=true" +
			"  （Lite 模式写 SQLite request_logs 表；无 PG，不需要 V2）",
	}, "\n")
}
