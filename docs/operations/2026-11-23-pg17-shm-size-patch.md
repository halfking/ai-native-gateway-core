# N11 补丁草案：211 `acc-pg17` 的 `/dev/shm` 放大到 1GB

> ★ **状态：★ 未实施（★ 等 owner 一句授权）**
> ★ **归属：★ 跨仓**（★ 目标文件在 `/opt/kaixuan/stacks/platform/docker-compose.platform.yml`，★ **不在任何 git 仓库里** ⇒ ⇒ ★ **本仓只留这份可审草案** ⇒ ★ 改动落地后请把实际 diff 回填本文件）
> ★ **主档**：`docs/operations/2026-11-23-211配置优化执行报告.md` §9.19·附4（根因与处置方案）＋ §9.27 ⑾''''''' ⑨·补（补丁誊回本仓的建议）
> ★ **只读现况核查器**：`scripts/pg17-shm-preflight.sh`（★ 严格只读、★ 一条命令出「现况 ＋ 爆炸半径」）

---

## ① 要改什么（一行）

`/opt/kaixuan/stacks/platform/docker-compose.platform.yml` 的 `acc_pg17` 服务段加一行 `shm_size: "1gb"`。

★ **为什么是 compose 键而不是启动参数**：★ compose v2 原生支持 `shm_size`（★ 等价于 `--shm-size 1g`）⇒ ★ 不必改 `entrypoint` / `command` ⇒ ★ 改动面最小。
★ ★ **为什么必须是这一条启动路径**：★ 仓内 `scripts/252-monitor/manual/pg17-start.sh` **早就带了 `--shm-size 1g`**（★ 2026-09-23 加入、★ 2026-09-29 的执行记录写「本次生效」）⇒ ★★ **但那不是 211 的启动路径**（★ 211 由 compose 起、★ 且 2026-11-09 被 compose replace 过一次 ⇒ ⇒ 参数回到 Docker 默认）⇒ ⇒ ★ **「处置存在过」≠「当前启动路径会执行它」**（★ 这是 N10 的同款通用式）。

## ② 精确 diff（★ 按 211 现网 compose 的 `acc_pg17` 段行号写，★ 改前请用核查器复核）

```diff
--- /opt/kaixuan/stacks/platform/docker-compose.platform.yml
+++ /opt/kaixuan/stacks/platform/docker-compose.platform.yml
@@ -114,6 +114,12 @@
   acc_pg17:
     image: "kx-citus-pg17:amd64"
     container_name: "acc-pg17"
     restart: unless-stopped
+    # ★ N11（2026-11-23）：/dev/shm 默认 64MB ⇒ 并行查询的 DSM 段逼近上限
+    #   ⇒ PG 报 53100（could not resize shared memory segment）⇒ 显式放大到 1GB。
+    #   ★ 数据不动：PGDATA 是 bind mount（/opt/kaixuan/data/pg/acc-pg17）⇒ 只换容器壳。
+    #   ★ 生效需要 recreate（★ 须维护窗口）：docker compose … up -d --force-recreate acc_pg17
+    #   ★ 回滚：去掉本段（或改回 "64m"）⇒ 再 recreate 一次。
+    shm_size: "1gb"
     entrypoint: ["docker-entrypoint.sh"]
     command: ["postgres"]
     environment:
```

★ **改前必做**：★ `cp` 一份同目录 `.bak-<时间戳>`（★ 该目录已有十余个 `.bak-*` 快照、★ 沿用同款留痕方式）＋ ★ 跑一次 `bash scripts/pg17-shm-preflight.sh` 确认现状仍是「`shm_size=67108864` ＋ `compose_shm_size_present=no`」。

## ③ 怎么生效（★ 唯一动作）

```bash
docker compose -f /opt/kaixuan/stacks/platform/docker-compose.platform.yml \
  up -d --force-recreate acc_pg17
```

★ **只指定 `acc_pg17` 一个服务** ⇒ ★ compose **不会**连带重建依赖它的其它服务（★ 它们只会短暂连不上它）。
★ **验证**：`docker inspect acc-pg17 --format '{{.HostConfig.ShmSize}}'` ⇒ 应变为 **1,073,741,824**；★ `docker exec acc-pg17 df -h /dev/shm` ⇒ 总量应变 **1.0G**。
★ **健康**：`docker ps --filter name=acc-pg17` ⇒ `healthy`（★ healthcheck 的 `start_period: 30s` ＋ `interval 10s` ＋ `retries 6` ⇒ ★ 冷启动到 healthy 的**机制下限**约 30–90s，★ **实际时长取决于库恢复量、本次未实测** ⇒ ★ 不给更精确的数）。

## ④ 回滚

```bash
# 去掉 shm_size（或改回 "64m"）⇒ 再 recreate 一次
docker compose -f /opt/kaixuan/stacks/platform/docker-compose.platform.yml \
  up -d --force-recreate acc_pg17
```

