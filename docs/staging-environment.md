# Staging 环境配置文档

**更新时间**: 2026-08-29  
**环境**: 154 Staging

---

## 服务器信息

### SSH 连接
```bash
ssh -p 25022 root@47.97.111.154
```

### 数据库连接
```bash
# DSN (从网关进程环境变量获取)
postgres://llm_gateway:***REDACTED***@172.16.2.210:5432/llm_gateway?sslmode=disable

# psql 连接
export PGPASSWORD='***REDACTED***'
psql -h 172.16.2.210 -U llm_gateway -d llm_gateway
```

**说明**: 数据库不在 154 本机，而是内网地址 172.16.2.210（可能是专用 DB 节点）。

---

## 代码部署

### 网关运行目录
```bash
# 当前运行版本
/opt/llm-gateway-go/current -> /opt/llm-gateway-go/releases/1797-f627ee24

# 网关进程
ps aux | grep llm-gateway-go
# PID: 22787

# 配置文件
/opt/llm-gateway-go/.env
```

### 验证分支部署
```bash
# 克隆位置
/root/llm-gateway-go-session-v2

# 当前分支
feat/session-turns-v2

# 最新 commit
de1df09b7 (已包含性能优化和 bug 修复)
```

---

## 工具链

| 工具 | 版本 | 状态 |
|------|------|------|
| Go | 1.25.0 linux/amd64 | ✅ |
| git | 2.43.0 | ✅ |
| psql | 已安装 | ✅ |

---

## 已编译的工具

| 工具 | 路径 | 大小 | 状态 |
|------|------|------|------|
| backfill_session_bodies | `/tmp/backfill_session_bodies_v3` | 17MB | ✅ 可用（已优化） |
| backfill_sessions_v2_v2 | `/tmp/backfill_sessions_v2_v2` | 13MB | ⚠️ ON CONFLICT 错误 |
| validate_sessions_v2 | `/tmp/validate_sessions_v2_final` | 13MB | ❌ 需要重构 |

---

## 数据库 Schema

### request_logs (主表)
**分区表**，按月分区（request_logs_2026_08, request_logs_2026_09 等）

**主要字段**:
```sql
request_id           text PRIMARY KEY
ts                   timestamptz
tenant_id            text
gw_session_id        text          -- 会话 ID
client_model         text
provider_id          bigint
credential_id        bigint
prompt_tokens        integer
completion_tokens    integer
cost_usd             numeric
success              boolean
compression_meta     jsonb
-- 注意：没有 body/response/usage 字段！
```

**索引**:
- `request_logs_2026_08_gw_session_id_ts_idx` (gw_session_id, ts DESC)
- 其他月份分区有类似索引

### request_logs_bodies (正文表)
**独立表**，存储请求/响应正文

**主要字段**:
```sql
request_id      text PRIMARY KEY
request_body    jsonb
response_body   jsonb
```

**索引**:
- `request_logs_bodies_hot_request_id_idx`
- `request_logs_bodies_2026_08_pkey`

**重要**: 覆盖率约 10%（大部分请求没有 bodies）

### session_turns (V2 元数据表)
**已存在**，有唯一约束

**约束**:
```sql
session_turns_pkey                              -- 主键
session_turns_tenant_request_partition_key      -- 唯一约束
session_turns_tenant_session_turn_partition_key -- 唯一约束
```

**当前状态**: 0 条数据（回填失败）

### session_bodies (V2 正文表)
**已存在**

**当前状态**: 1 条测试数据（会话 `gt_gw_76d27976-59e4-42a5-853b-299182b9b336`，turn 1）

---

## 已验证的测试会话

### 会话 1: gt_gw_76d27976-59e4-42a5-853b-299182b9b336
- **租户**: default
- **轮数**: 2
- **时间**: 2026-08-28 08:01
- **Bodies 覆盖**: 2/2
- **回填状态**: ✅ session_bodies 已回填（turn 1）

**数据验证**:
```sql
SELECT turn_no, request_id,
       jsonb_array_length(request_delta) AS req_delta_len,
       jsonb_array_length(response_delta) AS resp_delta_len
FROM session_bodies
WHERE session_id = 'gt_gw_76d27976-59e4-42a5-853b-299182b9b336';

 turn_no |            request_id            | req_delta_len | resp_delta_len
---------+----------------------------------+---------------+----------------
       1 | 8cb6929852a51eccf883824b1b29bfe0 |             1 |              0
```

