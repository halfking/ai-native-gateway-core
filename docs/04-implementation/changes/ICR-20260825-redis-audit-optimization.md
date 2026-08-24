# 2026-08-25 · pms-redis 大数据审计 + 综合优化方案（ICR-245-A5）

## 背景

承接 [`/tmp/handoff/handoff-20260825-0035-245-audit.md`](../../../handoff/handoff-20260825-0035-245-audit.md) 的 P2 加固阶段，对 252 共享 Redis（`pms-redis:6389`）进行四维度深度审计：多租户与容量决策（子代理 A）、大 value 与对象编码（子代理 B）、慢查询与危险命令（子代理 C）、共用代码层 owner/DB index 用法（子代理 D）。

四份子报告已合并到本 ICR：

| 子代理 | 报告 | 核心结论 |
|---|---|---|
| **A** 多租户 | [`/tmp/audit-redis-multitenant.md`](../../../../../../tmp/audit-redis-multitenant.md) | db0/db1/db2/db9/db15 owner 清晰，**`session:v2` 硬编码 db=0 是隔离漏洞**；当前 used 768 MB，maxmemory=0+noeviction 是隐患；建议 `maxmemory=1.5G` + `volatile-lru` |
| **B** 大 value | [`/tmp/audit-redis-large-values.md`](../../../../../../tmp/audit-redis-large-values.md) | db2 真正大 value 集中在 11 个 key（G1 `llmgw:live:actions` 487 KB 无 TTL 是最大隐患 + G2–G10 `session:apiKey:*:active` 9 个大 set）；`session:gw_*` hash 应迁 listpack 省 ~25%；存在 5 个孤儿无 TTL |
| **C** 慢查询 | [`/tmp/audit-redis-slow-queries.md`](../../../../../../tmp/audit-redis-slow-queries.md) | 慢查询主因是大体积 EVAL 脚本 + `SCRIPT FLUSH` 后 EVALSHA 回退风暴（1184/34469 ≈ 3.4%）；MGET 平均 1.48 ms 偏高；KEYS 已基本收敛但应 `rename-command` 兜底；SCAN 累计耗时 1063s，可下沉 client cache |
| **D** 治理闭环 | 合并 A/C 给出 | 缺失 Prometheus 告警、family-level owner/TTL policy、每日 SCAN 治理脚本 |

> **网络约束声明**：本批审计期间，172.16.2.210:6389 从本地 shell 不可达（connectx timeout）。子代理 B 在本机 `127.0.0.1:6379 db2` 镜像完成 SCAN（64k keys，schema 与生产一致）；子代理 A/C 基于仓库历史审计文档（`REDIS_CACHE_AUDIT_2026-07-23.md`、`lessons-learned-2026-08-04-swimlane-jump-redis-timeout.md`、`REDIS_TTL_OPTIMIZATION_2026-07-23.md`、`session-pipeline-verification-report.md`、`2026-08-19-canary-evidence-and-status.md`）+ 当前代码静态分析 + handoff 现场数据综合得出。**所有改动项落地前需在 252 `pms-redis` 重跑 §6 验证脚本确认生产数据形态**。

---

## 现状盘点（Pre-A5 baseline @ commit 06408163c）

| 指标 | 当前值 | 来源 | 风险 |
|---|---|---|---|
| `used_memory` (252 redis) | **~768 MB** | handoff §4 | 7d 增速 ~+8.7 MB/d |
| `maxmemory` | **0（无上限）** | 子代理 A §5.2 | 写满无门控 |
| `maxmemory-policy` | **`noeviction`** | 子代理 A §4.1 | 写满即业务中断 |
| 容器 cgroup `--memory` | **未设置** | 子代理 A §3.3 | 失控可牵连 252 宿主其他服务 |
| db2 总 keys | **~744,000**（估算；本机镜像 64,145） | handoff §4 / 子代理 B §1 | — |
| db2 avg TTL | **~44h / 19.9d**（口径不一） | handoff §4 / 子代理 B §1 | 99.9% 集中在 ~19.9d |
| `instantaneous_ops_per_sec` | **~6.7k**（修复后） | handoff §3 | — |
| EVALSHA 失败率 | **3.4%**（1184/34469） | 子代理 C §4 | 与 `SCRIPT FLUSH` 联动风暴 |
| SCAN 累计耗时 | **1063 秒**（542 万次调用） | 子代理 C §3.2 | 可下沉客户端缓存 |
| 4 个遗留 `redis-cli --latency-history` 进程 | PID 2225176/2225509/2225825/2226209（20 天） | handoff §4 | 占连接、掩盖真实 ops |
| 154 生产 build_seq | **1727**（未同步 1731 修复） | handoff §7 | **P0 阻塞** |

---

## 改动项总览（按优先级）

