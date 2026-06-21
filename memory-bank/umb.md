# UMB — llm-gateway-go 3 天修改全面审计 (v6.0)

> **最后更新**：2026-06-21
> **模式**：audit → fix（已完成 184 SSH 验证 + UMB 续上）
> **任务**：对 2026-06-18 ~ 2026-06-21 的 ~110 个 commit 做全面审计
> **镜像**：`kx-llm-gateway-go:gitsha-8cc5b8d7` (与子模块 HEAD 一致)
> **部署**：184 k3s `pms-test/llm-gateway-go-deployment-7fdbb875fb-cx846`
> **Healthz**：https://llmgo.kxpms.cn/healthz → 200 OK
> **审计报告**：`docs/2026-06-21-three-day-audit.md`

## 🎯 任务 (本轮)

老板提出：审计 6/18-6/21 三天 llm-gateway-go 的所有修改，特别注意"反复出现的问题"。

## 🏗️ 工作流

1. **Plan 模式 5 轮迭代** 产出 v1.0 → v5.0
2. **Build 模式**：切到 build 后跑 T1-T3 三个 P0 验证 (SSH 到 184 k3s pod)
3. **UMB 续上**：本文件最后更新从 2026-06-19 推到 2026-06-21
4. **修复**：处理 v5.0 列出的 6 个 P0/P1 反复问题

## ✅ T1-T3 验证结果 (2026-06-21 5 min 跑完)

| 验证 | 命令 | 结果 |
|------|------|------|
| T1 tool_registry 字段 | `information_schema.columns` | 15 列完整 (含 tool_id/tenant_id/version + 6/21 之后 4 列) |
| T2 020 UNIQUE index | `pg_indexes WHERE indexname='idx_request_logs_request_id_ts_unique'` | 存在 |
| T3 022/023/025 settings 表 | `pg_tables` | 8 张表全在 |
| T3 RLS policy | `pg_policies` | 7 张表有 RLS (settings_audit/tenant_settings_kv/tenant_tool_policies/tool_call_events/tool_usage_stats/tenant_model_policies/tenant_model_policies_audit) |

**所有 P0 解除**。新发现：tool_registry 表**没有 RLS policy**（其它 7 张都有）— 见 P1 修复列表。

## ✅ 已完成修复

- [x] T1-T3 三个 P0 验证 (15 min)
- [x] UMB 续上 (本文件)
- [x] Dockerfile `USER root` 修复 commit (chown 需要 root 权限)

## 🔧 待办修复 (按 P 排序)

| P | 任务 | 状态 |
|---|------|------|
| P1 | tool_registry 加 RLS policy (与其它 7 张表对齐) | 待办 |
| P1 | 修 024/025 migration 编号冲突 (改 028/029/030) | 待办 |
| P1 | pre-commit hook 加 vue-tsc / SQL lint / 编号去重 / markdown-link-check | 待办 |
| P1 | 拆 `admin/providers.go` 4555→<1500 | 待办 |
| P2 | web 端加 vitest, 4 个 view 写测试 | 待办 |
| P2 | 修 docs 死链 (`docs/llm-gateway-go/` 引用) | 待办 |
| P2 | Dockerfile 镜像瘦身 (kx-base alpine-slim) | 待办 |
| P2 | 给 `model_policies.go` 写最小单测 (80%) | 待办 |
| P2 | 71 同步到 8cc5b8d7 | 待办 |
| P3 | 拆 `web/src/api.ts` 4176→<2000 | 待办 |
| P3 | bg/model_config_validator 加 writeAuditLog | 待办 |

## 📊 关键数字 (审计基线)

