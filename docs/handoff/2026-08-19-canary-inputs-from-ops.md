# 隔离 Probe-Canary 验证 — 待 ops 提供的输入清单

> 读者：ops / DBA / 平台负责人（负责审批 + 提供隔离基础设施 + 决策发布）
> 配合文档：`docs/handoff/2026-08-19-canary-runbook.md`（执行细节）、`/tmp/handoff-20260819-probe-canary-audit-fix.md`（前置交接）
> 本清单的目的是把"等 ops 决定的事"显式列出来，避免开发在没有授权时擅自往前推。

---

## 1. 隔离基础设施（必填，缺一项 runbook 不启动）

| 项目 | 期望形态 | 隔离要求 | 决策人 |
|------|---------|---------|--------|
| 独立 PostgreSQL DSN | 独立实例或独立 db15（与 245 / 154 / 生产 schema 物理分离） | 不得共享任何 schema；建议新实例 | DBA |
| 独立 Redis | 独立 namespace（如 redis://.../15）或独立实例 | 与生产 redis db 物理隔离 | DBA |
| canary tenant | 字符串标识，与生产 tenant 不重叠 | `URSM_V2_CANARY_TENANTS` 唯一 | 平台 |
| canary credential ID | 新建独立 ID，不复用历史 | `URSM_V2_CANARY_CREDENTIALS` 唯一 | 平台 |
| canary raw model | 与生产 raw model 不重叠（或独立 alias prefix） | `URSM_V2_CANARY_MODELS` 唯一 | 平台 |
| `URSM_V2_REDIS_KEY_PREFIX` | 非默认、以 `:` 结尾（如 `ursmv2-canary-:`） | 防止与生产 key 串扰 | DBA + 平台 |
| `LLMGW_ADMIN_TOKEN` | 隔离环境专属 | 不复用生产 token | 平台 |
| `LLMGW_SELFCHECK_API_KEY` | canary 隔离凭据 | 仅供 direct + pinned gateway 烟囱用 | 平台 |

**验收：ops 需以书面（工单 / 邮件）形式确认上述 8 项全部到位，并指明 DSN/地址/凭据的提供方式（仅本会话内一次性 paste，不入仓）。**

---

## 2. 审批与决策人（每条决策需要可追溯到人）

| 决策点 | 触发条件 | 决策人 | 默认态度 |
|-------|---------|--------|---------|
| 是否启动隔离 canary runbook | runbook §0 全部满足 | ops 负责人 | 否（缺任一前置即不动） |
| 245 预发评估启动 | 隔离 runbook §5.1 全部通过 + 24h 观察通过 | ops 负责人 | 否（245 与生产 PG 共享，仍非完全隔离） |
| 154 生产评估启动 | 245 独立 24h 观察通过 | ops + DBA + 平台联合签字 | 否 |
| 生产 migration 538 执行 | 仅由 ops 在独立授权窗口执行 | DBA（主） + ops（副） | 否（绝不与 canary 同步） |
| 82 条 pseudo-success 只读核验 + 备份 + 精确清理 | 仅在 canary 全链路通过后 | DBA + 平台 | 否（参考 `sql/migrations/operations/2026-08-19-pseudo-success-cleanup.md`） |
| candidate-level URSM coverage/TTL/schema 观测是否纳入排期 | 持续缺口 | 平台 | 视排期 |
| 24h 异常回滚 | 触发 runbook §5.2 任一阈值 | runbook 执行人立即决策 | 是（默认回滚） |

---

## 3. 已知约束（ops 在审批时需要明确接受）

1. **245 与生产 PostgreSQL 共享**：handoff §7、runbook §0 已强调，245 不能替代隔离 canary；ops 批准 245 评估时必须把它当"预发烟囱"而不是"隔离诊断"。
2. **154 完全生产**：任何 154 上的改动都需要 ops + DBA + 平台三方授权；本会话不持有该授权。
3. **生产 migration 538 与清理脚本分离**：本会话只持有只读核验和备份脚本模板，真实执行需要 ops 在生产窗口签发。
4. **`version.json` 当前 build_seq=1621**：不证明 `47482be4f` 已部署；ops 部署任何环境前需要先确认目标实例的 commit hash。

---

## 4. 交付物（runbook 完成后回填）

执行人需在隔离环境完成后回填：

1. `docs/handoff/2026-08-19-canary-<task_id>.md`：单一 task ID 全链路证据（只读 SQL 输出、metric 截图、Redis key 列表）
2. `docs/handoff/2026-08-19-canary-24h-summary.md`：24h 观察窗口通过/未通过结论
3. 是否建议进入 245 / 154 评估（仅建议，不替代 ops 决策）
4. candidate-level URSM coverage/TTL/schema 缺口确认（按 runbook §4 表格如实记录）

---

## 5. 拦截清单（任何一条违反 → 立即停止）

- 使用 245 / 154 作为"隔离 canary"环境
- 在 runbook 通过前评估 245 / 154 的发布状态
- 在没有独立 ops 授权的情况下执行 production migration / cleanup / SSH / systemd 操作
- 把 canary 凭据 / key prefix / SQL 模板拷贝到生产环境
- 在隔离环境之外宣称 "URSM K2 已全量生效"（runbook §4 的观测缺口尚未实现）

---

## 6. 时间线建议（由 ops 决策）

| 阶段 | 建议时长 | 阻塞依赖 |
|------|---------|---------|
| 隔离环境搭建 + 凭据下发 | ops 自定 | §1 全部到位 |
| Runbook §1-§3 执行 | 半天 | 环境就绪 |
| Runbook §5 24h 观察 | 24h | §3 通过 |
| 245 评估启动 | ops 决议 | 隔离观察通过 |
| 154 评估启动 | ops 决议 | 245 独立 24h 通过 |
| 生产 migration 538 / 82 条 cleanup | ops + DBA 联合窗口 | canary 全链路 + 245 通过 |

本清单不替代 ops 内部审批流；它的作用是让审批时有明确、可勾选的输入项和决策人。