| ID | 优先级 | 标题 | 需 154 联调 | 工作量 | 主要文件 / env |
|---|---|---|---|---|---|
| **P0-1** | P0 | 154 同步部署 build_seq 1731 代码修复 | ✅ | 0.5 d | 走 `deploy-154-via-252.sh` |
| **P0-2** | P0 | 修复 `session:v2` 硬编码 db=0（隔离漏洞） | ✅ | 0.5 d | `domains/session/v2/cache_v2_redis.go:73` + 单测 |
| **P0-3** | P0 | 清理 4 个遗留 `redis-cli --latency-history` 进程 | ❌ | 5 min | 252 容器内 `pkill` |
| **P0-4** | P0 | 加 TTL/LTRIM 治理 `llmgw:live:actions`（487 KB 无界增长） | ❌ | 0.5 d | `admin/live_stream_redis_store.go` |
| **P0-5** | P0 | 补 5 个孤儿 `session:gw_*` 无 TTL hash + 加监控 | ❌ | 1 d | `cache_v2_redis.go` 写路径 + `cron/orphan_ttl_repair.sh` |
| **P1-1** | P1 | 设置 `maxmemory=1.5G` + `volatile-lru` + 容器 cgroup 2G | ⚠️ 通知 | 0.5 d | 252 容器 CONFIG SET + podman restart |
| **P1-2** | P1 | 减少 `SCRIPT FLUSH` 频次 + 用 `FUNCTION LOAD` 预热 | ❌ | 1 d | 部署脚本 / 迁移工具 |
| **P1-3** | P1 | 缩短 `llmgw:stats:delta:*` TTL（30d → 3d） | ❌ | 0.5 d | `domains/stats/` + `apply_redis_ttl_optimization.sh` |
| **P1-4** | P1 | 启用 `rename-command KEYS ""` / FLUSHDB / FLUSHALL 兜底 | ⚠️ 通知 | 0.5 d | `pms-redis` redis.conf |
| **P1-5** | P1 | 调 `hash-max-listpack-entries=64` + DEBUG RELOAD 迁移 | ❌ | 0.5 d | redis.conf |
| **P2-1** | P2 | `RATE_LIMIT_REDIS_URL` fallback 到主 Redis | ❌ | 0.5 d | `ratelimit/redis_sliding.go:161` |
| **P2-2** | P2 | 加 family-level owner/TTL policy SSOT + Prometheus 告警 | ❌ | 1-2 d | `redis-namespace-policy.yaml` + Grafana |
| **P2-3** | P2 | MGET/HGETALL payload 大小监控（>1 MB 告警） | ❌ | 1 d | 业务侧 wrapper + Prometheus |
| **P2-4** | P2 | EVALSHA NOSCRIPT 失败率监控 | ❌ | 0.5 d | `cmdstat_evalsha.failed_calls` exporter |
| **P2-5** | P2 | 客户端 ACL 限制（仅 154/245/本机）+ protected-mode | ⚠️ 通知 | 0.5 d | redis.conf ACL |
| **P2-6** | P2 | 观察 30 天增长曲线 → 触发独立容器拆分（方案 A） | ✅ | 5 d（触发后） | 新建 `pms-redis-llmgw:6380` + `.env` 改地址 |
| **P2-7** | P2 | `promote_request_wal_hot_to_partition` SQL stub 实现 | ✅ | 1-2 d | 新 migration + function |

> **优先级判定标准**：
> - **P0**：立即可做、风险低、收益明显（≤1 d 内可见）
> - **P1**：需 brief 设计评审（154 通知或单 PR 内 review）
> - **P2**：需 154 联调或大规模改造（拆分 / 大版本变更）

---

## 详细方案

### P0-1 · 154 同步部署 build_seq 1731 代码修复

- **背景**：handoff §7 阻塞项 — 154 生产仍跑 build_seq 1727，未享受 1731 的 OOM 止血 + 列存安全 + live-stream throttle + KEYS→SCAN 修复。共享 Redis、共享 PG，154 同等受益。
- **具体动作**：
  1. 走 `scripts/deploy/deploy-154-via-252.sh`（154 公网 SSH 故障通道）
  2. deploy 完成后比对 154 version.json build_seq ≥ 1732
  3. 154 端 `redis-cli -h 172.16.2.210 -p 6389 -a Veritrans9900 -n 2` 验证 ops/sec 6.7k（与 245 一致）
- **预计影响**：154 端 RSS 同步下降（245 已 1.86GB → 224MB）、Redis ops 同步下降（13k → 6.7k）、ColumnarScan 0A000 错误率归零
- **业务风险**：低（245 已验证 4min+ 稳定）
- **验证方式**：`version.json` build_seq ≥ 1732；`redis-cli INFO clients` 看 154 连接数；`pg_stat_statements` 看 ColumnarScan 计数
- **需 154 联调**：✅ **强制**（deploy 通道唯一）

### P0-2 · 修复 `session:v2` 硬编码 db=0（隔离漏洞）

- **背景**：子代理 A §7.2 发现 `domains/session/v2/cache_v2_redis.go:73` 硬编码 `DB: 0`，导致 `session:v2` governance cache 写入 db0（**PMS 共享区**），违反 handoff §6.1 "db2 隔离" 原则。
- **具体动作**：
  ```go
  // 当前 (cache_v2_redis.go:73)
  client := redis.NewClient(&redis.Options{
      Addr: redisAddr,
      DB:   0,  // ❌ 硬编码
      ...
  })
  
  // 改为
  db := cfg.RedisDB  // env LLM_GATEWAY_REDIS_DB 注入
  client := redis.NewClient(&redis.Options{
      Addr: redisAddr,
      DB:   db,
      ...
  })
  ```
  - 同步 `domains/credential/rpm_redis.go:52-60` 模式（已用 `cfg.RedisDB`）
  - 新增单测 `cache_v2_redis_test.go` 覆盖 db=0/2/5 三种注入
- **预计影响**：db0 停止受 llmgw 治理缓存污染；244/245 端 `redis-cli -n 0 DBSIZE` 不再增长，`-n 2 DBSIZE` 增加
- **业务风险**：低（db2 是 llmgw 主战场，db0 是 PMS 共享区；切到 db2 是回归正确状态）
- **验证方式**：
  - 部署前：`redis-cli -h 172.16.2.210 -p 6389 -a Veritrans9900 -n 0 KEYS "session:v2:*"` 计数（应 < 5 keys，残留）
  - 部署后 24h：db0 `session:v2:*` 不再增长；db2 `session:v2:*` 持续增长（治理 cache 写活跃）
- **需 154 联调**：✅ **必须双发**（否则 154 仍读 db0 找不到新数据，反之亦然）

### P0-3 · 清理 4 个遗留 `redis-cli --latency-history` 进程

- **背景**：handoff §7 阻塞 — PID 2225176/2225509/2225825/2226209 已运行 20 天，持续占连接（4 × 1 conn = 4 个客户端槽位）并可能掩盖真实 ops 指标。
- **具体动作**：
  ```bash
  # 252 容器内（或宿主 podman exec）
  pkill -9 -f "redis-cli --latency-history"
  
  # 验证
  ps aux | grep "redis-cli --latency-history" | grep -v grep  # 应为空
  redis-cli -h 172.16.2.210 -p 6389 -a Veritrans9900 INFO clients | grep connected_clients
  ```
