# 缓存命中率基线诊断 - 执行脚本

> **本目录提供完全被动的诊断脚本**（无需修改代码），用于采集 LLM 端 prompt cache 命中率基线。

## 快速开始

### 1. 连接 184 生产数据库

```bash
# 通过 env-injector 注入凭据
ACC_TOOLKIT_ROOT=/Users/xutaohuang/workspace/acc-toolkit \
  bash /Users/xutaohuang/.agents/skills/env-injector/scripts/env-injector.sh \
  inject huoshan-core-184

# 验证
echo "HOST_184=$HOST_184"
```

### 2. 跑全部 SQL（依次执行）

```bash
export PGPASSWORD="$PG_LLM_GATEWAY_PASS"

for sql in scripts/cache-baseline/*.sql; do
  echo "=== Running $sql ==="
  psql -h "$PG_LLM_GATEWAY_HOST" \
    -U "$PG_LLM_GATEWAY_USER" \
    -d llm_gateway \
    -f "$sql"
done
```

### 3. 拉 Admin API 数据

```bash
curl -s -H "Authorization: Bearer $LLM_GATEWAY_ADMIN_API_KEY" \
  "http://$HOST_184:8781/api/admin/usage/cache-economics?days=7" | tee /tmp/cache-7d.json
```

### 4. 验证 C2 prefix 稳定化

```bash
curl -i -X POST \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet-20241022",
    "messages": [
      {"role": "user", "content": "tail"},
      {"role": "system", "content": "system prompt"}
    ]
  }' \
  "http://$HOST_184:8781/v1/chat/completions" | grep -i 'X-Gw-Prefix'
```

### 5. 生成诊断报告

将 4 步的输出汇总到 `docs/cache-baseline-report-YYYY-MM-DD.md`。

## SQL 脚本清单

| 编号 | 文件 | 诊断目标 |
|---|---|---|
| 01 | `01-global-7d.sql` | 全局 7 天命中率基线 |
| 02 | `02-by-model.sql` | 按模型分组命中率 |
| 03 | `03-by-provider.sql` | 按 provider 分组命中率 |
| 04 | `04-by-session-length.sql` | 按会话长度命中率 |
| 05 | `05-by-prompt-length.sql` | 按 prompt 长度命中率 |
| 06 | `06-affinity-vs-cache.sql` | 粘性 vs 缓存（核心证据） |
| 07 | `07-rotation-vs-cache.sql` | 凭据轮换 vs 缓存 |

## 关键诊断 SQL

**最重要的查询是 `06-affinity-vs-cache.sql`**：
- 它直接回答审计报告中的核心问题："凭据稳定性是否影响缓存命中率？"
- 如果 `affinity_hit` 命中率显著高于 `affinity_miss`，证明原方案方向正确
- 如果两者相近，确认审计结论：缓存主要依赖前缀稳定性

## 数据保留

- 默认 90 天（`log.request_retention_days`）
- 如需查询更长时间窗，需先调整此设置或归档恢复

## 详细方案

完整诊断方案见：`docs/2026-07-09-cache-diagnosis-plan.md`