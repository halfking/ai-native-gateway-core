# Weekly Change Log — 2026-06-23 ~ 2026-06-30

> **范围**: main 分支，7 天回溯窗口
> **提交数**: 400（含 4 个 merge + 1 个 revert）
> **生成时间**: 2026-06-30
> **配套审计**: `docs/audit/2026-06-30-weekly-audit-report.md`

## 0. 一周整体节奏

| 日期 | 提交数 | 重点 |
|---|---|---|
| 06-23 | 54  | fp_slot/IR/relay 重构、credential manual_disabled 文档化 |
| 06-24 | 56  | work_types 三级路由、8 维评分、IR streaming P0、SIEM pkg #3 |
| 06-25 | 62  | R1.0–R1.5 重构 Phase 0–4、Agent Registry、Armor PoC、SIEM PoC |
| 06-26 | 50  | R1.12/R1.13 迁移、credentialfpslot 化、Thompson Sampling Bandit |
| 06-27 | 19  | settings/config hot-reload、telemetry upsert 阻塞修复 |
| 06-28 | 32  | probe v2 重构、Redis availability cache、可观测性 metrics 爆发 |
| 06-29 | 95  | Probe Health Dashboard、auth rule 20、JWT、auto-control、WIP 吸收 |
| 06-30 | 32  | agents Phase 7 + audit fixes、gateway-v2 审计修复、域名 SSOT |
| **合计** | **400** | |

**prefix 分布**: feat 162 / fix 124 / docs 38 / chore 33 / refactor 21 / test 8 / 其他 14
**风险信号**: `audit/audit-fix` 类 8 次、`WIP` 类 2 次、`Revert` 1 次 — 提示审计修复链路未闭合。

---

## 1. Agent / Agent Registry（Phase 3-7）

| 提交 | 摘要 |
|---|---|
| fb5b3693 | feat(agents): Phase 7 — asset health probe + MarkHealth/ListStale + /health |
| 8115bc28 | feat(agents): add Stats + Neighbors endpoints + frontend |
| 3596bca9 | feat(agents-ui): topology dialog（上下游邻居可视化） |
| 9bf9a649 | feat(agents-ui): stats overview cards |
| 5993c24e | refactor(admin): AgentService interface + 完整 agents API tests |
| c1503c41 | fix(agents): Neighbors depth limit — 上限 5 |
| b883b951 | fix(phase7): P1 — Health 分页 + 多租户 + ProbeOnce 分页 |
| bc82f72c | fix(phase7): P0 — 多租户 + LastSeenAt + txn type |
| 48409619 | fix(pg_store): make_interval(secs =>) 用于 ListStale |
| ed51a13d | fix(domains): detector/checker 表回退初始化 |
| 14dec45f | feat(admin): Agent Registry API（A3-1） |
| 5933d8d7 | feat(admin): Agent Registry UI（Phase 4 前端） |

**风险点**:
- Phase 7 出现 P0 + P1 双轮审计修复 → 第一次合入未通过审计。
- `c1503c41` 在「接受有效输入后」才截断 depth 5 → 可能存在 DoS 风险未在 commit message 中说清。
- `ed51a13d` 用 "table fallback" 兜底 detector/checker 初始化 → 暗示上游探测表可能存在时序问题，详见第 5 节 probe 章节。

---

## 2. Auth / Identity / Security（rule 20 主战场）

| 提交 | 摘要 |
|---|---|
| b68718e7 | feat(auth): rule 20 完整实现 — HttpOnly JWT + prefix 强制 |
| 2d0099b6 | feat(auth): SSOT 常量文件 + 迁移引用（rule 20 审计预防） |
| dd2ca4ac | fix(auth): rule 20 §6.1 cookie — SameSite=Strict, 24h TTL, Secure 自动 |
| d15f9227 | feat(security): rule 20 §8 auth 生产 fail-closed |
| 42175d5a | fix(identity): ClientIdentityHook 真正执行 |
| f5dfa345 | fix(audit): 补缺失的 auth_cookie_helpers.go |
| fb4ed4cd | feat(auth): 首次登录强制改密 |
| 376fd2bb | feat(security): SQL 注入白名单验证 |
| b0317681 | chore(security): 部署验证 + SECURITY.md v1.6 |