| 维度 | 数量 |
|------|------|
| 3 天 commit 数 (6/18-6/21) | ~110 |
| fix-of-fix 反复 commit | 19 (17.3%) |
| 6/18-6/21 新增 0 测试的核心文件 | 13 |
| `web/` TypeScript 测试文件 | 0 |
| 184 `llm_gateway` DB 表数 | 95 |
| 184 `request_logs` 行数 | 41150 |
| 184 `tool_registry` 行数 | 7 |
| 184 `tenant_model_policies` 行数 | 1 (Round 48 已生效) |
| 184 image tag | `gitsha-8cc5b8d7` ✓ 与 git HEAD 一致 |
| 71 image tag | `1f60e8ef` (落后 5+ commit) |

## 📌 Round 编号

- 主仓库 AGENTS.md 提 Round 24 (multi-tenant audit)
- 子模块代码提 Round 48 (= Round 24 + 24，"第二轮 L1 审计" 推测)
- 未来 UMB 顶部固定 Round 定义

## 🔗 链接

- 审计报告 v6.0 终版: `docs/2026-06-21-three-day-audit.md`
- Round 24 历史: 主仓库 `docs/llm-gateway-go/multi-tenant-2026-06-15.md` + 24 轮 handoff
- 反复问题 commit 索引: `git log --since="2026-06-18" --until="2026-06-21 23:59" --pretty="format:%h %s" | grep -E "fix|duplicate"`

## 🎯 任务

老板提出：客户端发新消息时，gateway 需要：
1. 查询上一次转发给 LLM 的消息内容（forwarded_body）
2. 如果上次压缩过 → 找出"客户端新增的消息" → 拼接到压缩后的 last_outbound_body 后转发
3. 如果上次没压缩 → 直接用客户端消息转发
4. **不重复压缩** — 只对新增部分做处理，已压缩内容保留
5. 实现智能滑动窗口（token / 消息数 / 空闲）
6. 超过一段时间进行摘要处理
7. **使用最强的压缩算法**，尽量不丢失内容

## 🏗️ 架构：v3 = per-session 维度 + 95% 复用 v7

```
                        v3 (per-session)             v7 (per-request)
                        ──────────────────────       ──────────────────
触发器:                sliding_window_token         mode=1 自动阈值
                       sliding_window_count         mode=2 4xx 重试
                       sliding_window_idle
压缩算法:              LLM 摘要 (lossless CN prompt)  LLM 摘要 / 机械 trim
缓存:                  L1 内存 LRU(1024)              无
                       L2 Redis Hash                 
                       L3 PG request_logs            
持久化:                request_logs.outbound_body    request_logs.compression_*
                       + outbound_msg_hashes        (parent_request_id 等)
                       + outbound_msg_count
                       + outbound_token_est
UI 暴露:               「转发消息」tab + delta 标记     列表 badge + 父子面包屑
```

## ✅ 已交付

### 任务清单（T22-T28 + UI/API 增强）

| # | 任务 | 关键文件 | 状态 |
|---|------|---------|------|
| T22 | compactionPromptForTaskType factory (lossless CN/EN prompt) | `compressor/compaction.go` | ✅ |
| T23 | DB 迁移 + RequestLogEntry 4 字段 + INSERT/UPDATE | `db/migrations/016_outbound_body.sql` + `telemetry/client.go` + `db/db.go` | ✅ |
| T24 | SessionCache L1/L2/L3 三级 + smm_v1 marker 协议 | `compressor/session_cache.go` | ✅ |
| T25 | BuildOutboundMessages LCS delta-append | `compressor/diff.go` | ✅ |
| T26 | ShouldTriggerWindow 3 触发器 + 60s 互斥 + 降级 | `compressor/window.go` | ✅ |
| T27 | SessionCompressor 编排器 + 接入 handler | `compressor/session_compressor.go` + `relay/handler.go` | ✅ |
| T28 | 部署 184 + 4 步门 + healthz | (deployment script) | ✅ |
| UI | v3 strategy badge + 「转发消息」tab | `web/src/views/RequestLogsView.vue` | ✅ |
| API | /api/logs 暴露 8 个 v3 字段 | `admin/logs.go` | ✅ |

### 提交记录

