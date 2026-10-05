# 252 PG SQL 日志审计第二十五轮（2026-10-05）—— modality 修复终验 PASS（28 行增长/零复发/回写=streak 期预期）+ podman 1GB 机制全链闭合（logrotate 死信根因）+ 证据保全层上线 + 路由 7d 提案拍板（否决硬窗）

以 round24 §八 + round23 §八为起点。**起点重算**：开局本地 main=ea4978cd1，`git fetch` 后 origin/main 领先 9 提交（并行线 S4 门/session_dim/dual-read + 审计 §9.234-242，触碰文件 cmd/gateway/*、db/*、admin/request_logs_read_inventory_test.go——与本工作区暂存的 8 个并行线 WIP 文件**零重叠**，逐一核对后 fast-forward 至 **a26e990cd**）；工作区并行线 WIP（admin/agents.go、apihub/* 四件、bg/asset_health_probe.go 分页修复、URSM runbook 增补 §10.27 + 未跟踪 admin/agents_stats_pagination_test.go）全程原样保留未提交；stash 栈 2 条旧条目（mavis、glm5-effort）未动（纪律 61）。**取证窗 = 2026-10-05 06:16 → 13:16（7h，白天窗）**，与 R24 夜间窗（4.36h）不可直比，折率一律标注窗口基数。快照 13:16（服务器钟比本机 +5min，已知常数）。本轮由用户 /goal 指令触发。

---

## §零、传输层巡检：podman 1GB 截断机制全链闭合 + R24 两个未解之谜归案 + 证据保全层上线

纪律 60 三件先验（本轮实测 13:03）：ctr.log 首行时间戳仍 **2026-10-05T01:54:37**（R24 悬崖后**无新截断**）；`podman inspect` HostConfig.LogConfig 仍 json-file + **1GB** 尾参；logrotate 状态文件 mtime 10-05 03:37（cron 跑过但未轮转本文件——原因见下）。活文件 11.96MB，real=apparent 无稀疏洞（01:54 conmon 清零重写后无 copytruncate 介入），现速 ~290B/s。

### §零.1 R24 §零.2「daily 到期却未轮转」之谜——根因闭合：podman-pg-252 是死信，活配置是 podman-ctr

`logrotate -d /etc/logrotate.conf` 实跑（纪律 62 的由来：cat 配置文件只见意图不见实效）现形：

1. **`/etc/logrotate.d/podman-ctr`（mtime 07-13）才是活配置**，glob `/var/lib/containers/storage/overlay-containers/*/userdata/ctr.log`，指令 `daily size 100M rotate 3 compress delaycompress missingok notifempty copytruncate`。
2. **`/etc/logrotate.d/podman-pg-252`（mtime 07-29，R24 引用的那份）自创建起从未生效**：它声明了同一 glob，logrotate 报 `error: podman-pg-252:1 duplicate log entry for …003fd2a8…/ctr.log → found error in file podman-pg-252, skipping`——**整个文件每次运行被跳过**。R24 记录的「现行配置 daily rotate 3 maxsize 100M」实为死信里的意图，从未执行过。
3. **10-05 03:37 未轮转的机制**：podman-ctr 用的是 `size 100M`（非 maxsize）——`size` 存在时**压过 daily**（logrotate -d 实证判定语：`log does not need rotating (log size is below the 'size' threshold)`）。悬崖清零后活文件 11MB < 100M ⇒ 不轮。10-02/03/04 之所以轮，是当年真实日志量（~250MB/天时代）天天过阈。
4. **R24 §零.3「963MB 明文归档压不下去」之谜**：`delaycompress`——最近一份归档留明文、等下一次轮转才压。10-05 不轮转（第 3 条）⇒ 永远轮不到它压缩。不是 gzip 失败。本轮已手工 gzip（§四.1）。

### §零.2 1GB 悬崖完整因果链（对 R24 §零.1 的机制深化）

conmon(json-file) 写 ctr.log **不带 O_APPEND**（R22 稀疏洞证据的本源）⇒ 外部 truncate（copytruncate）后写入按原偏移继续，文件立即长回「表观高水位」⇒ **表观尺寸只增不减**，与真实内容量无关 ⇒ 到容器级 log_size_max=1GB 时 conmon **静默清零重写**（10-05 01:54 实证，无任何服务端痕迹）。推论：**logrotate copytruncate 对容器日志既无效也无害但毫无用处**，且 10-04 03:15 那次轮转（表观 963MB 高水位）注定了 22.6h 后（01:54）悬崖——缺口不是「如果轮转就不会丢」，而是「只要表观高水位接近 1GB，悬崖随时到」。

### §零.3 处置拍板（三选一）：**接受 1GB 上限 + 外部增量快照兜底**；调大/换驱动登记为维护窗口提案

- 接受的理由：悬崖 ETA 月级（现速 ~40 天一轮，且 podman-ctr 在表观回爬过 100M 后还会再 copytruncate 一次），快照兜底后**任何一次悬崖的损失 ≤1h**（对比 R24 的 19.9h）；调大 log_size_max 与换 journald 都要**容器重建**（PG 重启 = 维护窗口动作，非审计轮可自主执行），收益被快照层覆盖后不构成紧急项。
- 实施见 §四.1。运维轨维持登记：podman-pg-252 死信文件建议删除或改窄 glob（**切勿**让它复活——其 copytruncate daily 指令对容器日志有害无益）；disk-watch 4× 同秒 CROND 来源仍未明（R24 §零.5，与本轮无关，维持移交）。

---

## §一、错误家族全景（窗 7h 白天；头锚定 `CST [pid] ERROR:` 二段计数）

| # | 家族 | n（7h） | 折率 | 归因与处置 |
|---|---|---|---|---|
| 1 | `inconsistent types deduced for parameter $4`（E6 pocket） | **84** | **12.0/h 恒定** | 节拍器实锤：13:05:54 / 13:10:54 / 13:15:54 精确 5min 间隔。催办维持 |
| 2 | `canceling statement due to user request` | 47 | 6.7/h | 应用侧取消已知容忍族，维持 |
| 3 | `canceling statement due to statement timeout` | 35 | 5.0/h | probe cycle 已知族（白天窗低于 R24 夜间 9.4/h），维持 |
| 4 | `connection to client lost` | 30 | 4.3/h | dev 隧道抖动，**逐小时散布**（6/1/5/8/1/1/5/3）非风暴簇，外部自噪音 |
| 5 | `could not determine data type of parameter $2`（股票筛选器） | **21** | 3.0/h（白天抬升，R24 夜间 1.6/h） | 外部客户端 Extended Protocol 裸 NULL，末次 10:24:46。催办维持 |
| 6 | `integer out of range`（limit_up_temp 溢出，同客户端） | 8 | — | 随 #5 并案，末次 10:23:57 |
| 7 | `invalid input syntax for type json`（json_hex） | **1** | — | **06:35:53 char-374 = R24 §四.1 负例对照自产物**（r24-probe-model 语句可证，且早于首行落库 06:43:58）。**修复后 json_hex-modality 零复发成立**；capability 臂连续第 3 天零复发（max last_tested_at 12:44:03 推进中） |
| 8 | `relation "v_model_modality_progress" does not exist` | 1 | 13:14:55 | **本轮自噪音**（r25b.sql 猜错视图名，正名 v_model_modality_verification_progress）——剔除 |
| 9 | `function string_agg(bigint, unknown) does not exist` | 1 | 08:45:06 | 外部/并行会话查询签名错，非本仓 |
| 10 | `deadlock detected` | 1 | 10:22:39 | 单发，无连环，观察 |

llm_usage_stats/llm_optimization 预期表探测（R24 #6）本窗 **0 条**。二段计数真阳性 = #1 + #2/#3 本库子集；acc_interval **0 条**（风暴销案后连续第 5 天）。

## §二、慢 SQL 全景（≥1s 共 1,562 条 / 5,191.6s / 7h ≈ 742 s/h；白天窗，R24 夜间 593 s/h 不可直比。**提取器 duration 单位为 ms，本节全部数字已显式换算成秒——纪律 48**）

| 形状 | n | sum | max | 判定 |
|---|---|---|---|---|
| select_*（通用桶，含模型匹配/抽屉/usage 族） | 838 | 2,787.8s | 29.6s | 白天负载形态 |
| analyze_llmateway_table_stats | 23 | 773.3s | 62.5s | D7' 已知（巨 payload 行宽采样），未破 10min 线 |
| stats_rollup | 146 | 327.0s | 20.4s | 已知族 |
| **session_turns 镜像 JOIN（t JOIN request_logs）** | 45 | 611.3s | 29.4s | ⑥ 判定见 §三.6 |
| project_backfill（WITH targets） | 70 | 224.3s | 25.8s | R8 已知 |
| **session_turns 覆盖率（count(*) 子查询）** | 50 | 182.4s | 19.9s | 同上并档 |
| probe_target 族 | 105 | 184.2s | 8.1s | R8 已知 |
| 路由读臂（latest_bucket SELECT） | 70 | 152.6s | 9.5s | **avg 2.18s**——白天均值与 R24 夜间 2.15s 持平（tick 驱动非负载驱动实锤），P2 证据再加一条 |
| ursm 快照聚合 | 26 | 98.5s | **7.5s** | **830 生效再证**：R23 时代 57.5s → 夜间 1.1-1.3s → 白天 max 7.5s，分区化收益全天候成立 |
| 路由写臂 INSERT index_hot | 26 | 51.4s | 4.4s | P2 维持（三臂全天候在费） |
| **cmb_update（probe_revert_timeout revert）** | 16 | 24.6s | 3.0s | 早间簇 07:52-09:39，**1-3s 贴线 = 白天基线**，非 R23 膨胀风暴（×763/14.8s）复发 |
| maas_buckets | 1 | 18.2s | 18.2s | 已知族 |

**pg_stat_statements 三臂刷新（累计自 09-29 D6 重建，13:05 读数）**：读臂 134,159s（717.8ms × 186,899）+ 写臂 94,036s（502.7ms × 187,074）+ DELETE 臂 68,775s（619.6ms × 111,004）——较 R24 快照（06:16）6.8h 增量 +172s/+69s/+0s，累计 ~297.0ks。REFRESH routing_analytics_7d 122,558s 纹丝不动（夜窗均值后见 §八跟踪）。

## §三、R24 遗留项逐项复核（§八①-⑥ + §五清单）

| R24 登记 | 本轮复核结果 |
|---|---|
| ① modality 修复终验（表增长/semantic 回写/json_hex 零复发） | **PASS（回写子项=预期未到期，见 §三.1）** |
| ② podman 1GB 处置跟进 | **本轮收口**：机制全链闭合（§零）+ 证据保全层上线 + 拍板（§零.3） |
| ③ 路由 7d 提案拍板 | **本轮拍板：否决硬窗落地**（§四.2），11-02 复核判据落字 |
| ④ E6 与股票客户端外部催办 | E6 12.0/h 恒定维持；股票客户端白天抬升（21+8 条），催办维持 |
| ⑤ 10-11 03:15 vacuum-bloat 同步后首跑终验 | 前置复核 PASS：服务器脚本 sha256 `025edf83…` 与仓库模板**一致**、bash -n OK、PGOPTIONS 方案在位、.bak-r24 备份在位；终验节点 10-11（周日）03:15 登记 |
| ⑥ 会话镜像探针族（340s/夜）若增长则立项 | **不立项**：镜像 JOIN 611.3s/45 + 覆盖率 182.4s/50 = **793.7s/7h ≈ 113 s/h** vs R24 夜间 427.9s/4.36h ≈ 98 s/h，量级稳定（+15% 在日/夜窗口口径差内），维持观察 |

### §三.1 modality 终验明细（判据逐条）

- **表行数增长 ✓**：model_modality_verification **0 → 28 行**（06:43:58 首行 → 11:44:07），created 分布 06h:1 / 08h:16 / 09h:8 / 11h:3（07h/10h 两 tick 无落行 = 探测未完成/失败不落证据，backoff 语义内）。carry_level accepted/rejected 双形态在位，read_level 全 unknown、streak 0-1。
- **json_hex 零复发 ✓**：窗内唯一 1 条 = R24 负例自产物（§一 #7）。persistRow 28 行全部成功落库 = **修复后的参数通道在生产实证**（rollupVerdict UPDATE 臂与 persistRow 用同一 `capabilityEvidenceParam` helper 同一形态 + AST 守卫钉死）。
- **models_canonical semantic 回写 = 0 行——属预期，不是失败**：判定视图 v_model_modality_verdict 对已探 14 canonical 全部 `verdict=unknown`（streakGoal=2：需同一 (credential,raw_model,modality) 行连续 2 次正向读探测才落 confirmed）；且扫描序 `ORDER BY (v.id IS NULL) DESC` = **未探测组合优先于已探测待确认行**，backlog（活跃组合 ~千级）排干前，已有 1 次正证据的行不会再被探 ⇒ **首次 semantic 回写预计在 backlog 显著排干之后**。判定依据是代码机制 + 真库数据，非「没出现就算过」。
- worker 常量（代码实测）：tick 1h / batchLimit 50 / scanFactor 4（scan LIMIT 200）/ staleAfter 30d / streakGoal 2。**观察登记**：实测落行 ~5.6 行/h ≪ batchLimit 50，说明探测失败率（上游超时/拒绝完成）主导吞吐——backlog 排干 ETA 周级起步，回写首现节点相应推后，下轮跟踪（不构成修复缺陷）。
- models_canonical 现态：inferred 961 / semantic 0 / manual 0 / structural 0；modality_evidence 非空 961 = 825 存量回填 `'{}'`（非探测产物）；modality_verified_at 全 NULL（与 semantic=0 自洽）。

## §四、本轮处置与拍板

### §四.1 证据保全层（podman 1GB 处置实施，零容器触碰）

- **`/opt/scripts/pg17-ctrlog-snapshot.sh`**（750，bash -n 过）：按字节偏移增量外抄 ctr.log → `/var/log/pg17-ctrlog-archive/ctrlog-YYYYMMDD.log`；末尾不完整行留待下轮（快照边界不切半行）；`CUR < OFF` 判 conmon 悬崖清零并打界标；只读 ctr.log，**绝不 truncate/轮转它**；flock 防重入；2 天前日归档 gzip、30 天前 .gz 清理（磁盘 MB 级/天）。
- **`/etc/cron.d/pg17-ctrlog-snapshot`**：`47 * * * *`（避开 pg17 cron 簇 :07/:15/:17/:18/:23/:49）。
- **首跑回填**：offset 0 → 11,955,727B 全量入档（覆盖 01:54→13:23 全部现行内容，含本轮取证窗）；二跑幂等（offset 不动）；归档尾行 = 活文件尾行核对一致。
- **孤儿归档压缩**：ctr.log-20261004（963,012,664B 明文，delaycompress 孤儿）→ **32,365,056B .gz**（释放 ~930MB，gzip -t 完整性过；30× 比率反推真实内容 ~250MB，与 R23「真实内容 252MB」相符）。磁盘 51%（94G 可用）。
- **回滚面**：删 /etc/cron.d/pg17-ctrlog-snapshot + /opt/scripts/pg17-ctrlog-snapshot.sh 即完全撤除；对 PG 容器与 logrotate 零改动。

### §四.2 路由候选 7d 回看提案——拍板：**否决硬窗落地，维持 R22 裁决；11-02 升级为正式复核节点**

- 证据现状（三臂证据齐 + 本轮刷新）：读臂 pss 累计 134,159s/718ms、写臂 94,036s/503ms、DELETE 臂 68,775s/620ms；本窗读臂 avg 2.18s 与夜间 2.15s 持平（tick 驱动）。
- 否决理由（沿用 R22 §二裁决并加固）：① R22 A/B 实测 268→267，**1 对活候选真实命中**——7d 硬窗存在「7 天无流量 → 退出候选 → 无流量即无新索引行 → 永久无法复活」的自锁语义，是产品行为变更而非性能修复；② 成本形态 = 后台 tick 驱动 + 锁等待占比大，无用户面故障（对照 R8 真功能故障 ×203 已由 hot 读臂根修）；③ **11-02 day-2 tick DROP credential_model_index_2026_09（301k 行）自然缓解在途**，4 周内落地硬窗 = 在自然缓解前夜引入语义风险。
- **11-02 复核判据（落字）**：DROP 生效后读臂（latest_bucket）夜间均值 **<1s** ⇒ 提案永久归档；**仍 >2s** ⇒ 以「7d 窗 + 保底复活通道」（保底保留最近活跃 N 个候选，杜绝自锁）为改良案重新拍板并随版落地（含 admin/auto_route.go:278 同形副本）。

## §五、登记与移交清单

1. **[P1·已闭环]** modality 修复终验 PASS（§三.1）。R26 首查：28 行 → backlog 排干进度、首个 verdict=confirmed、models_canonical semantic 首行回写、json_hex 连续第 4 天零复发。
2. **[运维轨·收口 2/4]** podman 1GB（本轮收口，§零.3）；logrotate daily 未轮转（本轮归因：podman-pg-252 死信 + podman-ctr size 压过 daily）；963MB 明文（本轮归因 delaycompress + 已压缩）。**仍开放**：disk-watch 4× 同秒 CROND 来源（R24 §零.5）；podman-pg-252 死信文件处置（删或改窄，切勿让它复活）；调大 log_size_max / 换 journald 驱动 = 维护窗口提案（需容器重建，非紧急）。
3. **[外部·催办]** E6 pocket 12.0/h 恒定（5min 节拍器实锤）；股票筛选客户端 $2×21 + limit_up_temp 溢出×8（末次 10:24:46）。
4. **[P2·已拍板]** 路由 7d：否决硬窗，11-02 复核判据见 §四.2。
5. **[终验节点]** **10-11（周日）03:15** vacuum-bloat 修复版首跑（前置复核全 PASS，§三.5）。
6. **[观察]** ⑥ 探针族 113 s/h 量级稳定不立项；modality worker 实测吞吐 ~5.6 行/h ≪ batchLimit 50（探测失败率主导，backlog ETA 周级）；cmb_update 白天基线 16/7h（1-3s 贴线）。

## §六、新纪律候选

- **62**：logrotate 的实效判定必须 `logrotate -d` 实跑读取，**cat 配置文件只见意图不见实效**——duplicate log entry 导致整文件死信跳过、`size` 压过 `daily`、`delaycompress` 压缩延期，三者在 R24 都是「读配置推不出现状」的坑，-d 一次现形。
- **63**：conmon 容器日志三特征——**无 O_APPEND**（外部 truncate 后按原偏移续写=稀疏洞）、**copytruncate 截不回写入偏移**（对容器日志无效）、**1GB 悬崖 = 表观高水位事件**（与真实内容量无关，静默清零）。唯一可靠保全 = 外部按偏移增量外抄（本机已部署），别指望 logrotate。

## §七、自审计

- A1：自噪音两条均先于结论剔除——13:14:55 `v_model_modality_progress` 不存在 = 本轮 r25b.sql 猜错视图名自产物（正名后重查）；06:35:53 json_hex = R24 负例对照（char-374 + 语句原文双证）。若不剔，会把「零复发」错写成「复发 1 条」。
- A2：`du -b` 第一次量「真实内容」量错了——`du -b` 是 apparent size，963MB 稀疏比未实测；改用 gzip 30× 比率反推真实内容 ~250MB（与 R23 实测 252MB 相符），如实登记测量方法。
- A3：R24 提取器 sed 复用时未改输出文件名（/tmp/pg252-r25/ 下仍是 errors_r24.tsv 等旧轮名）——物证以目录为界（/tmp/pg252-r25/），文件名带旧轮号是已知瑕疵，引用时注明。
- A4：ff 前逐文件核对入站 9 提交（cmd/gateway/*、db/* 等）与工作区暂存 8 文件零重叠才执行 merge --ff-only；并行线 WIP、stash 栈、URSM 巡检 cron、830 分区化工作面全程零触碰。
- A5：「semantic 回写 = 0」先查 streakGoal/扫描序机制再下「预期未到期」判定，并区分「修复通道已实证（persist 28 行）」与「回写触发条件未达（verdict 全 unknown）」两件事——未把「未出现」写成失败，也未写成成功。
- A6：拍板两项（1GB 接受+快照、7d 否决）全部给出了可操作的复核判据与回滚面，未留「待议」悬置，也未越权做容器重建/生产语义变更。

## §八、下一轮提示词（建议）

> 以本文 + R24 §八为起点。优先级：① modality 二阶观察——backlog 排干进度（28 → ?）、首个 verdict=confirmed 与 models_canonical semantic 首行回写（streakGoal=2/batchLimit=50/实测 ~5.6 行/h，失败率主导）、json_hex 连续第 4 天零复发；② 10-11 03:15 vacuum-bloat 修复版首跑终验（脚本 sha 025edf83 已核）；③ 快照层 24h 运行复核（:47 cron 首触发、offset 连续性、无 conmon reset 界标；若悬崖发生验证损失 ≤1h）；④ 路由三臂维持观察，11-02 复核节点（判据 §四.2）；⑤ E6/股票客户端催办维持；⑥ cmb_update 白天基线 16/7h 与探针族 113 s/h 复核是否漂移。纪律沿用 ⑪-59 + 60-63。

## §九、产物与物证

- 服务器（252）：/opt/scripts/pg17-ctrlog-snapshot.sh + /etc/cron.d/pg17-ctrlog-snapshot（:47 hourly）；/var/log/pg17-ctrlog-archive/ctrlog-20261005.log（11,955,727B 全窗回填）；ctr.log-20261004.gz（32,365,056B，原 963MB）；/tmp/pg252-r25/（ctr-snap-r25.log 11.8MB、r25a/r25b.sql、r25_extract.py、errors/slow/summary tsv+json——文件名沿用 r24 号，以目录为界）。
- 关键实测值：窗 06:16→13:16/7h；modality 表 0→28 行/首行 06:43:58；capability 138 行/max_tested 12:44:03；canonical inferred 961/semantic 0；json_hex 唯一 1 条=R24 自产物；E6 84=12.0/h；慢查 1,562/5,191.6s；pss 三臂 134,159/94,036/68,775s；台账 830；磁盘 51%/94G。
- 拍板记录：podman 1GB = 接受 + 快照兜底（调大/换驱动=维护窗口提案）；路由 7d = 否决硬窗（11-02 复核判据 <1s 归档 / >2s 改良案重拍）。
- 本报告：docs/audit/2026-10-05-252-sql-log-audit-round25.md。