**风险点**:
- `b68718e7` → `42175d5a` → `f5dfa345` → `2d0099b6` 形成一条 "实施 → 修复 → 补文件 → SSOT 迁移" 链路，4 个 commit 串成一周工作，强烈建议作为**单独审计单元**（见审计报告 §A1）。
- "fail-closed in production" 与现有 dev/local 行为需要环境变量门控 — `d15f9227` 是否默认 ON 需确认。
- 首次登录改密（`fb4ed4cd`）会改变现有用户登录路径，注意是否有降级 / 跳过路径。

---

## 3. Armor（提示词注入 + LLM-as-judge）

| 提交 | 摘要 |
|---|---|
| 16cd14c4 | feat(armor): Phase 5 middleware — inspect + judge + log |
| d44eff21 | fix(armor): 移除越界 armorJudge/armorLogger 引用 |
| f2dd989f | refactor(armor): wrapped handler 移到 v2 块外（连线更清晰） |
| 0475eb40 | fix(armor): armor 检查移到 provider resolution 之前 |
| e6d84462 | fix(armor): logger 对齐 migration 049 schema |
| 03a23d60 | feat(armor): Judge 接入 relay handler（B1-5） |
| 7b9284ae | feat(armor): Logger + main.go 启动 |
| 4eeb630b | feat(armor): 攻击模式库 B1-2（35 patterns） |
| ecea00cc | feat(security/armor): LLM-as-judge 抽象 + Policy（NOW-2） |
| 6a2e5fcd | fix(disguise): restore NewRedisPool 兼容性 |

**风险点**:
- `0475eb40` 调整 armor 时机（移到 provider 解析前）→ 可能影响 provider 选择路径上的延迟与日志。
- `d44eff21` 移除 "out-of-scope" 引用 + `f2dd989f` 调整 wrapper 位置 → 暗示第一版 wiring 不合理。
- "35 patterns" 攻击库是英中双语硬编码 → 需要看是否引入可执行策略（deny vs flag）。

---

## 4. Gateway-v2 / API Endpoints（OpenAI 兼容）

| 提交 | 摘要 |
|---|---|
| e3e404e2 | feat(gateway-v2): /v1/chat/completions（OpenAI 兼容） |
| 2463e257 | feat(gateway-v2): /v1/models/{id} + /v1/completions |
| aa7106b8 | feat(gateway-v2): /v1/messages + autoroute SpeedP95 + gateway-v2 E2E |
| e0651a1d | feat(gateway-v2): /v1/responses（OpenAI Responses API） |
| 4a0cd9a1 | fix(gateway-v2): 审计修复 — JSON 错误格式 + ID 前缀 + 流守卫 |
| e9861917 | feat(gateway-v2): 默认 demo model 升级 gpt-4o |
| 0203872c | feat(streaming): 移植 models/static handlers |
| d5a8356c | fix(gateway): 保留 compressor 在 legacy runtime 的接线 |
| c647221c | feat(r112): R1.12 v2 Pipeline 接入 cmd/gateway |
| e889a6d5 | feat(llmgw): R1.12 v2 Pipeline feature flag（默认 OFF） |

**风险点**:
- gateway-v2 一周内从无到 4 个 endpoint + 1 个审计修复 → 公开 API 表面快速扩张，需要**契约稳定性**专项审计。
- `e889a6d5` feature flag 默认 OFF 是安全做法，但需确认部署是否在 prod 真正启用。
- `4a0cd9a1` 修复 "JSON 错误格式 + ID 前缀 + 流守卫" → 暗示第一版错误格式、ID 格式、流关闭路径均有合规/可观测性问题。
- `d5a8356c` "保留 compressor 在 legacy runtime" → 暗示迁移期间两条 runtime 共存，需要确认关闭路径。

---

## 5. Probe / Probe Health / Credential FPSlot

> 本周 probe 相关提交最密集，且 `audit-fix` 频次最高 → **审计优先模块**。