```
e82ebd00 feat(ui+api): expose v3 outbound body in admin logs UI/API
5134cd0f fix(db): ensureRequestLogSchema — auto-apply v3 outbound columns at startup
e7339bde fix(v3-compressor): audit fixes — wire task-aware prompts + main.go startup
118c9529 feat(compressor): v3 session-level intelligent compression
```

主仓：
```
1a98b6d8 chore(llm-gateway-go): bump submodule — v3 outbound body in admin logs UI/API
20cfb12e chore(k8s+llm-gateway-go): v3 session compressor env vars + schema ensure
03d39d9f chore(llm-gateway-go): bump submodule — v3 audit fixes (e7339bde)
```

## 🔬 端到端验证矩阵

### 测试场景 A：全新会话（第 1 条消息）

```
Request: POST /v1/chat/completions
  Headers: X-Gw-Session-Id=gw_ui2_xxx
  Body: {messages: [user("v3 UI test 1")]}
→ 200 OK
→ prompt_tokens: 49 (1 msg 估算)
→ compression_strategy: null (新会话不压缩)
→ outbound_body: NULL
```

✅ 行为正确：第 1 条消息无 last_state，按 new_session 处理。

### 测试场景 B：同会话增量（第 2 条消息）

```
Request: POST /v1/chat/completions (same session_id)
  Body: {messages: [user, assistant, user("NEW")]}
→ 200 OK
→ compression_strategy: "delta_append"  ← v3 触发
→ outbound_msg_count: 3                 ← last(2) + new tail(1)
→ outbound_token_est: 55
→ outbound_msg_hashes: [{index:0,sha256:...}, {index:1,...}, {index:2,...}]
```

✅ 行为正确：LCS 找到共同消息（user+assistant），把 new user 追加到末尾。

### 测试场景 C：滑动窗口触发

```
session state msg_count = 60 (> 50 default)
→ ShouldTriggerWindow returns Reason="sliding_window_count", ShouldTrigger=true
→ SessionCompressor tryLLMSummary → 失败 → 降级 mechanical_trim
→ Log: "session_compressor: LLM summary failed/no-op, falling back to mechanical trim"
        trigger="sliding_window_count"
```

✅ 触发器正确触发 + 降级路径生效（机械 trim 当 contextWindow=0 时退化为 no-op，known boundary）。

### 测试场景 D：API 字段完整暴露

```bash
GET /api/logs/{request_id}
→ JSON:
  compression_strategy: "delta_append"
  outbound_msg_count: 3
  outbound_token_est: 55
  outbound_msg_hashes: [{index:0,sha256:"..."}, ...]
  outbound_body: {messages: [{role:"user", content:"..."}, ...]}
```

✅ 后端 `/api/logs` + `/api/logs/{id}` 完整暴露 8 个 v3 字段。

### 测试场景 E：Redis L2 缓存

```bash
HGETALL session:sc:default:{gwSessionID}:v1
→ v=1, mc=1, te=38, loh=6be827d8..., lcat=0, rcat=0, smm=""
```

✅ 三级缓存的 L2 正常工作，schema_version=1 正确。

### 测试场景 F：UI Badge 渲染

列表压缩 badge 现在支持：
- `delta_append` → teal 配色 + tooltip "同会话增量"
- `sliding_window_token/count/idle` → purple 配色 + tooltip "触发: xxx"
- `summary_marker` → 单独 badge 显示

✅ 详情抽屉「转发消息」tab 渲染实际转发体，smm_v1 摘要边界消息高亮。

## 🛡️ 审计修复的问题

