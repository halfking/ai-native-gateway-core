---
archived_from: docs/2026-07-14-deploy-154-schema-mismatch-lessons.md
archived_at: 2026-08-17
archived_by: docs-archive v1.0
backup_ts: 20260817-190606
status: archived
---

> 本文档已归档，原文保持不变。

# 154 部署 schema 不匹配事故经验

**日期**: 2026-07-13 ~ 2026-07-14
**影响**: 多次部署失败，首页 "database not configured"
**根因**: 代码 HEAD 引用的新 schema 对象未在 252 PG 上创建

---

## 1. 事故时间线

| 时间 | 事件 | 根因 |
|---|---|---|
| 07-13 23:08 | v995 部署后 `postgres disabled` | `column idempotency_key does not exist` |
| 07-13 23:09 | 回滚 v994 | 服务恢复 |
| 07-14 00:05 | v996 部署后 `postgres disabled` | 同上 + `column created_at does not exist` |
| 07-14 00:20 | 回滚 v994 | 服务恢复 |
| 07-14 00:30 | v997 部署后 `postgres disabled` | 同上 |
| 07-14 00:48 | 应用 389/390/391 迁移 | `route_incident_events` 创建 |
| 07-14 00:56 | v998 部署后 `postgres disabled` | `db.go` 使用 `CREATE TABLE IF NOT EXISTS`，旧表存在跳过，但 `CREATE INDEX created_at` 引用不存在的列 |
| 07-14 00:59 | v999 修复 `db.go` 部署 | `ALTER TABLE ADD COLUMN IF NOT EXISTS` 兼容旧表 → **成功** |

## 2. 根因分析

### 2.1 核心问题：代码与 schema 版本漂移

```
main HEAD 代码 → 引用 routing_audit_log.idempotency_key
                  ↓
252 PG schema_migrations → 只到 389，routing_audit_log 是 8 列旧 schema
                  ↓
db.go CREATE TABLE IF NOT EXISTS → 跳过（表已存在）
                  ↓
CREATE INDEX ... ON created_at → 列不存在 → ERROR → postgres disabled
```

### 2.2 为什么 deploy-154.sh 没有发现问题

1. **脚本不检查 schema 兼容性**：只做 healthz + version + auth smoke test，不检查 DB schema
2. **脚本不运行 SQL 迁移**：假设迁移由另一个流程完成
3. **smoke test 通过 ≠ DB 正常**：`/api/system/version` 不依赖 DB（返回静态值），`/healthz` 也不依赖 DB

### 2.3 为什么 v994 正常而 v996+ 不正常

- v994 (build_seq 992) 的代码**不引用** `routing_audit_log.idempotency_key`
- v996+ 的代码合并了 route_incidents Phase-2 功能（`bb83275b8` / `486893c03`）
- Phase-2 功能引用新 schema 对象，但 252 PG 没有运行 390 迁移

## 3. 关键经验

### 3.1 部署前必须验证 schema 兼容性

```bash
# 检查 schema_migrations 版本
ssh 154 "PGPASSWORD=xxx psql -h 172.16.2.210 -U llm_gateway -d llm_gateway -c 'SELECT max(version) FROM schema_migrations'"

# 对比代码引用的 schema 对象
grep -r "CREATE TABLE\|ALTER TABLE\|ADD COLUMN" db/db.go | wc -l
```

### 3.2 db.go EnsureSchema 必须用 ALTER TABLE

**❌ 错误**：
```go
CREATE TABLE IF NOT EXISTS routing_audit_log (... 20 columns ...)
```
→ 如果旧表存在，CREATE 跳过，但后续 CREATE INDEX 引用新列会失败

**✅ 正确**：
```go
ALTER TABLE routing_audit_log ADD COLUMN IF NOT EXISTS idempotency_key TEXT
```
→ 无论表是否存在，都能补齐列

### 3.3 部署后必须检查 `postgres disabled`

