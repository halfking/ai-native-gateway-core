package main

// PartitionManager 单实例开关（2026-10-06，审计 §10.51/§10.52）
//
// 生产实测：154 与 245 两台 gateway 的 DSN 指向**同一个库**
// （172.16.2.210:5432/llm_gateway），两台都装配了 PartitionManager。
// 窗口 06:00:53~12:51:04 内：
//
//	154 日志 'partition_manager: analyze stats'(tables:44)  7 次
//	245 journalctl 同样消息                              7 次
//	pg_stat_statements Δcalls                          14 次   ← 7+7 逐条闭合
//
// 且两台的时刻只差 4.2→0.6 分钟且**在收敛**，
// 即两次全量 44 表 ANALYZE 落在同一分钟内互相争 I/O 与 shared_buffers，
// 而不是把负载摊开。
//
// 同一批 hot 表只需要一个 promote/analyze 执行者。
// 本开关默认**开启**（不改变任何现有行为），
// 在不需要它做分区维护的节点上显式设为 false 即可。
//
// ⚠ 这只是提供手段；真正的单实例化（含选主/漂移保护）需要单独授权后实施。

import (
	"os"
	"strconv"

	"log/slog"
)

// partitionManagerEnabledFromEnv 读 LLM_GATEWAY_PARTITION_MANAGER_ENABLED。
//
// 判据：**只有能解析成 false 时才禁用**。
// 空值、拼错的值、非法值一律保持启用（默认开启）——
// 治理 worker 因一个拼错的 env 静默停摆，比多跑一轮 analyze 危险得多。
// 非法值会打 Warn，让「本以为关了其实没关」和「本以为开着其实关了」都可查。
func partitionManagerEnabledFromEnv() bool {
	v := os.Getenv("LLM_GATEWAY_PARTITION_MANAGER_ENABLED")
	if v == "" {
		return true
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		slog.Warn("partition_manager: LLM_GATEWAY_PARTITION_MANAGER_ENABLED 无法解析，按默认启用处理",
			"value", v)
		return true
	}
	return b
}