- **预计影响**：`connected_clients` 减少 4 个；后续 ops 指标更接近真实业务负载
- **业务风险**：无（这些是 debug 监控进程，非业务路径）
- **验证方式**：`ps aux | grep latency-history` 计数归零；24h 后 `INFO clients connected_clients` 至少下降 4
- **需 154 联调**：❌（纯 252 运维动作）

### P0-4 · 加 TTL/LTRIM 治理 `llmgw:live:actions`

- **背景**：子代理 B §5.5 — 单个 key **487 KB、无 TTL、2007 events**，且持续 append（`admin/live_stream_redis_store.go` 写入路径未设 TTL/LTRIM）。是当前 db2 最大隐患，单 key 可无限增长。
- **具体动作**（择一或组合）：
  ```go
  // 方案 1: LTRIM 截断（最小改动）
  const maxLiveActionsRetained = 500
  func AppendLiveAction(ctx context.Context, event LiveEvent) error {
      key := "llmgw:live:actions"
      pipe := redisClient.Pipeline()
      pipe.RPush(ctx, key, eventJSON)
      pipe.LTrim(ctx, key, -maxLiveActionsRetained, -1)
      _, err := pipe.Exec(ctx)
      return err
  }
  
  // 方案 2: 加 TTL（最简）
  func AppendLiveAction(ctx context.Context, event LiveEvent) error {
      key := "llmgw:live:actions"
      pipe := redisClient.Pipeline()
      pipe.RPush(ctx, key, eventJSON)
      pipe.Expire(ctx, key, 1*time.Hour)  // 或 24h
      _, err := pipe.Exec(ctx)
      return err
  }
  
  // 方案 3: 按日期分桶（最干净，需追溯 reader）
  func AppendLiveAction(ctx context.Context, event LiveEvent) error {
      dateBucket := time.Now().UTC().Format("20060102")
      key := fmt.Sprintf("llmgw:live:actions:%s", dateBucket)
      pipe := redisClient.Pipeline()
      pipe.RPush(ctx, key, eventJSON)
      pipe.Expire(ctx, key, 24*time.Hour)
      _, err := pipe.Exec(ctx)
      return err
  }
  ```
  - 推荐 **方案 1 + 2 组合**：LTRIM 500 + EXPIRE 1h
  - 单测：模拟 1000 次 RPUSH，验证 `LLEN` ≤ 500 且 TTL 已设
- **预计影响**：该 key 内存稳定 ≤ 50 KB；`INFO memory` 立刻可见（487KB → <50KB）
- **业务风险**：低（live-stream 是 dashboard 展示，允许历史回滚 ≤ 1h）
- **验证方式**：
  - 部署后：`redis-cli -h 172.16.2.210 -p 6389 -a Veritrans9900 -n 2 LLEN llmgw:live:actions` ≤ 500
  - `TTL llmgw:live:actions` 落在 (0, 3600] 区间
- **需 154 联调**：❌（代码变更不影响读路径语义；dashboard 回滚窗口缩短需要业务确认 1h 可接受）

### P0-5 · 补 5 个孤儿 `session:gw_*` 无 TTL hash

- **背景**：子代理 B §6.1 — 50 个无 TTL key 中，5 个 `session:gw_*` hash 是**异常**（理论上与全局 ~19.9d TTL 策略不符）。可能是 touch 路径覆盖、写入跳过 SETEX、或迁移残留。
- **具体动作**：
  1. **一次性修复脚本**（`scripts/ops/repair_orphan_session_ttl.sh`）：
     ```bash
     #!/bin/bash
     # 修复 5 个孤儿 session:gw_* 加 TTL
     for key in $(redis-cli -h 172.16.2.210 -p 6389 -a Veritrans9900 -n 2 KEYS "session:gw_*" | xargs -I {} sh -c 'redis-cli -h 172.16.2.210 -p 6389 -a Veritrans9900 -n 2 TTL {} | grep -q "^-1$" && echo {}'); do
         redis-cli -h 172.16.2.210 -p 6389 -a Veritrans9900 -n 2 EXPIRE "$key" 1717600  # 19.9d
         echo "Fixed: $key"
     done
     ```
  2. **写入路径审计**：`domains/session/v2/cache_v2_redis.go` SETEX/HSet 调用栈检查是否在 `Update` / `Touch` 分支跳过 TTL 设置
  3. **监控告警**：`redis_keys_without_ttl{db="2",prefix="session:gw_*"} > 10` 持续 5min 触发
- **预计影响**：5 个孤儿立即恢复 TTL；后续无 TTL 增长被监控捕获
- **业务风险**：极低（仅是给已有数据加过期时间）
- **验证方式**：
  - 修复后：`redis-cli -n 2 KEYS "session:gw_*" | xargs -I {} redis-cli -n 2 TTL {} | sort -u` 不应再有 `-1`
  - 7d 后再审计无 TTL 列表，孤儿数量保持 0
- **需 154 联调**：❌（154 端跑同一脚本即可，但建议 245 先验证）

---

### P1-1 · 设置 `maxmemory=1.5G` + `volatile-lru` + 容器 cgroup 2G

- **背景**：子代理 A §5 — 当前 `maxmemory=0` + `noeviction`，写满即业务中断。建议 `1.5 GB`（留 50% 缓冲）+ `volatile-lru`（仅驱逐有 TTL key，保护 `disguise:*` 等永久 key）。
- **具体动作**：
  ```bash
  # 1. CONFIG SET（运行时立即生效）
  podman exec pms-redis redis-cli -a Veritrans9900 CONFIG SET maxmemory 1.5gb
  podman exec pms-redis redis-cli -a Veritrans9900 CONFIG SET maxmemory-policy volatile-lru
  podman exec pms-redis redis-cli -a Veritrans9900 CONFIG REWRITE  # 持久化
  
  # 2. 容器 cgroup 上限（需重启）
  podman stop pms-redis
  podman run -d --name pms-redis \
    --memory=2g --memory-reservation=1.5g \
    # ... 保留原有 -v / -p / -e 标志
  # 或：更新 podman systemd unit 配置
  ```
  - 告警阈值：1200 MB (80%) warn，1400 MB (93%) critical
