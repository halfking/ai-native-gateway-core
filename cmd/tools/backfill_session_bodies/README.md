# session_bodies 回填工具

**用途**: 从历史 `request_logs` 与 `request_logs_bodies` 推导每轮增量正文（`request_delta` + `response_delta`），写入 `session_bodies`，为全面切换到 Sessions V2 做准备。

---

## 背景

- **问题**: `request_logs_bodies` 的 `request_body` 是累积式全量（套娃）：第 N 轮包含前 N 轮所有消息，导致存储 ≈ O(轮数²)。
- **V2 设计**: `session_bodies` 每轮只存 `request_delta`（本轮新增的用户消息）+ `response_delta`（本轮回复），消除重复，存储 ≈ O(总消息)。
- **回填必要性**: 存量会话在 V2 双写开启前没有 `session_bodies`，导致 V2 读路径无法服务，也无法做双读校验。本工具补齐历史数据。

---

## 工作原理

1. 查询 `request_logs_with_current_month` 按时间升序获取会话的所有轮次（`request_id`, `tenant_id`, `ts`）。
2. LEFT JOIN `request_logs_bodies_with_current_month` 取每轮的 `request_body` (累积全量) 与 `response_body`（本轮回复）。
3. 用纯函数 `DeriveTurnDeltas(fullMsgs, respMsgs)` 推导：
   - `reqDeltas[i]` = `fullMsgs[i]` 减去 `fullMsgs[i-1]`，再过滤只保留 `role=user` 的消息（assistant 回复已在上轮的 `response_delta`）。
   - `respDeltas[i]` = `respMsgs[i]` 直接取用。
4. 将 `(reqDeltas[i], respDeltas[i])` 包装成 `v2.BodiesRecord`，经 `SessionBodiesWriter` 写入 `session_bodies`（ON CONFLICT 幂等）。

---

## 使用方法

### 前置

1. 确保目标库已执行 migration 430/513/526（`session_bodies` 表存在）。
2. 先跑 `backfill_sessions_v2_v2` 回填 `session_turns` 元数据（本工具依赖 `request_logs` 的 turn_no 排序）。

### 编译

```bash
go build -o backfill_session_bodies ./cmd/tools/backfill_session_bodies
```

### 单会话回填（dry-run）

```bash
export DB="postgresql://user:pass@host:5432/llm_gateway"
export TENANT="tenant_xxx"
export SESSION="gw_abc123..."

./backfill_session_bodies \
  --dsn="$DB" \
  --tenant="$TENANT" \
  --session="$SESSION" \
  --dry-run=true
```

**输出示例**:
```
[dry-run] turn 1 request_id=req_001 req_delta=1 resp_delta=1
[dry-run] turn 2 request_id=req_002 req_delta=1 resp_delta=1
[dry-run] turn 3 request_id=req_003 req_delta=1 resp_delta=1
backfill session_bodies: session=gw_abc123 tenant=tenant_xxx turns=3 bodies=3 dryRun=true
```

### 实际写入

```bash
./backfill_session_bodies \
  --dsn="$DB" \
  --tenant="$TENANT" \
  --session="$SESSION" \
  --dry-run=false
```

### 批量回填（生产）

对存量会话做批量回填，建议写个脚本分批执行：

```bash
# 示例：取最近 30 天的所有会话
psql "$DB" -tA -c "
  SELECT DISTINCT gw_session_id, tenant_id
  FROM request_logs_with_current_month
  WHERE gw_session_id IS NOT NULL
    AND ts > NOW() - INTERVAL '30 days'
" | while IFS='|' read -r sid tid; do
  echo "Backfilling session=$sid tenant=$tid"
  ./backfill_session_bodies \
    --dsn="$DB" --tenant="$tid" --session="$sid" --dry-run=false \
    || echo "FAILED: $sid"
  sleep 0.1  # 限速
done
```

---

## 验证

回填后用 `validate_sessions_v2` 检查 V2 与 V1 一致性：

```bash
go run ./cmd/tools/validate_sessions_v2 \
  --dsn="$DB" --tenant="$TENANT" --session="$SESSION" \
  --mode=reconstruct
```

**期望输出**: `Reconstruction: ok` — V2 deltas 累积重建的消息列表与 V1 `request_body` 一致。

---

## 已知限制

1. **压缩场景**: 若 `request_logs_bodies.outbound_body` 是压缩后的（非全量），本工具无法从中推导完整 delta。需等压缩回填工具就绪。
2. **工具调用**: `tool_calls`/`tool_call_id` 按纯 JSON 保留，但 `Content` 提取为纯文本（多模态内容变 compact JSON）。
3. **幂等性**: `SessionBodiesWriter` 内部有 ON CONFLICT DO UPDATE，重复跑同一会话不会报错，但可能覆盖已有记录。

---

## 故障排查

| 错误 | 原因 | 解决 |
|------|------|------|
| `query turns: relation "request_logs_with_current_month" does not exist` | schema 未就绪 | 执行 migration 340/342 |
| `parse request: unexpected end of JSON` | `request_body` 为 `NULL` 或格式错误 | 检查该 `request_id` 的原始数据 |
| `write bodies turn N: duplicate key` | `session_bodies` 已有此轮 | 正常（幂等），或用 `DELETE FROM session_bodies WHERE session_id=...` 清空后重试 |
| `req_delta=0` 所有轮 | 所有轮的 `request_body` 完全相同 | 检查是否是单轮会话或 body 未更新 |

---

## 单元测试

核心 delta 推导逻辑在 `derive_test.go` 中有单测覆盖：

```bash
go test ./cmd/tools/backfill_session_bodies/...
```

---

**维护**: 本工具随分支 `feat/session-turns-v2` 演进，合并到 main 后归档到 `cmd/tools/`。
