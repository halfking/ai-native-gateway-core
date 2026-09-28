# R62 激活链路续审 + 凭据安全审计交接（2026-09-28）

> 会话性质：R62 installer 审计的续审轮（恢复上下文后完成实现核验 + 质量门禁 + 安全审查）。
> 本文档是该轮的审计结论与下一会话交接载体；除本文档外本会话未修改任何代码。

## 1. 范围与结论总览

| 项 | 结论 |
|---|---|
| R62 安装模式选择（lite/full，默认 full） | ✅ 已核验实现 |
| R62 自动注册激活（register 即激活，fail-open） | ✅ 已核验实现 |
| R62 launcher 后台展示（元信息卡片 + /status 白名单） | ✅ 已核验实现 |
| installer 质量门禁（build/vet/test） | ✅ 全绿（跑在 `5fe87cadc`） |
| DB 修订序列契约（`scripts/apply-db-revision-sequence_test.sh`） | ✅ 通过 |
| 凭据安全 | ⚠️ 4 项待修（1 项已由 R63 审计登记为 P3，本轮升级证据） |

## 2. 实现核验证据（真实路径）

此前审计轮使用的 `installer/main.go`、`installer/internal/activation/activation.go` 为过时路径。本轮确认实际路径：

- 安装入口/自动激活接线：`installer/cmd/llm-gw-installer/main.go`
  - `runInstall` 第 10/10 步调用 `runAutoActivate`，失败仅 logWarn，不阻断安装（fail-open）。
  - 存储模式经 `LLM_GATEWAY_STORAGE_MODE` 写入 .env；wizard 默认 full，兼容 `STORAGE_MODE`/`LLM_GATEWAY_STORAGE_MODE` 双键。
- 自动激活核心：`installer/internal/activation/auto_activate.go`
  - `RunAutoActivate`：skip → 无 key 无 trial 时 `skipped`（不发网络请求）→ trial 换 key → register（`license_key_hash` 携带完整 license key，主控同调用完成激活，不再调用不存在的 `/api/v1/license/activate`）。
  - `device_code = "GW-" + sha256(instance_id)[:12]`（非凭据，可展示）。
  - `instance.token` / `refresh.token` 独立落盘（0600，CreateTemp+rename 原子写），绝不进 `activation.json`。
  - `activation.json` 0600，`state/` 0700，Ed25519 私钥 `instance.ed25519` 0600。
- 注册客户端：`installer/internal/enrollment/register.go` → `POST /api/v1/instances/register`，409 `device_limit_exceeded` 有专门映射。
- launcher 侧：`installer/cmd/llm-launcher/main.go` 每次 `/status` 重读 `{installDir}/state/activation.json`（`instancemeta.Load`，缺失/损坏降级空元数据，不阻断旧 5 字段）；`installer/internal/launcher/api/api.go` 的 `Status` 白名单仅含非凭据字段，token 比较用 `subtle.ConstantTimeCompare`；前端 `web/index.html` 的 6 个 `meta-*` 元素 ID 与 `web/app.js` 绑定一致。
- 回归测试钉桩：`auto_activate_test.go`（token 不进 activation.json、license-key-wins-over-trial、skip/fail 路径、权限 0600/0700、真实公钥非 placeholder）、`register_test.go`、`launcher/api/api_test.go`（元数据合并 + 向后兼容 + failed 透传 activation_error）。

## 3. 质量门禁（跑在 `5fe87cadc`，当时工作树 clean）

```
installer: go build ./...          ✅
installer: go vet ./...            ✅
installer: go test -count=1 ./...  ✅（19 包，其中 6 包无测试文件）
仓库根:  bash scripts/apply-db-revision-sequence_test.sh  ✅ "contract passed"
```

注意：门禁执行后远端 main 已前进（现 HEAD `1e5cf2665`，R73/审计十七轮等提交）。本审计结论针对的 installer 激活链路文件在 HEAD 仍含下述发现（已复核 grep 命中），未重跑全量门禁。

## 4. 安全审计发现（本轮新增/复核，均待修）