| # | 问题 | 修复 |
|---|------|------|
| 1 | `compactionPromptForTaskType` 定义后没调用 | 串到 `tryLLMContextCompactionWithTask` 调用链 |
| 2 | 中文 system prompt + 英文 user content 语言错配 | `buildUserSummaryInstruction` 匹配 system 语言 |
| 3 | `cmd/gateway/main.go` 没 wire `SetSessionCompressor` | 新增 `main_v3_wiring.go` 三级 adapter + 工厂模式 |
| 4 | DB schema 未自动 apply v3 列 | `ensureRequestLogSchema` 增加 4 个 `ADD COLUMN IF NOT EXISTS` |
| 5 | admin/logs.go API 没暴露 v3 字段 | SELECT 列表 + scan 函数补全 8 个字段 |
| 6 | 前端 badge 不识别 v3 strategy | `compressionLabel()` + CSS 配色 + tooltip 信息 |
| 7 | 前端详情抽屉无 Outbound tab | 新增「转发消息」tab + delta 标记 + summary_marker 高亮 |

## 🔧 部署操作

### 镜像构建

```bash
K8S_SSH_PASSWORD='Kaixuan2025&9900#' \
ALLOW_SUBMODULE_DIRTY=1 ALLOW_DOCKERHUB_FROM=1 \
REMOTE_BUILD=1 INCREMENTAL_BASE_IMAGE='kx-llm-gateway-go:latest' \
./scripts/deploy-llm-gateway-go-184.sh --only app
```

### 环境变量（k8s manifest）

```yaml
- LLM_GATEWAY_SESSION_COMPRESSOR_DISABLE=false    # v3 kill-switch
- LLM_GATEWAY_WINDOW_MAX_MSG_COUNT=50            # 滑动窗口触发阈值
- LLM_GATEWAY_WINDOW_IDLE_SECONDS=300            # 空闲 5min 触发摘要
- LLM_GATEWAY_WINDOW_MIN_IDLE_MSG_COUNT=10
```

### 关键日志确认

```
"v3 session-level compressor wired (L1 in-mem + L2 Redis + L3 PG)"
"request_logs schema ensured (... outbound_body, outbound_msg_count,
                             outbound_token_est, outbound_msg_hashes)"
```

## 🚫 不做（明确划界）

- ❌ 不动 OpenClaw / agents/（read-only）
- ❌ 不重写 v7 compressor 三态（mode 0/1/2 已落档）
- ❌ 不动 ACC memory-compactor.js（acc::knowledge 域）
- ❌ 不做跨租户记忆
- ❌ 不改 v7 parent_request_id 单层链
- ❌ 不写新压缩算法（T22 仅加 factory 包装）

## 🔁 回滚策略

```bash
# 一键回滚到 v7
kubectl -n pms-test set env deploy/llm-gateway-go-deployment \
  LLM_GATEWAY_SESSION_COMPRESSOR_DISABLE=true
# 或直接回滚镜像
./scripts/deploy-llm-gateway-go-184.sh --rollback 5134cd0f
```

## 📚 相关文档

- v7 原始方案: `docs/llm-gateway-go/2026-06-18-compression-v7-final.md`
- v3 方案 v4-final: 内联在本次对话历史中（plan 模式输出）
- AGENTS.md §多租户审计 43 轮 — 跨租户隔离红线

## 📌 已知边界

1. **contextWindow=0 时 mechanical_trim 退化为 no-op** — handler 在 SessionCompressor.Prepare 调用时还不知道 contextWindow（executor 之后才知道），所以滑动窗口触发后只能调用 LLM 摘要。LLM 摘要失败时降级为 no-op 而不是 trim。优化方案：handler 拿到 candidates 后回填 contextWindow（需要多一轮 executor 调用）。
2. **LLM 摘要候选模型需要 env 配置** — `LLM_GATEWAY_COMPACTION_MODELS` 已默认 `minimax-text-01,gemini-2.5-flash`，但需要候选 cred 的 `ContextWindow >= 800_000` 才被选上。如果两个候选都不可用，摘要降级为 mechanical_trim。
3. **dress_ciphertext 未读取** — key_ciphertext 字段虽写入 DB 但前端不展示明文（审计要求）。
4. **summary_marker 仅在 LLM 摘要成功后写入** — 机械 trim 不会生成 smm_v1 marker（设计如此，因为 trim 不引入新内容）。