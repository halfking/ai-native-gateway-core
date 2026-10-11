# 迁移 358 `session_owners.end_user_id VARCHAR(128)` 溢出（独立运维修工单）

> **状态**：与 R-vapeur4（commit `ba1987a4d`）**无关**——本工单阻塞 host-first 本地 deploy
> 从零建库流程；不影响生产 245 / 154（生产 DB 早已应用过 358，本工单只在 fresh DB 上炸）。
> **不影响**：本轮 R-vapeur4 修复 / 单测 / 推送。生产只要当前 DB 已应用过 358，再次部署同一
> commit 不会触发这个 INSERT（apply-db-revision-sequence 是幂等的）。
> **触发人**：2026-10-12 R-vapeur4 本地 host-first deploy 实测。

## 症状

`scripts/deploy-local.sh deploy` 在走到 `sql/migrations/startup/358_session_ownership.sql`
第 330 行时报错：

```
psql:.../358_session_ownership.sql:330: ERROR:  value too long for type character varying(128)
```

迁移 4.2 节从 `request_logs` 聚合回填 `session_owners`：

```sql
-- 358_session_ownership.sql:304-330
INSERT INTO session_owners (
    gw_session_id, tenant_id, owner_user, end_user_id, ...
) SELECT gw_session_id, tenant_id, api_key_owner_user, end_user_id, ...
  FROM request_logs
  WHERE gw_session_id IS NOT NULL AND gw_session_id <> ''
  GROUP BY gw_session_id, tenant_id, api_key_owner_user, end_user_id, ...
  ON CONFLICT (gw_session_id, tenant_id, owner_user, end_user_id, api_key_id)
  DO NOTHING;
```

`session_owners` 的 schema 把 `end_user_id` 定成 `VARCHAR(128)`：

```sql
-- 358_session_ownership.sql:50-69
CREATE TABLE IF NOT EXISTS session_owners (
    id               BIGSERIAL PRIMARY KEY,
    gw_session_id    VARCHAR(128) NOT NULL,
    tenant_id        VARCHAR(255) NOT NULL,
    owner_user       VARCHAR(128),
    end_user_id      VARCHAR(128),  -- ← 这里
    api_key_id       BIGINT,
    application_id   BIGINT,
    application_code VARCHAR(64),
    client_id        VARCHAR(128),
    ...
    UNIQUE (gw_session_id, tenant_id, owner_user, end_user_id, api_key_id)
);
```

而 `request_logs.end_user_id` 是 `text`，实际数据已经累积到 150 字符。本机实测：

```sql
SELECT MAX(LENGTH(end_user_id)) FROM request_logs WHERE gw_session_id IS NOT NULL;
--  max
-- -----
--  150
```

## 根因

迁移 358 把目标表 `session_owners` 的 `end_user_id` 设为 `VARCHAR(128)`，但没做源数据
长度审计。`request_logs.end_user_id` 自始是 `text`（2026-06 schema 起就是 `text`），已有
长字符串 end_user_id 来自客户端自定义值。

类似的字段在 358 schema 里还有：

| 列 | session_owners | request_logs | 风险 |
|---|---|---|---|
| `end_user_id` | VARCHAR(128) | text | **会撞**（本工单） |
| `owner_user` | VARCHAR(128) | text | 潜在 |
| `api_key_owner_user` | VARCHAR(128) | text | 潜在 |
| `application_code` | VARCHAR(64) | text | 潜在 |
| `client_id` | VARCHAR(128) | text | 潜在 |

> **本机 10-12 实测只撞了 `end_user_id`**。其余字段本机最大值都 <20 字符（来自
> `apigpt / xai / 速云` 等短代码），但生产 245 / 154 是否撞需要运维独立跑同款 max-length
> 审计才能定论。**先做 end_user_id，其他看命中再分别处理**。

## 影响面

- **生产 245 / 154**：DB 已应用过 358，迁移 4.2 是幂等的（`ON CONFLICT DO NOTHING`），再次部署
  同一 commit 不会触发 INSERT（apply-db-revision-sequence 检测到已应用会跳过）。**生产不受影响**。
- **fresh DB / local host-first**：从零建库走到 358 必然炸，host-first 本地 deploy 阻塞。
- **R-vapeur4**：本轮修复 + 单测 / 变异验证全部 OK；与 358 无因果关系。

## 修复方案（三个，待 owner 拍板）

### 方案 A（推荐）——先源端截断，再正常迁移

```sql
-- 在跑 358 之前先：
UPDATE request_logs
SET end_user_id = LEFT(end_user_id, 128)
WHERE LENGTH(end_user_id) > 128;
-- 同理处理 owner_user / api_key_owner_user / application_code / client_id
-- 若对应字段在 358 schema 也是 VARCHAR(N)
```

**优点**：
- 干净：源数据统一，358 原文不动
- 不破坏既有 request_logs 行的可读性（LEFT 截断保留前 128 字符，对客户端自定义值的尾段多数是冗余哈希）

