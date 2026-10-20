# 2026-10-20 审计轮：candidate_failure_monitor 的 S4 停写假阳性二分类

> 触发：本机 `grep "failure log is stale\|watermark stalled past promote retention"` 后的取证。
> 产物提交：`aee31a117`（bg/candidate_failure_monitor.go + bg/candidate_failure_monitor_s4_stop_test.go）。
> ⚠ **生产 154/245 本轮未部署**，本修复只在 origin/main 上，未上线。

---

## 0. 一句话结论

**上一轮的读数判据错了，且被它带出的结论也错了。**
provider 侧修法**已在生产生效**（WARN + `source_max_aggregation_id`）；
真正未修的是 candidate 侧——它的修法部署了，但**机制不完整**：
`lastRequest` 门（`lastRequest=nil` ⇒ 不告警）在 S4 停写下**永远不成立**。

---

## 1. 读数手段本身的错（先判判据，再判产品）

objective 里给的判据是：

> grep `…/run/*.log`；含 `source_max_aggregation_id` 字段的 WARN 属正常（修法生效），
> 不含该字段的 ERROR ⇒ 二进制没换、需重部署。

**三条都在实测里被推翻**：

### 1.1 路径不存在

```
$ ls /opt/llm-gateway-go/run/
ls: /opt/llm-gateway-go/run/: No such file or directory
```

本机是 host-first 部署（日志在 `~/kaixuan/llm-gateway-go-v2/logs/`），
`/opt/llm-gateway-go/` 是 154/245 的蓝绿布局 ⇒ **判据里的路径是生产形态**，
在本机永远返回空。**判据错了 ⇒ 「没 grep 到任何东西」这个结果没有信息量**。

### 1.2 `tail` 吃掉了关键行

按 objective 的命令实际执行（改成本机路径）时，`tail` 只留下 `2026-10-19` 的行：

```json
{"level":"ERROR","msg":"provider_error_aggregator: watermark stalled past promote retention…",
 "stall":"57h10m1.176877s","pre_watermark":40}
```

**不含** `source_max_aggregation_id` ⇒ 按判据 = 「二进制没换」。**这是错的**。
因为同一批文件里 `2026-10-20` 的行才是关键：

```json
{"time":"2026-10-20T03:41:22.614642+08:00","level":"WARN",
 "msg":"provider_error_aggregator: watermark did not advance (source is caught up); no rows at risk of being stranded",
 "stall":"72h45m48.640491s","pre_watermark":40,"source_max_aggregation_id":0}
```

⇒ **provider 侧修法已生效**（`source_max_aggregation_id` 在场，且等级已从 ERROR 降为 WARN）。
⇒ **`tail` 是把「已修」误读成「没修」的直接原因**（LESSONS.md「量具」族同源）。

### 1.3 判据只覆盖两条 ERROR 中的一条

`source_max_aggregation_id` 是 **provider 侧**的字段。candidate 侧的
`candidate_failure_monitor: failure log is stale` **从来就没有这个字段**
（它的是 `last_insert_at` / `last_request_at` / `age` / `threshold`）。
⇒ 判据若照抄「ERROR 且无该字段 ⇒ 没修」，**会永远判 candidate 侧没修**，
哪怕修好了。

★⇒ **判据纪律（新增）**：一条「用日志字段判修法是否生效」的判据，
**必须逐个组件核对字段名**——同一条链路上的两个组件字段名不同是常态，
把一个组件的判据套到另一个组件上 = 恒假阴性。

---

## 2. 真缺陷：candidate 侧的修法机制不完整

### 2.1 现场证据（`2026-10-20`，修法已部署之后仍在打）

```json
{"time":"2026-10-20T18:04:50.632264+08:00","level":"ERROR",
 "msg":"candidate_failure_monitor: failure log is stale",
 "last_insert_at":"2026-10-20T17:20:35.617051+08:00",
 "last_request_at":"2026-10-20T18:04:00.898539+08:00",
 "age":"44m15s","threshold":1800000000000}
{"time":"2026-10-20T20:04:12.596243+08:00","level":"ERROR",
 "msg":"candidate_failure_monitor: failure log is stale",
 "last_insert_at":"2026-10-20T18:06:44.380727+08:00",
 "last_request_at":"2026-10-20T20:04:06.771319+08:00",
 "age":"1h57m28s","threshold":1800000000000}
```

`age` 30 分钟后又起跳（44m → 1h57m）⇒ 每 30 分钟一条，**不是假绿**。

### 2.2 机制错在哪