- **预计影响**：
  - 内存稳态 768MB → 上限 1536MB（满载 100% 时也有 50% 当前用量空间）
  - 写满时仅驱逐有 TTL key，业务不中断
  - 容器 cgroup 2G 防止 PMS 侧失控牵连 252 宿主其他服务
- **业务风险**：**中** — 154 生产 + 245 pre-prod 同时受影响：
  1. 任何写 hot key 但 TTL 缺失的场景会被驱逐（业务需复核 `disguise:*`、`ursm:v2:node:default:*`、`llmgw:stats:body:*`、`ursm:v2:meta:ready` 这 4 类 ~44 个永久 key，确认不会被错误驱逐）
  2. 154 业务侧需观察 24h 是否有 "写入失败" 错误（理论上 `volatile-lru` 不应失败）
- **验证方式**：
  - `INFO memory` 监控 `maxmemory:1572864000` + `maxmemory_policy:volatile-lru`
  - `INFO stats` 看 `evicted_keys` 增长速率（24h 应 < 100，否则 TTL 策略需调整）
  - 154 / 245 业务指标（error rate、Redis timeout）无抬升
- **需 154 联调**：⚠️ **必须 154 通知**（变更前 24h 邮件告警 154 owner，变更后 7d 紧密观察）

### P1-2 · 减少 `SCRIPT FLUSH` 频次 + 用 `FUNCTION LOAD` 预热

- **背景**：子代理 C §3.8 / §4 — EVALSHA 失败率 3.4%（1184/34469）主因是 `SCRIPT FLUSH`（290 次）后缓存失效，每次触发 EVALSHA → EVAL 回退风暴。慢日志里大体积 EVAL（10–50 ms）几乎全是 URSM 测试 / cleanup-cas 脚本。
- **具体动作**：
  1. **部署脚本改造**：把 `SCRIPT FLUSH` 从"每次 deploy 前"改为"仅 Redis 版本升级时"
  2. **预热机制**：deploy 完成后立即 `FUNCTION LOAD` 或 `SCRIPT LOAD` 关键脚本：
     ```bash
     # scripts/deploy/post-deploy-redis-preload.sh
     #!/bin/bash
     SCRIPTS=(
       "ursm:cleanup-cas"
       "ursm:v2:realsmoke:apply_probe"
       # ... 所有业务 Lua 脚本 SHA
     )
     for sha_file in "${SCRIPTS[@]}"; do
       cat "$sha_file.lua" | redis-cli -h 172.16.2.210 -p 6389 -a Veritrans9900 -x FUNCTION LOAD REPLACE
     done
     ```
  3. **大脚本拆分**：URSM 测试脚本 > 4KB 的拆分为多个小脚本或下沉到 `FUNCTION`
- **预计影响**：
  - EVALSHA 失败率 3.4% → < 0.5%
  - 慢查询中 EVAL 大脚本出现频次降 50%+
  - 部署后短暂回退 EVAL 减少
- **业务风险**：低（仅优化缓存预热，不改业务逻辑）
- **验证方式**：
  - `INFO commandstats` 监控 `cmdstat_evalsha.failed_calls` 24h 增量
  - `SLOWLOG GET 100` 24h 后大体积 EVAL 计数对比
- **需 154 联调**：⚠️ **通知**（部署脚本改 154/245 都生效，建议 245 先验证）

### P1-3 · 缩短 `llmgw:stats:delta:*` TTL（30d → 3d）

- **背景**：子代理 A §9 F5 + 子代理 B §6.2 — `llmgw:stats:delta:*` TTL 30d，与 `REDIS_CACHE_AUDIT_2026-07-23.md` 已知问题对齐；stat 数据 3d 已足够业务对账。
- **具体动作**：
  ```go
  // domains/stats/redis_store.go
  - const StatsDeltaTTL = 30 * 24 * time.Hour
  + const StatsDeltaTTL = 3 * 24 * time.Hour
  ```
  - 同步更新 `scripts/apply_redis_ttl_optimization.sh` 现有 `llmgw:stats:delta:*` 30d → 3d 规则
  - 历史 30d 数据的留存评估：业务侧确认无 30d stat 查询需求
- **预计影响**：
  - `llmgw:stats:delta:*` 占用下降 ~90%（30d → 3d，假设均匀增长）
  - 估算节省 ~5–10 MB
- **业务风险**：**低-中**（业务需确认 stat 查询窗口 ≤ 3d；handoff 提到 stats 用于 delta 计算，3d 足够）
- **验证方式**：
  - 部署后 7d：`redis-cli -n 2 KEYS "llmgw:stats:delta:*" | wc -l` 应下降 ~90%
  - 业务侧 stat 查询抽样验证
- **需 154 联调**：❌

### P1-4 · 启用 `rename-command KEYS ""` / FLUSHDB / FLUSHALL 兜底

- **背景**：子代理 C §3.1 + §5.1 — KEYS 调用已收敛（仅 2 次）但单次 16ms 是 O(N) 风险；FLUSHDB/FLUSHALL 是误操作灾难命令。`rename-command` 到 `""` 是禁用。
- **具体动作**（`pms-redis` redis.conf）：
  ```conf
  # 生产兜底
  rename-command KEYS ""
  rename-command FLUSHDB ""
  rename-command FLUSHALL ""
  rename-command CONFIG "CONFIG_b7f3c2"  # 不禁用但改名
  rename-command DEBUG ""
  rename-command SHUTDOWN ""
  ```
  - 注意：禁用 KEYS 后，`scripts/ops/audit-redis-keyspace.sh` 必须改用 SCAN
- **预计影响**：
  - 杜绝生产 KEYS O(N) 阻塞
  - 防止误 FLUSHDB/FLUSHALL 灾难
- **业务风险**：低（KEYS 已收敛、FLUSHDB/FLUSHALL 运维已用 SCAN + DBSIZE）
- **验证方式**：
  - 部署后：`redis-cli -a Veritrans9900 KEYS "*"` 应返回 `(error) ERR unknown command 'KEYS'`
  - `redis-cli -a Veritrans9900 FLUSHDB` 同上
