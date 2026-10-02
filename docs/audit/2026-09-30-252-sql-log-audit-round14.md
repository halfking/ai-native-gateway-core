# 252 PG SQL 日志审计轮·第十四轮（2026-09-30，D6' 部署窗口轮）

以第十三轮（docs/audit/2026-09-29-252-sql-log-audit-round13.md）§六提示词为起点：①D6' 252-dev 重部署窗口；②S4 停写 gate 落账后评估；③E6/F2 外部轨道回执；④F1 教训固化。纪律沿用 ⑪-㉞，本轮新增 ㉟㊱（§五）。

## §〇、取证与起点

- 起点：main HEAD = `dd7e45278`（本地 = origin/main，干净树）。R12-F1 祖先关系 `git merge-base --is-ancestor 04910c8ce HEAD` PASS。
- **身位修正（对 round13 的登记纠偏）**：252-dev 并非"裸进程"，实为 **systemd 单元 `llmgo-252-dev.service`**（Type=simple，`Restart=always`，EnvironmentFile=/opt/llm-gateway-go/.env.dev，`LLM_GATEWAY_LISTEN=127.0.0.1:8780`，ExecStart=/opt/llm-gateway-go/bin/gateway）。"裸"仅在"非容器化"意义上成立；重部署路径 = 换二进制 + `systemctl restart`。
- D6（PG 容器侧四项）已于 09-29 09:36 执行（docs/audit/2026-09-29-d6-pg17-rebuild-execution.md），本轮窗口复核全项（§二）。
- 窗口：2026-09-30 02:58（部署）→ 03:38（收尾取证），CST 深夜低峰窗；避开 06:00 审计 cron 与 09:00 观察窗（后者已删除，§三）。
- 版本身份：**2.5.8-dd7e4527-20260929-2338**（bump-version.sh floor 计数器出 seq 2338 > 全网 max 2324，防并行撞号；version.json SSOT 运行时读取，2026-07-14 起 ldflags 已废）。构建 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=mod`（89.6MB ELF x86-64 静态链接，`file` 实证）。~~版本身份三文件随本轮提交入库~~——**修正见 §九：version.json 三文件后被并行轨道消费（ef93cf72-2337），本轮部署身份以 252 服务器侧 version.json 为准。**

## §一、D6' 主体：252-dev 重部署 + 单相位收敛 —— PASS（本轮核心交付）

### 部署执行（全部实测）

| 步骤 | 证据 |
|---|---|
| 备份 | `gateway.bak.20260930-025804`（a0e4d9c5-2246 原 binary 保留） |
| 换装 + version.json | /opt/llm-gateway-go/bin/gateway = dd7e4527-2338；version.json 同步写入 |
| 重启 | `systemctl restart llmgo-252-dev`，02:59:36 起 MainPID 1438074，`NRestarts=0`，is-active=active |
| 健康核验 | `/healthz` = `{"status":"ok","version":"2.5.8-dd7e4527-20260929-2338","git_sha":"dd7e4527","build_seq":2338,"ready":false}`；`/readyz` database connected 1.5-17ms |
| 启动迁移链 | 252 schema_migrations max=758 = 仓库 startup 链 max（755-758 已于 09-29 03:07 随 154/245 部署应用；800 不在链内）→ **重启零迁移变更**（预期内） |
| ready:false 根因 | `/readyz` → `{"database":{"connected":true,"latency":"1.5ms"},"redis":null,"status":"not_ready"}`：**.env.dev 无任何 REDIS_* 配置，redis 客户端为 null**，readiness 门（DB+Redis 双连通）结构性 not_ready。新旧构建一致，**环境常态非回归**。refresher 不受影响：R12-F1 对齐为纯墙钟逻辑，协调走 advisory-only 路径（FIX-4 全局互斥兜底正是为此形态设计）。若需 ready:true 须给 .env.dev 配 Redis（宿主 16379 有监听但归属未核实）——留用户决策 |

### 单相位收敛验证（四窗全证据，传输层时间戳）

| 窗口 | 全集群 winner | PG 侧 duration 证据 | 252-dev 侧 journal 证据 |
|---|---|---|---|
| 03:00 | 154/245（pid 49244） | analytics 69.8s（03:01:09）+ audit_summary 4.4s | 03:00:16 skipped ×2（initial refreshAll 竞争失败） |
| 03:10 | 154/245（pid 49244） | analytics 36.7s（03:10:36）+ audit_summary 4.2s | **03:10:00 整** skipped ×2 |
| 03:20 | 245（pid 49503=172.16.2.241） | analytics 36.2s（03:20:36）+ audit_summary 49.4s | **03:20:00 整** skipped ×2 |
| 03:30 | **252-dev**（pid 49308/50846=172.16.2.210） | analytics 41.6s（03:30:41）+ audit_summary 6.2s | **03:30:41/03:30:57 refreshed elapsed=41.6s/6.3s（winner 本尊）** |

- **对账 §二（round13）预测**："双相位应收敛为单相位" ✅ 逐字兑现——旧 252-dev 的 :X9 相位（round13 实测 :X9:09-19）自重启后绝迹，三实例全部钉 :X0:00 边界；
- **每窗恰 1 个刷新周期**（1×analytics + 1×audit_summary），4/4 窗成立；winner 在 154/245/252-dev 间轮转（advisory 互斥公平竞争），R12-F1"对齐先于去重"在第三实例入列后继续成立；
- 252-dev 三次 skip tick 时刻 03:10:00/03:20:00 **秒级钉边界**（旧实例相位 :X8:52 启动），`nextAlignedWait` 对新启动进程同样成立；
- **频次：4 条/窗（双相位）→ 2 条/窗（单相位）**，R13 §八预测的"重建后 1 次/窗"兑现。

### 墙钟占比（如实登记：频次达标、时长抬升抵消部分收益）

- 本窗 4 窗样本：analytics+audit_summary 合计 248.5s / 2400s ≈ **10.4%**——与 R13 过渡期持平，**未达 ~5.2% 预测**。原因：单次 REFRESH 时长 36-70s（R12/R13 基线 15-27s，抬升 1.6-2.5×），抵消了频次减半；其中 03:20 audit_summary 49.4s 为离群值。
- 对冲项：**mv_data 漂移核查慢条目本窗为 0**（R13 家族内 40×120.9s ≈ 4 条/窗）——消失原因未查（或单次 <1s 不再触发慢日志），下轮全窗复测定性。
- 判定：**核心判据（2 次/窗→1 次/窗、单相位）达成**；墙钟 ~5.2% 预测**不宣称达成**，登记下轮以完整 98min 窗复测（本样本为深夜 40min 小窗，时长可能有日时段负载分量）。

## §二、D6 同窗项复核（09-29 09:36 执行 → 本轮全项回验）

| 项 | 本轮实测 | 判定 |
|---|---|---|
| max_wal_size | SHOW = 4096（4GB） | ✅ |
| checkpoint_timeout | SHOW = 900 | ✅ |
| log_statement / log_min_duration_statement | none / 1000ms | ✅ |
| /dev/shm | inspect ShmSize=1073741824（1GB） | ✅ |
| LogConfig | inspect size=1GB（容器 09-29 09:37 起未重建） | ✅ |
| **checkpoint 900s 增速**（D6 执行记录遗留复测点） | pg_stat_checkpointer num_timed 5269@02:58 → 5271@03:38 = **+2/40min**（900s 节奏预期 +2.7；旧 300s 节奏应 +8） | ✅ **900s 生效确凿，D6 §遗留关闭** |

**D16：provider_events 契约对齐 —— 双侧闭环**（同窗执行，DDL 文件 `deploy/sql/migrations/2026-07-26-provider-events-local.sql`，sha256 前 16 = `3487e5c39276a391`，走 `podman cp` + `psql -f` + `ON_ERROR_STOP=1` 文件通道，stdin 零污染）：

- 252 真库（对齐前）：5 列全 nullable、无序列、无默认、无 PK；146 行 id=1..146 零重复零 NULL → PK 安全。
- 252 执行：NOTICE skipped CREATE TABLE（幂等）→ CREATE SEQUENCE / ALTER SEQUENCE OWNED BY / SET DEFAULT / DO-block ADD PK / CREATE INDEX 全部成功；`setval(seq, max(id))=146`；终态 PK=provider_events_pkey、default=nextval、idx_provider_events_credential_ts 在位，**146 行零变化**。
- 本机同形态漂移（18 行无重复，无序列无 PK——该 parity 文件本机从未实跑过）→ 同文件对齐 + setval=18，终态同上。
- round11 A4"DDL 归 D16 登记不代执行"在本部署窗口解除，D16 关闭。

## §三、S4 停写 gate：落账复核 + 触发评估 + cron 清理

- **官方台账落账复核**：09-29 09:01 Day7/7 PASS 节已落账并 push（a0b665793，含"✅ 达标 + 可评估触发停写 + 提醒删 cron"）；09:30 收口任务（automation-3497ecaa）09:30:06 执行但未追加新节——09:01 节已实质覆盖其全部内容（达标标注/前置提醒/删 cron 提示），判定无增量。
- **触发评估已落台账**（本轮追加"### S4 停写触发评估（2026-09-30）"节，见 storage-observation-ledger.md）：
  - 前置① schema：711/712/747 在 252 实测在位 ✅；
  - 前置① 观察：**生产侧同类 7 天观察未跑**——gate 判定按环境独立（台账既定原则），**生产不触发**；
  - 前置② claim 漏镜像：D12 补偿（747）上线后观察期零复发，停写后度量自然消亡，不构成阻塞；
  - **结论：本机具备触发条件（settings 一键收口，plan §4-S4），实际拨动属存储行为变更，留待用户拍板；生产 252 观察前置未满足不触发。**
- **cron 清理**：`automation-15b858fd`（每日观察轮，09-29 跑满 Day7 使命，runCount=15）与 `automation-3497ecaa`（Day7 收口判定，completed）**已删除**；用户自建的每日 06:00 SQL 审计 cron（automation-f0a3bf63）不在清理范围，保留。

## §四、E6/F2 外部轨道回执状态（round13 §四移交项跟进）

| 项 | 本轮回执取证（podman logs --since 09-29 06:01） | 状态 |
|---|---|---|
| **E6 pocket $4** | `integer versus bigint` DETAIL ×44/20.8h（**全天均值其实低于 round13 ~6×**；夜间节奏近 1h ×13 ≈ 4.6min 与 round13 ~5min 相当，日间更稀疏）；签名未变（char135 `enabled = CASE WHEN $4=0`） | **未修复，持续**。维持移交 pocket 轨道；写方非本仓（round13 A3 grep 铁证不变） |
| **F2 smm 股票 SQL** | fundamentals/limit_up_temp 命中 ×19 + daily_kline 慢查 ×19/20.8h（05:55 突发型在后续窗口持续出现） | **未修复，持续**。维持移交 smm 轨道（与 E7 同轨） |
| E7 smm 认证失败 | 本窗未专项采样（round13 窗口 ×0） | 维持观察 |

外部轨道无回执通道（写方仓库不在本仓权限内），本轮以频次/签名比对为回执状态；两组均无恶化（同率持续），无新增签名需要移交。

## §五、本轮登记与纪律

**新事实**：

1. 252-dev = systemd 单元 `llmgo-252-dev`（round13"裸进程"身位纠偏）；重部署路径 = 换 binary + restart，version.json SSOT 随行。
2. 252-dev ready:false 根因 = 未配置 Redis（redis:null 结构性 not_ready），非构建缺陷；refresher 走 advisory-only 设计路径不受影响。
3. 252-dev 入列后 R12-F1 三实例收敛:每窗 1 winner 公平轮转（03:30 窗 winner 即新 252-dev,advisory 互斥不偏向老实例）。
4. mv_data 漂移核查慢条目本窗 0（R13 ~4 条/窗）——待定性。
5. 753/754 已于 09-28 02:43 应用（D6 执行记录"待存储轨道随部署执行"描述已过时,实测在位）。

**纪律新增**：

- **㉟ 对账窗口"缺失"判定前先打印服务器 now()**——`podman logs --since` 只保证下界,上界是取证时刻;"零条目"结论必须在取证时刻 ≥ 窗口上界 + 最长操作时长后才可下（本轮 A1 教训:03:26 取证判"03:20 窗全空疑集群挂起",实为 03:20:36 条目在晚 6 分钟的 pull 中才出现——不,条目 03:20:36 已存在,是 pull 时刻 ~03:19 早于窗口闭合;两次错判同源）。
- **㊱ 生产库 psql 纪律（F1 固化）**：stdin 严禁喂非 SQL 内容（round13 转录污染事件）；脚本一律走文件通道（scp/podman cp → `psql -v ON_ERROR_STOP=1 -f`）；ssh heredoc 只允许 `bash -s` 纯 shell、SQL 全部经 `-c "..."` 双引号参数传递；取证 grep 模式须覆盖结论的两面（skip 与 win 行形态不同,"materialized view refresh" 不匹配 "refreshed routing_*",head 截断可造假缺失）。

## §六、自审计（同日批判式复核）

| # | 初判 | 复核结果 |
|---|---|---|
| A1 | 03:20 窗 PG 零 REFRESH + journal 疑似静默 → 疑三实例集群挂起 | **时间基准错判**：多次取证时未先打 now(),误把 ~03:19 的 pull 当作 ~03:27;03:20:36 条目一直存在,252-dev journal 从未中断（03:28:16 apihub 等持续）。修正判据=纪律㉟。另"end at 03:15:05"是 journald 落盘滞后非进程停写 |
| A2 | 新构建 ready:false → 疑部署回归 | /readyz 拆解:database 1.5ms 通、redis=null,.env.dev 无 REDIS_* → 结构性 not_ready,旧构建同态。非回归 |
| A3 | pg_locks 见 245 双 advisory 锁（一 active 23.8s 一 idle-in-tx）→ 疑锁泄漏阻塞 03:20 窗 | 数秒后复查锁已自释放——正常短持锁（刷新/漂移核查路径）,非泄漏。勿把短持锁当事故 |
| A4 | 三窗 winner pid 各异（49244/49503/49308+50846）→ 疑多 winner 并发 | refreshView 每视图独立 Acquire,同实例不同池连接=不同 pid;以 pg_stat_activity pid→client_addr 钉身位（50846=210）确认仍单 winner 轮转。归属判定勿依赖 pid 相同 |
| A5 | 频次减半 → 直接宣布 ~5.2% 达成 | 实测墙钟 ~10.4% 未达预测（单刷时长 36-70s 抬升）——如实登记不宣称,下轮全窗复测。避免拿频次达标冒充墙钟达标 |
| A6 | "252-dev 03:30 窗无 skip 无 win → tick 未发生" | win 行在（03:30:41/03:30:57）,首轮 grep 模式（"materialized view refresh"）不匹配 win 形态（"refreshed routing_*"）且 head 截断。取证 grep 须覆盖结论正反两面（并入纪律㊱） |

## §七、下一轮提示词（建议）

> 以本文 §一 §二 §四 §五为起点。优先级：
> 1) **REFRESH 家族全窗复测**：252-dev（dd7e4527-2338）入列后单相位已实证（§一四窗）,但单刷时长 36-70s 抬升墙钟至 ~10.4%（R13 基线 15-27s）——以完整 ≥90min 窗判定时长抬升是日时段负载还是数据增长,并定性 mv_data 漂移核查零条目（§五.4）;
> 2) **S4 停写本机触发**：评估已落台账（本机可触发）,待用户拍板后执行 settings 一键收口并开停写后观察;生产 252 如启动观察期需重建 cron（流程复用 storage_observation_round.sh,gate 按环境独立）;
> 3) E6（pocket $4,×44/20.8h 未修复）/F2（smm ×19+19 未修复）外部轨道回执继续跟进;
> 4) 常规慢查/错误对账 + 252-dev 新构建稳定性观察（三实例混跑首日）。
> 纪律沿用 ⑪-㊱。

## §八、handoff 更新

- 本轮合入 main：本文档 + storage-observation-ledger.md（S4 触发评估节）。**无代码改动、无新迁移；版本身份三文件不在本轮提交内**（见 §九，部署身份以 252 服务器侧 version.json = 2.5.8-dd7e4527-20260929-2338 为准）。
- 记忆库：`llm-gateway-go-audit-cycle-progress` 追加第十四轮;`pg-252-sql-log-audit-facts` 增补（252-dev systemd 身份与重部署路径、ready:false 根因、四窗收敛数据、checkpoint 900s 实证、D16 双侧闭环、E6/F2 回执状态、纪律㉟㊱）。
- 原始物证：252 侧 podman logs / journalctl 输出已摘录于本文各表;部署备份 /opt/llm-gateway-go/bin/gateway.bak.20260930-025804;D16 DDL 文件 sha256 3487e5c39276a391。
- 部署状态：154 = e1b73e88-2323、245 = a8a34023-2324（09-29 切流,均含 R12-F1）、**252-dev = dd7e4527-2338（本轮 02:59 切换,含 R12-F1,ready:false 为无 Redis 环境常态）**——三实例全部携带 R12-F1,过渡期结束。

## §九、并行轨道事件与提交补记（同日 04:0x 补记）

本轮收尾提交时发现并行轨道（修订审计三十三/三十四轮线,迁移 759-762）在窗口内推进 main 并**把共享工作区切换到了其特性分支 `fix/glm5-effort-table-vs-upstream`**（工作树跟踪文件随 checkout 复位,未提交改动被吞）：

- **被吞改动**：①本节 S4 触发评估的台账首次追加（已重写重提,内容不变仅补注记）;②version.json/VERSION/web/public/version.json 的 2338 bump（未及提交）。
- **并行线消费 seq 2337**（version.json = ef93cf72-2337）——其 bump 在分支切换后的工作树上执行,floor 未拦住比已部署 2338 更小的号;本机已部署身份 dd7e4527-**2338** 与仓库现值 2337 形成**号序倒挂**（无碰撞,只影响下次 bump 从 floor 2338+1=2339 起算,无功能影响）。
- **事件后实测（全部 PASS）**：252-dev 服务身份仍 = dd7e4527-2338（/healthz）+ systemd active;252 schema_migrations max 仍 = 758（并行线 759-762 尚未触碰生产库）。
- **处置**：按 09-25 实战协议——不动并行会话的分支/工作区,用 `git worktree` 临时检出 origin/main,只提交本文档与台账两文件后推送删 worktree;**不触碰并行线的 version.json**,部署身份以服务器侧为准。
- **教训（并入纪律㉟族）**：共享工作树长窗作业（本窗 02:30-04:00 跨部署+取证）期间,未提交的文档/版本改动随时可能被并行轨道切分支吞没——**长窗内应分段及时提交**,至少在部署动作完成后立即提交版本身份文件。

## §十、同日批判式复审（修正轮，04:4x-05:2x）——发现 4 项实质问题并修正

复审原则：只认新取证,不认本文件前文的声明。逐项复核后发现并修正：

**F-A（P1，已修正）：D16 只修了存量库,源头未闭环。** `sql/objects/tables/provider_events.sql` 基线仍是裸表（无 PK/无序列/全 nullable）,parity 文件依旧无投递通道——新环境会重新产出漂移态,手工 DDL 也无台账重放保障。**修正 = 启动迁移 `763_provider_events_contract.sql`**（752 收编 036 同款）：与 parity 文件逐列一致 + conname 守卫 PK + **is_called 感知防回退 setval**（无脑 setval(max(id)) 在删行场景会把序列拨低 → 默认取号撞主键）+ fail-closed（存量 id 重复则 PK 显式失败）+ 文件自带 schema_migrations 自登记（695-705 定式）;`.down.sql` + 契约测试 `migration_763_test.go`（C1-C7,含 parity 双通道漂移守卫与 `apply-db-revision-sequence.sh` 注册断言）;登记进 `apply-db-revision-sequence.sh`。本机真库双跑幂等 PASS（数据 18 行零变化,seqval≥max_id,双台账行在位）。**758 纪律自检：全部 DDL 顶层语句,DO 块只做守卫/setval 逻辑。**

**F-B（P1，自纠）：763 初版把 credential_id/event_kind 收紧为 NOT NULL——会炸新环境。** `pg_reconciliation_store.go:287` 实证存在 `credential_id=NULL` 的真实写入路径。已改回 parity 文件逐列形态（仅 id/ts NOT NULL）。教训：**"契约对齐"禁止顺手收紧约束——以真实写入路径为准,不以理想形态为准。**

**F-C（P2，已修正措辞）：§四 E6"同率持续"不准确。** ×44/20.8h 全天均值比 round13（×20/98min）低 ~6×,只是夜间节奏（近 1h ×13≈4.6min）与 round13 窗（同为夜间）相当。已改为"夜间节奏相当、全天均值更低"。

**F-D（P2，登记局限）：单相位收敛的 winner 归属只钉实了 2/4 窗。** 03:20（pid 49503→241）与 03:30（pid 50846→210）已钉到单台;03:00/03:10（pid 49244）连接已关闭,只能收敛到 {154,245} 集合。"每窗恰 1 winner"的结论不受影响（PG 日志频率铁证）,但归属精度有上限——复审窗口内尽快查 pg_stat_activity,连接会失效。

**F-E（P2，新事实）：schema_migrations 混名陷阱。** 台账混有 V 前缀（V359,applied_at 回溯 2026-08-18,疑并行线台账回填）与日期命名行——`max(version)` 按文本序会返回 V359 而非数字链最高版本。**数字链水位必须 `WHERE version ~ '^[0-9]+$'` 过滤**（本文 §九 的"max=758"读数当时凑巧正确,方法学不严谨）。修正后复测：数字链 max 仍 758,759 空号、760-762 为并行线在途（文件在库、未登记 `apply-db-revision-sequence.sh`,755 同未登记）。

**F-F（P3，登记）：本机 dev 库账本缺口。** 本机缺 754-758/760-762 → `apply-db-revision-sequence.sh` 整链在本机盲跑会连带应用 756（request_logs 建索引,持写锁）等越权项——763 走外科手术通道（单文件双跑 + 双台账手工登记,形状与脚本一致）。本机 763 台账行先行于 754-758 属已知缺口,不遮蔽后续补应用（各迁移独立判账）。

**F-G（P3，复核补充证据）：部署二进制身份哈希闭环。** 本轮补做 sha256 双端比对:本地构建物 = 服务器 `/opt/llm-gateway-go/bin/gateway` = `86de03f9ecfaf33f…`（备份 a0e4d9c5 = d4b217bf… 另一值,身位无误）。前文 §一 的 /healthz 身份证据只读服务器侧 version.json,**单独不构成二进制证据**——此后部署验收必须含哈希比对。

**F-H（P3，观察登记）：provider_events 活写速率异常。** D16 修复后 1.5h 内 146→177（+31 行）,远超历史月级基线——疑并行轨道压测/告警风暴,D16 语义不受影响（PK 全程无冲突=活写验证）,登记观察不处置。

**F-I（P3，历史注记失真澄清）**：台账 09-26 节"automation-15b858fd 已达 maxRuns=12 停止"与实测 runCount=15 矛盾（实际持续跑到 09-29 使命完成）——不影响 Day 计数判定（每日一轮记录在案）,仅注记更正。

**复审后仍成立的结论**：D6' 部署/单相位收敛/checkpoint 900s/S4 评估/cron 删除/E6-F2 登记——全部经新取证复核无翻案。