### 其他有 bodies 的会话（待验证）
```sql
gt_gw_02029a6c-223b-4a58-a282-0fdcdfbcc6db (2 轮, 2 bodies)
gt_gw_9ca0e157-9aa7-44d0-9174-5fdabc42320d (2 轮, 2 bodies)
gt_gw_10694406-b984-4c38-bafa-183070d918ae (2 轮, 2 bodies)
gt_gw_8fedeb8d-d95a-48c3-a44a-d28dd4bd1b05 (2 轮, 2 bodies)
```

---

## 已知问题

### 1. bodies 覆盖率低 🟠 高风险
**现象**: 7 天内约 500 个会话，只有 ~50 个（10%）有 bodies 数据

**查询验证**:
```sql
-- 总会话数
SELECT COUNT(DISTINCT gw_session_id) 
FROM request_logs 
WHERE gw_session_id IS NOT NULL AND ts > NOW() - INTERVAL '7 days';
-- 结果: ~500

-- 有 bodies 的会话
SELECT COUNT(DISTINCT r.gw_session_id) 
FROM request_logs r
JOIN request_logs_bodies b ON b.request_id = r.request_id
WHERE r.gw_session_id IS NOT NULL AND r.ts > NOW() - INTERVAL '7 days';
-- 结果: ~50
```

**待调查**: 是策略性存储（只保留失败请求）还是双写未完全启用？

### 2. validate_sessions_v2 不兼容 ⚠️ 中风险
**问题**: 工具查询假设 bodies 在 `request_logs` 表，实际在独立的 `request_logs_bodies` 表

**需要修改**: `cmd/tools/validate_sessions_v2/loader.go`
- 所有 `LoadV1Turns` 相关查询
- 添加 `LEFT JOIN request_logs_bodies ON request_id`

### 3. session_turns 回填失败 🟡 中风险
**错误**: ON CONFLICT 约束不匹配

**SQL 函数**: `sql/scripts/backfill_sessions_v2_v2.sql`

**需要修复**: 检查 ON CONFLICT 子句，匹配实际表约束

---

## 性能基准

### 回填性能（已优化）
- **单会话查询**: 15ms (request_logs) + N×10ms (bodies)
- **2 轮会话总耗时**: 278ms
- **预估批量回填**: 100 会话约 30 秒，1000 会话约 5 分钟

### 查询超时配置
- **默认 statement_timeout**: 30 秒
- **可临时调整**: `SET statement_timeout = '60s';`

---

## 快速命令参考

### 重新编译工具
```bash
cd /root/llm-gateway-go-session-v2
git pull origin feat/session-turns-v2
go build -o /tmp/backfill_session_bodies ./cmd/tools/backfill_session_bodies
go build -o /tmp/validate_sessions_v2 ./cmd/tools/validate_sessions_v2
```

### 回填单个会话
```bash
export DSN='postgres://llm_gateway:***REDACTED***@172.16.2.210:5432/llm_gateway?sslmode=disable'
export SESSION='gt_gw_76d27976-59e4-42a5-853b-299182b9b336'
export TENANT='default'

# Dry-run
/tmp/backfill_session_bodies --dsn="$DSN" --tenant="$TENANT" --session="$SESSION" --dry-run=true

# 实际写入
/tmp/backfill_session_bodies --dsn="$DSN" --tenant="$TENANT" --session="$SESSION" --dry-run=false
```

### 查询会话 bodies 状态
```sql
SELECT 
  r.gw_session_id,
  r.tenant_id,
  COUNT(*) AS turns,
  COUNT(b.request_id) AS with_bodies,
  MIN(r.ts) AS first_turn
FROM request_logs r
LEFT JOIN request_logs_bodies b ON b.request_id = r.request_id
WHERE r.gw_session_id = '<session_id>'
GROUP BY r.gw_session_id, r.tenant_id;
```

---

## 联系人与权限

- **服务器访问**: 已有 root SSH 权限
- **数据库访问**: 已有 llm_gateway 用户权限（读写）
- **代码仓库**: https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git

---

**文档维护**: 每次环境变更后更新此文档
