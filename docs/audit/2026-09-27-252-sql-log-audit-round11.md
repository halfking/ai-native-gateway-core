# 252 PG SQL 日志审计轮·第十一轮（2026-09-27）

以第十轮（docs/audit/2026-09-26-252-sql-log-audit-round10.md）§八提示词为起点：①五项对账延续；②D13 部署回验；③饱和观察；④storage-merge 观察期 G2；⑤E6/E7/D6' 移交项跟进。纪律沿用 ⑪-㉚。

## §〇、取证与起点

- 起点：origin/main = local HEAD = `908256008`（push 前重算）。
- 窗口：2026-09-27 03:20:31→06:01:48 CST（161min）。快照 64,759 行 / 33MB（252:/tmp/pg252-round11-window.log.gz，本地 /tmp/pg252-round11-window.log）。
- 解析：podman ctr.log → PG stderr 条目切分，慢查/错误全按**查询形状**分层（纪律㉘）。
- 总量：>1s 慢查 **872 条（sum 3,364s）**、ERROR 1,133（user cancel 1,017 + pocket $4 33 + json hex 33 + statement timeout 3 + autovacuum cancel 2 等）、FATAL 111（conn lost 79 + smm 认证失败 32）。
- **部署身位（关键前置）**：154 = `17316e48`-2265（09-25，**不含 D13**）；245 = `1dfe88c0`-2271（09-26，**已含 D13**；ancestry 实证 `595380023` 为其祖先）；252-dev = `a0e4d9c5`-2246（ready:false=D6' 未修）。三台连接 252 PG 实测：209(154)×21、241(245)×16、210(252-dev)×15。

## §一、五项对账（161min）

