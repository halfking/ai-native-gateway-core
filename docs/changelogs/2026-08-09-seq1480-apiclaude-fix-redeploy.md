# 2026-08-09 — seq 1480: apiclaude/Claude 全部失败 — 重新部署含 fix 的二进制

## 症状

用户报告：通过网关调用供应商 apiclaude（provider 587，`claude-sonnet-5` / `claude-opus-5` 等模型）**全部失败**。

154 `gateway.log` 显示 provider_id=587 的请求持续报：

```
executor: transient error, trying next candidate  err="ir parse openai: transport: converter circuit open"
candidate_failed_trying_next  credential_id=17 provider_id=587
```

01:30–01:43 窗口统计：`/v1/chat/completions` 4011 条请求中 503×224 / 500×141；claude-opus-5/claude-sonnet-5 请求（sole-candidate，credential_id=17，sibling 31/33 均被 `manual_disabled`）在熔断窗口内几乎全部失败。

## 根因

1. `domains/transformation` 维护一个**进程内全局** `StreamCircuitBreaker`（3 次错误 / 1 分钟 → OPEN，冷却 1 分钟），`TransportIRConverter` 的 10 个 Parse/Serialize 方法共用同一实例，**没有按 provider 维度隔离**。apiclaude 上游偶发抖动（超时/工具调用校验失败）短时间内攒够 3 次错误就会把熔断器打开。
2. `claude-sonnet-5` / `claude-opus-5` 是 sole-candidate 模型（仅 credential_id=17 可路由），熔断打开期间该模型的每一次请求都直接被 `ErrConverterCircuitOpen` 拒绝。
3. 代码层面，`cddb9956`（seq 1479，2026-08-08 23:33）已经修复：`executor_anthropic.go` / `executor_chat.go` 的 IR converter 直调点加了 `errors.Is(err, ErrConverterCircuitOpen)` 检测，命中熔断态时 fallback 到 legacy 转换器而不是硬 503。
4. **但这个修复从未真正跑在 154 生产环境上**：`docs/audits/AUDIT-2026-08-09-seq1479-symlink-mislabel-and-audit-sql-regression.md` 记录的部署事故链——seq1479 声称已部署（`current` symlink 已指向含 fix 的 `1478-c4ab07de`），但运行中的进程因 systemd `TimeoutStopSec` SIGKILL 漂移，实际跑的是不含 fix 字符串的旧二进制（`52c23fcb...`, `releases/1478-3c98ac6c/`）；之后一次纠正部署又暴露了一个不相关的 P0（`provider/client.go` SQL 字面量里混入 Go `//` 注释导致 PostgreSQL 语法错误，79% 请求 500，已在 `f338f611` 修复），最终回滚回了**既没有 fallback 修复也没有 SQL 修复**的版本。
5. 用 `strings <running-binary> | grep ir_converter_circuit_open_fallback_to_legacy` 验证，命中 0 次——坐实了"fallback 代码从未在生产运行过"，而不是"fallback 生效后 legacy 路径本身也失败"。

## 修复动作

代码本身无需改动（`cddb9956` + `f338f611` + 当前 main 的其它累积修复均已在仓库里）。本次操作是**重新部署**：

```bash
bash scripts/deploy-seamless.sh deploy 154 --no-frontend
```

- 无前端相关改动，跳过前端构建（本机 web/node_modules 缺失，与本次修复无关）
- 交叉编译 `GOOS=linux GOARCH=amd64`，seq 1480，git_sha `e7391c6f`
- 原子符号链接切换 + restart，healthz/DB 自动校验通过（自带自动回滚安全网，未触发）
- 部署后 `strings` 验证运行中二进制包含 2 处 `ir_converter_circuit_open_fallback_to_legacy_*` 字符串（对应 executor_anthropic.go / executor_chat.go 两个调用点）

## 验证

- `go build ./...`：OK
- `go test ./domains/streaming/... ./domains/transformation/... ./provider/...`：全过
- 部署后实测：
  - `claude-opus-5` 单次请求 → 200 OK
  - `claude-sonnet-5` 5 次串行请求 → 全部 200
  - `claude-sonnet-5` 8 次并发请求 → 全部 200
  - 部署后时间窗口内 `provider_id":587` 无任何 `circuit open` 报错

## 后续建议（未在本次处理）

- `StreamCircuitBreaker` 应按 provider_id 维度隔离，避免一个不稳定中转商拖累全局 IR 转换通道（审计文档已提出，尚未实施）。
- 部署脚本应把 `sha256sum` + string-grep 校验做成强制门禁（而非事后人工验证），避免 symlink 指向的 release 目录内容与实际运行进程分歧的情况重演。
- 154 的 `TimeoutStopSec=25s` 仍可能导致优雅停止不足时被 SIGKILL；建议排查是否需要进一步调大或加 `kill -INT` 预停止钩子。