- **需 154 联调**：⚠️ **通知**（运维脚本需同步改 SCAN）

### P1-5 · 调 `hash-max-listpack-entries=64` + DEBUG RELOAD 迁移

- **背景**：子代理 B §5.1 — `session:gw_*` hash 18 fields < 128 阈值但仍 hashtable，**浪费 ~25% 内存**。预估 23,529 keys × 1,667 B → ~9 MB 节省。
- **具体动作**（`pms-redis` redis.conf）：
  ```conf
  hash-max-listpack-entries 64   # 当前 128
  hash-max-listpack-value 64    # 维持
  ```
  - 配置生效后 `DEBUG RELOAD` 触发迁移（**只读审计不动**，此处是变更建议）
- **预计影响**：
  - `session:gw_*` 内存下降 ~25%（~9 MB）
  - `OBJECT ENCODING session:gw_<sample>` 应从 `hashtable` 变 `listpack`
- **业务风险**：**中**（DEBUG RELOAD 是阻塞操作，154 流量峰值期会卡 1–2 秒；建议 245 低峰期验证再推 154）
- **验证方式**：
  - DEBUG RELOAD 前：`OBJECT ENCODING session:gw_<sample>` = `hashtable`
  - DEBUG RELOAD 后：`OBJECT ENCODING session:gw_<sample>` = `listpack`
  - `INFO memory` used_memory 下降 ~9 MB
- **需 154 联调**：⚠️ **通知**（DEBUG RELOAD 在共享实例执行，影响双方）

---

### P2-1 · `RATE_LIMIT_REDIS_URL` fallback 到主 Redis

- **背景**：子代理 A §7.3 — `ratelimit/redis_sliding.go:161-166` 优先 `RATE_LIMIT_REDIS_URL`，缺省 `REDIS_URL`，均缺省走 in-process。当前生产未误设，但若运维误配会绕开统一 DB。
- **具体动作**：
  ```go
  // ratelimit/redis_sliding.go
  url := strings.TrimSpace(os.Getenv("RATE_LIMIT_REDIS_URL"))
  if url == "" {
      url = strings.TrimSpace(os.Getenv("REDIS_URL"))
  }
  if url == "" {
      url = buildFromMainConfig()  // 拼接 LLM_GATEWAY_REDIS_ADDR+DB
      slog.Info("rate limit redis falling back to LLM_GATEWAY_REDIS_ADDR")
  }
  ```
- **预计影响**：消除误配隐患；rate limiter 与主网关共享 db 视图
- **业务风险**：极低（仅 fallback 路径）
- **验证方式**：
  - 单测：env 全清，rate limiter 应连主 redis（db=2）
  - 部署后：rate limit metrics 仍上报
- **需 154 联调**：❌

### P2-2 · 加 family-level owner/TTL policy SSOT + Prometheus 告警

- **背景**：子代理 A §4.4 + 子代理 B §11 #5 — 缺 SSOT 定义每 prefix 的 owner / TTL 上限 / member 上限，导致新增 redis key 无 TTL 规范。
- **具体动作**：
  1. 新增 `docs/04-implementation/redis-namespace-policy.yaml`：
     ```yaml
     version: 1
     updated: 2026-08-25
     families:
       - prefix: "session:gw_*"
         owner: session-governance
         ttl_default: 19.9d
         max_members: 1
         notes: "网关会话主状态，不应拆字段"
       - prefix: "session:key:*"
         owner: session-governance
         ttl_default: 19.9d
         max_members: 1
       - prefix: "session:apiKey:*:active"
         owner: session-governance
         ttl_default: 19.9d
         max_members: 5000
         alert_if_exceeds: 5000
       - prefix: "llmgw:live:actions"
         owner: live-stream
         ttl_default: 1h
         max_members: 500
         notes: "LTRIM 强制"
       - prefix: "llmgw:stats:delta:*"
         owner: stats
         ttl_default: 3d
       - prefix: "disguise:*"
         owner: disguise
         ttl_default: never
         max_keys: 100
       # ...
     ```
  2. CI gate：新 PR 涉及 redis SET/HSET/SADD 等新增 prefix，必须在 policy 文件登记
  3. Prometheus 告警（新增 `scripts/grafana/redis-policy-alerts.yaml`）：
     - `redis_keys_without_ttl{db="2",prefix=~"session:.*"} > 100` 持续 5min warn
     - `redis_db2_keys 24h 增量 > 5%` warn
     - `redis_evicted_keys 1h > 1000` critical（驱逐风暴）
- **预计影响**：建立可执行 SSOT；新增 key 强制登记
- **业务风险**：低
- **验证方式**：
  - 走一遍所有现有 redis key 写入路径，确认每 prefix 在 policy 登记
  - Grafana 告警规则 dry-run 1 周
- **需 154 联调**：❌

### P2-3 · MGET/HGETALL payload 大小监控（>1 MB 告警）

- **背景**：子代理 C §3.4 / §3.6 — MGET 平均 1.48 ms / HGETALL 累积风险，需 payload 大小告警。
- **具体动作**：
  - 在 `cmd/gateway/main.go` redis client wrapper 加 `with_size_limit`：
    ```go
    func instrumentedMGet(ctx context.Context, keys []string) ([]string, error) {
        // 调用前估算 payload
        estSize := len(keys) * avgPayloadPerKey
        if estSize > 1*1024*1024 {
            slog.WarnContext(ctx, "MGET large payload", "keys", len(keys), "est_bytes", estSize)
            metrics.RecordRedisLargePayload("MGET", estSize)
        }
        // 调用
    }
    ```
  - Prometheus 指标：`redis_large_payload_bytes{cmd="MGET|HGETALL"}` histogram
  - Grafana 告警：`histogram_quantile(0.99, redis_large_payload_bytes_bucket{cmd="MGET"}) > 1048576` warn