| 项 | 第十轮 161min 基线 | **本轮 161min** | 评估 |
|---|---|---|---|
| policy/alternatives 取消（FIX-1 真形状） | 0 | **0**（policy 关键字 cancel 仅 1 条=D2''' 探针族尾巴） | ✅ 零回归 |
| index_hot DELETE >1s（FIX-2） | 0 | **0** | ✅ |
| D10 candidate CTE 真形状 | 8 | **4** | ✅ 改善（饱和仍存） |
| D11 fingerprint 真形状 | 0 | **0** | ✅ |
| cancel 总量 | 603 | **1,017（+69%）** | ⚠️ 见 §二归因 |

cancel 分层（纪律㉙按 STATEMENT 关联）：claim UPDATE ×499、trace_events UPDATE ×452、api_keys last_used ×48、session_bodies ×13、其余 ~5。与饱和窗口（§三）及 154 未修 D13 的份额一致，非新洞。

## §二、D13 部署回验（第十轮 FIX-A）

- `uq_request_logs_2026_09_final_success_session` **idx_scan 实测 473→479（25min 增量）**——245 流量份额的 claim promoted 臂 Index Only Scan 已在生产生效（第十轮仅 EXPLAIN 预证）。
- claim 慢查 ×234（max 4.97s）+ cancel ×499 仍居首 = **154（不含 D13）的流量份额 + 饱和放大**的预期形态。claim 臂全扫（1.27M 行/次）仍在 154 发生。
- **处置**：154 下次受控部署（携 D13）后，claim 慢查/cancel 应大幅回落——列入下轮对账基线（本轮 234+499/161min）。

## §三、饱和观察（纪律㉚）

- 慢查时间分布均匀（03/04/05 时各 204/310/340 条），非第十轮"04:59 后爆发"型——**主机持续中压**：窗口初 load 16.5/4 核。
- 06:00 整点尖峰源=`find / -name pagemap` 全盘扫描（一次性，root，~9% CPU）+ redis BGSAVE fork；**find 结束后 25min load 16.5→9.37**，自愈。vite build 未复发（FIX-B 维持）。
- checkpoint 33 轮（5min 节奏如常）；autovacuum lock skip ×14 但四热表 last_autovacuum 均于 06:06-06:13 完成，无积压。
- `invalid length of startup packet` ×52 = **每 5min 恒定 2 连**的 TCP 端口探活（分时全窗口恒定），非我方代码，良性噪声登记。
- ShareLock 行锁等待族（log_lock_waits）= request_logs_hot 同元数行 claim vs trace 收尾写的竞争，饱和放大症状。
- **误标纠正**：第十轮"usage_facts date_bin ×32"实为 **supplier_errors 聚合**（`WITH base AS (SELECT date_bin(...) FROM supplier_errors_hot ∪ supplier_errors)`，bg/supplier_error_stats_aggregator，5min 窗）。本轮 EXPLAIN cost 4.78、分区全空（数据仅 hot 795 行）——查询健康，×75 慢查纯饱和膨胀，无需动。

## §四、storage-merge 观察期（GLOBAL_G2）

- **G2 漏镜像对账 = 0**（v1 claim 置位 ∧ 无 turns ∧ 无 outbox 登记，7h 窗，`session_turns` ∪ `session_turns_hot` 并集口径）。215 个 claimed 行全部在 session_turns_hot 有 turns。
- ⚠️ 本轮自摆乌龙一次：初版对账只查 `session_turns` 分区父表得 222"泄漏"——正是 request-logs-view-shape 记忆"对账须父表∪hot"陷阱的现场重演（近期 turns 全在 hot 侧未提升）。并集口径复测归零。**新纪律㉛：session_turns/request_logs 族对账一律父表∪hot 并集，禁止单侧口径**。
- outbox 近 24h 零行（done 已被 trim、pending 空）；dead 147 行全部为 09-23（D12 部署前）hook 尸体，此后零新增。
- **观察项 D17**：claim grant 率 22-50/h（约 700/24h）vs 第八轮"granted 9/24h"显著漂移；结合 outbox 零 claim 行，D12 补偿登记要么秒级被 replay+trim（拍点难观测）要么未触发——端态（零漏镜像）成立，不阻塞 S4 前置，登记归因项。

## §五、本轮修复（FIX-C）：providerprofile jsonb 参数族——生产告警/事件双断点根修

**发现**（`invalid input syntax for type json` ×33，全部集中在 04:50 引擎单轮）：

```
INSERT INTO provider_profile_alerts (..., details, action_taken)
VALUES (..., '\x7b22616374696f6e223a2261647669736f72795f6f6e6c79227d', 'none')
ERROR: Token "\" is invalid
```

**根因链（两层）**：
1. 网关全局 SimpleProtocol（pgx 默认 QueryExecMode，db/db.go）下 `[]byte` 参数被内联为 **bytea hex 字面量** `'\x7b…'`——落 jsonb 列解析必炸。`PGAlertStore.SaveIfNew` 的 `jsonBytesOrNULL(detailsJSON)` 直传 []byte → **凡带 Details 的告警自 2026-07-27（4ebbaa424）起 100% 丢失**：真库 805 行 details 全 NULL、max(trigger_date)=09-25、04:50 一批 31 条 critical 告警全灭且每轮重试重复烧。
2. `RecordEvent` 引用 `nextval('provider_events_id_seq')`——该序列**本地/252 真库均不存在**（provider_events 表为带外建的松散形态：无 PK/无默认值；`deploy/sql/migrations/2026-07-26-provider-events-local.sql` 未进 installer 通道）→ 42P01 必炸，先于 jsonb 问题。provider_events 双库 0 行=双断点叠加。

**同族甄别**：包内 5 处 marshalJSON→DB 流点，`pg_profile_store.go`/`pg_store.go` 传 `string(xxx)` + `::text::jsonb` 强转（正确写法，真库 1,334 行合法 JSON 实证）；**坏点仅 3 处**（alert_store/credential_actor/reconciliation_store）。`json.RawMessage` 入参全仓扫描零命中。

**修复**（代码根修，零迁移）：
- `jsonBytesOrNULL` → `jsonParamOrNULL`：非空返回 `string(b)`（SimpleProtocol 内联 JSON 文本字面量，jsonb 直收），空/nil 返回 NULL；
- `RecordEvent`：取号改 `COALESCE(max(id),0)+1`（沿用 reconciliation 同表同惯例；真库无 PK 无冲突面）+ 注明序列缺失约束；
- `pg_reconciliation_store.go` payload 同步 string 化。

**测试钉**（教训：旧真库测试从未设置 Details，两个月零报警；RecordEvent 测试因无 TEST_DATABASE_URL 永远 skip）：
- `pg_alert_store_internal_test.go` TestJSONParamOrNULL：断言返回 string 且合法 JSON（防 []byte 回归，`%T` 钉死）；
- `pg_alert_store_test.go` TestPGAlertStore_SaveWithDetails：真库 round-trip（details->>'action' + JSONEq）；
- 本机真库实跑：`TEST_DATABASE_URL=… go test ./domains/providerprofile/ -run 'TestJSONParamOrNULL|TestPGAlertStore|TestPGCredentialActor_RecordEvent' -count=1` → **ok**（含首次真库跑通的 RecordEvent 全链路）；无 DB 门控包套件 ok；go vet 净；gofmt 净（新改文件）。

## §六、登记（本轮不修，移交/顺延）

| 项 | 证据 | 处置 |
|---|---|---|
| **D15：board fallback COUNT(DISTINCT) 30s 击杀** | `fillOverviewCountsFromLogs`（admin/dashboard_board_fallback.go:36）7d/30d/90d 三连查 canonical 视图，03:21-03:23 三条 30s statement timeout（rolconfig）+ ×8 慢查 max 29.6s | admin 轨道：改读物化 stats（stats_usage_daily 语义断裂未修前先挂账）；饱和放大器非新退化 |
| **D16：provider_events 表形态漂移** | 真库无 PK/无序列/无默认值 vs 2026-07-26 迁移文件契约；该文件未进 installer embed 通道 | 控制窗口对齐（0 行表低风险）；本轮代码侧已兼容两种形态 |
| **E6 加剧：pocket $4** | ×33/161min（r10 ×12，~3×），节奏仍 ~5min | 维持移交 pocket 轨道（未回执） |
| **E7 加剧：smm 认证失败** | ×32/161min（r10 ×14），节奏 10-12min→**5min** | 维持移交 kaixuan smm 轨道；stale 凭据客户端未清且频率翻倍 |
| **D17：claim grant 率漂移** | 22-50/h vs 第八轮 9/24h；outbox 零 claim 行可观测 | 观察项：D12 登记要么秒级 trim 要么未触发，端态零漏镜像成立；下轮结合 154 部署后对账 |
| D6 / D6' / checkpoint 调参 / usage_facts date_bin(R67 749) | D6' 252-dev 仍 ready:false；749 索引树 valid（分区壳 0 字节属正常，pg_stat_user_indexes 不含父壳=查列陷阱） | 维持原登记；D6 重建窗口一并做 |

## §七、自审计（同日批判式复核）

| # | 初判 | 复核结果 |
|---|---|---|
| A1 | G2 对账 222 行泄漏 → D12 失效 | **假阳**：只查 session_turns 父表漏 hot 侧（215/215 有 turns）。已知陷阱现场重演，升格纪律㉛。教训：对账 SQL 上线前先跑"并集/单侧"差分 |
| A2 | idx_usage_facts_occurred_at "不在 pg_stat_user_indexes + 0 bytes" = INVALID 残留 | usage_facts 是分区表：父壳索引 0 字节、pg_stat_user_indexes 不含父壳均为**正常形态**，indisvalid=true。查分区索引状态必须逐子分区（与 request-logs-view-shape 的"查列数须指定视图名"同族陷阱） |
| A3 | 04:50 json 报错 33 条 → 疑外部客户端（纪律㉑格式指纹） | 字面量全内联 + 无 $n 残留 = Go SimpleProtocol 形态；定位到 hex []byte 内联，真库测试复现闭环。归因正确 |
| A4 | 想顺手补 provider_events 序列迁移 | 真库表形态与迁移文件契约漂移（无 PK/无 NOT NULL），补序列需控制窗口对齐全表契约；代码侧 max+1 已兼容两种形态——**DDL 归 D16 登记不代执行**（部署=维护窗口先例） |
| A5 | ×75 supplier_errors 聚合慢 → 想加 occurred_at 索引 | EXPLAIN cost 4.78 + 分区全空——查询健康，饱和膨胀。纪律㉚再验证：先主机后计划 |
| A6 | jsonAlerts 33 条以为持续泄漏 | 分时归位：04 时 31 条=引擎单轮全批失败，告警引擎低频批量运行。影响面="每轮全灭"而非"每条必炸" |

## §八、下一轮提示词（建议）

> 以本文 §一 §二 §四 §五为起点。优先级：
> 1) **154 部署回验**：154 携 D13 部署后，对账 claim 慢查/cancel（本轮基线 234+499/161min）应大幅回落；pss 增量确认 promoted 臂 idx_scan 斜率；
> 2) **G2 观察期收口**：earliest 09-29 满 7 天（对账一律父表∪hot 并集，纪律㉛）；D17 claim grant 率漂移顺带归因；
> 3) **FIX-C 部署回验**：下次受控部署后确认 provider_profile_alerts.details 开始落行、provider_events 首行出现、json 报错归零（本轮基线 33/161min）；
> 4) E6/E7/D15/D16 移交项跟进回执；
> 5) D6 容器重建窗口：max_wal_size/checkpoint_timeout + LogConfig 持久化 + /dev/shm 1g。
> 纪律沿用 ⑪-㉚ + 本轮新增 **㉛ session_turns/request_logs 族对账一律父表∪hot 并集（A1）**。

## §九、handoff 更新

- 本轮合入 main：FIX-C（providerprofile 3 文件 + 2 测试钉）+ 本文档。无迁移、无 schema 变更。
- 记忆库：`llm-gateway-go-audit-cycle-progress` 追加第十一轮；`pg-252-sql-log-audit-facts` 增补（63-69：D13 部署身位与 idx_scan 实证、providerprofile hex-jsonb 族、provider_events 序列缺失、G2 并集口径、supplier_errors 误标纠正、E6/E7 加剧、startup packet 噪声）。
- 原始物证：252:/tmp/pg252-round11-window.log.gz + 本地 /tmp/pg252-round11-window.log、/tmp/r11_parsed.json、/tmp/r11_probe*.sql（252:/tmp 同名）。
- 部署状态：154=17316e48-2265（无 D13）、245=1dfe88c0-2271（含 D13）、252-dev=a0e4d9c5-2246（ready:false）。FIX-C 与 D15 归属代码轨道，随下次受控部署上线。
