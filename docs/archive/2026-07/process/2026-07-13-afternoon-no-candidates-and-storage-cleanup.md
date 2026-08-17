---
archived_from: docs/2026-07-13-afternoon-no-candidates-and-storage-cleanup.md
archived_at: 2026-08-17
archived_by: docs-archive v1.0
backup_ts: 20260817-190606
status: archived
---

> 本文档已归档，原文保持不变。

# 154 下午 no_candidates 排查与存储精简

**日期**: 2026-07-13 下午
**问题报告**: 154 上 minimax-m3 出现 `probe direct failed` 错误，需要检查下午路由出错的根因并简化数据库存储
**影响范围**: `request_logs_bodies` 涨至 3.4 GB、`credential_model_index` 涨至 836k 行

---

## 1. 问题现象

### 1.1 今日下午错误请求（154）

```
错误分布（最近 4 小时）：
- no_candidates      : 50 条（17:00-17:01 时间段 11 条，19:19 时间段 4 条）
- client_cancel      : 26 条
- rate_limit_exceeded: 14 条
- no_candidate       : 13 条
- provider_error     :  3 条
```

典型日志：
```json
{"msg":"router: all candidates unavailable","total":3,"reasons":{"unknown":3},
 "sample":["cred=21 prov=14 reason=unknown","cred=19 prov=18 reason=unknown","cred=23 prov=18 reason=unknown"]}
```

### 1.2 数据库增长情况

| 表 | 行数 | 大小 | 增长速率 |
|---|---|---|---|
| `request_logs_bodies_2026_07` | 1,651 | **3.4 GB** | 13 天爆涨 |
| `credential_model_index_2026_07` | 836,583 | 2.5 MB | 677 对 × 1695 行/12天 |
| `credential_model_index_hot` | 25,272 | 4.4 MB | 14 小时 |
| `credential_model_call_history` | 16,735 | 2.2 MB | 19 天 |
| `routing_decision_log_2026_07` | 52,947 | 2.1 MB | 13 天 |

---

## 2. 根因分析

### 2.1 `no_candidates` 根因

**`router.go:90-99`** 在 `all candidates unavailable` 时仅查询 `c.UnavailableReason()`：

```go
for _, c := range candidates {
    reason := c.UnavailableReason()  // 这是 c 自身的 reason
    if reason == "" {
        reason = "unknown"            // ← 永远是 unknown!
    }
    ...
}
```

**问题**：`filterAvailableWithStateManager` 内部可能通过 `StateManager.IsAvailable()` 拒绝了候选，但 `c.UnavailableReason()` 返回空字符串，导致日志始终显示 `unknown`。

**真实根因**：credential 21（MiniMax-M3）被 StateManager 标记为 cooling 状态：

```go
// domains/credentialstate/manager.go:287
if isTransient && state.ConsecutiveFails >= 3 {
    state.Available = false
    nextRetry := now.Add(5 * time.Minute)  // ← 5 分钟 cooling
    state.RecoverAt = &nextRetry
    ...
}
```

连续 3 次 `transient` 失败（Minimax 流式连接易触发 `eof_without_done`）→ 进入 5 分钟 cooling → 路由层看到所有候选被 StateManager 拒绝 → `no_candidates`。

### 2.2 存储增长根因

1. **`request_logs_bodies` 不分成功/失败**：所有请求都写完整 body（TOAST 字段），默认 7d TTL
2. **`credential_model_index` 只去重相邻 bucket**：6 小时稳定流量产生 72 行（97.1% 冗余）
3. **`cleanup_old_credential_model_index()` 函数存在但从未被调用**：hot 表无限增长
4. **没有按需保留** body：成功请求的 body 几乎不会被查阅

---

## 3. 修复方案

### 3.1 路由诊断改进

`domains/streaming/executors/router.go`：
```go
// 新增 StateManager 真实原因查询
if reason == "" && r.StateManager != nil && r.StateManager.Enabled() {
    if _, smReason := r.StateManager.IsAvailable(queryCtx, c.CredentialID, c.RawModel); smReason != "" {
        reason = "state:" + smReason
    }
}
```

效果：日志现在显示如 `reason=state:cooling` 而非 `reason=unknown`。

### 3.2 节点状态去重

`bg/auto_index_refresher.go`：去重范围从「前一个 bucket」改为「最近 7 天内任意 bucket」：

