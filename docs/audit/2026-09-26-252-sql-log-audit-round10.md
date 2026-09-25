# 252 PG SQL 日志审计轮·第十轮（2026-09-26）

以第九轮（docs/audit/2026-09-26-252-sql-log-audit-round9.md）§八提示词为起点：①五项对账延续（含第九轮 FIX-2 残留 4 条归因闭环）；②5,032ms v_routable 健康度反向查观察；③storage-merge 观察期 G2 维持；④D6 容器重建顺延。纪律沿用 ⑪-㉕ + 第七轮 ㉓㉔㉕ + 第八轮 ㉖㉗ + 第九轮 ㉘㉙。

## §〇、取证与起点

- 窗口：2026-09-26 03:21→06:03 CST（161min），第九轮快照末尾起算。快照 109,108 行 / 80MB（252:/tmp/pg252-round10-window.log.gz，本地 /tmp/pg252-round10-window.log）。
- 解析：按 PG stderr 日志头正则切分 entry（duration 块 / ERROR / FATAL / STATEMENT 关联），慢查与错误全部按**查询形状**分层（纪律㉘）。
- 总量：>1s 慢查 **1,073 条（sum 3,133s）**、ERROR 617（user cancel 603 + scheduled_tasks 12 + 其它 2）、FATAL 175（conn lost 161 + smm 认证失败 14）。

## §一、五项对账（161min 窗口 vs 第九轮 55min 基线）

| 项 | 第九轮 55min 基线 | **本轮 161min（折算 55min）** | 评估 |
|---|---|---|---|
| policy/alternatives 取消（FIX-1 真形状） | 0 | **canonical 形状 0**；policy 关键字 cancel 15 条全部为 D2''' featured 探针家族（已登记） | ✅ 零回归 |
| index_hot DELETE >1s（FIX-2） | 4（未归因） | **0**；第九轮 4 条归因=整点饱和尾部（见 §二），本窗口零复发 | ✅ 闭环 |
| cancel 总量 | 549 | **603（≈206/55min，-62%）** | 改善；归因见 §三 |
| catalog 候选慢查（D10 真形状） | 1 | **8（≈2.7/55min）**，mean 1.54s vs 健康值 60ms | ✅ 计划保持 CTE MATERIALIZED；1.5s 为饱和膨胀（§二），非回归 |
| fingerprint 真形状（D11） | 2 | **0** | ✅ |

## §二、本轮主发现：全形状一致膨胀 = 主机 CPU 饱和（非查询退化）

慢查形状普查（前 8 族占 88%）：idx 读 `WITH latest_bucket` ×279（mean 1.76s）、idx 写 `INSERT credential_model_index_hot` ×232（mean 1.81s）、claim UPDATE ×111（1.84s）、recent-success 探针 ×86（1.51s）、usage_facts date_bin ×32（2.13s）、binding/nps 探针 ×43（max 24.8s）……

**关键证据链**：
1. **全部慢查集中于 04:59 之后**——03:21-04:59 共 100 分钟 >1s 慢查≈0，04:59 起所有形状同时爆发并持续到窗口末（非单查询退化形态）；
2. **膨胀幅度跨形状一致**（1.35-1.8s），连 `UPDATE api_keys SET last_used_at WHERE id=` 单行主键更新都 1.35s——计划问题不可能如此均匀；
3. **主机实测：4 核，load 28.89，run queue 峰值 44，vmstat sy 51-54%、wa 8-19%**——内核态抖动型超订（≈7×）。

**饱和源（主机侧取证）**：
- **僵死 vite build**：`node vite build`（buildah build 容器内，/src/frontend）自 09-18 19:32 起 R 态 7d10h、712 CPU-小时，孤儿进程（PPID=1）——恒定吃掉 ~18% 总算力（详见 §四 已处置）；
- redis-server 累计 19.7% CPU + BGSAVE fork 尖峰；pms-dev `*/10` deploy-check `--concurrent 4`；全盘 `find /`+`du containers/storage` 磁盘扫描；8 个 podman hash 定时器 5-10s 级联；PG checkpoint 每 5min 一轮、单轮 recycle 29 个 WAL 段（写放大显著）。

**结论**：本轮「慢 SQL」的主体是共享主机算力被协租户吃满导致的均匀膨胀，PG 侧查询计划质量整体健康（D10/D11/FIX-1/FIX-2 零回归佐证）。

## §三、cancel 603 归因（纪律㉙：按 STATEMENT 关联分类）

| 形状 | 数量 | 归因 |
|---|---|---|
| claim UPDATE is_final_success | 373 | 饱和下 claim（正常 60ms 级）膨胀→客户端 ctx 放弃；尝试量与第八轮 142/55min 量级一致，**非新洞** |
| trace_events jsonb UPDATE | 120 | 同上（写路径 ctx 超时） |
| binding/nps + recent-success 探针 | 37 | D2'/健康探针家族（已登记） |
| policy CTE（featured 探针） | 15 | D2''' 登记范畴 |
| session_bodies/turns 写入 | 20 | 客户端断开尾巴 |
| D10 candidate 及其它 | ~38 | 饱和尾巴 |