| 提交 | 摘要 |
|---|---|
| fb5b3693 | (agents) Phase 7 — health probe 跨模块 |
| fe093df0 | feat(wip): 2026-06-29 WIP — probe retry + response interceptor + probe health detail |
| 38cffffe | feat(wip): R1.13 cutover plan + probeutil + response interceptor chain |
| 46a5c71c / fc87e665 | feat(probe-health): 可点击行 → 7-tab 详情页 |
| 087011f3 | fix(probe-health-detail): 坏 tab 修复 + state summary + 价格 via /api/pricing/table |
| 8562de6b | fix(web): probe-health API 401 — 改用 req() 注 Bearer |
| 9d3d401b | fix(db): probe views 启动创建失败 — ROUND 不支持 PG |
| 67958366 | fix(db): probe views 第 2 次失败 — SELECT 列表不能用前列别名 |
| ea29edbe | fix(db): probe views 第 3 次失败 — CTE non-grouped 列不能在 FILTER |
| e99716c2 | fix(db): probe views 第 4 次失败 — state_summary 改子查询 |
| d9d99e1c | fix(db): ensureProbeHealthDashboardViews — 启动自动创建 |
| 990a2e39 | fix(probe): healthy_confirmed 反向同步 |
| fc8fa64f | feat(bg): 默认探测选择器 4→5 — domestic_featured 优先 |
| be948d61 | feat(probe): adaptive age-aware backoff + passive-failure boost |
| 215674b3 | feat(dashboard): probe health monitoring dashboard |
| 503a3475 | fix(dashboard): probe health dark theme |
| 32c4ac4d | test(verify): fp_slot 回归验证 |
| 3a910ca9 | fix(verify-fpslot): harden verification + redact audit doc |
| 18c22cf0 | docs(credentialfpslot): reclaim.go 标为 opt-in dead code |
| 96832f01 | feat(fp_slot): client-scoped sticky + 单槽释放 + 30min auto-expiry |
| 1f198554 | fix(db): ensureFpSlotLimit hook（036 孤儿导致 500） |
| b8330f10 | refactor(r1.13): credentialstate → domains/credential |
| d01c3b3a | feat(credentialfpslot): LRU preempt + 30min reclaim |
| f446cf30 | fix(credentialfpslot): 粘性 key 去模型 + 5min active gate |
| d7d8b43e | fix(routing): model_not_found 保持粘性（mnfStreak 仅信息） |
| 6a6abb18 | fix(credentialhealth): RecoverExpired 测试 |
| 48bcd9bb | fix(credentialfpslot): enable reclaim + TTL 注释 + hasPin expiry |
| c50e844b | feat(routing): route node health → credentialfpslot |
| b1703ccb | feat(routing): multi-level sticky 防跨模型污染 |
| 39c95f5b | feat(routing): 评分相等 → round-robin |
| e20e45a6 | feat(routing): V3.1 route node health + session preference |
| dee94b28 | fix(routing): 全 route node 禁用 → fail-open |
| 6503da98 | fix(routing+version): fail-open route nodes + robust version parsing |
| f098458d | fix(credential-health): 失败冷却 2h→15min |
| c25952d9 / c8c9180d / 24d56b19 | feat/fix(autoroute): CHANNEL_QUALITY_ROUTING 启停 + 审计修复 |
| 8b2b9498 / 4947735b | feat(autoroute): CHANNEL_QUALITY_ROUTING metrics + observability |
| 979cfd69 / 6b93ad72 | fix(autoroute): wake manual-disabled |
| f43586e0 / e25eb2ba | feat(bg/credential): BanditFlusher + 053 archive |
| 67fa7405 | feat(routing): Thompson Sampling Bandit scorer |
| 91fc816d | test(autoroute): SpeedP95 unsorted 回归 |
| 207badf0 | fix(executors): credentialfpslot tests 加 miniredis |
| 22613c1f | test(executors): fix force-unpin redis setup |

**风险点**（重点关注）:
1. **probe views 4 次连续失败**（`9d3d401b` → `67958366` → `ea29edbe` → `e99716c2`）→ 暗示迁移 review 严重不足，最终方案使用子查询；需审计当前 `v_probe_*` 视图是否能被 pg_dump 正确序列化。
2. **probe-health 前端 401**（`8562de6b`）→ 暗示前端鉴权层与新接口不兼容；需要审计所有 `req()` 改写是否一致。
3. **probe-health-detail 坏 tab**（`087011f3`）→ 暗示 tab 异步加载顺序或参数化错；7-tab 是否全部经过 e2e 待确认。
4. **WIP 直接落 main**（`fe093df0` / `38cffffe`）→ WIP commit 包含 response interceptor chain / probeutil，进入 prod 风险。
5. **fp_slot 模型去粘性 key**（`f446cf30`）→ 跨模型复用同一凭据指纹是设计还是 bug？需要追溯 `cda1067e` 与 `f446cf30` 的交互。
6. **fp_slot limit 迁移冲突**（`1f198554`, `2ddb398d`, `929c0c75`, `c9649c96`, `ad1efeca`, `395f844e`）→ 迁移编号历史混乱，需审计实际生效的迁移顺序。
7. **credential-health 2h→15min**（`f098458d`）→ 冷却大幅缩短，是否会引发探测抖动。
8. **autoroute 翻默认 ON**（`c8c9180d`）→ 涉及 hot path，需要确认 `24d56b19` 审计修复全部到位。

