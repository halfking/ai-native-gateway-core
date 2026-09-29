# 252 PG SQL 日志审计第十五轮（2026-09-30）

## 一、窗口与身位

- **取证窗口**：2026-09-30 00:19:54 → 06:46 CST（两段：归档 `ctr.log-20260930` 00:19:54–03:44:00 / 487MB·203K 行；现行 `ctr.log` 04:51:00–06:46+ / 177MB+；03:44–04:51 静默）。共 ~664MB / 5.9h ≈ 30KB/s 均值（基线 ~590B/s 的 **50×**，成因见 §四 F3）。logrotate（daily/rotate3/maxsize 100M/copytruncate/compress）于 03:44 跑当日轮转。
- **部署身位（全部实测，纪律㉞）**：154 = `e1b73e88-2323`、245 = `a8a34023-2324`（均 09-28/29 起，未变）、252-dev = `dd7e4527-2338`（09-30 02:59:36，NRestarts=0）——**三实例全携 R12-F1，新旧混跑过渡期结束**。
- 主机：load 8.1（4 核），kswapd 活跃（swap 2.2GB 用）；06:00 `find / pagemap` 恒常噪声；06:03 `[local] EXPLAIN` 与 06:12 CREATE INDEX = **并行会话在生产活动**（§六）。

## 二、修复（本轮落地）

### F1（P1，已修复+固化）：D6 重建丢失 pgvector `.so`，vector 依赖面全断

- **现象**：`could not access file "$libdir/vector"` 真条目 32（归档窗）+ 16（现行窗，最后一条 06:31:26），≈13/h 稳态节奏；语句形状=外部监控探针（`pg_total_relation_size` top5 体量盘点 / `pg_stat_user_tables` 枚举）——size 函数打开 `idx_task_type_centroids_embedding`（ivfflat 访问方法）即需加载 vector 库。
- **根因**：D6 受控重建（09-29 09:37）使用的镜像 `kx-citus-pg17:amd64` **不含 pgvector**；而 `/data` 持久卷里 pg_extension 目录完好（llm_gateway=`vector 0.8.3`，`task_type_centroids(centroid vector(1024))` 5 列 + ivfflat 索引；memora 1 列、redclaw 2 列）——catalog 与 .so 脑裂。旧容器（07-29 建）当时可写层内具备 vector（镜像 tag 重指或曾容器内安装，二者必居其一，无法回溯）。
- **实际影响=零（生产侧侥幸）**：embedding 分类器（`autoroute/embedding_classifier.go`，唯一代码读写方）在**三实例 env 均未配置 `LLM_GATEWAY_EMBEDDING_MODEL`/`LLM_GATEWAY_SA_MODEL_EMBEDDING`**（154/245/.env.dev 实测）→ 分类器关闭；且 `task_type_centroids` **0 行**（EMA 写路径从未在生产生效）。断供面仅监控探针与潜在未来启用。
- **修复过程**：容器无外网（deb.debian.org 亦不通）→ 阿里云 PGDG 镜像（`mirrors.aliyun.com/postgresql/repos/apt`）解析 trixie-pgdg 精确文件名 → 宿主下载 `postgresql-17-pgvector_0.8.6-1.pgdg13+2_amd64.deb` → `podman cp` + `dpkg -i`（热安装，PG 无需重启）→ `ALTER EXTENSION vector UPDATE`（0.8.3→0.8.6）。
- **验证**：①centroid 余弦查询（分类器同形状）PASS；②formerly 必炸的 top5 体量盘点查询 PASS（顺带产出 §三容量数据）；③最后错误 06:31:26 < 安装 06:39，其后零复发；④`vector.so`/`vector.control` 落位。
- **固化**：`252:/opt/scripts/pg17-start.sh` 追加**幂等恢复步**（`test -f vector.so` 守卫 → 阿里云镜像动态解析 deb → podman cp + dpkg，失败仅 WARN 不阻断 boot），`bash -n` 过，原文件备份 `.bak.r15`。下次容器重建不再丢失。

### 本轮**零代码修复**（仓内无改动提交）——修复全部在运维面（容器层+脚本），符合"审计轮不代生产改数据/不携未验证代码"纪律。

## 三、新登记

### D18（P2）：REFRESH 单刷时长回归——墙钟未达预测的真因

