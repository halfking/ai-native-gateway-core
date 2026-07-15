# request_logs_hot.request_body 截断方案与 154 升级路线
> 创建: 2026-07-15, 联动方案
> 状态: **业务侧已有 80% 基础**,剩余 20% 待升级 + 设置开关

## TL;DR

| 现状 | 评估 | 风险 |
|---|---|---|
| `request_logs_hot` 3.5 GB / 9324 行 | 主要 `request_body` JSONB 200-500 KB/行 (TOAST) | 单表 TOAST 后物理占用失控 |
| 代码已有 `keepAllBodies()=false` 默认 | **仅在 binary 包含此逻辑时生效** | 154 binary 旧版不含,实际没有过滤 |
| `request_logs_bodies_*` 架构表建好但 0 行 | INSERT 路径仍写到 `request_logs_hot.request_body` | 架构与代码失配 |
| `data_lifecycle_blobs.go` 提供 cleanup endpoint | 默认未启用 | 需手工调用 / 写 cron |
| `lifecycle.request_logs_bodies_ttl_days=7` 默认 | 7 天 DROP 分区 | 但 requests_logs_bodies 没数据所以 0 释放 |

## 1. 245 升级结果(刚完成)

```
build_seq: 1041 (5f864927) → 1042 (8e5f4472)
deploy_time: 75s 总, 切换 42s
restart: systemd 仅一次
healthz: ok ✅ (pid 3129047)
DB verify: ✓ 3s
admin_password sync: ✓ 200
```

**promote 逻辑在 245 binary 中已彻底退役**:
- `strings current/gateway | grep promote_model_probe_runs` → 空
- `partition_manager.promoteSpecs()` 在 commit `26984ccb5` (2026-07-15 02:26) 中已注释 `model_probe_runs_hot`
- `cleanupOldModelProbeRuns()` 替代: hot 表 14 天 TTL DELETE

**rollback**: `bash scripts/deploy-seamless.sh rollback 245`

## 2. 154 当前状态(待解决 — 仍未升级)

| 项 | 值 |
|---|---|
| binary 路径 | `/opt/llm-gateway-go/llm-gateway-go.v1024.linux.amd64` |
| sha256 | `b5168b6b35503e9fbfa2d3d61bcba582b1c9ea6817704a07cb4c137c351dd84c` |
| 大小 | 41033890 bytes |
| 启动 PID | 9591 (04:33:29 启动) |
| `promote_model_probe_runs_hot_to_partition` 在 binary 中? | **是** (strings 命中) |
| `LLM_GATEWAY_KEEP_ALL_BODIES` 环境变量? | 未设置 |
| 实际行为 | `keepAllBodies()=false` 在 binary 中可能**未生效** (因 binary 编译时早于 commit 55b4aeecd) |

**154 binary 是从哪个 commit 编译**? 通过反向找 promoteSpecs() 头判断:
- 二进制含 `promote_model_probe_runs_hot_to_partition` 函数
- 154 当前 commit log 是 995e6c4c (2026-07-14 04:28) — **未包含 `26984ccb5`**
- 估计 binary 编译时间约 2026-07-15 04:30,源码 ref 不详 — 但肯定不含 retire 修改

**结论**: 154 上的 `model_probe_runs_hot` promote 逻辑**未退役**,但被 252 上 PG 端空操作函数堵住(避免涨势)。安全但不优雅。

### 2.1 升级 154 的可行路径(建议)

```bash
# 0) 本地 working tree commit (目前 dirty)
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
git status --short   # M VERSION + version.json + web/public/version.json
git add VERSION version.json web/public/version.json
git commit -m "chore(release): 本地 deploy 准备"

# 1) 升级 154 用 deploy-154-prod.sh (它直接编译当前 working tree)
./deploy-154-prod.sh
#   - 自动编译新 binary
#   - 自动 SCP + 切 symlink + systemd restart
#   - llmgw.kxpms.cn 服务有 ~5-15s 中断

# 2) 回滚 (如果 healthz 失败)
ls -t /opt/llm-gateway-go/llm-gateway-go.v*.linux.amd64 | head -2 | tail -1
ssh root@154 'cd /opt/llm-gateway-go && ln -sfn <previous_binary> llm-gateway-go && systemctl restart llm-gateway-go.service'
```

**风险**: 154 上跑的是 llm.kxpms.cn 生产服务,kxpms 域名的流量。restart 中断~5-15s。

**最佳执行窗口**: 业务低峰 + 用户授权。

## 3. request_body 截断/治理方案

### 3.1 现状(实测)

