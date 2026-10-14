# 2026-10-14 晚间轮 — D1/D2 部署 + URSM 独立 Redis DB

## 交付结论

| 目标 | 状态 | 证据 |
|---|---|---|
| D1/D2 → 245 | ✅ | `2634-f280c9dc` :8781；`v1_write_liveness: retired` |
| D1/D2 → 154 | ✅ | 当前 `2637-*` :8781（本轮先上 `2636-f280c9dc`，并行会话随后推到 2637）；`retired` + `dedicated redis db=14` |
| D1/D2 → 211 | ✅ | 交叉编译二进制热换镜像 `llm-gateway-211:d1d2-2637`；`retired` + reconciler + system_monitor |
| URSM → 独立 Redis DB | ✅ | 共享 Redis `URSM_V2_REDIS_DB=14`（154/245）；211 本机 Redis `db=14`（会话库 db5） |
| persist timeout 缓解 | ✅ | 245/154 近 10min：`persist collect failed=0`，`persist committed≈10` |
| 252 慢 SQL/load 催办 | 📋 | 见下「owner 拍板/催办」 |
| nvidia egress / mac backup | 📋 | 见下 |
| 窗口外分支 / `-2` worktree | ⏸ | 仍待授权，未删 |

## 执行要点

1. **URSM 两遍复制**（245 跑 `scripts/ursm-redis-db-migrate.py`）  
   - Pass1：db2→db14，copied=1601  
   - 两台设 `URSM_V2_REDIS_DB=14` 后部署/重启  
   - Pass2：`--force` 补复制，copied=1451  
2. **245** `deploy-245.sh --seq 2633 --force`（清陈旧本地锁）→ 实际 `2634-f280c9dc`  
3. **154** 首轮因 252 load≈33 / EnsureSchema 超时回滚；加长 probe 后 `2636` 成功；其后并行会话推到 `2637`（行为仍含 D2 + db14）  
4. **211** `deploy-211-docker.sh --via` 的 tar 同步不完整 + docker build 卡在 `apk add gcc`；改为本机 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` 交叉编译 + 薄镜像覆盖；脚本已补 `SYSTEM_MONITOR` / `URSM_V2_REDIS_DB` 注入（工作区未提交）

## Owner 拍板 / 催办

### 252 load / 慢 SQL（催办）

- 部署窗实测 load **33→10**；主凶曾是  
  `python -m src.cli data backfill --target fundamentals --max 150`  
  （PID 1264457，跑约 7h，~67% CPU；收尾时已不见）。  
- Redis-server 同期高 CPU；PG `pg_stat_activity`≈99。  
- **建议**：给 backfill 加超时/单实例锁；慢 SQL（`analyze_llm_gateway_table_stats` 等）保持产品作业不改调度，但 load>20 时暂停非关键批处理。

### nvidia egress（拍板）

- gateway-mode probe 502/503、direct OK —— 既有登记：egress 代理缺失。  
- **待 owner**：修代理链路 / 降权 nvidia 凭据 / 维持现状。

### mac mini backup 腿（拍板）

- 252 现行 `llm.itestu.cn.conf`：  
  `server 100.106.169.63:8782 ... backup;`  
- mac mini 本地栈已消失 ⇒ backup 为死节点。  
- **待 owner**：摘 backup 腿 / 恢复 mac 热备 / 另指备用机。

### 分支与 `-2` worktree（授权）

- 窗口外 3 条九月远端分支仍留置。  
- `-2` checkout 的已合并本地分支 + linked worktree：**未动**，等一句话授权。

## 回滚

- URSM：去掉 `URSM_V2_REDIS_DB` 并重启（源 db2 键未删）。  
- 245/154：`bash scripts/deploy-seamless.sh rollback <target>`  
- 211：回切前一镜像 tag 或恢复容器。