- **R14 遗留复测结论（≥90min 全窗，实测 ~2h 干净单相位期）**：**单相位收敛成立**——05:00–06:20 连续 9 个 10min 窗 + 归档窗 03:00–03:44 各窗**恰 1 次 analytics REFRESH**（audit_summary 同窗口随赢家顺刷）；R14 四窗铁证外再添 13 窗证据。
- **墙钟 ≈ 6.7%**（analytics 均 34.2s + audit 均 6.0s，每 600s 窗）——较 R13 过渡期 10.4% 回落，接近 R12 预测 ~5.2%，**残差全在 analytics 单刷时长**：34.2s（n=44）vs R12/R13 时代 15–26s，抬升发生于 09-29 白天（R13 早 18–26s → R14 晨 36–70s → 本轮 30–44s）。
- **结构线索**：matview 本体仅 12MB（audit 64KB）——成本在**源侧**：`routing_analytics_source` 读 `request_logs_hot`，且 matview 定义含**每行 credentials 关联子查询**（`(SELECT provider_id FROM credentials WHERE id=…)`）。巨 payload 期 hot 表行宽/IO 压力增长与时长抬升时间轴吻合。**下轮 EXPLAIN ANALYZE matview 定义查询定案（纪律⑳），本轮不动写**（三实例共享 10min 节拍，EXPLAIN 须择低峰）。

### D19（P1 ops，容量赛跑）：bodies 月分区 vs 93% 盘

- 根盘 `/dev/vda3` 197G **93% 满、余 15G**；`/data`（PG 数据卷）同盘。
- `request_logs_bodies_2026_09` = **49.5GB / 1.39M 行**（top5 实测；session_bodies_2026_09 19GB、ursm_node_snapshot_min 19GB、request_logs 4GB、bodies_hot 4GB）。
- **TTL 语义=整分区 DROP**（`drop_old_request_logs_bodies_partitions`：`month_end ≤ CURRENT_DATE − ttl_days`；ttl=`lifecycle.request_logs_bodies_ttl_days`=7，hot-reload）→ 9 月分区 **10-08 才释放**。
- **赛跑数学**：10 月分区以 ~1.6GB/天增长（30 天 ~50GB），10-01→10-08 双分区并存 ≈ 62GB bodies + 其余日增 ~2GB/天 vs 余 15G → **预计 10-06/08 前后满盘，与分区释放同窗**——贴线赌命，任何流量尖峰即输。
- **建议（用户拍板，本轮未动）**：①把 `lifecycle.request_logs_bodies_ttl_days` 7→3（一条 settings UPDATE，9 月分区提前至 10-04 释放）；②或扩盘/迁卷；③session_bodies 的 TTL 键与 ursm 表保留策略下轮一并盘点。

### D20（P3）：慢日志体量与窗口收缩

- 630MB/5.9h，1g×2 轮转下取证窗口 ≈9h（非 12h 估计值）。放大器=巨 payload body INSERT 超过 1s 即全量入慢日志（单条语句文本 MB 级）。PG 无语句长度上限开关，无干净治理面；登记为观察项，若窗口继续收缩再议（如 log_min_duration_statement 1s→2s 的取舍）。

## 四、对账与改判

### F2（证伪 ×2）：`ttft_ms` NOT NULL 与 `session_owners` 不存在

raw grep 各得 3 条疑似真错误 → **头锚定复核 0 条**（`CST [pid] ERROR:` 正反两面 grep，纪律㊱）——全是**巨 INSERT 字面量内的 payload 文本**（会话上下文里携带的 Go 源码/错误串）。撤销登记。**新教训**：巨 payload 时代 raw `/ERROR:/` grep 必然假阳，分类必须锚 PG 条目头。

### F3（round13 归因重审）：630MB 夜间爆发 = body 记录合法形态，非 psql 灌库

- 机制实证：巨行是 `INSERT INTO request_logs_bodies_hot` **字符串字面量的内部续行**（SimpleProtocol 原样内联，字面量含换行→podman 逐物理行切分）；payload = ZCode 会话转录/源码/文档——**网关就是这些会话的 LLM 后端，body 记录照常落库**。
- **round13 F1"转录污染=psql 灌库"归因需重审**：`''` 双写转义与巨型 STATEMENT 条目同样可由"失败的/被取消的 body INSERT"产生（本轮 cancel 条目的 STATEMENT 即含完整 payload）。round13 证据（3,722 条巨型 STATEMENT + broken pipe FATAL ×13）与该机制完全兼容。**F1 教训条目建议改写**：非"严禁 psql 喂非 SQL"，而是"巨型 STATEMENT 先核是否 body INSERT，再谈灌库"。

### F4：cancel 与退出码归因

- user-request cancel 257（归档）+ 74（现行）≈57/h（R12 基线 14/h 的 4×）——**抽样 64% = `session_bodies_hot` INSERT 族**（客户端中止巨型写入→pgx ctx 取消→弃连 FATAL 13+56、parallel worker 终止 8+8）。与 R12/R13"数据零丢失"同机制（delta 幂等兜回），随 payload 变大窗口变宽。非缺陷，登记饱和关联。
- `>30s` 完成条目（归档 30 条）全部=180s pin 的 REFRESH/10min pin 的 analyze（59–117s，预算内）——**无 rolconfig 违约**，D6' 遗留疑虑关闭。
- E6 pocket `$4` 继续升频：41+23 ≈ 11/h（R14 的 2/h → 5×，外部 opencode-pocket 写方，维持移交）；**F2-smms 清零**（fundamentals/limit_up_temp/daily_kline 双窗 0 条——外部股票 cron 已停/已修）；E7 smm 认证失败 ×42 全在归档窗（00:19–03:44 突发后停止，无 client 归因面，登记观察）。

