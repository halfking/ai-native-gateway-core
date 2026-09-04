# 日志与资源监控审计报告

**审计范围**：持久化日志、资源监控及 `cmd/gateway` 集成
**审计日期**：2026-09-05

## 结论

- **评级**：通过（建议上线前继续观察资源趋势）
- 持久化日志使用 `lumberjack` 滚动文件，关键启动、关闭、异常退出和 panic 写入独立日志文件。
- 资源监控定时记录 Go 内存、RSS/VSZ、GC、CPU、goroutine、线程、文件描述符及系统内存指标。
- 监控 goroutine 受 `context` 控制；停止流程等待采集协程退出，并写入最终快照。
- 内存、文件描述符和 goroutine 阈值告警写入同一资源日志，便于离线趋势分析。
- 日志记录不包含请求正文、凭据或认证密钥；实例标识仅包含主机名、PID 和启动时间。

## 验证结果

- `go test ./pkg/logger ./pkg/monitor ./cmd/gateway`：通过
- `go test -race ./pkg/logger ./pkg/monitor`：通过
- `go vet ./pkg/logger ./pkg/monitor`：通过
- `git diff --check`：通过

## 配置入口

- `LLM_GATEWAY_PERSISTENT_LOG_DIR`：关键日志和资源日志目录，默认 `./logs`
- `LLM_GATEWAY_RESOURCE_MONITOR_INTERVAL`：采集周期，默认 30 秒
- `LLM_GATEWAY_MEM_GROWTH_THRESHOLD_MB`：内存增长告警阈值，默认 10 MB/分钟
- `LLM_GATEWAY_GOROUTINE_THRESHOLD`：goroutine 告警阈值，默认 10000
- `LLM_GATEWAY_FD_GROWTH_THRESHOLD`：文件描述符增长告警阈值，默认 100/分钟

## 已知限制

- 进程被 `SIGKILL`、机器掉电或内核崩溃时，应用无法执行最后一次 flush；滚动文件采用同步写入路径，已最大限度保留此前记录。
- 文件描述符上限字段依赖 gopsutil 平台实现；采集失败时该字段保持零值，不影响其他指标。
- 资源告警是趋势提示，不替代长期指标系统或 heap profile 分析。

## 审计工具说明

仓库中未找到技能文档指定的 `audit-pipeline.sh`，因此使用定向代码审计、`go vet`、单元测试和 race detector 完成验收。
