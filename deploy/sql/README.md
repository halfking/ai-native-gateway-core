# llm-gateway-go 数据库 Schema 快照

本目录存放 llm-gateway-go 数据库的结构快照（schema-only），用于参考生产环境的真实表结构。

## 文件说明

| 文件 | 大小 | 说明 |
|------|------|------|
| `llm_gateway_schema_<日期>.sql` | ~350KB | 从生产数据库导出的纯 schema（结构定义） |

文件名格式：`llm_gateway_schema_YYYY-MM-DD.sql`

## 导出命令

```bash
# 仅导出 schema（推荐，无敏感数据）
PGPASSWORD='<password>' pg_dump \
  -h <host> -p 5432 -U llm_gateway -d llm_gateway \
  --no-owner --no-acl --schema-only --clean --if-exists \
  -f deploy/sql/llm_gateway_schema_$(date +%Y-%m-%d).sql

# 完整 dump（包含数据，请勿提交到 git）
PGPASSWORD='<password>' pg_dump \
  -h <host> -p 5432 -U llm_gateway -d llm_gateway \
  --no-owner --no-acl --clean --if-exists \
  -f /tmp/llm_gateway_full_$(date +%Y-%m-%d).sql
```

## 当前导出源

- **服务器**: __INTERNAL_PUBLIC_IP__ (生产环境核心应用节点)
- **数据库**: `llm_gateway` (PostgreSQL 15.3)
- **导出方式**: `pg_dump --schema-only`，不含任何业务数据
- **导出日期**: 2026-06-26

## 为什么只导出 schema？

数据库中的 `request_logs*`、`request_wal*`、`routing_decision_log` 等表包含：

- 用户实际请求的提示词（prompt）
- 系统消息（system prompt）
- 出站 API Key 与响应内容
- 用户身份、租户、调用元数据

这些数据属于敏感生产数据，**不应提交到 git**。如果需要完整备份，请使用专用备份通道并妥善保管。