`151a0ba13` 的思路是把 `lastRequest` 换到「不含 session_turns UNION 的 v1-only 腿」，
理由是「S4 停写下该视图 0 行 ⇒ `lastRequest=nil` ⇒ 不告警」。
**但停写切的是 v1 族的写，不是所有请求的写** —— 会话族（session mirror）照写，
于是 `lastRequest` 恒有值 ⇒ **那道门在 S4 停写期永远打不开**。

⚠ 注意这与 §2.2 的注释里写的「S4 停写后该视图也 0 行」**矛盾**：
`last_request_at` 实测就在几分钟前 ⇒ 前提是错的。
（`885` 迁移后源换成 `db.SessionFamilyTurnsHotSourceSQL()` + 探针排除谓词，
**只是把「读哪一族」换了，「靠 lastRequest=nil 关门」这个机制本身没换**。）

### 2.3 修法：二分类

问题本质：**「failure 冻结 + request 活跃」这一个形态，两种成因**。

| 成因 | `source_max_failure_id` | 等级 |
|---|---|---|
| S4 退役 / 主动停写（writer 曾活跃） | > 0 | WARN |
| failure writer 真坏（从未写入） | 0 / NULL | ERROR |

`pre_watermark=40` / `source_max_aggregation_id=0` 那条的形状是反的
（source 空 ⇒ caught-up），本条不同（request 活、failure 停），**必须另判**。

改动（`bg/candidate_failure_monitor.go`）：
- SQL 增两条子查询：`max(aggregation_id) FROM candidate_failure_logs_with_current_month`
  与 `max(id) FROM <request surface>`；
- 抽纯函数 `candidateFailureMonitorClassifyStale(failureSourceMaxID int64) stalenessVerdict`；
- `switch` 分派：LikelyStopWrite → `slog.Warn`，default → `slog.Error`；
- 两条日志都带 `source_max_failure_id` + `source_max_request_id`（对齐 provider 侧形态）。

⚠ **fireAlert 两条分支都调**：只降日志等级、不降 webhook 投递——
「S4 停写」是运维已知状态，日志不该刷 ERROR，但审计台账仍要留行。

⚠ 已知有偏：`stalenessThreshold` 若短于「启动到首次失败」的最大间隔，
「刚启动没失败过」会被判成 writerNeverWrote ⇒ 打 ERROR。默认 30 分钟够用。

---

## 3. 门

`bg/candidate_failure_monitor_s4_stop_test.go`（新，3 个测试函数 / 6 个子用例）：

1. `TestCandidateFailureMonitorClassifyStale` — 表驱动 4 例钉死分类方向
   （fid=40 / 1048576 / 0 / 1）。
2. `TestCheckStalenessLogsWarnOnS4StopScenario` — 源码形态门：字段透传、
   两条 max(id) 子查询、`case stalenessLikelyStopWrite:` 与 WARN 文案共存、
   `default:` 在 WARN 之后、`slog.Error` 在 `default:` 之后。
3. `TestFailureLogIsStaleKeepsQuietGatewayBaseline` — 守住「网关无流量不告警」，
   防 S4 修复连带改坏原语义。

⚠ **判据自身踩过一次坑**：`slog.Warn(...)` / `slog.Error(...)` 是**多行调用**，
msg 在单独一行。最初 needle 写成 `slog.Error("…never wrote any rows)")`（含收括号）
⇒ 源码里收括号在下一行 ⇒ `Contains` 恒假 ⇒ 门红。
**两次错都指向同一条纪律：needle 只取「同一行内完整出现」的片段。**
第二次还发现位置断言（`body[warnIdx:errIdx]`）也不该用——
它把实现绑死到一种排版（case 标签在 WARN 之前还是之后）。
已改为「在 switch 块内查三个不变式 + 一个顺序不变式」。

变异验证（5 条，按预期）：**基线 md5 `0e65f167e6f93a2418fb26e57d25de02`，
每条变异后还原 md5 逐字一致**。

| 变异 | 期望 | 实测 |
|---|---|---|
| M1 `case stalenessLikelyStopWrite:` → `case stalenessWriterNeverWrote:`（S4 分支消失） | 红 | 红 |
| M2 `slog.Warn` → `slog.Error`（方向反） | 红 | 红 |
| M3 `max(id) FROM` → `max(NULL) FROM`（`source_max_request_id` 源被摘掉） | 红 | 红 |
| M4 `classifyStale` 里 `fid > 0` → `fid < 0`（分类恒反） | 红 | 红 |
| **M5（阴性对照）** 只在**注释**里加含 `case stalenessLikelyStopWrite:` 与 `slog.Warn` 的散文 | **绿** | **绿** |

