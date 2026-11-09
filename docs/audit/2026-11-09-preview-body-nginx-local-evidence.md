# Preview/body 与 nginx IPC 本地跟进证据

日期：2026-11-09（Asia/Shanghai）
代码基线：`9b2f4fbd0ffadeadf77ab1948d57f4aa35fd0d94`
执行位置：隔离 worktree `codex/preview-nginx-followup`。主 checkout 保持只读。

## Preview/body

先行 HTTP 回归复现了正文契约错误：`ActiveProbeExecutor.Run`、`RunGateway`、
`runModelsList` 和 Responses→Chat 降级都把最多 500 字符的 `RespPreview`
写进 `ProbeResult.ResponseBody`。旧 NodeProbe 路径也把其 512 字节预览映射到
遥测 `response_body`。JSONB 持久化器拒绝了这些不完整正文，因而正文字段落成
NULL；预览本应走单独的 `response_preview` 字段。

修复后 `RespPreview` 继续存储经凭据形态脱敏且有长度上限的诊断文本；
`ResponseBody` 只在读取完整、有界并通过 JSON 校验时设置。JSON 字符串值中的
已知凭据形态和敏感键值会在保留 JSON 结构的情况下脱敏。NodeProbe 的
`node_probe_runs.response_body` 继续按现有契约存储 512 字节预览，完整正文只进入
遥测 JSONB 字段；未改数据库迁移。Responses→Chat 降级使用降级响应正文，并保持
原有能力判定、错误分类和冷却状态。

验证：修改前新增的 HTTP 合同测试复现了截断；修复后以下检查通过：

- `go test ./bg -count=1`
- `go test ./domains/hooks/observability/telemetry -count=1`
- `go vet ./bg ./domains/hooks/observability/telemetry`
- `git diff --check` 与修改文件 `gofmt` 检查

回归覆盖 direct、gateway、models、Responses 降级、legacy NodeProbe、长 JSON、
非 JSON、读取错误、超限、JSON 脱敏，以及 emitter 将 preview/body 分栏。尚未执行
真实 PostgreSQL 端到端回读；本轮没有对生产数据库发起查询或写入。

留存的 `/tmp/lgw-48h-audit/154-app-after.raw.log` 部署/冒烟采样中有 121 条
`response_body` JSON 丢弃告警，其中 117 条原始长度为 500 或 512 字节。它覆盖部署
与探针冒烟，不等同于交接所述新窗口 7 条样本，也不能直接作为稳态趋势。

## nginx IPC

历史报告记录 154 的 nginx master 在 reload 时报告 `recvmsg() returned invalid
ancillary data`，旧 worker 仍代理至旧端口 8781；随后发现 `worker_connections`
耗尽，处理后旧 worker 优雅退出，新 worker 使用 8782。留存的
`/tmp/lgw-48h-audit/154-stale-worker-recovery.log` 显示 PID 10672 为新 worker、
PID 10780 为 shutting-down 的旧 worker，并记录 20 次 HTTP 200。
`/tmp/lgw-48h-audit/154-nginx-diagnostic.raw.log` 显示活动 upstream 指向
127.0.0.1:8782。

该组材料证明旧/新 worker 混跑及随后恢复，不能单独证明 FD 耗尽造成 ancillary
data 无效。历史 handoff 报告的 1024 worker connection/软 FD 限制与 4096 硬限制
支持资源压力假设，但没有保存故障时 FD 使用量或控制消息缓冲区状态。
当前执行机没有 `nginx` 二进制，因此未能在 loopback 实例复现；本轮未安装软件、
未登录生产主机，也未对生产 nginx 做故障注入。IPC 底层原因仍未决。

## 发布边界

历史发布报告记录 211/245/154 曾运行应用 sha `998269fb`，build_seq 分别为
2978/2956/2957，且当时 `/readyz` 的 DB/Redis 检查通过。此次没有重新读取三台
主机，所以这些是历史发布证据，不表示 2026-11-09 当前版本或 readiness 状态。
本轮只完成本地代码修正与验证，未部署、提交或推送。