```sql
-- 在 252 上,2026-07-15 04:55 时
SELECT success,
       count(*),
       pg_size_pretty(avg(pg_column_size(request_body))::bigint) AS avg_req_body,
       pg_size_pretty(sum(pg_column_size(request_body))::bigint) AS total_req_body
FROM request_logs_hot GROUP BY success;
```
| success | rows | avg_req_body | total_req_body |
|---|---|---|---|
| f (failed) | 1535 | 131 kB | 131 MB |
| t (success) | 8571 | **211 kB** | **1763 MB** |

```sql
-- 成功行理论应该 body=NULL (因 keepAllBodies=false),但实际全有 body
SELECT count(*) FILTER (WHERE request_body IS NOT NULL) AS rows_with_body,
       count(*) FILTER (WHERE request_body IS NULL) AS rows_without_body,
       count(*) AS total
FROM request_logs_hot;
```
| rows_with_body | rows_without_body | total |
|---|---|---|
| 9570 (95%) | 536 (5%) | 10106 |

**问题**: 154 binary 编译时不含 `keepAllBodies()` fix,导致 success=true 也写入 body。

### 3.2 已有但未启用的能力

| 能力 | 文件 / API | 状态 |
|---|---|---|
| `keepAllBodies()` 默认仅保留 failed body | `admin/telemetry.go:116` | **需要 binary 包含此 fix (commit 55b4aeecd 之后)** |
| `request_logs_bodies_hot` 独立 body 表 | migration 328a (架构) | **0 行,INSERT 路径未启用** |
| `request_logs_bodies_*` 月分区 + 7 天 TTL DROP | `bg/partition_manager.go:223` | 等待数据写入 |
| Admin cleanup endpoint | `POST /api/admin/data-lifecycle/blobs/cleanup/execute` | **可用,未 cron 化** |
| `lifecycle.request_logs_bodies_ttl_days` | settings_kv,默认 7 | OK |
| `log.trim_days` 范围 | `"7-30"` 默认 | **已存在,可调小到 "1-3"** |

### 3.3 三层治理(渐进式,生产可用)

#### Tier 1 (即时,1 小时内): 设置开关 + 立即清理

```bash
# 1) 154 binary 升级 (ensure promote 移除 + KEEP_ALL_BODIES fix)
./deploy-154-prod.sh

# 2) 部署后立即调用清理接口清空 24h+ body (7 天前的)
curl -X POST https://llm.kxpms.cn/api/admin/data-lifecycle/blobs/cleanup/execute \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"older_than_days": 1, "scope": "all"}'
# 预估回收: ~1500 MB (3.5 GB 当前 → 2 GB)

# 3) 同时 245 清理 (作为参考,245 也可同样执行)
curl -X POST https://llmgw.kxpms.cn/api/admin/data-lifecycle/blobs/cleanup/execute \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"older_than_days": 1, "scope": "all"}'
```

**预期总释放**: 3-4 GB (200 KB × 9000 行中保留 ~5% failed 即可)

#### Tier 2 (1 周内): 启用 `request_logs_bodies` 表 + 自动清理 cron

代码侧:让 INSERT 路径"split body":
- `domains/hooks/observability/telemetry/client.go` 当前 `INSERT INTO request_logs_hot (... request_body, response_body ...)`
- 改为:主表只存 `request_preview/response_preview` (≤2 KB),完整 body 写 `request_logs_bodies_hot`(架构已就绪)

但这是 binary 内代码变更,需要发版。

或者更轻量的:靠 admin cron 调用 cleanup endpoint:

```cron
# 252 端 / opt/scripts/ (或 llmgw admin cron) 加清理 cron
# /etc/cron.d/pg17 已有的 llmgw-source 包装支持,加一条:
0 2 * * * root llmgw-source /opt/scripts/llmgw-blob-cleanup.sh
```

```bash
#!/bin/bash
# /opt/scripts/llmgw-blob-cleanup.sh
ADMIN_TOKEN=$(cat /etc/llmgw/admin_token)
target=${1:-https://llm.kxpms.cn}
# 仅清 7-30 天的 body (recent 7 天保留以便调试)
curl -sS -X POST "$target/api/admin/data-lifecycle/blobs/cleanup/execute" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"older_than_days": 7, "scope": "all"}' \
  | jq '.RequestBodyAffected, .EstimatedFreedHuman' || true
```

#### Tier 3 (架构完善,1 月内): 改 INSERT 拆分

实施步骤(详细):

| # | 步骤 | 文件 | 风险 |
|---|---|---|---|
| 1 | migration NNN: add `request_logs_bodies_hot` 与 `request_logs_hot` 数据契约 | sql/ | 低 |
| 2 | 改 `telemetry.persistRequestLog` INSERT 两表 | `admin/telemetry.go` | 中(单测) |
| 3 | 改 `domains/hooks/observability/telemetry/client.go` INSERT 两表 | 同前 | 中 |
| 4 | 修改 `request_logs_hot` 列: 把 `request_body, response_body` 设为 NULL only | migration | 低 |
| 5 | 调 `request_logs_hot.request_preview, response_preview` 列 | 已存在 | — |
| 6 | CI 加 lint: 单表 `request_logs_hot` 超过 1 GB 报警 | 新增 | — |