- **预计影响**：可观测性增强；为后续拆批（≤ 64 keys）提供数据支撑
- **业务风险**：无（仅加监控，不改调用）
- **验证方式**：Grafana dashboard 新增 panel；24h 数据回看
- **需 154 联调**：❌

### P2-4 · EVALSHA NOSCRIPT 失败率监控

- **背景**：子代理 C §4 #2 — EVALSHA 失败率 3.4% 与 `SCRIPT FLUSH` 联动，需量化指标。
- **具体动作**：
  - 新增 `cmd/redis-exporter` 或复用 `redis_exporter`（如已部署）的 `cmdstat_evalsha_failed_total` 抓取
  - Prometheus alert rule：`rate(redis_evalsha_failures_total[5m]) > 10` critical（部署期除外，加抑制规则）
- **预计影响**：EVALSHA 风暴可被秒级告警
- **业务风险**：无
- **验证方式**：人工触发 `SCRIPT FLUSH` 验证告警生效
- **需 154 联调**：❌

### P2-5 · 客户端 ACL 限制 + protected-mode

- **背景**：子代理 C §5.4 — 客户端 IP 集中在 172.16.2.241/209/127.0.0.1，可用 ACL 锁紧。
- **具体动作**（`pms-redis` redis.conf）：
  ```conf
  bind 172.16.2.241 172.16.2.209 127.0.0.1
  protected-mode yes
  
  # ACL（Redis 6+）
  user llmgw-245 on >Veritrans9900 ~* +@all -keys -flushdb -flushall -config -debug -shutdown
  user llmgw-154 on >Veritrans9900 ~* +@all -keys -flushdb -flushall -config -debug -shutdown
  user pms on >Veritrans9900 ~* +@all
  user audit on >Veritrans9900 ~* +@read +slowlog +command
  ```
- **预计影响**：单实例误连/误操作风险下降
- **业务风险**：**中**（ACL 错误配置会导致业务连接失败；必须 245 验证后推 154）
- **验证方式**：
  - 154/245 业务路径全链路回归
  - 异常 ACL user 连接应被拒绝
- **需 154 联调**：⚠️ **通知 + 联调**（涉及连接层，154 owner 必审）

### P2-6 · 观察 30 天增长曲线 → 触发独立容器拆分（方案 A）

- **背景**：子代理 A §6.3 — 短期不拆，建议**方案 C（现状 + maxmemory 调优）立即**，**方案 A（新建 `pms-redis-llmgw:6380`）作为 3-6 个月后增长触发的备选**。
- **触发条件**：
  - 连续 7d `used_memory > 1.2 GB` (maxmemory 80%)
  - 或 `evicted_keys` 24h > 10,000（驱逐风暴）
  - 或 154 流量翻倍触发 db2 增速 > 50k keys/d
- **具体动作**（触发后）：
  1. 252 新建容器 `pms-redis-llmgw`（redis:7-alpine, port 6380）
  2. 数据迁移：`SCAN` + `DUMP` + `RESTORE` 流水线（低峰期）
  3. 154/245 `.env` 改 `LLM_GATEWAY_REDIS_ADDR=172.16.2.210:6380`
  4. 双写过渡期 7d（旧实例保留观察）
  5. 切流量后旧实例冻结 30d 再下线
- **预计影响**：154/245 故障域与 PMS 解耦；可独立调 maxmemory
- **业务风险**：高（实例迁移涉及 154 + 245 同步，必须灰度）
- **验证方式**：30 天监控数据 + 触发条件判定
- **需 154 联调**：✅ **触发后必联调**

### P2-7 · `promote_request_wal_hot_to_partition` SQL stub 实现

- **背景**：handoff §7 阻塞 — 当前 SQL stub 无 `created_at` 归档实现路径，正确做法是新 migration 加 `promote_request_wal_hot_to_partition(created_at)` function + DROP stub。
- **具体动作**：
  1. 新建 `sql/migrations/startup/573_promote_wal_hot_with_created_at.sql`
     ```sql
     CREATE OR REPLACE FUNCTION promote_request_wal_hot_to_partition(p_created_at TIMESTAMPTZ)
     RETURNS BIGINT AS $$
     DECLARE
       v_count BIGINT;
     BEGIN
       -- 拷贝到对应 partition（heap → heap，无 0A000 风险）
       WITH moved AS (
         DELETE FROM request_wal_hot
         WHERE created_at < p_created_at
         RETURNING *
       )
       INSERT INTO request_logs_partition_dispatcher(moved.*)  -- 通过 dispatcher 函数
       SELECT count(*) INTO v_count FROM moved;
       RETURN v_count;
     END;
     $$ LANGUAGE plpgsql;
     ```
  2. DROP 现有 stub 函数
  3. 加单测 `sql/migrations/startup/573_promote_wal_hot_with_created_at_test.sql`
- **预计影响**：wal_hot 表停止无界增长（245 当前 6.2 万行）
- **业务风险**：**高**（SQL migration 涉及 schema 变更，154 必须同步）
- **验证方式**：
  - 245 灰度：调用函数前/后 `SELECT count(*) FROM request_wal_hot` 验证行数下降
  - 数据完整性：抽样对比 `request_logs_<partition>` 行数
- **需 154 联调**：✅ **必须双发**（schema migration 不可分头）

---

## 退出条件（Exit Criteria）

### 本批 ICR 验收
- [ ] P0-1 154 build_seq ≥ 1732 部署完成
- [ ] P0-2 `session:v2` db=0 残留 ≤ 0（24h 观察）
- [ ] P0-3 `latency-history` 进程清理
- [ ] P0-4 `llmgw:live:actions` LLEN ≤ 500 + TTL 已设
- [ ] P0-5 孤儿 `session:gw_*` 数量归零
- [ ] P1-1 `maxmemory=1.5gb` + `volatile-lru` 已生效；`evicted_keys` 24h < 100
- [ ] P1-2 EVALSHA 失败率 < 0.5%
- [ ] P1-3 `llmgw:stats:delta:*` TTL 3d 已生效；key 数下降 ~90%
- [ ] P1-4 `KEYS`/`FLUSHDB`/`FLUSHALL` 已 `rename-command` 禁用
- [ ] P1-5 `session:gw_*` 编码从 hashtable → listpack（抽样验证）
- [ ] `redis-namespace-policy.yaml` SSOT 已建立
- [ ] Grafana 新增 ≥ 5 条 Redis 告警规则

