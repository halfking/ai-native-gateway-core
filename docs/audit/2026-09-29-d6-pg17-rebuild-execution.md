# D6 受控重建执行记录（2026-09-29 09:36 CST）

round11 §八⑤ / round12 §六 / round13 登记的 D6（pg-252-pg17 容器受控重建）落地记录。执行于 09-29 09:36（避开 06:00 审计 cron 与 09:00 观察轮，13:05 告警引擎轮之前）。

## 变更项（/opt/scripts/pg17-start.sh，备份 pg17-start.sh.bak-20260929）

| 项 | 重建前 | 重建后 |
|---|---|---|
| /dev/shm | 64MB（默认，ShmSize=65536000 实测） | **1GB**（--shm-size 1g 于 09-23 加入，本次生效） |
| max_wal_size | 2GB（configuration file） | **4GB**（容器命令行 `-c` 固化） |
| checkpoint_timeout | 300s（default，round11 实测 5min 节奏 ×33/161min） | **900s** |
| log_statement / log_min_duration_statement | none / 1s（auto.conf，依赖数据卷存活） | 同值，**固化到容器命令行**（容器重建/换卷双保险） |
| ctr.log 上限 | json-file 100m×3（实测 ≈30min 即轮转，2.8MB/min） | **1g×2**（取证窗口 ≈12h） |

log_line_prefix 保持 `%m [%p]` 不变（保持 r11+ 解析器兼容）；数据卷 /data/pg-data-252-pg17 不动。

## 执行与验证（全部实测）

- 重建用时 ≈60s（stop→rm→run→pg_isready READY）；重建前物证快照 252:/tmp/pg252-pre-d6-snapshot.log.gz（11.9MB）。
- SHOW 生效值：max_wal_size=4096 / checkpoint_timeout=900 / log_statement=none / log_min_duration_statement=1000 ✅
- inspect：shm=1073741824（1GB）、LogConfig size=1GB ✅
- 统计保留（同卷，非 clean-shutdown 清零路径）：`uq_request_logs_2026_09_final_success_session` idx_scan 重建前 6144 → 重建后 6149（继续增长）✅
- rolconfig 保留：llm_gateway = statement_timeout 30s + idle_in_transaction_session_timeout 60s ✅
- 数据保留：provider_profile_alerts 938、session_mirror_outbox dead 147（重建前原值）✅
- **网关自愈实证（更新"DB-less 不自愈须 restart"旧经验）**：PG 中断 ~60s 后 154（e1b73e88-2323）与 245（a8a34023-2324）均未重启即恢复 ready:true；pg_stat_activity 三台连接全部自动重连（209×16、241×20、210×17）。252-dev 裸进程（a0e4d9c5，:8780）连接亦恢复，维持其自 09-25 的常态 ready:false。
- 落行恢复：request_logs_hot 重建后 10min 118 行 ✅
- checkpoint 增速复测：重建时点 num_timed=5202（PG17 视图 pg_stat_checkpointer，同卷保留）；下次审计轮以 15min 增速 ≤1 判定 900s 生效。

## 执行坑（已固化为新纪律候选）

1. **podman run 的 `-c` 是 `--cpu-shares`**：postgres GUC 参数必须放在镜像名之后作为 entrypoint 参数（`podman run … image -c max_wal_size=4GB`），放在镜像前会被 podman 解析为 cpu-shares 直接报错——初版脚本因此 run 失败，容器已 rm、PG 短暂下线后 ~90s 内修复重跑（总下线 ≈2.5min，期间网关请求落库失败由幂等回填兜回）。
2. **PG17 统计视图改名**：checkpoint 统计在 pg_stat_checkpointer，列名 num_timed/num_requested（旧 pg_stat_bgwriter.checkpoints_timed 已拆走）。
3. podman inspect 的 LogConfig 上限在 size 字段（Config 子 map 为空是正常形态）。

## 遗留

- checkpoint 900s 增速、/dev/shm 1g 后 DSM 归零确认 → 下轮审计复测点（round13 §六 D6 项可随本记录关闭）。
- 753/754 真库首跑仍由存储轨道收口后随部署执行（本轮未携带，与本记录无关）。