---

## 6. Routing / Auto-route / 路由核心

| 提交 | 摘要 |
|---|---|
| 545c9b20 | fix(routing): 仅在 client/upstream 协议不同时转换 |
| b5a6b3d9 | feat(routing): 评分体系扩展至 8 维 |
| 91331cba | feat(routing): 三级漏斗热路径优化 |
| 0f1a15af | feat(routing): 编程任务识别精度优化 |
| 57321849 | fix(routing): 修复测试 + 编程信号判定 |
| 5f3955dc | feat(admin): work_types 三级路由配置 |
| 27fad7f8 | feat(admin): 模型管理 8 维评分字段 |
| 254874b0 | fix(work-types): GROUP BY (expr) — by_l1_task |
| 1ffd72f | fix(work-types): NULL-safe is_auto_request |
| 6d924a23 | fix(audit): NULL-safe specified_model_requests |
| e4af51dd | fix(analytics): NULL-safe explicit-model filter |

**风险点**:
- 三级路由 + 8 维评分一周内上线 → 热路径变更需要专项压测（未在 commit 中体现）。
- "NULL-safe" 系列（3 次连续）→ 暗示上线后统计口径不稳。

---

## 7. Streaming / IR / Protocol Conversion

| 提交 | 摘要 |
|---|---|
| 9147dae9 | fix(ir): 双向 tool role + tool_use extraction（P0） |
| 9298126e | fix(ir): tool role 序列化（Anthropic→OpenAI） |
| 05bee9f9 | refactor(anthropic_to_openai_stream): 使用 IR 层 |
| 90d350c8 | feat(stream): StreamChunk IR superset + 函数重命名 |
| 6e065a3e | refactor(relay): ObservePayload → ObserveChunk |
| ef37a9a8 | fix(streaming): 尊重 body-derived session IDs + 空响应 502 |
| e8eab7d1 | fix(streaming): 会话解析 + 空响应 2 个 bug |
| 10de79b8 | fix(streaming): 流式结构化 tool_calls |
| 068d2a77 | fix(relay): Claude Opus 4 流 chunk timeout |
| a91bcf01 | fix(relay): Claude Opus 4 long-thinking chunk timeout |
| 1e1bd35c | fix(q2-response): OpenAI → Anthropic Messages |
| bdd982ef | fix(q3-stream): IR-driven Anthropic→OpenAI SSE |
| 2b2fc911 | fix(stream): preserve anthropic finish reason on zero output |
| fe4ee29c | fix(relay): persist structured tool_calls |
| 545c9b20 | fix(routing): 仅在协议不同时转换 |
| 7a34d1df | fix(ir): 保留 tool params + response_format schema |
| 70f6352b | feat(ir): thinking signature 字段 opus-4-8 |
| 0203872c | feat(streaming): 移植 models/static handlers |
| 9c614f44 | fix(executor): P0 上游 hang 硬超时 + prod_e2e |
| fd362d21 | feat(streaming): pre-stream keepalive 防首字节超时 |

**风险点**:
- "Q2/Q3 response conversion" 连续多次修复 → 上游响应转换路径不成熟。
- `6e065a3e` `ObservePayload → ObserveChunk` 是 IR 重构关键节点，影响面需评估。
- Opus 4 thinking pause → 引发两次 timeout 检测调整（`068d2a77`, `a91bcf01`），可能仍欠压测。

---

## 8. DB / Migrations