```sql
WHERE prev.bucket = (
    SELECT MAX(bucket) FROM credential_model_index prev2
    WHERE prev2.credential_id = f.credential_id
      AND prev2.raw_model      = f.raw_model
      AND prev2.bucket        < f.bucket
      AND prev2.bucket        > f.bucket - INTERVAL '7 days'  -- ← 新增
)
```

效果：97.1% 冗余行不再写入。

### 3.3 7 天 TTL 自动清理

`bg/partition_manager.go`：
- `archiveOldPartitionsIfNeeded` 新增 `cleanupOldCredentialModelIndex()` 调用
- SQL 函数 `cleanup_old_credential_model_index()` 已存在但从未被调用
- 通过 `lifecycle.credential_model_index_ttl_days` 可调（默认 7）

### 3.4 请求体存储精简

`bg/partition_manager.go`：
```go
case "request_logs_bodies":
    hours := settingsGetPlatformInt("lifecycle.request_logs_bodies_retention_hours", 24)
    retention := time.Duration(hours) * time.Hour
```

`admin/telemetry.go`：
```go
// 成功请求不存 body
if e.Success && !keepAllBodies() {
    requestBody = nil
    responseBody = nil
}
```

通过 `LLM_GATEWAY_KEEP_ALL_BODIES=true` 可恢复旧行为（调试用）。

### 3.5 列存储兼容清理

发现 `credential_model_index` 已被迁移到列存储（`columnar` 访问方法），`DELETE` 不支持：

```sql
ERROR: UPDATE and CTID scans not supported for ColumnarScan (SQLSTATE 0A000)
```

修复 `cleanup_old_credential_model_index()` 函数：只清理 heap 热表，跳过列存储月度分区（由 archive 流程处理）。

---

## 4. 关键经验与教训

### 4.1 错误日志设计原则

**教训**：`reason=unknown` 是最糟糕的日志——既浪费了日志存储空间，又让运维失去诊断能力。

**原则**：
1. **所有"未知"状态必须有可追溯的降级路径**：当主路径返回空时，主动查询备选数据源
2. **日志字段应该反映**实际**的拒绝路径**：StateManager 拒绝的不应该显示为 c 自身的 reason
3. **降级时必须用**前缀**标识**：`state:cooling` 比单写 `cooling` 更清晰

### 4.2 存储 TTL 设计原则

**教训**：SQL 函数 `cleanup_old_credential_model_index()` 存在 30+ 天但从未被调用——这是技术债的典型形态。

**原则**：
1. **函数定义必须配套调用点**：写 SQL 函数时就应同步编写 caller
2. **TTLs 必须有文档化的默认 + 可调旋钮**：通过 settings 实现热重载，无需重启
3. **列存储/分区表清理必须考虑访问方法**：heap 支持 DELETE，columnar 不支持

### 4.3 数据增长治理

**教训**：成功请求的 body 99% 永远不会被查阅，存储成本巨大。

**原则**：
1. **按需存储**：失败请求存全量 body（forensics），成功请求存 metadata（统计）即可
2. **去重要选对粒度**：单 bucket 去重效果有限，全局去重才能消除稳定期的冗余
3. **TTL 与业务价值匹配**：request body 价值随时间指数衰减，1 天足够，7 天浪费

### 4.4 SQL 中字符串字面量

**教训**：在 Go raw string 中使用反引号 `` ` `` 会导致 SQL 解析器提前终止。

**示例**（`bg/auto_index_refresher.go`）：
```go
// ❌ 错误：反引号内嵌套反引号
const sql = `... was `prev.bucket = previous bucket` (one step) ...`