## §四、本轮修复（2 项）

### FIX-A（D13）：claim promoted 反连接索引解锁（代码根修）

**发现**：claim UPDATE 的 promoted 臂 `SELECT gw_session_id, is_final_success FROM ONLY "request_logs_2026_XX" WHERE is_final_success` 在 252 EXPLAIN 为 **Seq Scan**——四个 heap 月分区上明明有 `uq_request_logs_2026_XX_final_success_session` partial 唯一索引（谓词含 `gw_session_id IS NOT NULL AND <> ''`），但查询只过滤 `is_final_success`，planner 无法证明谓词蕴含而放弃索引。2026_09 分区 1,265,567 行 × 每次 claim 尝试全扫；按 ~8.5 次尝试/min×3 实例 ≈ **2,700 万行/min 纯 seq-scan 烧 CPU**——本身就是饱和的贡献源，也是 111 条 >1s 慢查 + 373 条 cancel 的主体。

**修复**（telemetry/client.go claimSessionFinalSuccessExec 臂拼装）：
1. 臂内补 `AND gw_session_id IS NOT NULL AND gw_session_id <> ''`——与索引谓词对齐。语义恒等：外层 WHERE 已要求 `COALESCE(gw_session_id,'') <> ''`，半连接相关等值下 promoted 臂里 NULL/'' 行本就不可匹配（纯重言式，不改 NOT EXISTS 结果）；
2. 投影去掉 `is_final_success`——该列不在索引里，保留会强制回表破掉 index-only。

**252 真库 EXPLAIN 取证（纪律⑳/㉖）**：改写后 2026_09 臂翻转为 **Index Only Scan using uq_request_logs_2026_09_final_success_session**（07/08/10 为空分区 Seq Scan 可忽略），外层整体升级为 Hash Right Anti Join。**无 schema 变更、无迁移**（复用既有索引）。

### FIX-B（D14）：252 主机僵死 vite build 终止（运维处置）

`kill -TERM→KILL` 3789340（node vite build）+ 3789205（npm run build，孤儿）。证据：R 态 7d10h / 712 CPU-小时 / PPID=1 无监管 / vite 构建正常分钟级。处置后 1min load **28.89→14.95**，60min 内 1min 均值降至 13.7；饱和期 mean 1,757ms 的 idx GROUP BY 计时对照降至 **443ms（-75%）**。

## §五、storage-merge 观察期（GLOBAL_G2）

- `session_mirror_outbox` 近 3h：hook|pending|1（reaper 待消化，正常）；**漏镜像对账（v1 claim 置位 ∧ 无 turns ∧ 无 outbox 登记）= 0 行**。
- 观察期继续：D12 claim 补偿（迁移 747）持续零漏；S4 停写安全前置保持满足。

## §六、登记（本轮不修，移交/顺延）