| 提交 | 摘要 |
|---|---|
| (见 §5) | probe views 4 次失败 + ensureProbeHealthDashboardViews |
| 48409619 | fix(pg_store): make_interval 用于 ListStale |
| 34fdb30b | feat(db): RLS for candidate_failure_logs + request_wal（migration 052） |
| e25eb2ba | feat(credential): archive_request_logs migration 053 |
| c0d11178 | feat(db): 输出合规监控数据库架构 |
| e81c2aca | feat(db): 提示词注入检测数据库架构 |
| 18cc8105 | feat(db): Session 聚合视图 |
| 1f198554 | fix(db): ensureFpSlotLimit hook |
| 1c64829c | feat(web): 未登录首页改版（DB 无关，前端） |
| c9649c96 / ad1efeca / 395f844e / 929c0c75 / 2ddb398d | 迁移编号冲突修复（5 个 commit） |
| 70a87d36 / 3b28ccc0 / f44191bb / de69f857 | credential_monitor schema 兼容修正 |
| 9176eb67 / fecbe5cf / f9f7cc61 | credential-state recover_at 修复 + revert + 修复 |
| 6c04e4bb | feat(cache/prefix): prompt-prefix stabilization |
| 9d7d71d9 | chore(submodule): bump llm-gateway-go |
| 70b8e2bb | chore: update deploy/sql/02-seed.sql from production DB |

**风险点**:
- **迁移编号冲突 5 个 commit** → 强烈建议运行 `migrate version` 全量核对实际生效序列。
- `70b8e2bb` 从生产库抽 seed.sql 落仓 → 暗示手工同步流程，可能含敏感数据。
- `9176eb67` → `f9f7cc61` (revert) → `fecbe5cf`（重做）→ 标准 "上 revert 修" 模式，需审计重做后的版本语义一致性。

---

## 9. Telemetry / Audit / Logging

| 提交 | 摘要 |
|---|---|
| ef91bdae | fix(telemetry): client_request_id 接入 UPDATE |
| 0c75b62a | fix(telemetry): client_request_id 走 upsertRequestLogFallback |
| b12cfc51 | fix(telemetry): client_request_id 走 INSERT/UPDATE |
| bca04b89 | fix(telemetry): server-generated request_id + client_request_id 列 |
| 6b2ba0e2 | fix(telemetry): unblock request_logs upsert conflict |
| e7d4deb6 | fix(telemetry): qualify client_request_id in upsert |
| 0ffa7466 | fix(telemetry): disambiguate request_logs update row match |
| c4e1ae47 | fix(telemetry): qualify request_logs update predicate |
| a059c265 | fix(telemetry): 移除 target alias from update |
| b35bb750 / 7260709 | fix(telemetry): unblock failure-path request_log updates |
| 48b8ecc3 / de90c553 | fix(audit): 审计发现的 3 个问题（×2） |
| a921709c | fix(audit): Handler 误用 Web 框架 |
| 245fb3e7 | fix(telemetry): 记录 per-attempt latency + 真 request_id |

**风险点**:
- telemetry upsert 一周内被修复 **10 次**，从 `bca04b89` 到 `b35bb750` 形成完整的 INSERT/UPDATE/upsert 三路径修复 → 暗示 telemetry 是当前**生产最大单点故障**。
- `48b8ecc3 / de90c553` 同一标题 commit 出现 2 次 → 可能存在重复提交未清理。

---

## 10. V4 Pipeline / Governance / Analysis / Publish

| 提交 | 摘要 |
|---|---|
| 1aa2c177 | feat(v4): PR-V4-10 publish + flusher — request.completed → analysis_events → intent_aggregates |
| a37c932e | feat(v4): PR-V4-09 async analysis loop + Suspend → approval_queue |
| 98f3504e | feat(v4): PR-V4-08 approval adapter + clean pg_poll |
| 21bbf1fe | feat(governance+interception): V4 治理层 + 请求拦截引擎 |
| 542d3a3b | feat(analysis): V4 异步分析层 + intent 资产沉淀 |
| 4cbcd5cb | chore(admin+gateway): bandit 拆除 + V4 pipeline 接入 |
| b4e96360 | feat(pipeline+domain): request_envelope + pipeline stage |
| 5ead858d | docs(security): V4 security/audit 报告 |
| afe5cc6f | feat(pipeline): 集成 Authentication Hook |
| 3c57ec00 | docs(hook): Hook 集成重构规划（方案 B） |

**风险点**:
- V4 PR-V4-08 → 09 → 10 三天连续交付 → 涉及异步链路 + DB poll + flusher，需要专门的并发与一致性审计。

---

## 11. Compliance / Output Compliance / Prompt Injection / Session

