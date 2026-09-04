# 日志与资源监控审计报告（第二轮复审）

**审计范围**：`pkg/logger`、`pkg/monitor`、`cmd/gateway/main.go` 集成
**审计日期**：2026-09-05
**基线提交**：a25d412bf（feat: persist critical logs and monitor resources）

## 发现与处置

| # | 级别 | 位置 | 问题 | 处置 |
|---|------|------|------|------|
| 1 | P1 | `pkg/monitor` Stop/Start | 未启动时 `Stop()` 会关闭 `stopDone`；此后若调用 `Start()`，采集循环永不退出（goroutine 泄漏），且其 `defer close(stopDone)` 与已关闭通道冲突，存在 double-close panic 隐患 | 新增 `stopped` 标志：`Stop` 标记后 `Start` 为 no-op；`stopDone` 仅由 `collectLoop` 关闭。补充回归测试 `TestStopBeforeStartThenStart` |
| 2 | P2 | `cmd/gateway` 异常退出路径 | 三处 `os.Exit`（启动 panic、监听失败、pprof 配置错误）与首个 panic recover defer 均未停止资源监控、未关闭持久化日志，退出时缺最终资源快照 | 各退出点在退出前依次执行：`resourceMonitor.Stop()`（写最终快照）→ 写入异常/panic 记录 → `persistentLogger.Close()` |
| 3 | P2 | `pkg/logger` LogStartup | 读取 `os.Getenv("GO_VERSION")`，运行环境通常未设置，字段恒为空 | 改用 `runtime.Version()` |
| 4 | P3 | `pkg/logger` 各 Log* | `append(args, ...)` 可能原地改写调用方变参切片的底层数组 | 新增 `withFields` 帮助函数，复制后再追加固定字段 |

## 确认项（无需修复）

- `lumberjack.Logger` 重复 `Close` 安全；优雅关闭与异常退出路径可能两次调用 `Stop`/`Close`，无副作用。
- `writeSnapshot`/`writeAlert` 共享同一 `json.Encoder`，均持有 `rm.mu` 写锁；`detectLeaks` 不在持锁状态下写日志，无死锁路径。
- 泄漏检测阈值为构造期只读字段，无并发写。
- 内存增长检测已使用有符号差值，RSS 下降不会因无符号下溢误报（首轮已修）。
- 日志内容不含请求正文、凭据或密钥；`instance_id` 仅含主机名、PID、启动时间戳。
- 同目录多实例共写 `resource_monitor.log`，靠每行的 `instance_id`/`pid` 字段区分，属预期设计。
- `RLIMIT_NOFILE`/`NumFDs` 在部分平台（如 macOS）不可用时字段置零，不影响其余指标采集。
- `migrate` 子命令在日志模块初始化之前退出，属 CLI 行为，无需持久化记录。
- 进程被 `SIGKILL`、掉电、内核崩溃时无法执行最终 flush，属应用层不可控边界（首轮已记录）。

## 验证结果

- `go test -count=1 ./pkg/logger ./pkg/monitor ./cmd/gateway`：通过
- `go test -race -count=1 ./pkg/logger ./pkg/monitor`：通过
- `go vet ./pkg/logger ./pkg/monitor`：通过
- `gofmt`：无格式偏差
- pre-commit 钩子（go vet / SQL / Migration 检查）：通过

## 结论

第二轮审计发现的 1 个 P1、2 个 P2、1 个 P3 问题已全部修复并有测试覆盖；复审通过。