★ 回滚的代价＝**同样长的一次窗口**（★ 重建 ⇒ PG 重启）⇒ ★ ⇒ **回滚预案必须在动手前就确认过**（★ 改回一行、再跑同一条命令、★ 这条路径本身已在本文件里写死）。

## ⑤ ★ 爆炸半径（★ 2026-11-23 15:4x 实测，★ 来自 `scripts/pg17-shm-preflight.sh`）

★ **6 个网络、共 50+ 个容器连着它**（★ 报告 §9.19·附4 当时只写了「6 个网络」⇒ ★ 本轮量化到容器级）：

| 网络 | 成员容器数 | 关键成员 |
|---|---|---|
| `shared-infra` | **38** | ★ `acc-nginx` / `acc-blue` / `goacos` / `ms-gateway` / `smm` 全家 / `memora` 全家 / `casdoor` / `nbjl-app` |
| `redclaw-local-stack` | **12** | ★ redclaw 的 api / admin / gateway / facade / console / worker / dal / agentcontainer / casdoor / authagent / orchestrator |
| `llm-gateway-net` | **13** | ★ ★ **`llm-gateway-211-a`（★ 在役网关实例 ⇒ 它的主库就是这台）** ＋ `ai-session-manager-asm-1` / `kaixuan-ask-service` / `wm-chain-ask-a` / `memora-*` / `nbjl-redis` |
| `acc-pg-dataplane` | 4 | ★ `acc-pg17` ＋ 三个 `acc-auth-*20261109`（★ 快照/恢复用容器 ⇒ 不受影响） |
| `shiyi_backend` | 2 | ★ `shiyi-shiyi-1` |
| `ai-session-manager` | 2 | ★ `ai-session-manager-asm-1` |

★ **对外暴露**：`0.0.0.0:15432`（★ LAN 内 `salesdecorate` 客户端走它）＋ `127.0.0.1:5432`（★ 宿主回环）。
★ **数据安全**：`PGDATA` 是 **bind mount**（★ `/opt/kaixuan/data/pg/acc-pg17` → `/var/lib/postgresql/data`）⇒ ★ **重建不动数据**（★ 只换容器壳）⇒ ★ ★ **这是本处置唯一让人放心的部分**：★ 数据风险为零、★ 风险全在**可用性窗口**。

★ ⇒ ★ **因此窗口必须覆盖的是「服务不可用」而不是「数据可能丢」** ⇒ ★ 建议：**低峰时段 ＋ 提前在网络层摘掉对 15432 的外部访问 ＋ 窗口内准备好回滚命令**。

## ⑥ ★ 本次仍未闭合的相邻问题（★ 如实登记，★ 不在本补丁范围）

★ **① `/dev/shm` 满了会怎样**：★ PG 的并行查询共享内存（DSM）段分配失败 ⇒ 报 **53100**（`could not resize shared memory segment`）⇒ ★ 表现为**查询报错**，★ **不是**数据损坏 ⇒ ★ 本补丁把「天花板」抬高，★ **不解决**「为什么这些路径会开并行」。
★ **② 已知的免并行路径**：★ 迁移 994 与 `request-logs` 查询已显式 `SET max_parallel_workers_per_gather = 0`（★ 关并行 ⇒ 不占 DSM）⇒ ★ **未收编路径**：★ `dashboardapi` 全家、`getLog` 详情、`top-models`、`lifecycle` 大扫描、★ MV refresher（★ 357 族之外的视图）⇒ ★ 这些路径在补丁生效后**仍是候选**（★ 只是天花板高了一倍）。
★ **③ 落库不在版本控制**：★ `/opt/kaixuan/stacks/platform/` **不是 git 仓库**（★ 实测 `git rev-parse` ⇒ `no`）⇒ ★ 改动无 diff 可审 ⇒ ★ ⇒ **本文件就是那份 diff**（★ 改完请把实际改动回填进来）＋ 留 `.bak-<时间戳>`。

## ⑦ 通用式（★ 本轮新增一条）

★ ★ **「处置存在过」≠「当前启动路径会执行它」** —— ★ 仓内的启动脚本带 `--shm-size 1g`、★ 历史记录写「已生效」⇒ ★ 但 211 的 compose 在 2026-11-09 **replace 过容器** ⇒ ⇒ ★ 参数回到 Docker 默认 ⇒ ⇒ ★ **判定一条处置是否真的生效，要看「当前真正生效的那条启动路径」，而不是看仓库里有没有那行参数**。
★ ★★ **推论**：★ 凡是要**改启动参数**的处置，★ 都必须落进**真正生效的那条路径**（★ compose 键 / 启动脚本 / 编排清单）⇒ ★ ⇒ ★ 并且**要有能查「当前值」的只读量具**（★ 本轮那个量具就是 `scripts/pg17-shm-preflight.sh`）⇒ ★ ⇒ **处置的验收＝「量具读数变了」，而不是「我改了文件」**。