| 提交 | 摘要 |
|---|---|
| b88b5145 | feat(outputcompliance): 输出合规检测引擎 |
| 522e0c2c | feat(promptinjection): 多层提示词注入检测引擎 |
| c0d11178 / e81c2aca | (DB 架构，见 §8) |
| 26c10399 | feat(web): 会话分析 Dashboard UI |
| ef1612f8 | feat(web): 提示词注入管理 UI |
| e83cfccd | feat(admin): 会话分析 Dashboard API |
| ac8437a3 | feat(admin): 提示词注入管理 API |
| 832053bb | feat(sessionsummary): AI 驱动会话总结 |
| 7576c021 | fix(sessions): unified session management |
| 18cc8105 | feat(db): Session 聚合视图 |
| 35dbaf66 | feat(compliance): 合规报告模板引擎 |
| c6ee414b | feat(siem): SIEM 配置 schema + 验证 |
| 914baeb9 | feat(observability/siem): CEF formatter + FileSink |
| f1c54133 / 905c1476 | feat(web): session alias 预览 |
| f12b5517 | feat(settings): hot-reload session id body aliases |
| 91edb69a | feat(web): tag editor for session alias |
| a1edfb00 / d85acdd8 | refactor(admin): 移 memora 到 memory 包 |

---

## 12. Web / Dashboard / Admin UI

| 提交 | 摘要 |
|---|---|
| 26c10399 / ef1612f8 | 会话分析 + 提示词注入 UI |
| 846e1aba / 938d3df8 / 503a3475 / 215674b3 | probe-health dashboard |
| 8562de6b / 087011f3 | probe-health bug fixes |
| f1b31137 / 26e5056d / aa4696d0 | credentials monitor / sankey |
| f4bf8f9c / 7e89c5de / 1261e8ea / f74e96e7 / 101874fc / a6542b71 / afd5e350 / ba2c99d3 / e37f20a4 | FpSlotVisualizer + 凭据监控 |
| 1c64829c | feat(web): 未登录首页改版 — SI-LLM-Gateway 品牌 |
| f81fa5dc | feat(credentials-monitor): 4-tab 详情 + 6 模型字段 |
| a2d9ff87 | fix(examples): build ignore tag |
| 5993c24e | refactor(admin): AgentService + tests |

**风险点**:
- `503a3475` dark theme 适配 + `938d3df8` UI 紧凑 → UX 改动密集，可能引发回归。

---

## 13. Refactor (R1.13 域切流)