### H-1（高）离线 activation code 明文进入日志、DB 与运维 API
- 默认 notifier 把 `activation_code` 写结构化日志：`licensing/activation_notify.go:19-30`、`licensing/offline.go:96-109`。
- 明文持久化 `offline_activation_requests`：`licensing/store_pgx.go:506-533`（迁移 376 加列）。
- 运维概览原样返回最近 50 条 code：`admin/ops_overview.go:290-336`。
- 影响：日志/DB 只读权限或运维数据面访问者可获取仍有效的激活凭据。
- 建议：DB 存 hash（验证比对 hash）；日志与运维 API 掩码；审计只记 request ID + code 指纹 + 审批人/时间。

### H-2（高）refresh token 在主控 DB 明文等值存取
- `center/store_pgx.go:397-423`：`WHERE refresh_token = $1` / `SET refresh_token = $2`。
- 影响：DB 备份/导出/只读账号泄漏即可换取 instance token（高熵不能抵消明文可重放）。
- 建议：存 `HMAC-SHA256(token, 服务端 pepper)`，查询同变换；签发时仅返回一次原文；迁移用双读→回填→切换。

### M-1（中）试用 license key 明文落 `state/activation.json`
- `installer/internal/activation/auto_activate.go:59`（`LicenseKey` 字段）+ `:362`（trial 路径赋值）。
- R63 审计轮已登记为 P3（`docs/audit/2026-09-24-r63-48h-audit-round.md:99-101`）；本轮确认测试 `auto_activate_test.go:452` 反而在**钉桩要求写入**该 key，修复需同步改测试。
- 影响：0600 文件 + launcher 白名单当前未外泄，但备份/诊断/支持包边界一旦放宽即泄漏可激活凭据。
- 建议：状态 JSON 仅存掩码或指纹；完整 key 进独立 0600 凭据文件（或注册后即不再本地需要，直接删除持久化）。

### M-2（中）试用唯一性仅靠 Redis，无 DB 约束
- 邮箱占位：`cmd/license-authority/trial_handler.go:160-175`（SETNX 365 天）；事务 `licensing/store_pgx.go:124-155` 与 `license_trial_consents` 唯一约束（`01-schema.sql:9353-9360`）均不约束 email。
- 影响：Redis 丢 key/淘汰/绕过时同邮箱可多领试用。
- 建议：normalized email 的 trial-only partial unique index（或事务内锁+存在即拒），Redis 仅做限流层。

### 观察项（低/待定）
- `token_audit_events` 表（`sql/objects/tables/token_audit_events.sql`）schema 完整但未发现生产写入点——审计闭环存疑，需判定废弃或补写入链路 + 集成测试。
- `docs/03-design/.../API.md` trial 描述（7 天、携带 instance/hw 字段）与实现（默认 15 天、仅 email+agree）漂移。

## 5. 下一会话任务（按优先级）

1. **M-1 修复**（本仓 installer，最小改动）：`ActivationState` 移除 `LicenseKey` 持久化 + 改 `auto_activate_test.go:452` 断言 raw JSON 无 `license_key`；门禁：`go build/vet/test -count=1 ./...`（installer 目录）+ 仓库根 `bash scripts/apply-db-revision-sequence_test.sh`。
2. **H-1 修复**：activation code hash 化 + 日志/运维 API 掩码（涉及 licensing + admin + 迁移，先出双读迁移方案）。
3. **H-2 修复**：refresh token HMAC 化存储（center + 迁移 + token_refresh 联动）。
4. **M-2 修复**：trial email partial unique index 迁移。
5. 观察项裁决并记录。

约束：每项修复独立提交、独立回归；改 DB 需 forward-only 迁移并过修订序列契约；不修改 launcher `/status` 公开契约与 installer fail-open 语义。

## 6. Git 状态记录

- 本轮核验时工作树 clean、`5fe87cadc` 落后 codeup/main 2 提交（未 pull，只读审计）。
- 交接落档时 HEAD 已被其他会话推进至 `1e5cf2665` 且与 codeup/main 一致；本会话除本文档外无代码变更，本文档随轮提交推送。