| 项 | 证据 | 处置 |
|---|---|---|
| **E6：pocket scheduled_tasks $4 类型冲突** | `UPDATE scheduled_tasks SET last_run_at=$1,last_status=$2,last_error=$3,run_count=run_count+1,next_run_at=$4…` 报 `inconsistent types deduced for parameter $4: integer versus bigint`，161min ×12（~5min 节奏）；表在 **pocket 库 opencode_pocket schema**，非本仓库代码 | 移交 pocket 轨道：Go 侧 $4 参数类型与 bigint 列推理冲突，需显式 cast/typed param |
| **E7：smm 角色认证失败 ×14** | `password authentication failed for user "smm"`，~10-12min 节奏（吻合 `*/10` track-check cron）；**crontab 中的 DSN 密码实测认证成功**（252 容器内 psql 复现），失败来自另一持旧凭据的客户端 | 移交 kaixuan smm 轨道排查 stale 凭据（容器内配置/旧 env） |
| **D6'：252-dev ready:false** | /readyz `database.connected=true` 但 discovery `credential decrypt failed: no decryption key available (fernet=0 bytes)` → models=0 → not_ready（:8780 dev 实例缺 fernet 密钥 env） | 252-dev env 补密钥（配置缺口，第八轮 D8 更新二进制时未配齐） |
| **usage_facts date_bin 慢查 ×32** | R67 迁移 749（occurred_at 前导索引）在 main 已合入但 **252 二进制仍是 a0e4d9c5（09-25 部署）未包含** | 下次受控部署自然收敛；部署=维护窗口动作（09-21 复核轮先例），本轮不携 P4/R67 未验证改动上线 |
| **主机协租户负载** | pms-dev */10 deploy-check、磁盘扫描 cron、podman 高频定时器、redis 19.7%、checkpoint 5min/recycle 29 WAL | 共享主机治理超出本仓库范围；max_wal_size/checkpoint_timeout 调参归 D6 容器重建受控窗口一并做 |
| D10' / D11' / D2''' / D6 / D8 / D9 | 维持第九轮 §七 | 不变 |

## §七、自审计（同日批判式复核）

| # | 初判 | 复核结果 |
|---|---|---|
| A1 | 1,073 条慢查、5 大未知形状 → 逐形状做索引/改写优化 | 交叉证据（04:59 同步爆发、api_keys 主键更新 1.35s、load 28.9/4 核）推翻——**逐形状优化是方向错误**，主体是主机饱和。先归因后动刀，避免为一个膨胀数字做五套改写。教训升格纪律㉚：**全形状一致膨胀 = 先查主机资源，再谈查询计划** |
| A2 | credential_model_index 读写 ×511 慢 → 表膨胀/缺索引 | 真库实测 11,699 行/101 个 5min 桶（自带保留），小表毫秒级——纯饱和膨胀，无需动 |
| A3 | claim UPDATE ×111 慢 → 归因饱和即收工 | EXPLAIN 复查发现 promoted 臂 **Seq Scan 1.27M 行**（计划级缺陷，饱和只是放大器）——改写解锁 Index Only Scan。教训：**饱和归因不豁免计划复查**，烧 CPU 的大扫描本身在加剧饱和 |
| A4 | 新臂形状先想到加新 partial 索引（迁移） | 四个分区的 partial 唯一索引**已存在**，只是查询谓词不齐——纯查询改写零迁移即可解锁（真库 EXPLAIN 翻转验证）。先找既有索引再新建 |
| A5 | EXPLAIN 改写版仍见 07/08/10 Seq Scan → 以为改写失败 | 三分区 n_live_tup=0（空表），仅 2026_09 有数据且已走 Index Only Scan——**空分区 Seq Scan 是零成本噪声**，看行数分布再下结论 |
| A6 | 断言正则两次失败（Go 解释串 `\s` 非法、漏引号闭合 `08" WHERE`） | 编译期/测试期拦截；pgxmock 正则钉形状时引号边界用 `.` 通配 |
| A7 | 倾向本轮直接部署三台网关 | 部署 main HEAD 会携带 P4 endpoint-selector + R67（各自轨道未部署验证）；遵循 09-21 复核轮「部署=维护窗口动作非审计轮」先例，D13 随下次受控部署上线（EXPLAIN 收益已在真库预证） |

## §八、下一轮提示词（建议）

> 以本文 §一 §二 §四为起点。优先级：
> 1) **D13 部署回验**：下次受控部署（携 P4/R67 各轨道就绪后）后，55min 窗口对账 claim UPDATE 慢查/cancel（本轮基线 111+373/161min）应近归零，pss 增量确认 promoted 臂 idx_scan；
> 2) **饱和观察**：vite 终止后主机 load 基线（本轮 28.9 峰值→13.7）；若慢查仍整点爆发，按 §六 协租户清单逐项对账（pms-dev cron/磁盘扫描/redis）；
> 3) **storage-merge 观察期**：G2 持续归零（本轮 0），earliest 09-29 满 7 天；
> 4) **E6/E7/D6' 移交项**跟进回执；
> 5) D6 容器重建窗口时一并做 max_wal_size/checkpoint_timeout 与 LogConfig 持久化。
> 纪律沿用 ⑪-㉙ + 本轮新增 **㉚ 全形状一致膨胀 = 先查主机资源，再谈查询计划（A1）**。

## §九、handoff 更新

- 本轮合入 main：D13 改写（telemetry/client.go + final_success_claim_test.go 收紧钉死）+ 本文档。无迁移。
- 记忆库：`llm-gateway-go-audit-cycle-progress` 追加第十轮；`pg-252-sql-log-audit-facts` 增补（4 核/load 28.9/vite 712 CPU-h、claim 臂谓词-索引蕴含细节、E6/E7/D6' 移交证据、2026_09=唯一满分区 1.27M 行、07/08/10 空）。
- 原始物证：252:/tmp/pg252-round10-window.log.gz（80MB 快照）+ 本地 /tmp/pg252-round10-window.log、/tmp/r10_slow.json、/tmp/r10_errors.json；EXPLAIN 脚本 /tmp/claim_explain.sql、/tmp/claim_explain_v2.sql、/tmp/claim_new_full.sql（252:/tmp 同名）。
- 部署状态：三台仍 a0e4d9c5（2245/2246/2246）；252-dev :8780 ready:false（D6' 缺 fernet 密钥）。D13 上线依赖下次受控部署。
