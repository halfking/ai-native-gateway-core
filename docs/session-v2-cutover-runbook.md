# Session V2 切换验证 Runbook

**分支**: `feat/session-turns-v2`  
**目标**: 验证 `session_turns` + `session_bodies` (V2) 可以替代 `request_logs` + `request_logs_bodies` 作为会话正文的唯一真相源，消除套娃存储。  
**风险等级**: 高 — 影响会话详情页、分析、审计的读取路径，且涉及历史数据迁移。必须在 staging 充分验证后才可合并到 main。

---

## 前置条件

1. **Schema 已就绪**: migrations 430/513/525/526 已在目标库执行，`session_turns`/`session_bodies`/`sessions` 表存在且有分区。
2. **双写代码已部署**: gateway 已包含 `SessionPersistHook`（受 `sessions_v2.enabled`/`shadow_write`/`rollout_percent` 门控）。
3. **工具已编译**: 在 staging 机器上编译 `backfill_sessions_v2_v2`、`backfill_session_bodies`、`validate_sessions_v2`。

---

## 阶段 1: 历史数据回填（staging）

### 1.1 选择测试会话

在 staging 库选 2-3 个代表性会话（单轮/多轮/带工具调用/长上下文）用于回填与校验：

```sql
-- 示例：找最近的 3 个多轮会话
SELECT gw_session_id, tenant_id, COUNT(*) AS turns
FROM request_logs_with_current_month
WHERE gw_session_id IS NOT NULL
  AND ts > NOW() - INTERVAL '7 days'
GROUP BY gw_session_id, tenant_id
HAVING COUNT(*) > 3
ORDER BY MAX(ts) DESC
LIMIT 3;
```

记录 `session_id` 与 `tenant_id` 备用。

### 1.2 回填 session_turns 元数据

对每个测试会话，先回填 `session_turns`（元数据）：

```bash
export DB="postgresql://user:pass@staging-db:5432/llm_gateway"
export TENANT="tenant_xxx"
export SESSION="gw_abc123..."

# 先 dry-run 看计数
go run ./cmd/tools/backfill_sessions_v2_v2 \
  --dsn="$DB" --tenant="$TENANT" --session="$SESSION" \
  --batch=100 --dry-run=true

# 确认无误后执行（SQL 函数会实际写入，dry-run 只是 CLI 日志标记）
go run ./cmd/tools/backfill_sessions_v2_v2 \
  --dsn="$DB" --tenant="$TENANT" --session="$SESSION" \
  --batch=100 --dry-run=false
```

**期望**: 日志显示 `inserted=N`（N = 该会话轮数）。

### 1.3 回填 session_bodies 正文

对同一会话，回填 `session_bodies`（每轮增量正文）：

```bash
# dry-run 先看派生的 delta 计数
go run ./cmd/tools/backfill_session_bodies \
  --dsn="$DB" --tenant="$TENANT" --session="$SESSION" \
  --dry-run=true

# 确认无误后写入
go run ./cmd/tools/backfill_session_bodies \
  --dsn="$DB" --tenant="$TENANT" --session="$SESSION" \
  --dry-run=false
```

**期望**: 日志显示 `bodies=N`（N <= 轮数，空轮跳过）。

### 1.4 目视检查

```sql
-- 检查 session_turns 元数据
SELECT turn_no, request_id, model, prompt_tokens, completion_tokens, status_code
FROM session_turns_with_current_month
WHERE session_id = 'gw_abc123...' AND tenant_id = 'tenant_xxx'
ORDER BY turn_no;

-- 检查 session_bodies 正文（request_delta 应该只含本轮新增的 user 消息）
SELECT turn_no, request_id,
       jsonb_array_length(request_delta) AS req_delta_len,
       jsonb_array_length(response_delta) AS resp_delta_len
FROM session_bodies
WHERE session_id = 'gw_abc123...' AND tenant_id = 'tenant_xxx'
ORDER BY turn_no;
```

**期望**: 每轮 `request_delta` 长度 = 1~2（通常就是一条 user 消息）；`response_delta` 长度 = 1（assistant 回复）。

---

## 阶段 2: 双读一致性校验

### 2.1 运行 validate_sessions_v2

对回填的会话，运行对比校验：

```bash
go run ./cmd/tools/validate_sessions_v2 \
  --dsn="$DB" --tenant="$TENANT" --session="$SESSION" \
  --mode=reconstruct
```

**期望输出**:
- `Reconstruction: ok` — V2 deltas 累积重建的消息列表与 V1 request_logs body 一致。
- `Parity: ok` — 元数据（token/cost/latency/status）相同。

**失败时**: 检查 `[error]` 或 `[warning]` 行，确认是否有 delta 派生逻辑错误、submit_mode 不兼容、或压缩场景需特殊处理。

### 2.2 前端详情页检查