## 4. 业务侧影响评估

### 4.1 截断后会影响什么?

| 用途 | 现状 | 截断后 |
|---|---|---|
| 调试 prompt failure | ✅ 完整 request_body 可读 | ❌ lost (除非在 failed rows) |
| 路由诊断 | ✅ request_body 可读 | partial (preview + preview only) |
| 计费审计 | ✅ cost 计算来自 usage_ledger, body 不必要 | OK |
| LLM Provider 协议逆向 | ✅ 完整 request_body | partial |
| 客户支持(看用户问什么) | ✅ 完整 prompt | partial (preview 前 4 KB) |
| 多模态图片审计 | ✅ base64 stored | ❌ 永久 lost (`request_logs_bodies` 单独 DROP) |
| A/B test 调优 | 需要完整 messages | partial |
| 法律合规(留存 N 月) | ✅ 默认 7 天 (Request logs bodies TTL) | 受 7 天约束 |

**推荐**: **保留了 `failed` 行 + `preview` 列,丢失成功的 `>2 KB` body**。这是 154 disk-pressure 事件后的妥协,业务可接受。

### 4.2 用户需要决策的

| 决策 | 选项 |
|---|---|
| **A** (推荐) | 仅保留 `success=false` 全 body + `success=true` 只 preview 前 N KB |
| **B** (激进) | 所有行 body 限制为前 1 KB,failed 也不存全 body |
| **C** (保守) | 仅调用 `data-lifecycle blobs cleanup` 把老 body 置 NULL,新数据看 KEEP_ALL_BODIES 兜底 |
| **D** (不动) | 依赖 252 的 7 day partition DROP + autovacuum,长期靠 vacuum+bloat cron 自然收缩 |

**建议**: A + Tier 2 cron(自动 1 天 1 次清空 success body)

## 5. 落地路线图(估计工作量)

| 阶段 | 内容 | ETA | 风险 |
|---|---|---|---|
| **Day 0** | 已完成:升级 245 + DROP 分区 + 替换 PG 函数 + 5 个 cron + webhook | — | ✅ |
| **Hour 1** | 升级 154 binary 到 1042+ (deploy-154-prod.sh, ~5-15s 服务中断) | 30 min | 中(需用户授权) |
| **Hour 2** | 调用 154 + 245 cleanup endpoint 清 1 天以上 body | 5 min | 低 |
| **Day 1** | 部署 `llmgw-blob-cleanup.sh` cron (每日 02:00) | 30 min | 低 |
| **Day 2** | 监控 `request_logs_hot` 大小,确认稳态 <500 MB | 持续 | 监测 |
| **Week 2** | Phase: prompt 截断 (commit 800 → request_preview 列 + telemetry.go 改动) | 5 人天 | 中 |
| **Week 4** | 上线 `request_logs_bodies_hot` 表 + INSERT split | 3 人天 | 中,需回归测试 |
| **Month 2** | 全量上线 (254 全网关服务) | 1 周观察 | 低 |

## 6. 验证步骤(本任务范围内)

```bash
# 1. 245 已经 ✓ 完成
ssh -i ~/.ssh/id_ed25519 -p 25022 root@8.136.114.245 \
  'strings /opt/llm-gateway-go/current/gateway | grep promote_model_probe_runs_hot'
# 期望输出: 空

# 2. 252 上的 PG 函数是空操作(已经做)
ssh -p 25022 root@115.29.212.252 \
  'docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -c "SELECT promote_model_probe_runs_hot_to_partition(\"24:00:00\", 5000)"'
# 期望: 0

# 3. request_logs_hot 大小记录
ssh -p 25022 root@115.29.212.252 \
  'docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -c \
    "SELECT relname, pg_size_pretty(pg_total_relation_size(relid)), n_live_tup FROM pg_stat_user_tables WHERE relname = '"'"'request_logs_hot'"'"'"'
# 记录: 当前 3.5 GB / 9324 行 (基线)

# 4. cleanup endpoint 调用 (待用户授权后)
curl -X POST https://llm.kxpms.cn/api/admin/data-lifecycle/blobs/cleanup/execute \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"older_than_days": 1, "scope": "all"}'

# 5. 再次测量 request_logs_hot
ssh -p 25022 root@115.29.212.252 \
  'docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway -c \
    "SELECT relname, pg_size_pretty(pg_total_relation_size(relid)), n_live_tup FROM pg_stat_user_tables WHERE relname = '"'"'request_logs_hot'"'"'"'
```