### F5：自取证显式扣除（纪律㉕）

本轮自身故障查询计入窗口并剔除：坏引号 26 库巡检（`column/type "vector" does not exist` ×46）、`operator || "char"` ×1、`syntax ""[\n\r ]+""` ×1、聚合 GROUP BY ×1、indisvalid 别名 ×1。并行会话取证另见 §五。

## 五、并行会话活动登记（非本轮产物）

- 06:03 `[local] EXPLAIN`（生产库直连）；05:59:20 UNION 列数不齐 ×4（admin 对账验证查询族，同 pid 连发=其查询批自带 bug）；06:12:19 `CREATE INDEX idx_request_logs_tenant_ts`（**本仓迁移 355 的对象**，7.4s 建成，现 `indisvalid=true`，父表+hot 两侧在位）——疑某会话手动补 ensure 或实例 boot 触发（252-dev NRestarts=0 无 boot；154/245 无法从本机核 boot 时间）。对象合法、无锁事故残留，登记不处置。
- 06:46 现行窗还有 `column "routing_analytics_7d" does not exist` ×1 一次性条目（同源并行会话取证手误形态）。

## 六、五项对账（对照 R14）

| 项 | R14 基线 | 本轮 | 判定 |
|---|---|---|---|
| REFRESH 击杀/叠跑 | 0（四窗铁证） | 0；13 窗复证恰 1 winner/窗 | ✅ 维持 |
| current_role 语法错 | 0 | 0 | ✅ 维持 |
| jsonb hex（FIX-C 族） | 0 | 0 | ✅ 维持 |
| 42703 列错误（生产形状） | 0 | 0（`session_id`/`num_timed`/`ttft_ms` 全 payload 文本或会话取证） | ✅ 维持 |
| folded 23505 | 0 | 0 | ✅ 维持 |
| pocket $4 / smm | 44/20.8h 升势 / 19+19 | 11/h 持续 / **0（清零）** | E6 恶化维持移交；F2 关闭 |
| checkpoint 900s | +2/40min | 未复测（上轮已关闭） | — |

## 七、自审计（A1–A5）

- **A1** 巨行首判"转录污染灌库"错误——被 00:19:57 原始行样本推翻（payload 在 body INSERT 字面量内部）；**先看原始行再下机制结论**。
- **A2** "column/type vector" ×46 险些登记为新错误类——发现时间戳与自身巡检命令完全吻合后剔除；**错误分类前先对自家命令时间轴**（㉕ 适用于并行会话与新错误双重场景）。
- **A3** `ttft_ms`/`session_owners` 首判"真缺陷（usage_facts 写路径）"——头锚定 grep 证伪后撤销。**raw grep 分类的假阳率在巨 payload 时代不可接受，头锚定是唯一正典**。
- **A4** 墙钟数字曾算出 13.8%（把 audit 极值 44–49s 当均值）——用全量 clean 慢日志重算均值 6.0s 后定案 6.7%；**均值必须全样本，极值样本不得外推**。
- **A5** pg17-start.sh 补丁内嵌 awk 的 `\$2` 转义经三层引用（ssh→heredoc→python→bash 双引号），以 `bash -n` + 内容 grep 双验；**多层引用补丁必须逐层展开验证**。

## 八、下一轮入口

1. **D19 容量拍板执行**（ttl 7→3 或扩盘——用户决策后一条 UPDATE/一次扩容，10-04 前必须落地）；session_bodies/ursm 保留策略盘点。
2. **D18**：低峰期 EXPLAIN (ANALYZE, BUFFERS) `routing_analytics_7d` 定义查询（源=`routing_analytics_source`→request_logs_hot + credentials 关联子查询），定根修方案。
3. vector 修复 24h 复核（`$libdir/vector` 应持续 0）；下次容器重建后验证 pg17-start.sh 恢复步实战。
4. E6 pocket $4 升频 5× 的外部归因（若继续恶化移交 opencode-pocket 轨道）。
5. （承接 R14）S4 停写触发本机侧待用户拍板，未变。

## 九、本轮产物与物证

- 报告：`docs/audit/2026-09-30-252-sql-log-audit-round15.md`（本文件）；仓内**零代码变更**（运维面修复）。
- 252：`/tmp/r15_classify.awk`、`/tmp/r15_deep.sh`、`/tmp/r15_clean_slow.txt`、`/tmp/r15_cur_slow.txt`、`/tmp/postgresql-17-pgvector_0.8.6-1.pgdg13+2_amd64.deb`、`/opt/scripts/pg17-start.sh.bak.r15`、pg17-start.sh 已固化 vector 恢复步。
- 容器内：pgvector 0.8.6 已装（dpkg），llm_gateway `ALTER EXTENSION vector UPDATE` 至 0.8.6。