### 长期跟踪（30d 内）
- [ ] `used_memory` 24h 增长曲线建立
- [ ] P2-6 拆分触发条件数据采集
- [ ] P2-7 migration 落地（依赖 154 联调窗口）

---

## 同步

- **上游基线**：`main @ 06408163c`（build_seq 1731 已 push origin/main）
- **关联 handoff**：
  - [`/tmp/handoff/handoff-20260825-0035-245-audit.md`](../../../handoff/handoff-20260825-0035-245-audit.md)
  - [`/tmp/handoff/handoff-20260825-023014-245-redis-pg-audit.md`](../../../handoff/handoff-20260825-023014-245-redis-pg-audit.md)
- **关联子报告**：
  - [`/tmp/audit-redis-multitenant.md`](../../../../../../tmp/audit-redis-multitenant.md)（子代理 A）
  - [`/tmp/audit-redis-large-values.md`](../../../../../../tmp/audit-redis-large-values.md)（子代理 B）
  - [`/tmp/audit-redis-slow-queries.md`](../../../../../../tmp/audit-redis-slow-queries.md)（子代理 C）
- **关联 lessons-learned**：
  - `docs/archive/process/process/2026-08/lessons-learned-2026-08-04-swimlane-jump-redis-timeout.md`（db2 隔离决策）
- **关联 deploy 脚本**：
  - `scripts/deploy/deploy-to-245.sh`
  - `scripts/deploy-154-via-252.sh`
  - `scripts/clean_redis_leaks.sh`
  - `scripts/apply_redis_ttl_optimization.sh`

---

## 引用

- 子代理 A §6 多租户决策 / §9 关键发现汇总
- 子代理 B §5 大 Key 深度评估 / §8 优化建议
- 子代理 C §4 关键发现 / §5 治理建议（配置层 / 应用层 / 监控层 / 安全层）
- 子代理 D 合并于 A 报告 §7（代码层 owner / DB index 用法）
- handoff §7 阻塞 / 风险（本 ICR 全部 P0 项对齐）

---

**ICR 编制时间**：2026-08-25
**预期合入窗口**：P0 当周 / P1 1 周内 / P2 1-3 个月触发
**作者**：本次会话（综合子代理 A/B/C/D 输出）

---

## 实施落地状态（Post-A5 follow-up @ build_seq 1736）

> 2026-08-25 二次会话（接 ICR-A5 编制后）：对 P0 + 部分 P2 项完成代码修复并部署到 245。

### 本批已落地代码修复（commit pending, push 待执行）

| ICR ID | 修复 | 状态 | 验证 |
|---|---|---|---|
| **P0-2** | `session:v2` DB0 → cfg.RedisDB (==2) | ✅ 代码 + 测试 | `NewSessionCacheV2(db, addr, redisDB)` / 4 处测试 caller 已更新 / `ratelimit/ratelimit/ratelimit` 链路 gateway version 1736 启动日志确认 `addr=172.16.2.210:6389 db=2` |
| **P0-4** | `llmgw:live:actions` LTRIM 5000 + Expire 24h 续约 | ✅ 代码 + 测试 | `internal/liveactions/liveactions.go` 加 `pipe.Expire(ctx, RedisKey, redisKeyTTL)` + `redisKeyTTL = 24h` 常量 |
| **P2-1** | rate limiter fallback `LLM_GATEWAY_REDIS_ADDR` | ✅ 代码 + 测试 | `ratelimit/redis_sliding.go` NewRedisLimiterFromEnv 加 LLM_GATEWAY_REDIS_ADDR/DB/PASSWORD fallback, 启动日志确认 `rate limiter using main gateway Redis (LLM_GATEWAY_REDIS_ADDR fallback)` |
| **子代理修复** | 压缩/上下文模块 system-reminder 过滤 + body budget trim | ✅ 代码 + 测试（保留子代理未提交改动） | 12 个文件 +229/-35: `domains/hooks/compression/*` + `domains/transformation/ctx_compress.go` |
| **运维清理** | 4 个遗留 redis-cli --latency-history 进程（20 天） | ✅ kill -9 on host | `ps -p 2225176,2225509,2225825,2226209` 已无输出 |

### 部署验证（build_seq 1736 @ 245）

| 指标 | build_seq 1731 | build_seq 1736 | 评估 |
|---|---|---|---|
| 网关 RSS | 224MB（启动期）→ ~500MB（稳态）| 333MB（40s 运行）| 正常 |
| cgroup MemoryCurrent | 312MB / 3GB | 321MB / 3GB | 健康 |
| ColumnarScan 错误/4min | 0 | 0 | 持续为 0 |
| ERROR 数 / 4min | 0 | 1（启动期单次）| 接近 0 |
| snapshot throttle 设置 | ✅ | ✅ | 持续 |
| rate limiter fallback | n/a (env 配置) | ✅ | 主 db2 生效 |

### 本批未落地（依赖 154 联调或大规模改造）

| ICR ID | 状态 | 阻塞 |
|---|---|---|
| **P0-1** | ⏸️ 154 同步部署 build_seq 1731+1736 | 公网 SSH 故障，需走 `deploy-154-via-252.sh` |
| **P0-5** | ⏸️ 5 个孤儿 session:gw_* TTL 修复脚本 | 需独立脚本（`scripts/ops/repair_orphan_session_ttl.sh`） |
| **P1-1** | ⏸️ `maxmemory=1.5gb` + `volatile-lru` | 252 容器配置变更，需 154 通知 |
| **P1-2** | ⏸️ SCRIPT FLUSH 治理 | 部署脚本改造，影响 deploy 链路 |
| **P1-3** | ⏸️ `llmgw:stats:delta:*` TTL 30d→3d | 业务影响面广，需业务确认 |
| **P1-4** | ⏸️ `rename-command KEYS ""` / FLUSHDB / FLUSHALL | redis.conf 变更 |
| **P1-5** | ⏸️ `hash-max-listpack-entries=64` | redis.conf 变更 + DEBUG RELOAD 阻塞风险 |
| **P2-2 ~ P2-7** | ⏸️ 监控/告警/SSOT/拆分/migration | 中长期 |