```bash
# 部署后立即检查（不是只看 is-active）
journalctl -u llm-gateway-go.service --since '1 minute ago' | grep "postgres disabled"
# 如果有输出 → 立即回滚
```

### 3.4 部署脚本应增加 DB health check

```bash
# 增强 smoke test：检查 DB 端点
DB_CHECK=$(ssh 154 "curl -sS -o /dev/null -w '%{http_code}' http://localhost:8781/api/system/background-tasks")
if [[ "$DB_CHECK" == "503" ]]; then
  err "DB not available (503), likely schema mismatch"
  err "检查: journalctl -u llm-gateway-go.service | grep 'postgres disabled'"
  exit 1
fi
```

### 3.5 version.json 必须在构建前更新

```bash
# ❌ 错误：构建后再改 version.json，导致 ldflags 注入旧版本
go build -ldflags "-X main.BuildNumber=992"  # 硬编码或读取旧 version.json

# ✅ 正确：先 bump version.json，再构建
python3 -c "import json; v=json.load(open('version.json')); v['build_seq']=999; json.dump(v, open('version.json','w'), indent=2)"
go build -ldflags "-X main.BuildNumber=$(python3 -c 'import json; print(json.load(open(\"version.json\"))[\"build_seq\"])')"
```

## 4. 部署检查清单（更新版）

部署 154 前必须完成：

- [ ] **1. 代码编译通过**：`go build ./cmd/gateway`
- [ ] **2. 单元测试通过**：`go test ./bg/... ./admin/...`
- [ ] **3. version.json 已 bump**：build_seq + 1
- [ ] **4. 252 PG schema_migrations 版本 ≥ 代码需要的最小版本**
- [ ] **5. db.go 中所有 EnsureSchema 使用 ALTER TABLE（不是 CREATE TABLE IF NOT EXISTS 对已存在表）**
- [ ] **6. scp 二进制 + version.json**
- [ ] **7. systemctl restart**
- [ ] **8. smoke test：healthz (200) + version (build_seq 正确)**
- [ ] **9. DB health check：background-tasks 非 503**
- [ ] **10. journalctl 检查：无 `postgres disabled`**
- [ ] **11. 如果步骤 9/10 失败：立即回滚**

## 5. 回滚流程

```bash
# 1. 停服务
ssh 154 "systemctl stop llm-gateway-go.service"

# 2. 切换到上一个稳定版本
ssh 154 "ln -sfn /opt/llm-gateway-go/llm-gateway-go.v994.linux.amd64 /opt/llm-gateway-go/llm-gateway-go"

# 3. 启动
ssh 154 "systemctl start llm-gateway-go.service"

# 4. 验证
curl -sS http://47.97.111.154:8781/api/system/version
ssh 154 "journalctl -u llm-gateway-go.service --since '1 minute ago' | grep -E 'postgres connected|disabled'"
```

## 6. 相关文件

- `scripts/deploy-154.sh` — 部署脚本（需增加 DB health check）
- `db/db.go:2771` — `ensureRouteIncidentPhase2Schema`（已修复为 ALTER TABLE）
- `sql/migrations/startup/389_route_incidents.sql` — route_incidents 表
- `sql/migrations/startup/390_routing_audit_log.sql` — routing_audit_log Phase-2
- `sql/migrations/startup/391_route_incidents_audit_safety.sql` — 安全补齐迁移
- `~/.agents/skills/deploy-154/SKILL.md` — 部署技能文档（需更新）

## 7. 改进建议

1. **CI 检查**：main HEAD 引用的 schema 对象必须存在于最新 migration
2. **deploy-154.sh 增加 DB schema 版本检查**：部署前比对 schema_migrations
3. **deploy-154.sh 增加 DB health check**：部署后检查 `postgres disabled`
4. **db.go 规范**：所有 `EnsureSchema` 对已存在表使用 `ALTER TABLE ADD COLUMN IF NOT EXISTS`
5. **version.json SSOT**：构建前先 bump，构建时从 version.json 读取 ldflags
