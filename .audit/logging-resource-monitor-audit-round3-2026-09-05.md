# 日志与资源监控审计报告（第三轮复审）

**审计范围**：`pkg/logger`、`pkg/monitor`、`cmd/gateway/main.go` 集成
**审计日期**：2026-09-05
**基线提交**：c762da99a（fix: harden logger and resource monitor after audit）

## 发现与处置

| # | 级别 | 位置 | 问题 | 处置 |
|---|------|------|------|------|
| 1 | P2 | `logger.InitPersistentLogger` / `monitor.InitResourceMonitor` | 首次初始化失败后 `sync.Once` 已消费；后续调用中 `err` 为新声明的局部变量（nil），返回 `(nil, nil)`。调用方 `if err != nil` 判断走成功分支，但实例为 nil，与"失败后重试保持失败"的语义不符 | 用包级 `persistentInitErr` / `resourceMonitorInitErr` 记录首次结果，后续调用返回 `(nil, 首次错误)` |
| 2 | P2 | `pkg/monitor` RLIMIT_NOFILE | 硬编码 `limit.Resource == 7` 仅在 Linux 成立；Darwin 上 RLIMIT_NOFILE=8，导致 macOS 上 `max_fds`/`used_ratio` 恒为 0 | 按平台拆分常量：`rlimit_nofile_linux.go`(7)、`rlimit_nofile_darwin.go`(8)、`rlimit_nofile_other.go`(0=跳过匹配)；0 值时不再误匹配 |
| 3 | P2 | `cmd/gateway/main.go` panic 兜底 | recover defer 只在持久化日志初始化成功的 `else` 分支安装；初始化失败时，其后的 `cfg.ValidateRuntimeRole` panic 与 auth fail-closed panic 无持久化记录即崩溃 | defer 改为无条件安装（内部已有 `persistentLogger != nil` 守卫） |
| 4 | P3 | `pkg/monitor` `writeSnapshot`/`writeAlert` | `jsonEncoder.Encode` 返回值被丢弃，磁盘满/权限故障时快照与告警静默丢失 | 记录错误并输出 `slog.Warn`（写入 stderr/主日志，不经过监控自身 writer，无递归） |

## 交叉验证

- `go build ./...`（darwin/arm64）：通过
- `GOOS=linux go build ./pkg/monitor/`：通过（rlimitNOFile=7 分支编译）
- `GOOS=darwin go build ./pkg/monitor/`：通过（rlimitNOFile=8 分支编译）
- `GOOS=windows go build ./pkg/monitor/`：通过（rlimitNOFile=0 跳过匹配分支编译）
- `go test -count=1 ./pkg/logger ./pkg/monitor ./cmd/gateway`：通过
- `go test -race -count=1 ./pkg/logger ./pkg/monitor`：通过
- `go vet ./pkg/logger ./pkg/monitor`：通过

## 测试覆盖说明

问题 1 的失败路径需污染包级 `sync.Once` 状态，同进程单测无法在成功用例之后复现（`TestGlobalPersistentLogger` 已消费 once）；已覆盖其成功语义（重复调用返回同一实例且 err 为 nil），失败语义由代码结构保证（首次结果提升为包级变量，无局部变量遮蔽路径）。

## 复核确认（沿袭前两轮，未回归）

- Stop/Start 生命周期：`stopped` 标志防止 Stop-before-Start 泄漏采集协程；`stopDone` 仅由 `collectLoop` 关闭。
- 异常退出路径：启动 panic / 监听失败 / pprof 配置错误均先停监控、落盘异常记录、再关闭 writer。
- lumberjack `Close` 幂等，优雅关闭与异常退出重复调用无副作用。
- `withFields` 复制变参，不再改写调用方底层数组；`LogStartup` 使用 `runtime.Version()`。
- 内存增长检测使用有符号差值，RSS 下降不误报。
- 日志不含请求正文、凭据、密钥。

## 结论

第三轮审计发现的 2 个 P2 初始化/平台缺陷、1 个 P2 panic 兜底缺口、1 个 P3 静默丢写问题已全部修复，交叉编译与全量验证通过；复审通过。