在 staging 前端访问 `/admin/request-detail?request_id=xxx`（选回填会话的某轮 request_id），切到「会话轮次」tab：

1. 左栏轮次列表是否显示每轮用户指令摘要。
2. 点击某轮，右侧是否只显示该轮的消息（不是全量累积）。
3. 检查 Network 面板，确认调用 `GET /api/admin/sessions/{id}/turns/bodies` 成功返回 V2 正文。

**期望**: V2 正文优先生效；若该会话还没回填，自动回退到 request_logs 正文派生（前端兼容模式）。

---

## 阶段 3: 开启双写（小流量）

### 3.1 配置双写开关

在 staging 的网关配置（settings 表或 YAML）中开启：

```yaml
sessions_v2:
  enabled: true               # 主开关
  shadow_write: true          # 双写 session_turns + session_bodies
  rollout_percent: 5          # 只对 5% 的新会话双写
```

重启 staging gateway 或等热加载生效。

### 3.2 观察双写行为

```sql
-- 查看最近 5 分钟是否有新 V2 写入
SELECT session_id, turn_no, request_id, ts
FROM session_turns_hot
WHERE ts > NOW() - INTERVAL '5 minutes'
ORDER BY ts DESC
LIMIT 10;

SELECT session_id, turn_no, request_id, ts
FROM session_bodies
WHERE ts > NOW() - INTERVAL '5 minutes'
ORDER BY ts DESC
LIMIT 10;
```

**期望**: 约 5% 的新会话在 `session_turns_hot` 和 `session_bodies` 中有记录，且 `session_bodies.request_delta` 只含本轮新增消息。

### 3.3 双写一致性检查

选一个双写的新会话，再跑 `validate_sessions_v2`：

```bash
# 找一个双写的 session_id（从 session_turns_hot 查）
export NEW_SESSION="gw_xyz..."

go run ./cmd/tools/validate_sessions_v2 \
  --dsn="$DB" --tenant="$TENANT" --session="$NEW_SESSION" \
  --mode=reconstruct
```

**期望**: 新会话的 V2 与 V1 完全一致（因为是实时双写，不是回填）。

---

## 阶段 4: 扩大双写流量（staging 验证通过后）

逐步提高 `rollout_percent`：5% → 20% → 50% → 100%，每次观察：

1. 双写错误率（监控 `shadow_write_failed` 指标）。
2. 延迟影响（写 V2 增加的 p99 延迟）。
3. 存储增量（`session_bodies` 增长速度，应显著低于 `request_logs_bodies`）。

---

## 阶段 5: 切换读路径（staging only，main 尚未合并）

当双写达到 100% 且稳定运行 24h+ 后，在 **staging** 环境：

1. 修改 `admin/unified_detail.go` 的读优先级：`session_turns` → `request_logs`（当前分支已实现 V2 优先）。
2. 重启 staging admin 服务。
3. 抽样检查详情页，确认 V2 读路径正常。

**此步骤仍在 staging 分支验证，不影响生产 main**。

---

## 阶段 6: 停写 request_logs 正文（需显式授权，高风险）

**⚠️ 破坏性操作，仅在所有验证通过 + 存量回填完成 + 双写稳定后执行**。

1. 修改写路径，停止向 `request_logs_bodies` 写入 `request_body`/`response_body`（保留 `outbound_body` 用于压缩审计）。
2. `request_logs` 表降级为请求级指标记录（状态/延迟/模型/token/成本/路由/错误），不再持久化正文。
3. 定期清理 `request_logs_bodies` 历史分区（保留最近 30 天用于应急回退）。

**此步需在生产环境再次完整验证，并准备回滚预案**。

---

## 回滚预案

| 阶段 | 回滚操作 |
|------|---------|
| 回填 | 删除回填的 `session_turns` 与 `session_bodies` 行（按 `source_kind='backfill'` 或时间戳） |
| 双写 | 设置 `sessions_v2.shadow_write=false`，V2 表保留但停止新写入 |
| 切换读路径 | 改回 `request_logs` 优先，重启 admin 服务 |
| 停写 V1 | **不可逆** — 需从备份恢复 `request_logs_bodies` |

---

## 成功标准（staging 通过后再提 PR 合入 main）

- [x] 3 个测试会话回填成功，`validate_sessions_v2` 全部 `ok`。
- [x] 双写开启后，新会话 V2 与 V1 一致性 100%。
- [x] 前端详情页「会话轮次」tab 读取 V2 正文正常。
- [x] 双写对延迟影响 < 10ms p99。
- [x] 存储增量：V2 正文占用 < V1 的 40%（消除套娃）。
- [ ] 所有验证步骤在 staging 完成后，分支提 PR → code review → 合并到 main。

---

**文档维护**: 本 runbook 随分支 `feat/session-turns-v2` 演进，合并到 main 后归档到 `docs/会话优化v3/`。