| 提交 | 摘要 |
|---|---|
| 67340e57 | refactor(llmgw): R1.13 cutover — 14 个 legacy 包 → _to-be-deprecated |
| c5d62919 | chore(r1.13): 删除 5 个已 deprecated 包 + 更新 manifest |
| 6162efc8 / c13167b9 / 4211831f / b8330f10 | r1.13: memora / routing / credentialstate → domains/* |
| 38cffffe | feat(wip): R1.13 cutover plan |
| 93be3d49 / f84cb6f1 | refactor(llmgw): 旧路径清理 |
| c647221c / e889a6d5 | feat(r112): R1.12 v2 Pipeline 接入 cmd/gateway（feature flag OFF） |
| ae5b5cb9 / c054779f / 865facb9 / 54bc63d7 / 51dc4b60 / 06186075 | refactor: Phase 0–4 域迁移 |
| abf0aa31 / 7c43019f | refactor: R1.6 compressor + Phase 3.4 指南 |
| 6179acf3 / 3e934943 / 2643cf91 | docs(llmgw): architecture / Phase 1.5 / R1.12 本地测试 |
| 67340e57 | R1.13 cutover 关键 commit |

**风险点**:
- 14 个 legacy 包迁到 `_to-be-deprecated` 但仍保留 → 是否真正无引用需 `grep -r "_to-be-deprecated" --include="*.go"` 验证。
- `0e4a5dbf` refactor(gateway): isolate legacy memora wiring → 暗示本次重构**仍保留了 legacy 接线**。

---

## 14. Deploy / Scripts / Infra / Version

| 提交 | 摘要 |
|---|---|
| 75842cfa | feat(scripts): sync 184→local handles port 25022 + pg_dump 15.18 \restrict |
| 98ee0001 | feat(deploy): rule 22 标准化 — deploy/*.sh + doctor.sh + .env.example + LOCAL_CONFIG |
| db556316 | fix(dockerfile): registry.internal.example.com → registry.internal.example.com |
| feae6f38 | feat(scripts): 184→local DB sync + one-click deploy/verify |
| a921709c | fix(scripts): deploy to /opt/llm-gateway-go/llm-gateway |
| 1c481705 | fix(scripts): sanitize deploy-to-71.sh — 替换硬编码 secret |
| 53922204 | infra(llmgw): R1.12 本地测试环境（docker-compose + 4 scripts） |
| 642ab4b2 / 225bfc12 | chore(gitignore): ignore build artifacts |
| (多次) | chore(version): bump 2.0.5 → 2.0.6 / r1.13-done |
| 2475bc77 | fix(docker+scan): scrub Dockerfile registry |

**风险点**:
- `1c481705` 替换硬编码 secret → 暗示之前存在**已落仓的 secret** → 必须做 `git log -p` 全量扫描确认是否还有残留。
- `feae6f38` 184→local sync 走端口 25022 + pg_dump 15.18 `\restrict` → 需要审计是否对生产 DB 安全。
- `98ee0001` rule 22 标准化 → 期望产生 `LOCAL_CONFIG.md` / `doctor.sh` 是否真的全部就位。

---

## 15. Cache / Compressor / Memory / SpecBoost / API Hub

| 提交 | 摘要 |
|---|---|
| 885167bc | feat(apihub): API Hub 资产中心骨架 + DB migration 047/048 |
| cadea044 | feat(apihub): PGStore + main.go 启动 AssetWatcher |
| a951f19f | fix(apihub): PGSyncer schema 列名 |
| fcb9c01e | fix(apihub): version 列 NOT NULL 时默认 '0.0.0' |
| e761be35 | feat(armor+apihub): B1-1 migration + 主干 A watcher 草稿 |
| fdc9accf | feat(cache/semantic): 语义缓存接口 + 内存实现（C2-1） |
| e4706e12 | feat(compressor): 智能滑动窗口 + 会话缓存压缩位置追踪（v5） |
| bca4463e | feat(specboost): SpecBoost PoC — LLM 工具描述增强 |
| ae198e61 | feat(specboost/source): 工具调用数据源接口 + 内存实现 |
| 26b134e3 | docs(a2a): A2A 协议调研 |
| 347f374e | docs(armor): SDP Presidio 集成调研 |
| 0e4a5dbf | refactor(gateway): isolate legacy memora wiring |

---

## 16. Background / Observability / Metrics

| 提交 | 摘要 |
|---|---|
| 10d5e30f / 95666fcf / 06e7260b / 4444cbc6 / 3c2ecd6f / 960f65aa / e1a26984 / d3d95b3d / 43eb4a03 / ce41d371 / b3211845 / 5941428c | observability/metrics 一波（probe redis cache、suspicious exit、partition archive） |
| 8fcdee74 | docs(admin): partition archive 部署与运维 |
| f43586e0 / e25eb2ba | BanditFlusher + async flush |
| 207badf0 / 22613c1f | executors tests 配套 |

---

## 17. WIP / 杂项

| 提交 | 摘要 |
|---|---|
| fe093df0 | feat(wip): 2026-06-29 WIP batch — probe retry + response interceptor + probe health detail |
| 38cffffe | feat(wip): remaining WIP — probeutil + response interceptor chain + R1.13 cutover plan |
| 1087953e | docs(final): 🎊 项目最终完成报告 |
| 8cadb680 / 4f15ea07 / 4c3aef2d / c1490555 / 6cc14cd4 / ab167ad6 | docs(progress): 各类里程碑 |
| 39740fe8 | docs: 完整项目 changelog |
| f9f7cc61 | Revert "fix(credential-state): persist unavailable_recover_at + revive broken_confirmed probes" |

**风险点**:
- WIP 直接进 main → 严重违反 rule 03 deploy safety，需清理或拆分。
- 多个 emoji-only docs(progress) 提交（`8cadb680` `4f15ea07` `1087953e`）→ 信息密度低。

---

## 18. 总结：高风险模块 Top-5

1. **Probe / Probe Health** — 4 次连续 DB 视图修复 + WIP 入主 + 前端 401 → 最大风险点。
2. **Telemetry** — upsert 路径一周修复 10 次 → 建议作为故障演练专项。
3. **Auth (rule 20)** — 4 commit 链路 → 单独审计。
4. **Gateway-v2 / API** — 一周新增 4 个 endpoint + 审计修复 → 契约稳定性专项。
5. **Routing / Auto-route** — 翻默认 ON + 多 sticky 改造 + 8 维评分 → 热路径压测专项。

---

> **下一步**: 进入逐模块审计，详见 `docs/audit/2026-06-30-weekly-audit-report.md`。