**缺点**：
- 永久丢失尾段（如果末 22 字符是关键的，去重 / 关联键可能撞）
- 必须先扫所有 VARCHAR(N) 字段并各自截断

### 方案 B（最小改动）——改迁移 358 schema 为 TEXT

```sql
-- 358_session_ownership.sql 第 50-69 行
end_user_id      TEXT,         -- 原 VARCHAR(128)
owner_user       TEXT,         -- 原 VARCHAR(128)
api_key_owner_user TEXT,       -- 原 VARCHAR(128)
application_code TEXT,         -- 原 VARCHAR(64)
client_id        TEXT,         -- 原 VARCHAR(128)
```

**优点**：
- 改动最小，单文件
- 不丢任何数据

**缺点**：
- 索引大小会膨胀（如果生产 245 / 154 上对应字段真的累积了 >128 字符的大量数据）
- 失去 schema 强校验（128 字符业务上是否合理本来就是个边界）

### 方案 C（中间路线）——加 `LEFT(...)` 截断在 INSERT

```sql
-- 358 第 310-318 行（聚合 SELECT）
end_user_id = LEFT(end_user_id, 128),  -- 截断到 schema 上限
```

**优点**：
- 改动定位精准，只动 INSERT 那一段
- 不改 schema，校验语义不变
- 不需要先 UPDATE request_logs（不用 DDL 操作）

**缺点**：
- 与方案 A 同：永久丢失尾段
- 同名聚合请务必统计所有 VARCHAR(N) 字段

## 运维侧先决动作（任一方案前都做）

```sql
-- 1. 全 VARCHAR(N) 字段长度审计（生产 245 / 154 上跑）
SELECT 'end_user_id' AS col, MAX(LENGTH(end_user_id)) AS max_len,
       COUNT(*) FILTER (WHERE LENGTH(end_user_id) > 128) AS over_128
FROM request_logs WHERE gw_session_id IS NOT NULL
UNION ALL
SELECT 'owner_user', MAX(LENGTH(owner_user)),
       COUNT(*) FILTER (WHERE LENGTH(owner_user) > 128)
FROM request_logs WHERE gw_session_id IS NOT NULL
UNION ALL
SELECT 'api_key_owner_user', MAX(LENGTH(api_key_owner_user)),
       COUNT(*) FILTER (WHERE LENGTH(api_key_owner_user) > 128)
FROM request_logs WHERE gw_session_id IS NOT NULL
UNION ALL
SELECT 'application_code', MAX(LENGTH(application_code)),
       COUNT(*) FILTER (WHERE LENGTH(application_code) > 64)
FROM request_logs WHERE gw_session_id IS NOT NULL
UNION ALL
SELECT 'client_id', MAX(LENGTH(client_id)),
       COUNT(*) FILTER (WHERE LENGTH(client_id) > 128)
FROM request_logs WHERE gw_session_id IS NOT NULL;
```

输出 5 行 × `max_len / over_128` 两列。`over_128 > 0` 的字段就是方案要处理的字段。

## 复现 / 验证步骤

1. **复现**：从零建库 + 跑 apply-db-revision-sequence 到 358 行：
   ```bash
   bash scripts/apply-db-revision-sequence_test.sh 2>&1 | grep -E "358|ERROR"
   ```
   预期：第 330 行报 `value too long for type character varying(128)`。

2. **修复后复现**：跑方案 A / B / C 之一，重跑脚本，预期第 330 行不再报错；session_owners
   表行数与 `request_logs WHERE gw_session_id IS NOT NULL` 的去重 session 数大致相当。

3. **host-first 联调**：在 fresh DB 上跑：
   ```bash
   bash scripts/deploy-local.sh --root ~/kaixuan/llm-gateway-go deploy
   ```
   预期：deploy 走完所有迁移，gateway 在 8782 起来 `/healthz` ready。

## 与 R-vapeur4 的关系

- R-vapeur4（`ba1987a4d`）：代码修复，proactive chat→responses 升级，**与本工单无关**。
- 本工单：host-first fresh DB 部署 blocker，**与 R-vapeur4 无关**。
- 合并处理顺序：建议先独立修本工单（fresh DB 走完 358），再把 R-vapeur4 带上生产部署；
  这样生产部署只是「走一遍 apply-db-revision-sequence（幂等跳过）+ 起新 binary」，干净。

## 责任人 / 决策

需要 owner 拍板方案 A / B / C，并在 245 / 154 跑一次字段长度审计再决定。
本工单不归本会话 owner（除非另行授权）——交接给运维 owner。

## 状态机

- [x] 问题登记（2026-10-12）
- [ ] 245 / 154 字段长度审计（运维）
- [ ] 拍板方案 A / B / C（owner）
- [ ] 修复迁移 358 并复现 / 验证（运维）
- [ ] host-first fresh DB deploy 走完（验证修复）
- [ ] R-vapeur4 `ba1987a4d` 部署上 245 / 154（独立工作流）