⚠ **M3 第一版不算数**：最初把 `sourceMaxRequestIDKey, rid,` 换成 `_ = rid,`
⇒ **语法错误、整包 build failed** ⇒ 「红」是因为编译不过，**不是因为门有牙**
⇒ 这正是「红的原因要写明，否则等于没验」的族。改成可编译的 M3（`max(NULL)`）后才是有效变异。
⚠ M5 是必要的**阴性对照**：门读的 `body` 是从
`func (m *CandidateFailureMonitor) checkStaleness(` 起截的**函数体**，
M5 证明 needle 匹配不会把**注释里的同名字样**误算进代码位
（对应 AGENTS.md 判据纪律第 35(a) 条）。

---

## 4. `bg/passive_probe_listener.go` 三段扫：**阴性结论**

objective 要求按「形态 / 时间窗 / 数据源」扫同链路。四处读点逐一定性：

| 读点 | 形态 | 时间窗 | 数据源（实测） | S4 停写下是否假阳性 |
|---|---|---|---|---|
| L125 `resetCountersOnSuccess` | UPDATE … DISTINCT，streak 归零 | 5 min | 会话族 canonical view | 否（surface 空 ⇒ 无行 ⇒ 不误归零） |
| L182 `pollNewErrors` step 1 | INSERT … SELECT + ON CONFLICT 累加，arming | 5 min（去重 45 s） | 同上 + `SessionBodiesSourceSQL()` | 否（只在 `request_status='failure'` 上 arm） |
| L230 `pollNewErrors` step 2 | UPDATE … 分母刷新 | 5 min | 同上 | 否（空 ⇒ `COALESCE(win.total,0)` ⇒ 分母 0 ⇒ ratio 达不到 0.6） |
| L406 `reviewResolution` 成功计数 | `COUNT(*)`，0 ⇒ 标 unreachable | 6 min（有意宽于 5 min） | 同上 | 否（mirror 照写 success 行；DB 错误时显式 `skippedOnDBError` 不落标） |

**为什么与 candidate 侧同病不同源**：candidate 侧的病根是
**「靠 lastRequest=nil 关门」这个机制**；passive_probe_listener 四个读点
**没有一个用「source 空」当关门条件**——它们的空 surface 一律落到
「不 arming / 分母归零 / 不落标」的安全侧 ⇒ **同链路，不同病**。

⚠ **顺带记一条本轮**未**修的疑点（未定性，留给下轮）**：
L182 的去重 `LEFT JOIN … pps.last_seen_at > NOW() - INTERVAL '45 seconds'`
与 30 s `pollInterval` 不整除 ⇒ 「同一 5 分钟窗被重新聚合」的窗口可能是 3 分钟而非 5 分钟。
这会让 `consecutive_count` 的**实际口径短于名义窗口**。
**本轮未量化、未改**——它与 S4 无关，属「窗口口径漂移」族，需要先量再判。

---

## 5. 本机验证汇总

| 项 | 结论 |
|---|---|
| `/opt/llm-gateway-go/run/*.log` | **不存在**（生产形态，本机 host-first 在 `~/kaixuan/llm-gateway-go-v2/logs/`） |
| provider 侧修法 | **已生效**（`2026-10-20T03:41:22` WARN + `source_max_aggregation_id=0`） |
| candidate 侧修法 | 部署了但**机制不完整** ⇒ 本轮已修（`aee31a117`），**未上线** |
| 运行中二进制 | `2.5.8.2747` / `21780d1a`，`/Users/niuer/kaixuan/llm-gateway-go-v2/bin/2.5.8.2747/gateway` |
| 该二进制是否含 `151a0ba13` | **含**（`git merge-base --is-ancestor 151a0ba13 21780d1a` ⇒ exit 0） |

⚠ **顺带撞见一条与本轮无关但每 5 秒刷一次的 ERROR**（未处理，留给下轮）：
```
"auto route listener: refresh failed",
"error":"rollup credential_model_index: insert: ERROR: column rl.credential_id does not exist (SQLSTATE 42703)"
```
⇒ 42703 = 列不存在 ⇒ 与 `890` v1 族退役同族（读面已切会话族，列名不同步）。
**本轮未修**：它涉及 `credential_model_index` 的 rollup 语义，不在本次授权范围内。

---

## 6. 遗留风险

1. ★ **生产 154/245 未部署 `aee31a117`** ⇒ candidate 侧仍会每 30 分钟打一条 ERROR。
2. ★ **`auto route listener` 的 42703 未修**（见 §5），本机每 5 秒一条。
3. ★ **L182 窗口口径疑点未量化**（见 §4）。
4. **`stalenessThreshold` 的有偏边界**（见 §2.3）未加门。
5. **`fireAlert` 两条分支都投递 webhook** ⇒ S4 停写期 webhook 仍会收到
   `candidate_failure_log_stale`（只是等级降了）。若属主要用告警面，需属主拍板是否一并静默。