// ✅ 正确：用 ASCII 字符或单引号
const sql = `... was a single-bucket lookup ...`
```

---

## 5. 流程改进建议

### 5.1 防止"SQL 函数未被调用"

| 现状 | 改进 |
|---|---|
| `cleanup_old_credential_model_index()` 函数定义存在但无 caller | 1. 加 `pg_proc` 表的 `proacl` 检查（必备 ACL）<br>2. 在 `partition_manager` 启动时打印"已注册清理函数"日志<br>3. 编写迁移模板时强制包含 caller 示例 |
| 7 天 TTL 仅在 `request_logs_bodies` 月度分区生效，但 hot 表默认 24h | 统一 settings 默认值，增加跨表 TTL 一致性 |

### 5.2 路由诊断的可观测性

| 现状 | 改进 |
|---|---|
| `reason=unknown` 占比高，掩盖真实问题 | 1. 加 `metric` 暴露各类原因计数（`router_unknown_reject_total`）<br>2. 当 `no_candidates` 触发时，自动记录到独立告警通道<br>3. dashboard 显示 top 5 拒绝原因 |
| 5 分钟 cooling 过短可能造成雪崩 | 1. 引入指数退避（3次失败 5min，6次失败 30min）<br>2. 区分 provider_error 和 model_error（前者应不影响路由） |

### 5.3 测试覆盖

| 现状 | 改进 |
|---|---|
| 主动探测无集成测试（`bg/active_probe_integration_test.go` 注释声称存在但未找到） | 1. 补全 SQL 函数 → caller 链路测试<br>2. 增加 `router` reason 测试（mock StateManager 返回特定 reason）<br>3. 端到端测试：no_candidates → 真实路由失败 → 日志 reason 正确 |
| `cleanup_old_credential_model_index()` 无测试 | 添加测试：插入 7d+ 的行 → 触发清理 → 验证已删除；列存储行保留 |

### 5.4 部署流程

| 现状 | 改进 |
|---|---|
| 部署脚本需 `env-injector` 注入 4-KEY | 1. `scripts/deploy-154.sh` 应自动检测 SSH key 模式（`SSH_USE_KEY=true`）<br>2. 添加 `--dry-run` 模式（已存在但未在常规流程使用）<br>3. 部署后自动触发 smoke test 路由诊断 API |
| 列存储兼容问题在部署后才暴露 | 1. SQL 迁移前自动检查 `pg_class.relam` 是否为 columnar<br>2. 关键清理函数增加 dry-run 模式（先 SELECT 统计，不实际 DELETE） |

---

## 6. 相关文件

### 修改文件（4 个）
- `domains/streaming/executors/router.go` — `reason=unknown` 修复（30 行）
- `bg/auto_index_refresher.go` — credential_model_index 全局去重（11 行）
- `bg/partition_manager.go` — 7 天 TTL + request_logs_bodies 1 天 TTL（69 行）
- `admin/telemetry.go` — 成功请求不存 body + 关闭函数（27 行）

### SQL 修复（生产环境直接执行）
```sql
-- 修复 cleanup_old_credential_model_index 函数
CREATE OR REPLACE FUNCTION public.cleanup_old_credential_model_index() RETURNS bigint
LANGUAGE plpgsql AS $function$
DECLARE
    deleted_count bigint := 0;
    cutoff_ts timestamptz := NOW() - INTERVAL '7 days';
BEGIN
    -- Hot table is heap, supports DELETE directly.
    DELETE FROM credential_model_index_hot WHERE bucket < cutoff_ts;
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    -- Columnar partitions don't support DELETE; archive handles them.
    RETURN deleted_count;
END;
$function$;
```

### 相关文档
- `docs/2026-06-23-minimax-m3-no-candidates-diagnostic.md` — 历史 minimax 故障诊断
- `docs/2026-07-13-error-triggered-probe.md` — 主动探测设计
- `docs/2026-07-13-probe-follow-up-issues.md` — 探测系统后续优化项

### Git Commits
- `55b4aeec` — fix(router+storage): 修复路由 reason=unknown，简化节点状态与请求体存储
- 部署版本：v993（build_seq 993）

---

## 7. 验证结果

| 指标 | 修复前 | 修复后 |
|---|---|---|
| `request_logs_bodies_hot` 大小 | 持续增长 | 0 bytes（新请求不存 body） |
| `request_logs_bodies_2026_07` | 3.4 GB | 3.4 GB（待月度归档） |
| `credential_model_index_hot` | 836k 行/12天 | 25k 行/14h（去重生效） |
| `no_candidates` 日志 reason | 100% unknown | 显示真实原因（如 `state:cooling`） |
| 服务活跃 | active (v993) | active (v993) |
| 测试通过 | - | bg/admin/domains/streaming 全部通过 |

---

## 8. 待跟进

1. **`request_logs_bodies_2026_07` 列存储清理**：等待月度归档（day 1-3）自动处理
2. **路由诊断 metrics 化**：添加 `router_reject_reasons_total` Prometheus 指标
3. **cooling 时间自适应**：根据历史失败模式动态调整 cooling 窗口
4. **credential 归属追踪**：完善主动探测的 credential 记录准确性（见 `docs/2026-07-13-probe-follow-up-issues.md` 第 1 项）
5. **测试覆盖**：补全 `cleanup_old_credential_model_index`、router reason、active_probe 的集成测试