### 下一步

1. commit 本批修复并 push origin/main（本次会话直接完成）
2. 154 同步部署 1736（按 P0-1 流程走 `deploy-154-via-252.sh`）
3. P1 改动项按 PR 走 154 联调
4. P2 改动项安排后续会话

---

**会话二补充时间**：2026-08-25 05:25（接 ICR-A5 编制后约 2 小时）
**作者**：本次会话（基于子代理 A/B/C/D 报告的具体修复）

---

## 验证发现 P0 隐患（Post-deploy 发现于 build_seq 1736 部署后验证）

> 2026-08-25 03:30+ UTC：build_seq 1736 部署后做 L1/L2/L3 健康检查时发现。
> 245 网关跑生产 schema 与代码不同步 → INSERT 持续触发 SQLSTATE 42703 错误。

### 现象

`/var/log/llm-gateway-go/gateway.stderr.log` 中：
```json
{"level":"WARN","msg":"telemetry request db persist failed; fallback written",
 "request_id":"...","op":"insert",
 "error":"ERROR: column \"request_body\" of relation \"request_logs_hot\" does not exist (SQLSTATE 42703)"}
```

每个 INSERT/UPDATE 请求写主表都失败，被 fallback 写到 `request_logs_bodies_hot` + Redis trace 滞留（trace flush 失败）。

### 根因

**migration 573 `drop_request_logs_body_columns.sql`（2026-08-19 已部署到 252 PG17）** DROP 了：
- `request_logs_hot.request_body`（2026-07-22 已迁至 `request_logs_bodies_hot`）
- `request_logs_hot.response_body`
- `request_logs_hot.outbound_body`（2026-08-24 Phase 1 已迁）

但 **Go 客户端代码 `domains/hooks/observability/telemetry/client.go` 未同步删除 INSERT/UPDATE 中的列名和占位符**。

注释已更新（"-- 2026-08-24 Phase 1: outbound_body routes to request_logs_bodies_hot"），但实际 SQL 仍包含 `request_body, response_body,` 等。

### 影响

- 每次请求 INSERT 都失败（fallback 落盘，request_logs_hot 失数据）
- 每次 UPDATE 也失败（请求状态无法更新到主表）
- Redis trace 累积（trace.FlushToPG: request log row not found, retaining Redis trace ~316 次/4min）
- 大约 22 万请求/h × INSERT/UPDATE 失败率近 100% → request_logs_hot 主表几乎为空
- 不影响请求路径（fallback 兜底），但**审计/账本/分析数据完全丢失到 hot 表**

### 验证（已现场确认）

```sql
-- 252 PG17 上 request_logs_hot 表
SELECT count(*) FROM information_schema.columns
 WHERE table_name = 'request_logs_hot'
   AND column_name IN ('request_body', 'response_body', 'outbound_body');
-- 0 rows (confirmed: columns dropped)
```

### 尝试的修复

在本会话中尝试修复 `client.go` 的 INSERT 列名 + 占位符对齐（删 3 个 body 列 + 重新编号 $N 占位符），但**手工重排 100+ 个占位符错误率高、风险大**，未提交。

代码已 `git checkout HEAD -- domains/hooks/observability/telemetry/client.go` 还原。

### 修复指南（后续会话建议）

**最小化安全修法**：用 Postgres 部分列 ADD 回占位符（接受NULL）+ 不动 Go 代码：
```sql
ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS request_body jsonb;
ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS response_body jsonb;
ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS outbound_body jsonb;
```
缺点：浪费存储、再次与 body hot 表分裂。

**推荐修法**：写一次性 migration，强制对齐：
1. Go 端：删 INSERT 列名列表中 `request_body, response_body,` + `outbound_body,`
2. Go 端：删 Go args 列表中 `nil, // request_body` + `nil, // response_body` + `nil, // outbound_body`
3. Go 端：重排 VALUES 中后续所有 `$N` 占位符（手动降序重排极易出错）
4. 测试：用 pgx 跑 `TestRequestLogInsertParamCount` live DB 测试（已存在）端到端验证
5. 部署：先在 245 灰度 24h，验证 `telemetry request db persist failed` 归零 + `request_logs_hot` 行数开始增长

**或**：使用代码生成器（sqlc / sqlc-go）从 schema 自动生成 INSERT 语句，根除人工漂移。

### 优先级

**P0 业务影响**——fallback 已工作，但 request_logs_hot 主表近乎空写，影响所有依赖主表的查询（admin dashboard、telemetry、计费对账、batch promote）。

### 245 实际状态

由于 245 部署架构是符号链接 `current → releases/<build_seq>-<git_sha>`，本次本会话的尝试性 INSERT 修复（build_seq 1737）**未真正部署**——245 仍跑 `releases/1737-8bd18f36/gateway`（即原始 1737 build，并非本会话 1737）。这意味着：

- ✅ 245 健康运行，未受中间尝试状态影响
- ❌ 但 42703 错误持续发生，main 表近乎空写
- ✅ 本会话 ac83c4665 推送的 1736/A5 代码修复（session:v2 db0、live-stream TTL、rate limiter fallback）**已在 154 走 seamless deploy**，**未受影响**

### 后续行动

1. **下个 ICR 会话专门修复 42703 bug**（建议拆独立任务，避免本会话这种"半成状态"）
2. 部署需严格走 `scripts/deploy-245.sh`（不直接 `mv`/`cp`，会破坏符号链接）
3. 测试需要 `TestRequestLogInsertParamCount` live DB 验证（需要 LLM_GATEWAY_PG_TEST_URL 网络连通）

---

**会话三补充时间**：2026-08-25 05:48（验证 build_seq 1736 后）
**作者**：本次会话（健康检查验证 + 文档化 42703 bug）
