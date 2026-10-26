# request_logs → session_turns 双写退役：2026-10-20 复核轮

> 基线（**方法 + 观测值**，不写死终态 SHA —— 上游每几十分钟就被推一次，写死必然过期）：
>
> ```bash
> cd <仓库根> && git fetch origin --prune -q
> git rev-list --left-right --count main...origin/main   # 期望 0  0
> git branch --show-current                              # 期望 main
> git status --porcelain                                 # 期望空
> ```
>
> 本轮收尾时的观测值：本地 `main` 与 `origin/main` 一致、工作树干净。
> **网关在跑的发布产物** = `2.5.8.2756`（`git_sha=b45b3725`）—— 这个 sha **可作证**，
> 因为构建时**工作区干净且 HEAD==origin/main**（版本号类提交不改 Go 树）。
> 环境：本机 PG 17.10（`llm-gateway-pg` 容器，127.0.0.1:54318）、Redis 7（6379）、
> 入口 `http://127.0.0.1:8782/v1`
> 口径：只写实测到的；未验证项显式标注。
> 覆盖范围：本轮所有读数均来自**本机**（§5.11.3 末尾已单列覆盖范围）。

本轮是**复核轮**，不是从零实施轮。2026-10-16/17 两轮已把退役主线落地
（改名、停写门、读源切换、逐文件四族分类）。本轮做三件事：

1. 把仓库与上游的偏离收敛（合并 74 个上游提交，冲突 1 处）；
2. 在**最新 schema**（870–878 已入库）上重新验证「写侧是否真的停写、
   读侧是否真的切到 session 族」；
3. **发现「请求返回 200 但数据没落库」这个静默失效，并定位到根因、修复、加门。**

---

## 0. 结论先行

| 问题 | 判定 |
|---|---|
| v1 宽族是否还在被写？ | **否**。代码三处流量写点全挂 S4 门，真库 `_request_logs*` 实测 0 行 |
| 物化视图是否已切？ | **是**。8 张 MV 定义零 `request_logs` 引用，链经 canonical 视图到 `session_turns` |
| API 读源是否已切？ | **主体已切**（canonical 视图含 session 臂）；3 张中间层视图仍 v1-only（**有意**，有门钉住） |
| **数据存储是否可用？** | **修复前：不可用。** 请求返回 200，但 session 族几乎不落库（正文 null、trace 丢、turn_no 错位）。**根因是本机 PG 的 `shared_buffers=400kB`**，加上 `sessions_v2.write_timeout_ms` 的 spec 默认（500ms）**静默撤销**了一次「提到 2000ms」的修复。两者已修并加门 |
| 效率 | 修 PG 内存后，规划耗时 1195ms → 50ms（**24×**），网关请求延迟 2–8× 改善 |
| **逐页浏览器验证（104 页）** | **103/104 文档级 200**（唯一例外 `/maintain` 503 = 本机未部署该服务）；**0 个 API ≥2000ms**；**0 个 JS 运行时异常**；抓出 **9 处真 5xx**（5 个根因，全部 curl 可复现）—— 已修 4 个 + 1 个是功能未启用（503 预期） |
| **逐页抓出的最重根因** | `prompt_injection_detections` 的**正典形状**（`sql/schema/` + `sql/objects/tables/`）与**历史迁移 315 的形状**不是一回事，而 315 的 `CREATE TABLE IF NOT EXISTS` 在正典库上是**空操作** ⇒ 315 声明的列**从未被创建** ⇒ 读侧恒 42703、**写侧恒失败（审计记录静默丢失）**、364 的视图建不出来 ⇒ **全环境**缺陷（不是本机没升级） |
| **门禁自身的竞态（本轮新发现）** | `TestDataLifecycleTableSizesFieldsAreComplete` 偶发红：判据与端点读**同一个** `pg_total_relation_size`，却跨两次读做**精确相等**，而首行大表正被网关后台 worker 写入 ⇒ **端点是对的，判据的前提是错的**。已按该文件注释**早已声明的 ±1%** 修正并做变异验证（§5.7） |
| **逐页扫描抓到的唯一一条 JS 异常** | `/quality-correlations` 在**无数据**时抛 `TypeError: Cannot read properties of null (reading 'reduce')`：后端 `breakdown` 是 Go **nil slice** ⇒ 序列化成 `null`，而前端 TS 类型声明成**非空数组** ⇒ 类型成了假的安全网。已改后端契约（恒返 `[]`）+ 前端**边界**归一化，3 道新判据全部做过变异验证（§5.8） |
| **★ 模型请求与写入测试（补做步骤）** | `deepseek-v4-flash` **5/5 = 200**（用户清单里其余 6 个在本机**目录中不存在**或无健康凭据）；**读回证据**：5 轮 `turn_no` 1..5、`success=t`、`latency_ms` 1795→28756、正文三列 `jsonb_typeof` **全是 `array`** ⇒ **「数据存储可用」成立**。08:02 起全量 503 的归因是**三个环境原因**（凭据 auth_failed / 代理节点库 0 行 / 上游余额耗尽），**不是仓库缺陷**（§5.10） |
| **★ 累积式多轮（补做）** | 改用**累积对话**（每轮带全部历史）重做：`prompt_tokens` **84 → 92 → 107 → 386 → 1255 严格单调递增**（这才是「多轮」的可验证形态；每轮只发一条消息时它只反映本轮提示词长度）⇒ 5/5 = 200、`turn_no` 1..5 连续、正文三列 `jsonb_typeof` **全是 `array`**、退役族 `_request_logs*` **0 行** ⇒ **4 条判据全 PASS**。过程中抓到**两条量具坑**（BSD sed 不剥 `\n` 标记 ⇒ 请求体被清空；探活一次碰多模型 ⇒ 一个模型的 429 冷却整个凭据）（§5.11） |
| **★ 新发现一：凭据级熔断会连坐兄弟模型** | 一个模型的 429（`EndpointTPMExceeded`）经 goal-retry 连打 3 次 ⇒ `nodehealth` 熔断阈值（付费档 3）⇒ `EffectSetCredentialUnavailable` ⇒ **该凭据名下 10 个绑定全部 `available=FALSE`** + `credentials.availability_state='cooling'` ⇒ 刚 200 过的兄弟模型立刻 `no_candidate`（3 分钟）。是 `reducer.go:434` 的**明确设计**，但代价要记账（L9） |
| **★ 新发现二：探针 chat 腿缺「预算耗尽」豁免** | Responses 腿接受 `status=incomplete` + `max_output_tokens` 作正向证据，**chat 腿没有** ⇒ reasoning 模型（探针预算只有 32 token）在 `content:null` 时被判 `probe_response_inconclusive` ⇒ 永远到不了 `healthy_confirmed`。与「2xx 不等于健康」是刻意规矩冲突，本轮**不改**（L10） |
| **★ L1 订正（上一轮自己的结论前提有误）** | 「7 张分区不带 `_` 前缀 ⇒ 新装/存量形状分叉」**不成立**：那是迁移 860 的**明确设计**（待删标志在父表上），正典基线 schema 造的就是无前缀分区名，862 那个 `_request_logs_2026_10` 只是**合成夹具名** ⇒ 两边形状相同。真正要修的是**按名字枚举分区族的口径**（两处运维脚本 ⇒ 已改成按 `pg_inherits` 走父表，真库差集证明**零回退、只补漏**）。⚠️ 这两个脚本是**仓库级运维资产、不在发布链上** ⇒ 已提交≠已生效，生产机上的拷贝件仍是旧口径（当前**无排程在跑**，详见 §5.9.3.1） |
| **★ D1：两个活指标读冻结族** | `customer_cost_view.active_concurrent`（**5 分钟**窗）与 `model_cost_per_task_view.avg_latency_ms`（7 天窗）读的是**已冻结**的 `_request_logs` ⇒ 前者在**任何环境都恒 0**（冻结族里永远没有「最近 5 分钟」的行）、后者只读到停写那一刻的快照，**且两者都不报任何错**。已用**迁移 883** 切到会话族（5 分钟窗=hot 单腿、7 天窗=hot∪parent；会话族 `api_key_id` 是 text ⇒ 必须 `::text`），canonical/正典/baseline/installer/runner/部署通道/TSV **七处登记齐全**，2 道门（真库行为门 + 5 道契约门）**4 处变异全部转红**（§5.12.2）。**部署后活体直查**：旧读源形状全是 0/NULL，新读源取到真实延迟（`deepseek-v4-flash` 54 行 avg **4046.1ms**、`glm-5.2` 29 行 avg **6395.8ms**），视图内子查询在键匹配时同样取到这些值 ⇒ **读源确实切对了、键空间也对**（§5.12.6） |
| **★ D1 的另一半：成本端点仍 `[]`** | **不是**本轮没修好，而是**聚合链从未接线**：`api_key_model_cost` 的 trigger（`trg_update_api_key_model_cost`）在正典与全部 startup 迁移里**都不存在**，**真库全触发器清点也确认它没挂在任何表上**（函数 `update_api_key_model_cost()` 存在但**无调用方**），该表**0 行**、**无唯一索引** ⇒ 原函数的 `ON CONFLICT` 会报 42P10。⇒ 这两个端点**从功能设计出来那天起**就一直是 `[]`，与退役无关。复活需按序补：写侧 `canonical_id`（现 0/NULL）→ 唯一索引 → 会话族 trigger ⇒ 登记为 **L11**（§5.12.3） |
| **★ D3：列表端点对同一行集扫两遍 → 一遍** | 上一轮记 L3 时说「本地小数据集量不出收益」——**量法选错了**：buffers 确实量不出（三种写法都是 137 hit，第二趟全命中缓存），但**执行次数**是结构性的。已把 COUNT 与各 SUM 合并成**一条**聚合（执行次数 2→1），并保持「聚合失败 ⇒ 退回单独 COUNT、聚合字段回零、页照常出；COUNT 也失败才 500」的旧降级语义。判据用 **pgx QueryTracer 数查询**（必须恰好 1 条非分组聚合扫描）+ 与旧两扫口径**逐值对账**，变异（拆回两查）转红（§5.12.4）。**部署 `2.5.8.2757` 后读回**：200/82ms，`count=324`、`total_requests=324`、`prompt_tokens=13109`、`completion_tokens=11875`、`by_model` 20 项 ⇒ **口径与部署前逐值一致**（§5.12.6） |
| **★ 一条「判据污染度量」的实测发现** | 用来证明「`api_key_model_cost` 从未被写」的证据 `pg_stat_user_tables.n_tup_ins` 被**门自己**污染了：真库门用 `defer tx.Rollback()` 造夹具，而 **PG 计入回滚事务的插入**（本轮受控实验实测：`BEGIN; INSERT; ROLLBACK;` 后计数 +1）⇒ 读数从 0 变成 13，看起来像「表被写过后又删了」。**连跑该门 3 次 ⇒ 计数 13→16（恰好 +3）、行数恒 0** ⇒ 那 13 次插入全是门自己的回滚夹具，该表从未被生产流量写过。⇒ 证据已换成**「触发器在不在 + 现在几行」**（§5.12.2 订正框、§5.12.3 证据链） |
| **★ 一把坏尺子：`LIKE '%_request_logs%'` 里的 `_` 是**单字符通配符** | 上一轮「5 张 `v_*` 视图读 v1 裸母表」是**坏仪器量出来的**：SQL 的 `LIKE` 里 `_` 匹配**任意单字符** ⇒ 这条判据把 canonical 视图名 `request_logs_with_current_month` 也判成命中 ⇒ **精确重判后实为 3 张**，`v_model_health_dashboard` / `v_probe_system_health` 是**假阳性**。⇒ 本轮自己写的 883 POST_CONDITION 也是这条坏判据，已一并订正为 `strpos`（内容指纹变化 ⇒ 通道按契约**自动重放**，幂等，视图体不变）。⇒ 本仓自己的门 `internal/sqlreadguard` 用的是**正则 + 词边界**，那个门是对的 ⇒ 坏的只有我们临时写的 ad-hoc 读数与文档里的命令。⇒ **判别动作：判「某个名字出现了没有」别用 `LIKE` 配 `_`**（§5.13.1） |
| **★ 3 张 v1 裸母表分析视图已改读会话族（迁移 884）** | `v_continuation_effectiveness` / `v_node_switch_analysis` / `v_timeout_effectiveness` 原读 `_request_logs`（v1 停写并清空 ⇒ **恒 0 行、无错误信号**），且是「后期删除 `_request_logs`」的**依赖阻塞项**（`pg_depend` 清点：v1 族被 **7 个视图**依赖，**无任何表/MV/函数**依赖）⇒ 这 3 张又是**唯一没有任何 API/Go 代码消费**的 v1 读面 ⇒ 零 API 影响地收口。会话族 `session_turn_details` 上 6 个细节语义列**类型逐列一致**（真库 `information_schema` 核过）⇒ 能改读且**不丢能力**。结果：`v_node_switch_analysis` **0 行 → 16 行真实数据**；另两张仍空但**成因可证明**（底层 `is_continuation` / `effective_timeout_seconds` 确实 0 行）⇒ 空从「不知道为什么」变成「知道为什么」（§5.13） |
| **★★ 884 一度**破掉新装路径**（本轮自己引入、自己抓住）** | 884 顺手改了**安装器实际使用的那份基线**，而基线里的 `CREATE VIEW` **急切校验**它引用的关系、且 `session_turns_hot` / `session_turn_details*` **由启动序列建、基线不建** ⇒ 新装报 42P01 并**中止整个安装**；而**升级路径是绿的** ⇒ **「升级路径绿」≠「新装路径绿」**。已把 5 张视图的 4 份副本全回 **v1 fallback 体**（基线自建 `request_logs` ⇒ 基线时点有效），**部署形态交给迁移换上** ⇒ 两条路径跑完启动序列后**逐字同形**（实测 `sess=t, lit_v1=f`），并立了**基线自洽门**（静态常驻，变异验证转红）（§5.13.7） |
| **★★ 新装链本身断在 853（既有缺陷，与本轮无关）** | 853 在新装链报「`credential_probe_queue` 不存在」⇒ **整个新装中止**。根因：853 **自己声明**依赖 489/490（队列表本体），而那两条**从未登记**进链（853 的 runner 注释却写着「once 489/490 create the queue」）。**既有性证明**：pristine HEAD 工作树复跑**同样失败**。已补登 489/490（幂等自包含）⇒ 整链 **0 of 282 fail**；顺带修了**链门自己的量具**（它无条件 `--single-transaction`，不尊重 `dbinit:no-transaction` ⇒ 7 条假破坏）⇒ 已审阅失败集合清空、门从红转绿；顺带修了 2 条被 853 掩盖的**过期 pin**（hot 表 104→**106** 列；`request_logs`→`_request_logs`，因 860 改名）。`TestFreshInstallerSessionTurnsHotBootstrap` 从「在 853 中止」**转 PASS**（§5.13.8） |
| **★ 部署后复验（API 全量 + 104 页逐页）** | API 全量 **511 条路由、0 个 >2s、0 个 5xx**；逐页 **101×200 + 1×503**（`/maintain` 本机未部署）、**0 个 JS 运行时异常**、**0 个慢 API**、**≥2000ms 的页面 0 个**（最慢 `/dashboard` 1965ms）。与 2756 基线逐条 join：路由集合**完全相同**、状态变化 **4 处全是 `500→200`**、页面标记集合**完全一致** ⇒ **0 处回退**。`/api/logs` 稳态 **26–48ms**。⚠ 复验中抓到一次**量具污染**：API 全量扫与逐页扫**并发**跑 ⇒ `/dashboard` 读数虚高到 2920ms，**独占重跑 = 1965ms**（比基线还快）⇒ 教训见 §10.7（§5.12.7） |
| **★ 2758 部署后复验（API 全量 + 104 页逐页 + 累积式多轮）** | API 全量 **511 路由、0 个 >2s、0 个 5xx**，与 2757 逐条 join **状态变化 0 处**（与 2756 基线的 4 处变化仍全是 `500→200`）⇒ **0 回退**；逐页 **101×200 + 1×503**、**0 个真 JS 异常**、**0 个真 5xx**、**0 个慢 API**、**0 个慢页面**，`navMs` 中位 1122 / 最慢 1965（与 2757 独占跑**逐位相同**）；累积式多轮 **5/5 = 200**、`prompt_tokens` **13→32→46→314→1066 严格单调**，写回读回**全项 PASS**（轮次连续、正文三列 `jsonb_typeof` 全 `array`、退役族零新增）。⚠ 过程中又抓到**两把坏尺子**：页扫 `NOT_FOUND` 标记命中「**404.2 MB**」这类**数字**（假阳性，已修：词边界 + 小数/单位排除 + 判定窗口与落盘窗口统一），以及写回验证脚本在 **bash 3.2** 下「检查没跑成却打印 PASS」（已改用 `~/sdk/bash5/bin/bash`）（§5.13.10） |
| **★ 定时补跑：三模型累积式多轮 + 凭据状态读数** | `glm-5.2` **5/5 = 200**（`prompt_tokens` **13→28→42→307→577 严格单调**、写回读回全项 PASS）；`deepseek-v4-flash` / `deepseek-flash` **5/5 = 503** —— 归因**不是**凭据冷却（凭据 1 = `ready`、恢复点已过），而是这两个模型绑定的探针状态 **`node_probe_failed`**（且 `proxy_nodes` 0 行 ⇒ 无第二候选）⇒ **环境状态，非仓库缺陷**；**失败请求同样落库**（`success=f`/`status_code=503`）⇒ 审计链完整。退役族 4 表在 15 次请求后仍**全 0**。⚠ 过程中又抓到 **4 处量具缺陷**（`$var` 紧跟全角括号 ⇒ 整段不执行；B/C 判据缺前提；C 段结论行与 FAIL 自相矛盾）⇒ 判据脚本需要「**前提 / 段级 SKIP / 段级结论**」三件套（§5.13.11、§10.14） |

---

## 1. 合并与构建

| 项 | 结果 |
|---|---|
| 合并 | `git merge origin/main`，1 处冲突（`LESSONS.md`）——两侧各追加了**不同**的经验条目（HEAD 侧 §138/§139，upstream 侧 2026-10-19/20 若干条），**两边都保留**（HEAD 在前） |
| 工具链 | `go1.27.2 darwin/arm64`（`~/sdk/go/bin/go`）。⚠️ `AGENTS.md` 写的 Homebrew 路径 `/opt/homebrew/bin/go` **已不存在** |
| 构建 | `CGO_ENABLED=0 go build ./...` → RC0 |
| 迁移 | `scripts/apply-db-revision-sequence.sh` → RC0，ledger 233 → 241 腿，870~878 全部入库 |
| 部署 | `deploy-local.sh deploy` → `VERIFY_PASS=1`，release `2.5.8.2746` |

**续做轮（§5.12 的 D1/D3）的构建与部署**：

| 项 | 结果 |
|---|---|
| 代码提交 | `ddc2f3362`（D1 迁移 883 + D3 合并聚合 + 2 道新门） |
| 迁移 | `scripts/apply-db-revision-sequence.sh` → RC0，`883_session_family_cost_view_metrics.sql` 入账（台账 marker `session-summary-and-integrity-2026-09:883_…`，`content_sha256 = 84a614746d06…`） |
| 部署 | `deploy-local.sh deploy` → **`VERIFY_PASS=1`**，release **`2.5.8.2757`**，版本身份 `2.5.8-ddc2f336-20261020-2757`（**sha 与代码提交对得上**） |
| 部署后再次重放 | 883 的**注释**订正（见 §5.12.2 订正框）使文件内容指纹变化 ⇒ 通道按契约**自动重放**（`content replay (ledger 84a614746d06 -> file c005c02cb438)`）⇒ 台账收敛到 `c005c02cb438`，视图定义体不变（`f|f|t|t`） |
| 门禁 | `gofmt` 干净；`./admin/`（193s）、`./internal/sqlreadguard/`、`./sql/migrations/startup/`、`installer/...` 全绿；`scripts/apply-db-revision-sequence_test.sh` 9 通过 1 跳过 |

**踩坑留档（DSN 指向歧义）**：迁移脚本在 `psql` 不在 PATH 时走 docker 通道，
`docker exec … psql … "postgresql://…@127.0.0.1:5432/llm_gateway"` 看起来像连到了
**本机另一个项目的 PG**（`acc-pg17` 恰好把 5432 映射到宿主）。实际无害：DSN 是在
**容器内部**解析的，127.0.0.1:5432 指的就是 `llm-gateway-pg` 自己；而 `acc-pg17`
里根本没有 `llm_gateway` 库（实测 `database "llm_gateway" does not exist`）。
⇒ 判别动作：**看到端口先问「这个端口是谁的」，再问「这个连接在哪一侧解析」**。

---

## 2. ★ 头号发现：请求返回 200，但 session 族几乎不落库

### 2.1 现象

用任务指定的入口 key（`http://127.0.0.1:8782/v1`）对 `deepseek-v4-flash` /
`glm-5.2` 各发 3 轮真实多轮请求，全部 200；逐轮回查库：

```
 session_id                          | turn_no | model             | success | has_trace | req    | resp   | out
 gw_656b3fae…（首轮，PG 修前）        |    1    | glm-5.2           | t       | t         | null  | null   | null
 gw_656b3fae…                        |    2    | glm-5.2           | t       | f         | null  | null   | null
 gw_656b3fae…                        |    3    | glm-5.2           | t       | t         | null  | null   | null
```

⇒ 正文（`session_bodies`）**三件套全为 JSON null**；`trace_events` 2/3 丢失；
还有一次整轮**一行都没落**（第二次运行：`session_turns_hot` 里查不到那 3 个会话）。

> ⚠ 量具纪律：正文是否落库一律用 `jsonb_typeof(col)='array'` 判，
> **不用 `IS NOT NULL`**（JSON `null` 也满足 `IS NOT NULL` ⇒ 假绿）。

### 2.2 证据链（自下而上）

1. **网关日志**（`~/kaixuan/llm-gateway-go-v2/logs/gateway-8782.log`）：
   ```
   sessionv2mirror: V2 shadow write failed  error="begin tx: context deadline exceeded"
   sessionv2mirror: session_dim upsert failed error="timeout: context deadline exceeded"
   sessionv2mirror: outbox registration commit failed, degrading to in-process backlog
   trace.FlushToPG: request log row not found, retaining Redis trace
   failed to get previous body for submit mode detection  error="query latest bodies: timeout"
   ```
2. **PG 服务端日志**：`canceling statement due to user request`（客户端 deadline
   在语句执行途中取消），被取消的语句包括
   `INSERT INTO session_dim … ON CONFLICT (gw_session_id) DO UPDATE`、
   `INSERT INTO stats_event_inbox …`、以及 `session_dim` 触发器里的
   `UPDATE session_summaries SET gw_project_id = v_ref …`。
3. **不是锁等待**：采样 `pg_stat_activity` 期间无 `pg_blocking_pids`；
   `pg_stat_activity` 空闲时全 idle ⇒ 不是持续性的锁问题。
4. **是「每条语句都慢」**：连 `SELECT count(*) FROM session_turns_hot WHERE 1=0`
   这样的空查询，**规划**都要 1195ms（执行只要 0.03ms）。

### 2.3 根因一（环境）：本机 PG 的 `shared_buffers` 被改成 400 kB

```
shared_buffers | 50 | configuration file | /var/lib/postgresql/data/postgresql.conf:129
```

⇒ **50 × 8 kB = 400 kB**（PG 自己的默认是 128 MB）。该值是**手改**在容器数据目录
里的（`postgresql.conf:129` 写着 `shared_buffers = 400kB  # min 128kB`）。

后果：规划期要读的 catalog 页根本进不了共享缓冲 ⇒ **每条语句的规划 ~1s**。
而写路径的 deadline 是毫秒级的 ⇒ 写不进 ⇒ 静默丢数据。

对照（同一台机、同一张表、同一条语句，只改这个值）：

| `shared_buffers` | `session_turns_hot` 规划 | 执行 | `session_summaries` 规划 | `session_bodies_hot` 规划 |
|---|---|---|---|---|
| 400 kB | **1195 ms** | 0.03 ms | 745 ms | 271 ms |
| 2 GB | **50 ms** | 0.03 ms | 99 ms | 12 ms |

⇒ 差 7–24×，**而执行时间根本没变**。这解释了为什么「EXPLAIN 看执行计划」
查不出问题：执行是快的，慢的是规划。

**处置**：`ALTER SYSTEM SET shared_buffers='2GB'; effective_cache_size='6GB';
work_mem='8MB';` + 重启容器（本机 64 GB 内存，PG 当时只占 329 MiB，无压力）。
生产侧 154/245 的 PG 是 3.5 GB（`docs/database/2026-10-02-db-audit-34pg17-and-optimization.md`
实测），**不受影响**。

**加门**：`scripts/deploy-local-lib.sh` 新增 `dl_pg_check_memory`，
在 PG 探活成功后检查 `SHOW shared_buffers`，低于 128 MB 时告警
（`DL_PG_MIN_SHARED_BUFFERS_REQUIRED=1` 可 fail-closed）。
判据：`scripts/deploy-local-lib-pgmemory_test.sh`（7 组断言）。

> ⇒ **教训**：「部署成功（VERIFY_PASS=1）」与「数据可用」是两件事。
> 本仓的部署验证探的是**进程起来没、端口通不通、版本对不对**，
> 从来没探过「写进去没有」。这道内存门补的是**可观测口径**的缺口。

### 2.4 根因二（代码）：`sessions_v2.write_timeout_ms` 的 spec 默认静默撤销了一次修复

`internal/sessionv2mirror/hook.go` 的注释写着：

> raise the default budget to 2000ms

它把常量 `defaultShadowWriteTimeoutMs` 从 500 提到了 2000。**但那次修复从未生效。**

`settings.GetPlatformInt(key, fallback)` 的解析顺序是
**DB → env → `spec.Default` → 代码里的 fallback**：

```go
// settings/helpers.go
raw, _, err := Global.EffectiveValue(sp.Scope, key, "")   // ← 命中 spec.Default
if err != nil || len(raw) == 0 { return fallback }          // ← 走不到这行
```

而 `settings/spec_sessions_v2.go` 里这条的 `Default: 500` **一直没改**。
⇒ 实际预算一直是 **500 ms**。

500ms 撑不住一次 shadow write 的串行步骤（session_dim 维度写（其触发器还要
UPDATE session_summaries）→ session_turns → session_turn_details → session_bodies →
session_summaries）⇒ 细节层写超时 ⇒
- ① 正文（`session_bodies`）**一行都不落**（写顺序在 details 之后）；
- ② 整笔写失败 ⇒ 走 outbox 异步重放 ⇒ **重放顺序 ≠ 请求顺序** ⇒
  **turn_no 与时间序错位**（实测：第 1 轮拿到 `turn_no=3`）；
- ③ trace 找不到父行 ⇒ `trace_events` 丢失。

**处置**：`spec.Default` 500 → **2000**（与常量对齐）。
**加门**：`settings/session_write_timeout_default_test.go` —— 钉住
「spec.Default == `internal/sessionv2mirror` 的常量」，两边不许再各写各的；
另加一道**方向性**门（值不得低于 2000ms）。
判据从**源码字面量**取常量（不是复制品），常量改名/改值都会红。

### 2.5 根因三（代码）：维度写与轮次写共用同一个 deadline

`runShadowWrite` 里，`session_dim` 维度写与轮次写共用一个 ctx（同一个 deadline）。
维度写是 `INSERT … ON CONFLICT (gw_session_id) DO UPDATE`，其触发器还要去
`UPDATE session_summaries`；这一步慢或多等几把锁，就把**轮次写**的预算吃光。

⇒ 形态正是 LESSONS「同一次判定里的不同判据可以有不同的时间语义，不要因为共用一个
函数就让它们共用同一个 gate」：**维度写是 best-effort 的旁路，轮次写才是判定主体。**
让旁路吃主体的预算 ⇒ 主体失败、旁路却「成功」了。

**处置**：维度写用**自己的** ctx；轮次写的 ctx **在维度写之后才建**。

> ⚠ **第一版修法不够，被自己的判据当场抓住**：光「再开一个 ctx」没用 ——
> 两个 `WithTimeout(bg, timeout)` 的 deadline 是**同一个绝对时刻**（t0+timeout），
> 维度写照样把主体的 deadline 耗光。实测轮次写拿到的剩余预算是 **−0.8 ms**。
> ⇒ 轮次写的预算必须**从「轮次写开始」起算**。
>
> 代价（如实标注）：一次 shadow write 的**总**墙钟上限从 `timeout` 变成
> ≤ 2×timeout。这是有意取舍：上限变松，换主体不再被旁路饿死。
> 调用方是后台 goroutine（`shadowWriteSema` 限流），worker 侧不受影响。

**加门**：`internal/sessionv2mirror/shadow_write_budget_test.go`（行为门：
让维度写把预算耗光，轮次写仍必须拿到**完整**预算）。

### 2.6 修复后的实测

同一条命令、同一批模型、同样 3 轮：

| | 修复前 | 修复后 |
|---|---|---|
| HTTP | 5×200 + 1×503 | **6×200** |
| 单轮延迟 | 4.1 / 9.6 / 7.8 / 30.2 / 56.5 / 7.0 s | **4.1 / 3.3 / 4.8 / 7.2 / 5.3 / 12.6 s** |
| 落库轮次 | 3/6 | **6/6** |
| `trace_events` | 2/6 | **6/6** |
| 正文三件套 = array | 0/6 | **4/6**（另 2 轮是修复前的旧行） |

### 2.7 变异验证

| 变异 | 结果 |
|---|---|
| M1 维度写改回与轮次写共用 ctx | ✅ 红（轮次写剩余预算 ≤ 预算一半） |
| M2 spec.Default 2000 → 500 | ✅ 红（方向性门点名「撑不住一次 shadow write」） |
| M3 常量 2000 → 1500 | ✅ 红（一致性门点名「两边不一致就等于悄悄改了写预算」） |
| M4 PG 内存门：比较恒假 | ✅ 红（400kB 不告警） |
| M5 PG 内存门：MB 换算系数错 | ✅ 红（128MB → 128000kB） |
| M6 PG 内存门：默认改成 fail-closed | ✅ 红（默认必须 warn，不能掐死别人的部署） |
| M7 PG 内存门：判据脚本自身被改坏 | ✅ 红 |

每个变异都**逐字还原并用 md5 核对**（`internal/sessionv2mirror/hook.go`
`a814fa36…`、`settings/spec_sessions_v2.go` `259e6ba2…`，还原后两道门复跑全绿）。

---

## 3. 写侧复核：v1 宽族是否真的零写入

### 3.1 代码侧：三个流量写点全部挂门

| 写点 | 位置 | 门 |
|---|---|---|
| telemetry 主行 INSERT | `domains/hooks/observability/telemetry/client.go:1430` | `logsWrite := requestLogsWriteEnabled()`（:1360），`if logsWrite {` 开启整段（:1418 起） |
| telemetry 正文 UPSERT | `domains/hooks/observability/telemetry/client.go:3119` | `upsertRequestLogBodies` 只被门内调用点 `:534` 触达；`mirrorRequestBodies` 在 `:1216` 门内 |
| 音频计量行 | `domains/streaming/audio_usage_log.go:62` | `if !settings.RequestLogsWriteEnabled() { return }`（:53）——2026-10-13 补挂，「宽表家族最后一个未挂门的流量写点」 |
| admin ingest | `admin/telemetry.go:463` / `:572` | `if requestLogsWriteEnabled() {`（:454） |

### 3.2 数据侧：实测零行（**在真实流量之后仍然零**）

```sql
_request_logs_hot        0 行    _request_logs            0 行
_request_logs_bodies_hot 0 行    session_turns_hot       有行（max ts 随流量前进）
session_bodies_hot       有行    session_turns / session_bodies  有行
```

开关实测值（`settings_kv`）：`storage.request_logs_write_enabled=false`、
`sessions_v2.enabled/l3_read/primary_read/shadow_write=true`、
`storage.session_bodies_native_read=true`、
`storage.admin_logs_native_turns_read` **无记录 → 默认 false**（有意，见 §4.3）。

⇒ **写侧退役成立**：v1 宽族零写入，新流量全部落 session 族。

---

## 4. 读侧复核：视图 / 物化视图 / API 三个层面

### 4.1 物化视图（8 张）：全部已切到 session 族或与 v1 无关

| 物化视图 | 源 |
|---|---|
| `routing_analytics_7d` | `credentials` + `routing_analytics_source` |
| `routing_audit_summary_7d` | `routing_analytics_source` |
| `session_client_stats` / `session_owner_stats` / `session_task_stats` / `session_client_task_matrix` | `session_summaries` |
| `tuning_signals_5m` / `tuning_signals_daily` | `tuning_signals` |

`routing_analytics_source` → `request_logs_with_current_month` → session 臂
⇒ **MV 链已切**。实测库里 8 张 MV 的定义**零** `request_logs` 引用。

### 4.2 视图（13 张触及 request_logs 族）：1 张已切、12 张仍读 v1

| 视图 | 判定 |
|---|---|
| `request_logs_with_current_month` | **已切**（canonical，定义含 `session_turns`） |
| `request_logs_with_current_month_without_customer_id` | 仍读 v1（`_request_logs_hot` ∪ `_request_logs`） |
| `request_logs_with_current_month_without_request_class_due_at` | 仍读 v1（包装上一张） |
| `request_logs_bodies_with_current_month` | 仍读 v1（`_request_logs_bodies_hot` ∪ `_request_logs_bodies`） |
| `request_logs_bodies_progress` | 仍读 v1（v1 正文回填进度；停写后恒 0 —— 对**冻结**族是**正确**读数） |
| `customer_cost_view` | 主表 `api_key_model_cost` + **v1 子查询** `active_concurrent`（5 分钟窗） |
| `model_cost_per_task_view` | 主表 `api_key_model_cost` + **v1 子查询** `avg_latency_ms`（7 天窗） |
| `routing_analytics_source` | 读 canonical 视图 ⇒ 实际已切 |
| `v_continuation_effectiveness` / `v_node_switch_analysis` / `v_timeout_effectiveness` | ⚠ **本行已订正（2026-10-20 §5.13.1）**：上一轮写「5 张 `v_*` 视图仍读 v1 裸母表」**是坏尺子量出来的**（`LIKE '%_request_logs%'` 里的 `_` 是单字符通配符 ⇒ 把 canonical 视图名 `request_logs_with_current_month` 也判成命中）。**精确重判后是 3 张**（上列前三个），另两个（`v_model_health_dashboard` / `v_probe_system_health`）是**假阳性**。这 3 张已于**迁移 884** 改读会话族（`v_node_switch_analysis` 从 0 行变成 16 行真实数据）⇒ 登记为 `DEBT(R47)` 的 3 条也已由自清洁门移除（§5.13） |

前 3 张 v1-only 视图是**有意建成**的：`cmd/gateway/request_logs_view_session_arm_pin_test.go`
把它们登记在 `v1OnlyRequestLogsViews`，注释写明「577/734 迁移为规避投影缺失而故意
建成 v1-only 的中间层」。该门从 `pg_get_viewdef` 重算并与代码双向比对
⇒ **视图形状漂移会被当场抓住**。

### 4.3 canonical 视图的真实形状（效率相关）

`request_logs_with_current_month` 是**三臂 UNION**：

```
臂1  session_turns_hot   LEFT JOIN session_turn_details_hot
臂2  session_turns       LEFT JOIN session_turn_details
臂3  (request_logs_with_current_month_without_request_class_due_at v
        LEFT JOIN LATERAL ( _request_logs_hot ∪ _request_logs ) )
      WHERE NOT EXISTS (session_turns_hot …) AND NOT EXISTS (session_turns …)
```

臂3 是「v1 行里扣掉会话族已有的那些」。它的两个 `NOT EXISTS` **没有时间窗**，
所以每次读都要对整个 `session_turns` 族建哈希探针。修复 PG 内存后本地实测
（871 行会话数据，7 天窗）：**15.4ms**。

> ⚠ 这条**不是**本轮新发现：仓内已有裁决记录（`db/v1_freeze_cutoff.go` 头注：
> R36-A1 实测「canonical 视图的反连接 + 租户过滤 COUNT 30s 超时」），
> 且 `db.V1FreezeCutoffSQL` 已被 `admin/tenants.go` 与 `bg/auto_index_refresher.go`
> 采用。**canonical 视图本体仍走反连接**是有意保留的（去重必须逐行恰计一次）。

### 4.4 API 读源

`admin/logs.go` 的列表/详情读源由 `logsSourceFromSQL()` 二选一：

- 默认 → `request_logs_with_current_month rl`（canonical 视图）
- 打开 `storage.admin_logs_native_turns_read` → `db.SessionFamilyTurnsSourceSQL()`

**为什么默认不开**（`admin/logs_turns_source.go` 头注有实测依据）：真库 2,321,464 个
request_id 里有 **641,452 个（27.6%）** 在原生源查不到（无会话头流量：探针/自检按
设计排除、in_progress 占位、标题/摘要生成器回环）。日志列表是**不带会话谓词**的
通用流量读路径 ⇒ 切过去会静默少 27.6% 的行。
⇒ 判据是**谓词形态**而非「镜像补齐没有」：按 request_id 反查 / 全量时间窗聚合
**禁止**迁；带 `gw_session_id` 的会话内读**可以**迁（已迁）。该分类固化为
`admin/session_view_dependency_risk_test.go`。

正文腿（`fetchRequestBodies` / `fetchRequestOutboundBody`）顺序是
**session 族优先 → v1 兜底**（`admin/logs_bodies_session_leg_order_test.go` 钉死）。

---

## 5. 效率

### 5.1 修 PG 内存前后（同一台机、同一批数据）

| 指标 | 修前 | 修后 |
|---|---|---|
| `session_turns_hot` 空查询**规划** | 1195 ms | **50 ms** |
| `session_summaries` 空查询规划 | 745 ms | **99 ms** |
| `session_bodies_hot` 空查询规划 | 271 ms | **12 ms** |
| canonical 视图 COUNT（7 天窗） | — | 15.4 ms |
| 真实请求延迟 | 4.1–56.5 s | **3.3–12.6 s** |

⇒ **本机此前所有的「慢 SQL」读数都是环境问题，不是 SQL 问题。**
这一点必须写进交接：本地量出来的毫秒数在生产（3.5 GB shared_buffers）不成立。

### 5.2 列表端点的三扫（结构性，未改）

`admin/logs.go:listLogs` 对**同一行集**做三次独立扫描：

1. `SELECT COUNT(*) FROM <logsFrom> WHERE …`（:635/:636）
2. `SELECT SUM(prompt_tokens), SUM(completion_tokens), … FROM <logsFrom> WHERE …`（:678/:680）
3. 分页列表本体

⇒ 读一次列表页 = 3× canonical 视图成本。COUNT 与 SUM 可合并为一次聚合。
本轮**未改**（在本地小数据集上无法证明收益，且改动面在热路径上）。

### 5.3 索引侧（本轮随上游入库）

862 给两个分区父表补**无谓词 (ts) 前导索引**；870/873 给 usage_ledger 补覆盖索引；
874 给 `session_turns.client_request_id` 补索引；877 给 `session_turns.search_text`
补 trigram（配合 `?q=` 检索）。

### 5.4 ★ `/api/admin/data-lifecycle/storage/tables` 稳定 500：两个独立缺陷

**现象**（API 全量扫描时发现，随后 3/3 复现）：

```
GET /api/admin/data-lifecycle/storage/tables  → 500, t=15.02s
{"error":{"detail":"查询表大小失败"}}
日志：storage: table sizes query failed error=context deadline exceeded
      admin handler internal error op=查询表大小失败 caller=data_lifecycle_storage.go:724
```

**根因一（代码）：TOAST 列是一条相关子查询，既取错值、又逐行重扫全表。**

原表达式：

```sql
COALESCE(pg_total_relation_size((
  SELECT t.oid FROM pg_class t WHERE t.reltoastrelid = c.oid)), 0)
```

- **值恒为 0**：子查询找的是「c 自己是某张表的 TOAST 表」；而本查询限定
  `c.relkind IN ('r','p')`，TOAST 表的 `relkind` 是 `'t'` ⇒ **永远匹配不上**。
  实测：用户 schema 下 451 张 r/p 表**可命中 0 张**，而其中 **435 张确实有 TOAST 表**。
  ⇒ `toast_bytes` 这个字段一直是 0，而 `StorageOverview.vue:186` 照着渲染它。
- **代价极高**：planner 把它落成对 `pg_class` 的**逐行**扫描，且有**两种**形态：
  - (a) `SubPlan` → `Seq Scan on pg_class`，`loops=N`；
  - (b) 半连接拉起 → `Nested Loop` 的内层
    `Seq Scan on pg_class Filter=(... AND (reltoastrelid = c.oid))`。

  实测同一台机、同一批数据，坏/好写法**交错重复**各 3 轮：

  | 写法 | 第 1 轮 | 第 2 轮 | 第 3 轮 | 中位 |
  |---|---|---|---|---|
  | 坏（相关子查询） | 5596 ms | 5791 ms | 6686 ms | **5791 ms** |
  | 好（`c.reltoastrelid`） | 2976 ms | 2677 ms | 3241 ms | **2976 ms** |

  ⇒ **−49%，两组区间不重叠**，差异远超噪声。
- 改法：直接用 `c.reltoastrelid` 这一列（零 join、零子扫描，且是真值；
  无 TOAST 时为 0，`pg_total_relation_size(0)` 返回 NULL，被 `COALESCE` 兜成 0）。

**根因二（代码）：采集预算 == 请求预算 ⇒ 采集永远跑不完，快照永远建不出来。**

`storageCollectTimeout` 原为 15s，与 handler 的请求预算同值。采集的 ctx 是从
`context.WithoutCancel` 派生的（采集**不**挂在请求 ctx 上）⇒ 请求超时**不会**
掐死采集，**真正会掐死它的只有 `storageCollectTimeout`**。于是：冷页缓存下一次
采集要 11–17s ⇒ 采集在 15s 被自己掐掉 ⇒ 快照建不出来 ⇒ 端点 500 ⇒
**每次请求都重试一次、再掐死一次**，没有任何自愈路径。
⇒ 改：`storageCollectTimeout` 15s → **60s**（请求侧预算仍是 15s，用户不等待更久）；
并把两个预算提成具名常量 `storageRequestTimeout` / `storageCollectTimeout`，
用一条门钉住「采集预算 > 请求预算」这个结构前提。

**环境因素（不是缺陷，但必须记账）**：本机 PG 的数据目录挂在 Docker Desktop 的
**virtiofs** 上（`docker inspect`：`bind /Users/niuer/kaixuan/postgres-citus →
/var/lib/postgresql/data`）。于是**每次 `stat()` 是一次到宿主机的 RPC**，
实测 0.2–1.2 ms/次；`pg_total_relation_size` 一次要对 451 张表 stat
主 fork / fsm / vm / toast / 每个索引（≈6000 次）⇒ 光 stat 就要 **4.2s**。
生产（Linux 原生文件系统）没有这层开销，**该开销在生产不成立**。
⇒ 判别动作：本机读到的「秒级 size 查询」先问「`stat()` 一次多少钱」，
再决定是改 SQL 还是改存储层。

**新增判据（4 条，全部做过变异验证）**

| 判据 | 钉住什么 | 变异 |
|---|---|---|
| `TestTableSizesToastBytesReportsRealValue` | 端点给的 `toast_bytes` 与 PG 真值**逐字节相等**且 > 0 | 把表达式改回相关子查询 → 红（`ToastBytes = 0` vs 真值 5709824） |
| `TestTableSizesQueryPlanHasNoCorrelatedSubplan` | 计划里**任何条件**都不得引用 `reltoastrelid` | 同上 → 红（`Filter=(... (reltoastrelid = c.oid))`） |
| `TestStorageCollectBudgetOutlivesRequestBudget` | 采集预算 > 请求预算 | 预算压回 15s → 红 |
| `TestTableSizesCollectSurvivesCallerCancellation` | 调用方 ctx 取消后，采集**仍跑完并写入快照** | 采集改回挂调用方 ctx → 红（90s 后快照仍未出现） |

> ⚠ 计划形状门**换过一次**：第一版钉「计划里有没有 `SubPlan` 节点」，
> 变异时计划恰好落到半连接形态 ⇒ 门**假绿**。现版钉的是**不变量**
> （条件里不得出现 `reltoastrelid`），与 planner 选哪种落地形态无关。

---

## 5.5 ★ 逐页浏览器验证（104 页）：0 个慢 API、0 个 JS 异常，抓出 9 个真 500

> 口径：Maestro 方法论（逐页 + 逐 API + 计时），但**不用 Maestro CLI**
> （本机无 Java/adb），改用**无头 Chromium 脚本**（playwright-core 1.64.1 +
> 本机缓存的 Chrome for Testing 1223）。
> **为什么换载体**：内嵌 Browser 每个标签页常驻一条 SSE（`/api/admin/live-stream`），
> 而 Chromium 对同源 HTTP/1.1 只有 **6 条**连接（整个浏览器共享）⇒ 多标签页时
> 新请求被**客户端取消** ⇒ 服务端看到 `context canceled` ⇒ 页面「完整、无报错、
> 数据恒为空」。这是**工具/环境约束**，不是应用缺陷（同一请求 curl 恒 200）。
> 无头脚本**单页顺序访问** ⇒ 每时刻最多 1 条 SSE ⇒ 争抢消失。
>
> 覆盖范围：85 条直接可访路由（`web/src/router.ts` 里不含 `:`/`*` 的 path，
> 去掉 `/login`、`/forbidden`、`/`）+ **19 条带参路由的具体实例**
> （从真库取真实 id 构造，不造假 id）= **104 页**。
> 取数命令：
>
> ```bash
> cd /tmp/llmgw-retire-20261020/pw
> node sweep.mjs            # 逐页：document 状态 + 每个 /api/ 响应的状态与耗时
>                           #       + console/page error + main 容器文本标记
> # 产物：sweep.tsv（104 行），逐页一行
> ```
>
> 认证：`Authorization: Bearer <admin JWT>` 注入到 context（不落盘、不打印）。

### 5.5.1 结论

| 口径 | 结果 |
|---|---|
| 文档级可达 | **103/104** 返回 200；唯一例外 `/maintain` = 503（本机未部署 maintain 服务，**预期**） |
| 路由改写 | 20 条重定向到规范别名（`/maas/*`→`/tenant/*`、`/customer/*`→`/customer/update-activate`、`/dev/*`→`/`（dev 路由只在 `import.meta.env.DEV` 注册））——**全部有意** |
| **API 耗时 ≥ 2000ms** | **0 页 0 个**（用户验收线「超过 2 秒要定位慢因」全部达标） |
| 页面加载耗时 Top1 | 1940ms（`/dev/waterfall-preview`），**全部 < 2000ms** |
| **JS 运行时异常（pageerror）** | **0 页**（104 页全部为空） |
| console error | 全部页面都有，**内容只有一种**：`503 (Service Unavailable)` —— 即 `/maintain-api/healthz` 与 `/maintain-api/menu/ops`（本机无该服务，前端已优雅降级） |
| **5xx API** | **9 处 / 6 页**（剔除 maintain-api 的 503）—— 见 §5.6 |

### 5.5.2 逐页扫描顺带暴露的真问题：`/request-logs` 与 `/admin/turns` 的「共 0 条 / 加载中…」

内嵌 Browser 里 `/request-logs` 恒显示「共 0 条 / 加载中…」，服务端
`admin listLogs count query failed scope=super_admin error="context canceled"`。
本轮把它查实了：

1. **不是服务端慢**：`GET /api/logs` 用 curl 逐字重放（同一 query 串）恒 **200 / 151–156 条**。
2. **是客户端取消**：服务端 `context canceled` ⇒ Go 的 `http.Server` 在**客户端断开**时
   取消 request ctx。浏览器同源连接池（6 条）被同浏览器其它标签页的 SSE 占满时，
   排队中的请求会被**放弃**⇒ 服务端记 500。
3. **同一端点、同标签页，时好时坏**：`/api/admin/turns/sessions?limit=20`
   在 06:26:34 = **500**（ctx canceled）、06:26:36 = **200 / 39ms**、06:26:40 = **500**。
   ⇒ 与「端点有缺陷」不相容 ⇒ 争抢相关。
4. **前端没有「用 0 冒充没有」**：`RequestLogsView.load()` 有 `catch → error.value`，
   `TurnsListView` 有 `error-banner` + 「加载会话失败，请重试」⇒ **两端视图都做了错误态**，
   「共 0 条 / 加载中…」是**请求还没结束时的中间态**（`loading=true` 时刷新按钮 disabled）。
5. **服务端仍有一处真缺陷**：客户端取消被记成 `500 / error.kind=server_error`
   （`middleware/logging_mw.go` 的 `errorKind(status)` 只看状态码）⇒
   **「客户端跑了」与「服务端坏了」在监控里长得一样**。本轮**未改**（共享中间件，
   改动面大），记为遗留 **L7**。

> 判据纪律：**「端点坏」与「端点被取消」必须用同一请求的两种证据分开**
> ——本轮用的是「curl 逐字重放 200」+「服务端 ctx canceled」两条一起。

---

## 5.6 ★ 逐页扫描抓出的 5 个真 500：形状错配、缺列、空聚合、非法 id

9 处 5xx 里 4 处是同一端点的多次触发 + 2 处 maintain-api（预期）⇒ **5 个真问题**。
**全部用 curl 逐字复现**（⇒ 不是浏览器假象）：

| 端点 | 复现 | 服务端错误原文 | 根因 |
|---|---|---|---|
| `/api/admin/prompt-injection/stats` | 500 | `42P01 relation "prompt_injection_stats_enhanced" does not exist` | 视图建不出来（见下） |
| `/api/admin/prompt-injection/detections?page=1&page_size=20` | 500 | `42703 column "detection_score" does not exist` | 代码查的是**非正典**的列 |
| `/api/admin/sessions/{id}/snapshot` | 500 | `42703 column s.user_tags does not exist` | 该列**不在正典基线**里 |
| `/api/admin/session-analytics/users/{owner}?days=30` | 500 | `can't scan into dest[1] (col: total_requests): cannot scan NULL into *int64` | 空聚合扫进非空标量 |
| `/api/v1/approvals/1` | 500 | `22P02 invalid input syntax for type uuid: "1"` | 非法 uuid 喂给 uuid 列 |
| （`/api/free-discovery/*` ×3） | 503 | `free-discovery is not available (database disabled)` | **功能未启用**，非缺陷 |

### 5.6.1 ★ 最重的一个：正典表形状与「历史迁移 315 的形状」不是一回事

`prompt_injection_detections` 的**正典形状**在
`sql/schema/01-schema.sql:11398` 与 `sql/objects/tables/prompt_injection_detections.sql`：

```
risk_level integer NOT NULL   -- 带 CHECK (risk_level >= 1 AND risk_level <= 10)
rule_id / rule_name / category / matched_pattern / input_sample
categories injection_category[] / canary_token_leaked / approval_id / …
（**没有** detection_score / matched_rules / matched_rules_count / detection_layers）
```

而**历史迁移 315**（`sql/migrations/startup/315_prompt_injection_detection.sql:129`）
声明的是另一套形状（`detection_score INT NOT NULL` + `matched_rules JSONB` +
`matched_rules_count` + `detection_layers` + `risk_level VARCHAR`）。

**致命的一步**：315 的建表语句是 `CREATE TABLE IF NOT EXISTS` ⇒ 在**已有正典表的库上
是空操作** ⇒ 315 的那几列**从未被创建**。于是：

1. **读侧**（`admin/prompt_injection_handler.go`）查 315 的列 ⇒ 恒 `42703` ⇒ 端点 500；
2. **写侧**（`domains/promptinjection/detector.go` 的 `DetectAndLog`）也 INSERT 315 的列
   ⇒ 恒 `42703` ⇒ **每一次注入检测的审计记录都被静默丢弃**
   （失败只落一行 `slog.Warn` ⇒ 检测照常生效/拦截，只是没有留痕
   ⇒ 于是「这批请求没有注入命中」成了假结论）；
3. **视图** `prompt_injection_stats_enhanced`（迁移 364）引用 `d.detection_score`
   ⇒ **在任何正典库上都建不出来** ⇒ 统计端点 42P01。
   就算建得出来，它写的 `((risk_level)::text = 'critical')` 对**整数**列恒不匹配
   ⇒ 那 4 个严重等级分桶**恒为 0**。

⇒ **⇒ 这不是「某个环境没升级」，是**全环境**的形状/代码错配**（本机与新装库同形）。
2026-10-12 的 schema 对齐审计（`docs/audit/2026-10-12-schema-parity-252-vs-local/`）
已把它记为「本地 0 行，可直接 NOT NULL」并给了一份 `02-local-fix-extra.sql`，
但那份修复 SQL **自身也自相矛盾**（补了 `detection_score`，却仍按字符串比 `risk_level`）。

**本轮的修法（以正典形状为准，不发明语义）**：

- 分数落 `risk_level`（正典语义：它就是 1..10 的分值）；
- 等级字符串由**同一套阈值**派生：`≥10 critical / ≥8 high / ≥6 medium / else low`
  —— 阈值**沿用**代码里既有的 `calculateRiskLevel`，不是新口径。
  写侧（Go）与读侧（SQL）共用**同一个**定义
  （`domains/promptinjection.RiskLevelFromScore` / `RiskLevelSQLExpr`），
  否则会出现「按 high 筛出来的行里写着 medium」；
- `matched_rules` ← 命中的最高危规则的 `rule_name`；
  `matched_rules_count` ← `categories` 的元素个数；
  正典表没有的 `rule_id` / `matched_pattern` / `input_sample` **留空而不是编造**；
- 统计端点**不再依赖那个建不出来的视图**，直接在正典表上聚合。

### 5.6.2 其余三个的修法

| 缺陷 | 修法 | 为什么这么修 |
|---|---|---|
| `sessions.user_tags` 缺列 | 加进 `db.go` 的 **ensure 链**（`ensureSessionUserTags`）：列不在位才 `ALTER TABLE … ADD COLUMN IF NOT EXISTS`，并用 `columnsAllPresent` 预探测（列已在位就**不做 DDL**，`ADD COLUMN IF NOT EXISTS` 即便列已存在也要先取 ACCESS EXCLUSIVE，§10.97 同族） | 该列**不在正典基线**里 ⇒ 只有走 installer（`StartupFiles` 含 467）的新装库才有 ⇒ 任何 deploy 路线演进来的库都缺 ⇒ **ensure 链是本仓既有的「环境对齐」通道**（096141ecc 同族） |
| 画像聚合 NULL 扫描 | `COALESCE(SUM(...), 0)` 覆盖 4 个聚合；`MIN/MAX` 时间戳改用可空指针接收后落零值 | 「该 owner 没有会话」是**正常状态**，不该 500；零值时间 = 从未出现，不是 1970 事实 |
| `approvals/{id}` 非法 uuid | 在**错误分类**层把 PG 的 `22P02` 归一为 `ErrNotFound`（⇒ 404），**不做入参校验** | 域层不该知道「id 是 uuid」这种存储细节；也正因如此既有单测用非 uuid 假 id 是合法的，入参校验会打断它们 |

### 5.6.3 顺带修掉的两个「被掩盖的缺陷」

- `detected_at`（timestamptz）扫不进 `*string` ⇒ 改 `*time.Time`
  （JSON 仍是 RFC3339 字符串，前端 `fmtTime` 可解析；列可空时如实给 null）。
  这个缺陷**一直被掩盖**：旧查询在更早的 `42703` 就挂了。
- 计数查询原先靠 `strings.Replace` 把 SELECT 换成 `SELECT COUNT(*)`
  ——**字符串级替换**：SELECT 文本一改（换行/空格/字段顺序）就**静默不生效**
  ⇒ count 查询返回整行集 ⇒ `Scan(&total)` 报 "cannot scan ..."。改为
  **显式 count 查询 + 共享 WHERE**（`promptInjectionDetectionsCountFrom`）。

### 5.6.4 新增判据（6 道，全部做过变异验证）

| 判据 | 层 | 变异（改坏） | 结果 |
|---|---|---|---|
| `TestInsertDetectionSQL_LandsInCanonicalShape` | 写侧（真库，事务+回滚） | INSERT 换回 315 形状 | **红** ✔ |
| `TestInsertDetectionSQL_OldShapeStillFails` | 写侧（真库） | —— | 绿（旧形状必须仍失败） |
| `TestRiskLevelFromScore_MatchesSQLExpr` | Go↔SQL 阈值同源（真库逐档比对） | 阈值漂移 | 可被抓 |
| `TestPromptInjectionQueries_RunOnCanonicalShape` | 读侧（真库逐字跑生产 SQL） | 投影换回 315 形状 / 统计改读视图 | **红** ✔ / **红** ✔ |
| `TestPromptInjectionHandler_NoStringReplaceForCount` | 读侧（静态） | 计数回到字符串替换 | 可被抓 |
| `TestPromptInjectionStats_NoDependencyOnMissingView` | 读侧（静态） | 统计改读视图 | **红** ✔ |
| `TestUserProfileStatsSQL_NullAggregatesAreCoalesced` | 画像（真库逐字跑生产 SQL） | 去掉 COALESCE | **红** ✔ |
| `TestEnsureSessionUserTags_RealDB` / `_IsOnTheBootChain` | ensure（真库 + 静态） | ensure 脱链 | **红** ✔ |
| `TestIsMalformedUUIDInputError` | 错误分类（纯函数，同层） | 恒 false | **红** ✔ |

> **这几道门在写它们的过程中当场抓到了 4 个我自己的错误**：
> `AS c` 把列别名写成了表别名；`FROM` 跑到了子查询**外面**；
> `categories` 绑 JSON 字节（PG 数组字面量解析失败 22P02）；
> 判据自己把 `categories` 扫进 `*string`。
> ⇒ **真库门（让 PG 自己解析）能抓到 mock 抓不到的东西**——这与攻击向量库
> 那次（a56338ee2）同源：**pgxmock 对参数类型不敏感，旧 bug 在 mock 下照样全绿**。

### 5.6.5 修后复验（部署 `2.5.8.2753`，`VERIFY_PASS=1`）

**逐页全量重扫（同一脚本、同一 104 页、同一口径）**：

| 口径 | 修前 | 修后 |
|---|---|---|
| 真 5xx（剔除 maintain-api 503） | **9 处 / 6 页** | **3 处 / 1 页** —— 剩下 3 处全是 `/free-discovery` 的 **503「功能未启用」**（预期） |
| 慢 API ≥2000ms | 0 | **0** |
| 页面加载 ≥2000ms | 0 | **1 页**（`/dashboard` 2004ms；它首屏打 87 个 API，属边界值） |
| 文档级不可达 | 1（`/maintain` 503） | 1（同上，预期） |
| JS 运行时异常 | 0 | **0** |

**逐端点 curl 复验（修前 → 修后）**：

| 端点 | 修前 | 修后 |
|---|---|---|
| `GET /api/admin/prompt-injection/stats` | 500 | **200**（`total_detections:0` 等全零） |
| `GET /api/admin/prompt-injection/detections?page=1&page_size=20` | 500 | **200**（`detections:[]`） |
| `GET /api/admin/sessions/{id}/snapshot` | 500 | **200**（真实数据：`total_turns:1, total_tokens:33`） |
| `GET /api/admin/session-analytics/users/default?days=30` | 500 | **200**（零值） |
| `GET /api/admin/session-analytics/users/<不存在的 owner>` | 500 | **200**（零值，时间戳落零值 `0001-01-01T00:00:00Z`） |
| `GET /api/v1/approvals/1`（非法 id） | 500 | **404** `{"error":"approval not found"}` |
| `GET /api/v1/approvals/<合法 uuid>` | — | **404**（不存在的审批，正确） |

> 产物来自**未提交的工作区** ⇒ `version.json` 里的 `git_sha` 仍是 HEAD
> （`8cec460b`），**不能**用它证明新代码在跑 ⇒ 用**行为变化**证明
> （上表：500 → 200/404，且 snapshot 返回真实轮次数与 token 数）。
> ⇒ 判别动作：**当部署产物来自未提交源码时，「版本号对得上」不是证据**。

---

## 5.7 ★ 判据自身的竞态：`data-lifecycle/storage/tables` 的「字节数对账」在活库上间歇性红

### 5.7.1 症状

`go test ./admin/`（带 `TEST_PG_URL`）在**同一份代码**上出现**第三条**失败：

```
--- FAIL: TestDataLifecycleTableSizesFieldsAreComplete (1.44s)
```

但同一提交**再跑一次就过**（`-v` 全绿），pristine worktree（`8cec460b3`，
不含本轮改动）也**过** ⇒ 不是本轮引入，也不是稳定复现的缺陷 ⇒ 先怀疑**判据本身**。

### 5.7.2 根因：判据与被测对象读的是**同一个函数**，却跨两次读做精确相等

- 端点口径：`admin/data_lifecycle_storage.go` 的 `tableSizesSQL` 里
  `TotalBytes` 取的是 **`pg_total_relation_size(c.oid)`**；
- 判据口径：`admin/data_lifecycle_storage_tables_perf_test.go` 的 live 查询用的**也是**
  `pg_total_relation_size($1::regclass)`。

⇒ 两个值相等的前提是「**两次读之间那张表没变**」——而首行是**全库最大的表**，
本机实测首行为 `public.ursm_node_snapshot_min`（231MB），**网关的 URSM worker
正在批量写它**（`n_tup_ins=49283`、`n_tup_del=0`，实测 5 秒涨 64KB）。
快照采集与随后的 live 查询之间只要落进一次写入，两者就不相等。

**取数命令**（本机 PG 54318；DSN 走 `DATABASE_URL`，**不落盘**）：

```bash
source /tmp/lgw-env.sh
PSQL="$HOME/kaixuan/pg18/bin/psql"
"$PSQL" "$DATABASE_URL" -tAc "SELECT c.relname, pg_total_relation_size(c.oid) \
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace \
  WHERE n.nspname='public' AND c.relkind IN ('r','p') ORDER BY 2 DESC LIMIT 5;"
"$PSQL" "$DATABASE_URL" -tAc "SELECT n_tup_ins, n_tup_del, n_live_tup \
  FROM pg_stat_user_tables WHERE relname='ursm_node_snapshot_min';"
```

**口径**：`pg_total_relation_size` = heap + TOAST + indexes；端点与判据口径**完全一致**，
所以差异只可能来自「两次读之间表变了」。
**覆盖范围**：`public` schema 的 `r`/`p` 两类关系；端点 `tableSizesSQL` 的
`WHERE n.nspname NOT IN ('pg_catalog','information_schema')` **还包含非 public schema**
（见 §5.7.3）。

### 5.7.3 第二层竞态：首行可能是**用例自己建的临时 schema 里的表**

端点的 `WHERE` 不排除非 public schema，而 admin 包的真库门普遍用
`dlperf_<test>_<pid>_<nanos>` 这类临时 schema（用例结束 `DROP CASCADE`）。
⇒ 首行可能是一张**即将被删掉**的表，live 查询直接 42P01。

### 5.7.4 判据与它自己声明的不一致

该文件**上方的注释**写的是「字节数一致（**±1%**）」，**实现**却是
`if first.TotalBytes != liveBytes` —— 逐字节精确相等。
⇒ **判据没做到它自己声明的事**（判据纪律：注释不是证据，断言才是）。

### 5.7.5 修法

- 按**它自己声明的 ±1%** 比对（容差取 `max(1%, 64KB)`：小表上 1% 可能小于一个页
  8KB，要给页级漂移留余量）；
- 关系已被删（42P01）或 live 值为 0 时**改判下一行**；
- 保留「**至少有一行成功对账**」的前提断言（否则会退化成「压根没对账」的假绿）。

**端点本身不改**——它返回的就是真实读数，变的是**判据的前提**。

### 5.7.6 变异验证（证明新判据不是假绿）

把生产读数改坏（`queryTableSizes` 里 `t.TotalBytes *= 2`），判据如期变红：

```
--- FAIL: TestDataLifecycleTableSizesFieldsAreComplete (2.13s)
    data_lifecycle_storage_tables_perf_test.go:146: 第 0 行 public.ursm_node_snapshot_min
      的 TotalBytes = 462536704，PG 实时 = 231268352（偏差 231268352 字节 > 容差 2312683）
      —— 响应不是真实读数
```

⇒ 容差 2312683 字节（1%）能抓住 100% 量级的读数错误；活库的写入漂移实测在 KB 级
（64KB/5s），两者差三个数量级 ⇒ 不会把「读数在变」误判成「读数不对」。

---

## 5.8 ★ 逐页扫描抓到的**唯一一条** JS 运行时异常：`/quality-correlations` 在「无数据」时崩

### 5.8.1 症状

合并上游 22 个提交并重新部署后，104 页全量重扫里出现了**唯一一条** JS 运行时异常：

```
[Vue Error] TypeError: Cannot read properties of null (reading 'reduce')
    at ac.fn (http://127.0.0.1:8782/assets/QualityCorrelationsView-Bq54SLjJ.js:1:2961)
    at Wi (http://127.0.0.1:8782/assets/vue-vendor-BSI0bAkk.js:9:3566)
    ...
```

页面**文档级 200、其它 API 全部正常**，只有这一条异常（不崩页，但那一格数据算不出来）。

### 5.8.2 根因：契约两端不一致 —— 后端返 `null`，前端类型说「非空数组」

| 端 | 事实 |
|---|---|
| 后端 | `admin/quality_correlations.go` 的 `var breakdown []QualityCorrelationRow` 是 Go 的 **nil slice** ⇒ JSON 序列化成 **`null`** 而不是 `[]`；而**同一个 handler** 里下方的 `insights := []QualityCorrelationInsight{}` 被**显式**初始化了 ⇒ 同一响应里两个数组字段的空值形态不一致，是疏漏 |
| 前端 | `web/src/api/tuning.ts` 的 `QualityCorrelationResponse.breakdown` 声明为 `QualityCorrelationRow[]`（**非空**）⇒ **类型是假的**；页面于是直接 `resp.value.breakdown.reduce(...)`，null 时当场抛异常。同一页另有 3 处 `resp.breakdown.length` / `resp.insights.length` 同样会炸 |

触发条件是**合法业务状态**，不是异常输入：本地库 `session_turns` 里
`is_auto_request IS TRUE` 的行数为 **0** ⇒ 聚合查询 0 行 ⇒ `breakdown` 为 `null`。

**取数命令**（DSN 走 `DATABASE_URL`，**不落盘**）：

```bash
source /tmp/lgw-env.sh
PSQL="$HOME/kaixuan/pg18/bin/psql"
# ① 端点实际返回
curl -s -H "Authorization: Bearer $TOKEN" \
  "http://127.0.0.1:8782/api/admin/auto-route/quality-correlations?days=7"
#   ⇒ {"window_days":7,"by":"prompt_length","breakdown":null,"insights":[],"generated_at":...}
# ② 为什么是 null：查询读的是 session 族，而 auto 轮次为 0
"$PSQL" "$DATABASE_URL" -tAc "SELECT count(*) FROM public.session_turns WHERE is_auto_request IS TRUE;"
```

**口径**：端点读 `SessionFamilyTurnsSourceSQL()`（session_turns）LEFT JOIN
`SessionBodiesSourceSQL()`（session_bodies_hot），过滤 `is_auto_request = TRUE`
且 `ts >= NOW() - INTERVAL '1 day' * $1`。
**覆盖范围**：本机 PG 54318 的 `public.session_turns` 全表（876 行）——
`is_auto_request IS TRUE` 命中 **0** 行。

### 5.8.3 归属：既有缺陷，**不是**本轮引入

- 合并**没有动过** `admin/quality_correlations.go` 与 `web/src/views/QualityCorrelationsView.vue`
  （`git diff 8cec460b3 c75ec26ee -- <两文件>` 为空）；
- 上一轮（修完 5 个 500 之后）那次 104 页扫描报「0 个 JS 运行时异常」⇒ 当时
  `breakdown` 还不是 `null`（本地库那会儿有 auto 轮次）。

### 5.8.4 修法：后端改契约 + 前端在**边界**归一化（两层都做）

1. **后端**：`breakdown := []QualityCorrelationRow{}`（与同函数里的 `insights` 对齐）
   ⇒ 数组字段恒返 `[]`。
2. **前端**：`normalizeQualityCorrelations()` 放在**写进 `resp` 之前**（边界），
   把 `null`/非数组一律兜成 `[]`。
   - 为什么放在边界而不是模板里：模板继续用 `resp.breakdown`（既有跨层契约门禁
     `QualityCorrelationsView.responsive.test.ts` 钉的就是这个形态），而防御散落在
     4 个模板表达式里**就会漏掉第 5 个**；
   - 为什么前端也要做：前端**不能依赖后端**（同一份视图可能被别的后端实现喂数据）。

### 5.8.5 新增判据（3 道，全部做过变异验证）

| 判据 | 钉的不变量 | 变异 |
|---|---|---|
| `admin/quality_correlations_null_array_test.go::TestQualityCorrelations_ArrayFieldsAreNeverNull` | **数组型字段在响应里必须是 JSON 数组，永远不是 null**（真库 + 真 handler + **解进 map** 才能看见 `null` 与 `[]` 的区别） | 改回 nil slice ⇒ 红：`字段 breakdown 序列化成了 null` |
| `admin/quality_correlations_null_array_test.go::TestQualityCorrelations_BreakdownIsNotDeclaredAsNilSlice` | **写法**：源码里不能出现 `var breakdown []QualityCorrelationRow` | 同上 ⇒ 红：`又把 breakdown 声明成 nil slice 了` |
| `web/src/views/QualityCorrelationsView.nullbreakdown.test.ts` | **页面的承受力**：后端返 null 时必须渲染空态、不得抛异常（**不**断言「后端返 null」这个形态，否则后端修好后这道门变废门） | 去掉边界归一化 ⇒ 红，且报的正是生产同款 `TypeError: Cannot read properties of null (reading 'reduce')` |

> 真库门与静态门分工：真库门钉**输出**，静态门钉**写法** ——
> 把 nil slice 换回去在**有数据**的环境里输出不变，只有静态门能抓住。

### 5.8.6 连带修：行号锚点型登记表

`web/src/components/ui/mobileListUx.spec.ts` 的 `NON_NAVIGABLE` 登记表按
`[文件, 行号, 理由]` 锚定调用点。前端在 `<script setup>` 头部插入 37 行后，
`QualityCorrelationsView.vue` 的 `ResponsiveDataView` 调用点由 **186 → 223** ⇒
登记表必须跟着走（否则门报的是「找不到」而不是「不合格」—— 量具坏了，不是产品坏了）。

已按本仓先例改键并在登记处写明原因与行数（先例：
`internal/billguard/month_key_test.go` 2026-10-13 `:126→:145`、
2026-10-17 `:91→:243`）。

### 5.8.7 修后复验（部署 `2.5.8.2755`，`VERIFY_PASS=1`）

**端点行为变化**（部署产物来自**未提交**源码 ⇒ 版本号不能当证据，用行为变化证明）：

```
修前：{"window_days":7,"by":"prompt_length","breakdown":null,"insights":[],...}
修后：{"window_days":7,"by":"prompt_length","breakdown":[],"insights":[],...}
```

**104 页全量重扫**（同一脚本、同一 104 页、同一口径）：

| 口径 | 合并后（修前） | 修后 |
|---|---|---|
| 文档级不可达 | 1（`/maintain` 503） | 1（同上，预期） |
| **真 5xx**（剔除 maintain-api 503 / free-discovery 503） | 0 | **0** |
| **JS 运行时异常** | **1 页**（`/quality-correlations`） | **0 页** |
| 慢 API ≥2000ms | 0 | **0** |
| 页面加载 ≥2000ms | 1（`/dashboard`） | 1（`/dashboard` 2042ms，首屏 87 个 API，属边界值） |
| console error | 104 页（全为 maintain-api 503 的资源加载失败，预期） | 同上 |

**量具本身的修复**：`sweep.mjs` 写 TSV 时只 `replace(/\t/g,' ')`，**没剔换行** ⇒
JS 堆栈是**多行**的，整表被写坏（解析出来「106 行、route 重复、docStatus 为空」）。
已补 `flat()` 统一把换行也换成空格 ⇒ 本次读数 104 行、可逐行解析。
⇒ 判别动作：**量具产出必须先自检「行数 == 预期」再读数**。

---


| # | 项 | 性质 | 建议 |
|---|---|---|---|
| ~~**L1**~~ | ~~7 张 v1 分区表未带 `_` 待删标志 … 形状分叉~~ ⇒ **2026-10-20 复核轮内已订正，见 §5.9**：这不是缺陷，是迁移 860 的**明确设计**（待删标志在**父表**上，子表名按设计保持不变）；「新装与存量形状分叉」的前提**不成立**（正典基线 schema 造的就是无前缀分区名，862 那个 `_request_logs_2026_10` 只是**合成夹具名**）。**真正要修的是按名字枚举的口径**（已修，见 §5.9.3） | ~~真实不一致~~ → **前提有误** | 不出改名迁移（改名会与 860 的设计、基线 schema、`ensure_request_logs_partition` 的动态前缀脱节）；改为把两处按名字前缀枚举分区族的运维脚本改成**按 `pg_inherits` 走父表** |
| ~~**L2**~~ | ~~`customer_cost_view.active_concurrent`（5 分钟窗）与 `model_cost_per_task_view.avg_latency_ms`（7 天窗）是活指标读冻结族 ⇒ 恒 0 / 恒 NULL~~ ⇒ **2026-10-20 复核轮续做已修读源**（迁移 883 → 切会话族；真库行为门 + 5 道契约门，4 处变异转红；部署 `2.5.8.2757` 后活体直查：旧读源形状全 0/NULL，新读源取到真实延迟 4046.1ms / 6395.8ms，§5.12.6）。⚠ **但端点仍返回 `[]`**：外层聚合表 `api_key_model_cost` 的 trigger **从未接线**（真库全触发器清点确认它没挂在任何表上、该表 0 行、无唯一索引）⇒ 端点空是**新功能缺口**不是退役残留 ⇒ 记为 **L11**（见 §5.12.3） | ~~恒 0 / 恒 NULL~~ → **读源已修**，端点仍空（L11） | 复活聚合链：写侧 `canonical_id` → 唯一索引 → 会话族 trigger（§5.12.3 给了顺序与理由） |
| ~~L3~~ | ~~列表端点 COUNT / SUM / 分页三扫同一行集~~ ⇒ **2026-10-20 复核轮续做已修**：COUNT 与各 SUM 合并成**一条**聚合（同一行集的执行次数 2→1），降级语义保持；判据用 pgx QueryTracer 数查询 + 与旧两扫口径逐值对账（§5.12.4） | ~~效率~~ → **已收口** | 无 |
| L4 | ~~5 张 `v_*` 视图 + `request_logs_bodies_with_current_month` 仍读 v1 裸母表~~ ⇒ **2026-10-20 订正 + 收口**：① 「5 张」是**坏仪器**量出来的（`LIKE '%_request_logs%'` 的 `_` 是单字符通配符 ⇒ 把 canonical 视图名也判成命中）⇒ 精确重判**实为 3 张**，`v_model_health_dashboard` / `v_probe_system_health` 是**假阳性**；② 那 **3 张真读 v1 的分析视图已由迁移 884 收口**（改读会话族，`v_node_switch_analysis` 0 行 → 16 行）⇒ **本轮之后全库仍读 v1 族的视图恰好 4 张**（真库精确清点，2026-10-20）：`request_logs_with_current_month`（canonical，**含臂3**）、`..._without_customer_id`、`..._without_request_class_due_at`、`request_logs_bodies_progress`（+ `request_logs_bodies_with_current_month` 读 bodies 族）⇒ 与 **L12** 的阻塞项集合一致 | ~~5 张~~ → **3 张已收口**，余 4 张见 L12 | 「历史可读（保留）vs 活指标（必须迁）」的逐张判定已在 §5.13 做掉：3 张分析视图是**活指标且无 API 消费** ⇒ 收口；余下 4 张是**有意建成的中间层** ⇒ 随 L12 一起动 |
| L5 | `storage.admin_logs_native_turns_read` 默认关闭 ⇒ 列表页继续付 canonical 视图的 v1 臂 + 反连接成本 | 已决策 | 保持关闭（27.6% 缺口），但**成本要显式记账** |
| L6 | 本机 PG 的 `shared_buffers` 原值 400 kB 是**手改在容器数据目录**里的，不在仓库内 | 环境 | 已在本机修好并加部署门；**任何新建的本机 PG 都要按门检查**（门只在本仓的 deploy-local 路径生效） |
| **L7** | **客户端取消被记成 `500 / error.kind=server_error`**（`middleware/logging_mw.go` 的 `errorKind(status)` 只看状态码）⇒ 「客户端跑了」与「服务端坏了」在监控里**长得一样**；且多数 handler 的裸 `writeError(500, …)` **不留任何日志**（只有 `handleTurnsSessions` 等少数几处打了 WARN） | 观测 | 在 `emitRequestLog` 里判 `r.Context().Err() == context.Canceled` ⇒ `error.kind=client_aborted`（不改级别，避免把真 5xx 降级）；并让裸 `writeError(500,…)` 路径也留一行 WARN。**本轮未改**（共享中间件，改动面大且会动全部请求的日志形态） |
| **L8** | `prompt_injection_stats_enhanced`（迁移 364）**在正典库上建不出来**（引用不存在的 `detection_score`）；本轮已让统计端点**不再依赖**它，但**视图本身仍在** ⇒ 任何直接查它的外部消费者（`web-mobile/src/api/promptInjection.ts` 的注释仍这么写）继续 42P01 | 债 | 出一条新迁移用**正典形状**重建该视图（历史迁移字节不可改）；本轮未做（见 §5.6.1 的口径冲突：视图的等级分桶与正典整数 `risk_level` 不兼容，必须同时定口径） |
| **L9** | **一个模型的 429 会冷却整个凭据**：`nodehealth` 熔断（付费档连续 3 次）⇒ `EffectSetCredentialUnavailable` ⇒ `credentials.availability_state='cooling'` + **该凭据名下全部绑定 `available=FALSE`** ⇒ 兄弟模型（哪怕刚 200 过）一起 `no_candidate` 3 分钟。写侧的 `writeModelLevelFailureOnly` 本来是对的（只冷自己），是**熔断路径**把范围放大 | 设计代价 | 本轮**不改**：改成模型级会重新打到同一个已超配额的端点上，更糟。若要改，需先答「凭据级限流 vs 模型级限流怎么区分」——上游 TPM/RPM 限流是**账号级**的，凭据级冷却在这类场景是对的；真正要收窄的是**被误判为模型故障的凭据级限流** |
| **L10** | **探针 chat 腿没有 Responses 腿那个「预算耗尽也算证据」的豁免**：reasoning 模型在探针预算（`ProbePingMaxTokens=32`）内把预算全花在 `reasoning_content` 上、`content:null` ⇒ chat 腿判 `probe_response_inconclusive` ⇒ 永远到不了 `healthy_confirmed` ⇒ 模型不可路由。实测命中 `z-ai/glm-5.3-flash`（凭据 2） | 规则不对称 | 本轮**不改**：「2xx 不等于健康、必须有正向证据」是本仓 2026-10-20 12h 审计轮**刻意**立的规矩（`bg/probe_observation_beat_gate_20261020_test.go` 整篇在钉）。要改必须单独一轮，并同时回答「reasoning 模型的探针预算该给多少」（32 ⇒ 多少）和「`finish_reason=length` + `content:null` 算不算协议可达证据」 |
| **L11** | **auto-route 成本聚合链从未接线**：`api_key_model_cost` 的填充 trigger `trg_update_api_key_model_cost` 在正典与**全部** startup 迁移里都不存在（只在归档 feature doc 里建过，且建在 v1 表上）；**真库全触发器清点确认它没挂在任何表上**（函数 `update_api_key_model_cost()` 存在但**无调用方**）⇒ 该表**0 行**、**无任何索引** ⇒ 原函数里的 `ON CONFLICT (bucket, api_key_id, raw_model)` 会报 42P10。⇒ `/api/admin/auto-route/cost/customer` 与 `/api/admin/auto-route/cost/model` **从功能设计出来那天起就一直返回 `[]`**，与 request_logs 退役**无关**。⚠ **证据口径订正**：本条原先用 `pg_stat_user_tables.n_tup_ins = 0` 当「一行都没写过」的证据，**该读数已被本轮的真库门污染**（PG 计入回滚事务的插入，门用 `defer tx.Rollback()` 造夹具 ⇒ 连跑 3 次门计数 13→16、行数恒 0）⇒ 证据已换成**「触发器在不在 + 现在几行」**（§5.12.2 订正框 / §5.12.3） | 新功能缺口（非退役残留） | 按依赖序复活：① 写侧补 `canonical_id`（会话族现 0/NULL）与 `cost_usd` 的计价来源 → ② `api_key_model_cost (bucket, api_key_id, raw_model)` UNIQUE 索引 → ③ 在 `session_turns_hot` 上挂会话族版聚合 trigger（v1 版的 `outbound_model`/`client_model` 列在会话族不存在，**不能直接复用原函数体**）。**读源切换已完成**（迁移 883）。本轮**不做**（属新功能 + 要动写热路径） |
| **L12** | **「删除 `_request_logs`」这一步的真正阻塞项是 canonical 视图的 v1 臂（臂3）**：`pg_depend` 真库清点 ⇒ `_request_logs` 被 **7 个视图**依赖，其中 3 张分析视图已于迁移 884 收口 ⇒ 还剩 **4 个**：`request_logs_with_current_month`（**臂3**：`..._without_request_class_due_at` LEFT JOIN LATERAL（`_request_logs_hot ∪ _request_logs`）带双反连接去重）、`..._without_customer_id`、`..._without_request_class_due_at`、`request_logs_bodies_progress`。后 3 张是**有意建成**的 v1-only 中间层（`cmd/gateway/request_logs_view_session_arm_pin_test.go` 双向钉住形状与代码清单）⇒ **不能单独删**；臂3 现在**恒空**（v1 已清空）⇒ 删掉它不改变读数，但**会改变 canonical 视图的形状** ⇒ 撞上多道形状门 | 最终删除步骤（与 canonical 臂3 绑定） | ① 先让 canonical 视图在**不依赖 v1** 的前提下重建（臂3 恒空 ⇒ 语义上可去，但要同步改 shape 门与 `db/request_logs_view_schema.go` 的 composer、`db/request_logs_view_dump_generation_test.go` 的世代钉）；② 再把 3 张 v1-only 中间层与它们在 `v1OnlyRequestLogsViews` 的登记一并去掉；③ 最后 `DROP TABLE _request_logs*`（注意 860 的设计：待删标志在**父表**，月分区名**不带**前缀 ⇒ 一起删）。**本轮不做**（属最终删除步骤，牵动 canonical 形状） |

### 6.1 既有失败的归因（**不是**本轮引入）

`go test ./admin/`（**带 `TEST_PG_URL`**，否则这些真库门会被跳过）在
**pristine worktree**（`git worktree add /tmp/llmgw-pristine2 HEAD`，HEAD = `8cec460b3`，
不含本轮任何改动）上复跑，**逐字相同**地失败：

| 失败 | 消息 | 归因 |
|---|---|---|
| `TestFirstTurnSamplesReturnsRealSamplesAfterChannelGap` | `super_admin got 0 samples (total 0), want 3` | **数据依赖**：本地库没有 first-turn 样本数据。**与本轮改动无关**（pristine 复现一致） |
| `TestFirstTurnSamplesSessionsTitleShapeRealDB` | `public.sessions.title present = true`（✓）+ `super_admin Total = 0, want 3` | 同上（title 断言**过**，失败的是样本数） |

⇒ 这两条**只有**在设了 `TEST_PG_URL` 时才跑；上一轮把它们记成「既有失败」时
**没设**该变量 ⇒ 实际是被**跳过**了。⇒ 判别动作：**「绿」要问是「过」还是「跳过」**；
`t.Skip` 的门在没配环境变量时永远绿。
⇒ 它们与 §5.6 的 5 个 500 同源：**本地 PG 落后于正典 schema/数据**
（`user_tags` 缺列这条已由 ensure 链补上；样本数据与 `prompt_injection` 的
315 形状属另一层，见 §5.6.1 与 L8）。

**第三条（本轮新出现，已修）**：`TestDataLifecycleTableSizesFieldsAreComplete`
在一次全量跑里红了、**同提交再跑就绿**、pristine worktree 也绿 ⇒ 判据自身的竞态，
**不是**本轮引入的产品缺陷。根因与修法见 **§5.7**（判据与端点读同一个
`pg_total_relation_size`，却跨两次读做精确相等，而首行大表正被网关后台 worker 写入）。

---

## 5.9 ★ L1 订正：「7 张分区不带 `_` 前缀」**不是**缺陷，是 860 的设计；真正的问题是**按名字枚举的口径**

### 5.9.1 上一轮写下的结论（**前提有误**）

上一轮在遗留表里记了 L1：「7 张 v1 分区表未带 `_` 待删标志（`pg_inherits` 显示父表
是 `_request_logs`）⇒ **新装与存量环境形状分叉**」，并建议「出一条迁移把存量分区改名」。

**本轮复核后：这条结论的前提不成立。** 决定性证据有三条：

| 证据 | 读数 | 出处 |
|---|---|---|
| 860 的**明确设计** | 「monthly partitions and archive children (`request_logs_2026_10`, `request_logs_archive_2026_08`, `request_logs_default`, …) —— they **inherit the marker through their renamed parents** and are dropped together with them; their index names likewise stay」 | `sql/migrations/startup/860_rename_request_logs_family_pending_delete.sql:18-21` |
| **正典基线 schema 造的就是无前缀分区名** | `CREATE TABLE public.request_logs_2026_07` / `request_logs_2026_08` + `ALTER TABLE ONLY public.request_logs ATTACH PARTITION public.request_logs_2026_07 …`；860 随后只改**父表** | `deploy/sql/schemas/baseline/01-schema.sql:13002/13150/19569`（`sql/schema/01-schema.sql` 同） |
| **862 的 `_request_logs_2026_10` 只是合成夹具** | 那是 `migration_862_integration_test.go:54` 里的**夹具**建表语句；862 的**生产 SQL 本身是父表派生**的（`CREATE INDEX … ON public._request_logs (ts)`，不硬编码任何子分区名） | `sql/migrations/startup/862_partition_family_leading_ts_idx.sql:60`；全仓 `grep` 无任何生产 DDL 造 `_request_logs_YYYY_MM` |

⇒ **新装环境与存量环境产出的形状完全相同**（父表 `_request_logs` + 子表
`request_logs_2026_*`）⇒ 「形状分叉」不成立，两边**都**是这一形状。
⇒ 而且**改名不是正确的修法**：860 的函数体改写**刻意跳过**了动态前缀
`'request_logs_' || to_char(...)`（它的边界守卫明确写了「dynamic prefixes …
must not be touched」），所以只改存量分区名、**不改函数体**的话，一旦重新开启
v1 写路径，函数又会造出**无前缀**的新分区 ⇒ 分叉原地复活。
⇒ 正确动作是**把口径从「按名字」改成「按父表」**，而不是改名字。

### 5.9.2 但确实有一个**真缺陷**：按名字前缀枚举分区族的口径会漏

| 脚本 | 旧口径 | 漏掉的 |
|---|---|---|
| `scripts/ops/analyze-partition-stats.sh` | `relname ~ '^(credential_model_index\|model_probe_runs\|_request_logs)_(202[0-9]_[0-9]{2}\|hot)$'` | **5 张**：`request_logs_2026_07/08/10/11/default` |
| `scripts/pg-columnar-rotate.sh` | `child.relname ~ '^(_request_logs\|credential_model_index\|usage_ledger\|request_wal)_2026_(0[1-9]\|1[0-2])$'` | **4 张**：`request_logs_2026_07/08/10/11` |

⇒ 这两张表的**统计信息不被刷新**（`last_analyze` 停在旧值）、**列存轮转跳过它们**。
本机实测这 7 张分区合计 2,277,376 bytes、**0 行**（v1 族已冻结）⇒ 当前影响小，
但**是结构性的**：族一旦解冻或迁到别的环境，缺口立刻变成真问题。

### 5.9.3 修法与**差集证明**（真库实测）

两处都改成**按 `pg_inherits` 走父表**（父表名单 = 家族名；月份窗口仍按**子表自己的
后缀**判定，与旧口径等价）。仓库里已有这个写法的先例：
`deploy/sql/schemas/baseline/01-schema.sql:1436` 的
`c.relname ~ ('^' || parent_name || '_\d{4}_\d{2}$')`。

**旧口径 vs 新口径的差集（真库，2026-10-20）**：

| 脚本 | 新口径**补上** | 新口径**漏掉**（回退） |
|---|---|---|
| `analyze-partition-stats` | `request_logs_2026_07` / `_08` / `_10` / `_11` / `_default`（**5**） | **0**（旧口径的 8 张一张不少） |
| `pg-columnar-rotate` | `request_logs_2026_07` / `_08` / `_10` / `_11`（**4**） | **0**（旧口径的 10 张一张不少） |

**取数命令**（DSN 走 `DATABASE_URL`，不落盘）：

```bash
source /tmp/lgw-env.sh
PSQL="$HOME/kaixuan/pg18/bin/psql"
# 旧口径 vs 新口径的差集（两个 WITH + 双向 EXCEPT 的等价写法）
"$PSQL" "$DATABASE_URL" -f /tmp/anacmp.sql   # analyze 口径
"$PSQL" "$DATABASE_URL" -f /tmp/rotcmp.sql   # rotate 口径
# 分区归属（证明父表是谁）
"$PSQL" "$DATABASE_URL" -tAc "SELECT c.relname, p.relname FROM pg_class c
  JOIN pg_inherits i ON i.inhrelid=c.oid JOIN pg_class p ON p.oid=i.inhparent
  WHERE p.relname ~ '(^|_)request_logs' ORDER BY 1;"
```

**口径**：按 `pg_inherits` 的父子关系枚举分区，家族以**父表名**标识。
**覆盖范围**：`public` schema，`_request_logs` / `_request_logs_bodies` /
`credential_model_index` / `model_probe_runs` / `usage_ledger` / `request_wal`
六个家族的分区子表 + 各自的 `_hot` 表。

### 5.9.3.1 ★ 这条修复的**生效范围**（别把「已提交」当「已生效」）

两个脚本是**仓库级运维资产**，**不在部署物里**：本机部署目录
`~/kaixuan/llm-gateway-go-v2/` 只有 `bin/ logs/ run/ raw-logs/ attachments/ backups/`，
**没有 `scripts/`** ⇒ 改脚本**不需要重新部署网关**（网关二进制里没有它们）。

但**生产机上的脚本副本是旧的**（实测 154）：

```
/opt/llm-gateway-go/build-2515/scripts/ops/analyze-partition-stats.sh   pg_inherits=0   （10-08）
/opt/llm-gateway-go/build-src/scripts/ops/analyze-partition-stats.sh    pg_inherits=0   （08-25）
```

⇒ 若运维**从那两份副本**直接执行，跑的仍是**旧口径**（会漏 ANALYZE / 列存轮转那 4~5 张）。
⇒ **好消息是当前没有排程影响**：245 / 154 / 252 三台机 × 5 类排程源
（用户 crontab、root crontab、`/etc/cron*`、systemd unit/timer、**其它所有用户的
crontab**）**全部零命中**这两个脚本 ⇒ 没有任何定时任务在跑它们。
⇒ **处置建议**：运维改从**仓库 checkout** 执行这两个脚本（或把 154 上那两份副本刷新到
当前 `origin/main`）。**本轮未刷新那两份副本**（它们不在发布链上，属另一会话/运维的资产，
且当前无排程消费者）。

```bash
# 核对「哪台机上有旧副本、有没有排程在跑」
for h in 245 154 252; do
  ssh "$h" 'for u in $(cut -d: -f1 /etc/passwd); do
      crontab -u $u -l 2>/dev/null | grep -q "analyze-partition-stats\|pg-columnar-rotate" && echo "$u"
    done
    grep -rli "analyze-partition-stats\|pg-columnar-rotate" /etc/cron* /etc/systemd/system 2>/dev/null
    for f in $(ls -d /opt/llm-gateway-go/*/scripts/ops/analyze-partition-stats.sh 2>/dev/null); do
      printf "%s pg_inherits=" "$f"; grep -c pg_inherits "$f"
    done'
done
```

### 5.9.4 顺带订正一条**基线 schema 不该动**的判断

`deploy/sql/schemas/baseline/01-schema.sql:201` 的分区名正则里用的是**无前缀**的
`request_logs` / `request_logs_bodies` —— 看着像漏了 `_`，其实**是对的**：
基线 schema 造出来的分区本来就叫 `request_logs_2026_07`（无前缀），
860 是在基线**之后**才改的父表名。⇒ **不动基线 schema**（它是历史快照）。

---

## 5.10 ★ 补做「模型请求与写入测试」步骤：5/5 成功并完整落库 + 之后全量 503 的完整归因

### 5.10.1 可用的模型集（用户给的 7 个里只有 1 个在本机目录中）

> ⚠️ **2026-10-20 补正**：本小节「本机**目录里根本没有这些族**」的说法**不准确**——
> `/v1/models` 的目录**随可用凭据变化**（凭据 1 挂掉后 89 → 77，正好等于凭据 2 的
> provider 2452 的模型数）；用户清单里的模型**其实都在 `provider_models` /
> `credential_model_bindings` 里**，只是**不可路由**。
> 逐个归属（含每个模型不可路由的**真实原因**）见 **§5.11.6**。

`GET /v1/models`（本机）返回 **89** 个模型。用户清单里的族在本机的实际可用性：

| 用户清单里的模型 | 本机结果 | 原因 |
|---|---|---|
| `deepseek-v4-flash` | **200** | 有健康凭据 |
| `glm-5.3-flash` | 503 | 目录里只有 `glm-5.2` / `glm-5.3`，没有 `glm-5.3-flash` |
| `minimax-m2.7` / `grok-4.6` / `claude-sonnet-5-5` / `gpt-5.6-luna` / `mimo-v2.6-flash` | 503 / 400 | 本机**目录里根本没有这些族**（`minimax`/`grok`/`claude`/`gpt-5`/`mimo` 命中数均为 0）；`mimo-v2.6-flash` 是 `400 invalid_model: Model 'mimo-v2.6-flash' is not supported by this gateway` |

⇒ 实际可跑：`deepseek-v4-flash`、`deepseek-flash`、`glm-5.2`（探针 200/200/200，
`glm-5.3` 与 `deepseek-v4.1-flash` 503）。

### 5.10.2 成功的 5 轮与**读回证据**

会话头 `X-Session-Id: gw_add2edb31d14a16e8d027a602a63dd2b`（**必须** `gw_` 前缀，
否则被静默丢弃 ⇒ 多轮退化成 N 次单轮）。

**读回（`session_turns_hot`；正文在 `session_bodies_hot`，晋级有延迟）**：

| turn_no | model | prompt_tokens | completion_tokens | success | latency_ms |
|---|---|---|---|---|---|
| 1 | deepseek-v4-flash | 84 | 26 | t | 1795 |
| 2 | deepseek-v4-flash | 86 | 19 | t | 1799 |
| 3 | deepseek-v4-flash | 98 | 528 | t | 7469 |
| 4 | deepseek-v4-flash | 114 | 1323 | t | 19964 |
| 5 | deepseek-v4-flash | 103 | 2211 | t | 28756 |

**正文是否落库**（量具纪律：用 `jsonb_typeof(col)='array'` 判，**不用** `IS NOT NULL`，
因为 JSON `null` 也满足 `IS NOT NULL`）：

| turn_no | `outbound_body` | `response_delta` | `request_delta` |
|---|---|---|---|
| 1–5（全部 5 行） | **array** | **array** | **array** |

⇒ 5 轮全部落库、轮次序号连续、正文三列**都是数组** ⇒ **「数据存储可用」成立**。

### 5.10.3 一个**量具**坑：每轮只发一条消息 ≠ 多轮

首版脚本每轮只发**一条** user 消息 ⇒ 网关看到的每轮都是独立请求，
`prompt_tokens` 只反映**本轮提示词长度**。读数 `r4=114 > r5=103` 看着像「不单调」，
其实是因为 r5 的中文提示词比 r4 **短** —— 那是**提示词长度**，不是多轮上下文。
⇒ 已改成**累积对话**（每轮带上全部历史 user/assistant），这才是「多轮」的可验证形态
（`prompt_tokens` 必然单调增长）。

### 5.10.4 08:02 起全量 503 的**完整归因**（三个独立环境原因，**都不是仓库缺陷**）

| # | 现象 | 根因（读数） | 性质 |
|---|---|---|---|
| 1 | `no_candidate`（`candidates_count:0`） | 凭据 1 `availability_state='auth_failed'`（`state_updated_at=08:02:36`，`availability_recover_at=08:17:36`）⇒ 被 `autoroute/index.go` 的 `refreshIndexSQL` 里 `COALESCE(cr.availability_state,'ready')='ready'` 谓词滤掉 | **环境/上游**：凭据被判 auth 失败 |
| 2 | `upstream_down`：`provider requires egress proxy but none is available: no available active nodes` | `proxy_nodes` **0 行**、`proxy_subscriptions` **0 行** ⇒ 网关自己的出网代理节点库是空的 | **环境**：代理节点库没配 |
| 3 | 429 `budget_exceeded` | 凭据 3 `quota_state='permanently_exhausted'`、`health_error='responses status 429: {"error":{"message":"Organization balance insufficient","code":"budget_exceeded"}}'`、`lifecycle_status='suspended'` | **上游账单**：账户余额耗尽 |

⇒ 网关的行为是**正确**的：摘除失败候选、给出明确原因（`no_candidate` /
`upstream_down` / `429 budget_exceeded`），而不是把请求路由到已知坏的凭据。
⇒ 判定「这不是产品缺陷」的两条独立证据：
① 同一份二进制在 08:00:15–08:01:15 连续 5 次 200 且完整落库；
② 刷新作业本身**健康**（`auto index refreshed` 每 5 分钟一次，最新桶
`2026-10-20T00:05:00Z`、`credential_rows=7`），`credential_model_index_hot` 里
`deepseek-v4-flash|1` 在位 ⇒ 计划数据在库里，缺的是**凭据与代理节点**。

**取数命令**：

```bash
source /tmp/lgw-env.sh
PSQL="$HOME/kaixuan/pg18/bin/psql"
# 三个凭据的当前可用性
"$PSQL" "$DATABASE_URL" -tAc "SELECT id, provider_id, availability_state, health_status,
   quota_state, LEFT(COALESCE(health_error,''),60), availability_recover_at
   FROM public.credentials ORDER BY id;"
# 代理节点库
"$PSQL" "$DATABASE_URL" -tAc "SELECT count(*) FROM public.proxy_nodes;"
"$PSQL" "$DATABASE_URL" -tAc "SELECT count(*) FROM public.proxy_subscriptions;"
# 候选计划（生产 SQL，逐字）
# autoroute/index.go 的 refreshIndexSQL
```

### 5.10.5 顺带确认的一条**读侧事实**：`session_turns` 不是写入落点

写侧落在 **`session_turns_hot`**（本轮 15 行新数据全在 hot 表），晋级到
`session_turns` 有延迟 ⇒ **用 `session_turns` 直接查「刚发的请求」会查不到**，
必须查 hot 表或按时间窗查父表。本机读数：请求前 `session_turns=876` /
`session_turns_hot=174` / `session_bodies_hot=174` / `sessions=989`；
本轮 15 次请求后 hot 表新增 15 行（`turn_no` 1..5 × 3 会话）。

---

## 5.11 ★ 补做「累积式多轮」测试：`prompt_tokens` 84→1255 单调增长，退役族零新增

### 5.11.1 为什么要重做：§5.10.2 那 5 轮**不是多轮**

§5.10.3 已记：每轮只发**一条** user 消息 ⇒ 网关看到的是 5 个独立请求，
`prompt_tokens` 只反映**本轮提示词长度**（读数 84/86/98/114/103，r4 > r5
是因为 r5 的中文提示词更短，**不是**「不单调」）。

本节用**累积对话**（每轮带上全部历史 user/assistant，与真实聊天客户端一致）
重做，唯一目的是让「多轮」变成**可验证的形态**：`prompt_tokens` 必须单调不减。

### 5.11.2 两条**量具**坑（现象与「产品缺陷」同形，必须先排除量具）

**坑 1：BSD `sed` 不认 LHS 里的 `\n` ⇒ 标记没剥掉 ⇒ 请求体被清空。**

脚本用 `sed 's/\n__HTTP__[0-9]*__T[0-9.]*$//'` 剥 `curl -w` 追加的标记。
macOS 的 **BSD sed 不把 LHS 的 `\n` 当换行**（GNU sed 才当）⇒ 标记留在 payload 里
⇒ `json.loads` 报 `Extra data` ⇒ **history 被悄悄置空** ⇒ 下一轮请求体变成空串
⇒ 服务端回 `400 sanitization_failed: Invalid request body`。

服务端日志（这一条是**服务端自己**说的，但**根因在量具**）：

```
sanitize_middleware: sanitize failed
  error="invalid sanitizable request: JSON object required: unexpected end of JSON input"
```

本地对照（同一份输入，两种剥法）：

```
old=[{"a":1}
__HTTP__200__T1.5]        ← BSD sed：标记没剥掉
new=[{"a":1}]              ← python rfind：剥掉了
```

⇒ 修法：改用 python 按 `rfind("\n__HTTP__")` 剥；并让 **history 更新失败时显式中止**，
不再让空 history 继续跑（空 history ⇒ 请求体退化 ⇒ 现象与服务端 bug 同形）。
⇒ **判别动作：量具的失败可以伪装成服务端的失败。** 看到「服务端说请求体非法」，
先回头验量具的**输出**（本例：落盘的 payload 里还带着 `__HTTP__` 标记）。

**坑 2：探活一次碰多个模型 ⇒ 一个模型的 429 会把整个凭据冷却 ⇒ 后面的模型全部 `no_candidate`。**

详见 §5.11.4。⇒ **判别动作：探活一次只碰一个模型**，否则会把「凭据被冷却」
误读成「这个族没有可用模型」。

### 5.11.3 累积式多轮：**4 条判据全 PASS**

会话头 `X-Session-Id: gw_927eebbc5a24f59b580dd1fb0d678925`（`gw_` 前缀必需）。

**客户端读数**（每轮都带全部历史）：

| round | http | 耗时 | prompt_tokens | completion_tokens |
|---|---|---|---|---|
| 1 | 200 | 1.43s | 84 | 38 |
| 2 | 200 | 1.65s | 92 | 41 |
| 3 | 200 | 7.32s | 107 | 717 |
| 4 | 200 | 16.46s | 386 | 1352 |
| 5 | 200 | 25.13s | **1255** | 2158 |

**读回判据**（独立脚本 `verify-model-writeback.sh`，只读库、不发请求，与发送侧互不污染）：

| 判据 | 结果 |
|---|---|
| **A. 轮次连续** | `turn_no` = 1..5 连续，**5 行**，`success=t` 全绿，`latency_ms` 1247/1443/7123/16226/24951 |
| **B. 多轮生效** | `prompt_tokens` = 84 → 92 → 107 → 386 → **1255**，**严格单调递增** ⇒ 累积对话确实生效 |
| **C. 正文落库** | `session_bodies_hot` 5 行，`outbound_body` / `response_delta` / `request_delta` 的 `jsonb_typeof` **全是 `array`**（口径：用 `jsonb_typeof='array'` 判，**不用** `IS NOT NULL`） |
| **D. 退役族零新增** | `_request_logs` / `_request_logs_bodies` / `_request_logs_archive` / `_request_logs_hot` / `_request_logs_bodies_hot` **全部 0 行**；同族对照 `session_turns_hot=306` / `session_bodies_hot=306` / `session_turns=876` |

⇒ **A+B 证明「多轮是真的」；C 证明「正文落库」；D 证明「退役族没被复活」。**
⇒ 与 §5.10.2 的 5 轮合起来：**非累积 5/5 + 累积 5/5，共 10 轮全 200 且完整落库。**

**取数命令**（口径与覆盖范围一并落账）：

```bash
source /tmp/lgw-env.sh
PSQL="$HOME/kaixuan/pg18/bin/psql"
SID=gw_927eebbc5a24f59b580dd1fb0d678925

# A/B：轮次连续 + prompt_tokens 单调不减（写侧落在 hot 表）
"$PSQL" "$DATABASE_URL" -tAc "
  SELECT turn_no, model, prompt_tokens, completion_tokens, success, latency_ms
  FROM public.session_turns_hot WHERE session_id='$SID' ORDER BY turn_no;"

# C：正文是 JSON 数组（**不用** IS NOT NULL —— JSON null 也满足）
"$PSQL" "$DATABASE_URL" -tAc "
  SELECT turn_no, jsonb_typeof(outbound_body), jsonb_typeof(response_delta),
         jsonb_typeof(request_delta)
  FROM public.session_bodies_hot WHERE session_id='$SID' ORDER BY turn_no;"

# D：退役族零新增（覆盖父表 + hot + bodies + archive 四张）
for t in _request_logs _request_logs_bodies _request_logs_archive \
         _request_logs_hot _request_logs_bodies_hot; do
  echo -n "$t: "; "$PSQL" "$DATABASE_URL" -tAc "SELECT count(*) FROM public.$t;"
done
```

**覆盖范围说明**：本节只覆盖**本机**（`127.0.0.1:8782` + 本机 PG 17.10）、
**凭据 1（provider 2453，sensenova）**、**模型 `deepseek-v4-flash`**。
其它凭据/模型的可用性见 §5.11.6；本节**不**声称其它环境已验证。

### 5.11.4 新发现一：一次 429 会把**整个凭据**冷却 3 分钟，兄弟模型一起连坐

**实测链**（2026-10-20 08:20:45 → 08:24:44）：

1. 08:20:45 探 `deepseek-v4-flash`（我自己的探针带了 `max_tokens:16`）
   → 上游 `token.sensenova.cn` 返回 **200 但内容为空**（reasoning 模型被 16 token 截断）
   → 网关判 `empty_response` ⇒ failover。**该错误不计入连续失败数**
   （`nodehealth/reducer.go` 的 `ErrorKindEmptyResponse` 直接 `break`）。
2. 08:20:54 / 08:20:59 / 08:21:10 探 `deepseek-flash` → 上游 **429
   `RateLimitExceeded.EndpointTPMExceeded`**（"inference exceeds tpm/rpm limit"）
   ⇒ goal-retry 对**同一节点**连打 3 次。
3. 3 次后 `nodehealth` 的 `ConsecutiveFailures`（NodeKey = 租户+provider+**凭据+模型**）
   达到 `circuitFailureThreshold(billing_mode)` = **3**（付费档；免费档 6）
   ⇒ `effectsFor` 触发 `EffectRecordCircuitFailure` + `EffectSetBindingUnavailable`
   + **`EffectSetCredentialUnavailable`**。
4. `EffectSetCredentialUnavailable` → `credential.Writer.SetCredentialUnavailable`
   ⇒ `credentials.availability_state='cooling'`（`availability_recover_at` = +3min）
   **且把该凭据名下全部 10 个绑定置 `available=FALSE`**。
5. ⇒ **刚刚才 200 过的 `deepseek-v4-flash` 立刻变成 `no_candidate`**，直到 08:24:44
   恢复 ticker 把它翻回 `ready`。

**读回证据**（冷却窗口内，cred1 的 10 个绑定）：

```
f|auto_credential_rate_limit|SenseChat-5
f|auto_credential_rate_limit|SenseChat-5-Thinking
f|auto_credential_rate_limit|SenseChat-Turbo
f|auto_rate_limit|deepseek-flash              ← 模型级（writeModelLevelFailureOnly 做的，对）
f|auto_credential_rate_limit|deepseek-v4-flash ← 凭据级（连坐）
f|auto_credential_rate_limit|deepseek-v4.1-flash
f|auto_credential_rate_limit|glm-5.2
f|auto_credential_rate_limit|kimi-k3
f|auto_credential_rate_limit|sensenova-6.8-flash-lite
f|auto_credential_rate_limit|sensenova-u1.5-lite
```

⇒ 注意两条路径**同时**发生：写侧 `WriteOnError` 对 `KindConcurrent` 走
`writeModelLevelFailureOnly`（**只**冷 `deepseek-flash` 自己的绑定，**不碰**
`credentials.availability_state`，`writer.go:518` 明写「✅ DO NOT update
credentials.availability_state」）；而 `nodehealth` 的熔断路径
（`EffectSetCredentialUnavailable`）把范围**放大到整个凭据**。
⇒ **归属**：熔断「连打 N 次 ⇒ 整个凭据不可用」是 `reducer.go:434` 的**明确设计**
（注释：凭据级熔断 + 免费档阈值 6），不是本轮引入的回归；**代价**是
「一个模型的限流会摘掉该凭据名下全部模型」。记为遗留 **L9**，本轮不改
（改成模型级会重新打到同一个已超配额的端点上，反而更糟）。
⇒ **判别动作**：看到「某个模型族全部 `no_candidate`」时，**先查凭据级状态**
（`credentials.availability_state`），再查模型级绑定；两者原因完全不同。

### 5.11.5 新发现二：探针的 chat 腿**没有** Responses 腿那个「预算耗尽也算证据」的豁免

- **Responses 腿有豁免**：`bg/probe_http_validation.go` 的
  `responsesProbeProtocolEvidence` 接受
  `status=incomplete` + `incomplete_details.reason=max_output_tokens` + 非空 `output`
  ⇒ 判为**有正向证据**（注释：「A complete Responses token-budget terminal proves
  the former even if all of its output budget went into reasoning」）。
- **chat 腿没有**：`bg/credential_probe_response_validation.go:147-152` 要求
  `choices[].message.content` 是**非空字符串**（或含文本的 content 数组 / 带名字的 tool_calls）。
  `content: null` + 只有 `reasoning_content` ⇒ **没有证据** ⇒ `probe_response_inconclusive`。
- **实测**（凭据 2 / provider 2452 / NVIDIA NIM）：`z-ai/glm-5.3-flash` 探针
  连续多轮拿到 **HTTP 200**，正文是
  `{"message":{"content":null,"reasoning_content":"The user just said \"ping\"..."}}`、
  `finish_reason:"length"` ⇒ 判 `probe_response_inconclusive`、attempt 递增、
  一直退避 ⇒ 该模型**永远到不了 `healthy_confirmed`** ⇒ 不可路由。
- **探针的 token 预算是 32**（`providercap.ProbePingMaxTokens = 32`，注释里明写
  「a reasoning model can burn the entire budget invisibly」）⇒ 对 reasoning 模型
  而言 32 token 常常**只够写 reasoning**。
- ⇒ 这是**规则不对称**（同一件事，Responses 腿放行、chat 腿定罪），但
  「2xx 不等于健康、必须有正向证据」是本仓 2026-10-20 12h 审计轮**刻意**立的规矩
  （`bg/probe_observation_beat_gate_20261020_test.go` 整篇在钉它）⇒
  **本轮不改**，记为遗留 **L10**（要改需要单独一轮，并且必须同时回答
  「reasoning 模型探针该给多少预算」）。

### 5.11.6 订正 §5.10.1 的说法：用户清单 7 个模型在本机的**逐个**归属

§5.10.1 当时写的是「本机**目录里根本没有这些族**」。这句话**不准确**：
`/v1/models` 的目录是**随可用凭据变化**的（凭据 1 挂掉后目录从 89 个变成
**77 个**，正好等于凭据 2 的 provider 2452 的模型数），而**用户清单里的模型
其实都在 `provider_models` / `credential_model_bindings` 里**——只是**不可路由**。
逐个归属（真库读数）：

| 用户清单模型 | `provider_models` | 凭据 | 绑定 | **不可路由的真实原因** | 性质 |
|---|---|---|---|---|---|
| `deepseek-v4-flash` | id=1（provider 2453） | 1 | available | 凭据 1 曾 `auth_failed`（08:02:36→08:17:36，08:18:05 恢复）⇒ 期间 `no_candidate` | **环境**（已恢复） |
| `glm-5.3-flash` | id=3 `z-ai/glm-5.3-flash`（2452） | 2 | available | 探针 200 但 `content:null` ⇒ `probe_response_inconclusive` ⇒ 永远退避（§5.11.5） | **仓库规则**（L10） |
| `minimax-m2.7` | id=2 `minimaxai/minimax-m2.7`（2452） | 2 | available | **上游 410 Gone**：`The model 'minimaxai/minimax-m2.7' has reached its end of life on 2026-07-27T00:00:00Z and is no longer available` | **上游退役**（不可修） |
| `grok-4.6` | id=5（provider 2454） | 3 | available | 凭据 3 `quota_state='permanently_exhausted'`（429 `Organization balance insufficient`） | **上游账单**（不可修） |
| `claude-sonnet-5-5` | id=6（2454） | 3 | available | 同上 | **上游账单**（不可修） |
| `gpt-5.6-luna` | id=4（2454） | 3 | available | 同上 | **上游账单**（不可修） |
| `mimo-v2.6-flash` | **无行** | — | — | 本机从未收录该模型（`provider_models` 里没有任何 `mimo` 记录） | **环境**（未配置） |

补充读数：凭据 2 的探针失败分布（`node_probe_runs` 全量）——`http_404` **266** 次
（NVIDIA 侧 `Function 'c322f327-…' Not found for account` ⇒ **该账号没部署这些模型**）、
`timeout` 206、`http_500` 138、`http_410` 96（EOL）、`probe_response_inconclusive` **10**、
`http_400` 17、`http_429` 6、`network_error` 3、`http_502` 2
⇒ **凭据 2 不可用的主因是「账号没部署这些模型」，不是探针规则**；
探针规则只影响其中 10 次。

**取数命令**：

```bash
source /tmp/lgw-env.sh
PSQL="$HOME/kaixuan/pg18/bin/psql"
# 模型 → provider_models → 凭据 → 绑定可用性
"$PSQL" "$DATABASE_URL" -tAc "
  SELECT pm.id, pm.provider_id, pm.raw_model_name, pm.available, c.id
  FROM provider_models pm JOIN credentials c ON c.provider_id=pm.provider_id
  WHERE pm.raw_model_name ILIKE '%deepseek-v4-flash%' OR pm.raw_model_name ILIKE '%glm-5.3-flash%'
     OR pm.raw_model_name ILIKE '%minimax%' OR pm.raw_model_name ILIKE '%grok-4.6%'
     OR pm.raw_model_name ILIKE '%claude-sonnet-5-5%' OR pm.raw_model_name ILIKE '%gpt-5.6-luna%'
     OR pm.raw_model_name ILIKE '%mimo%';"
# 探针失败分布（按 err_code 计数）
"$PSQL" "$DATABASE_URL" -tAc "
  SELECT direct_err_code, count(*) FROM node_probe_runs WHERE credential_id=2 GROUP BY 1 ORDER BY 2 DESC;"
# 上游 410 的原文
"$PSQL" "$DATABASE_URL" -tAc "
  SELECT id, raw_model_name, direct_http_status, left(direct_err_detail,160)
  FROM node_probe_runs WHERE credential_id=2 AND raw_model_name='minimaxai/minimax-m2.7'
  ORDER BY id DESC LIMIT 1;"
```

---

## 5.12 ★ 续做：从遗留表里挑出「本轮真能收口」的两项（读源 D1 / 扫描次数 D3）

### 5.12.1 怎么挑的：只动**有可观测效果**且**在目标内**的两项

上一轮遗留表里有 6 项（L1/L2/L3/L4/L5/L6/L7/L8）。本轮按三条标准筛：

| 候选 | 在目标内？ | 有可观测效果？ | 本轮处置 |
|---|---|---|---|
| **L2**（两个活指标读冻结族） | ✅「API 读源转到 session 族」 | ✅（恒 0 / 恒 NULL 是错的） | **做**（§5.12.2） |
| **L3**（列表端点同一行集扫两遍） | ✅「优化列表效率，不要表扫描」 | ✅（执行次数 2→1） | **做**（§5.12.4） |
| L4（`v_*` 视图读 v1 裸母表） | ✅ 但**全是历史只读面** | ❌ 活指标不依赖它们 | 留待（L4）⇒ ⚠️ **该行的「5 张」是坏仪器量出来的**（`_` 通配符），精确重判实为 3 张；那 3 张**已于 §5.13 由迁移 884 收口** |
| L5（`admin_logs_native_turns_read` 默认关闭） | ⚠️ 是**开关决策**不是缺陷 | — | 留待（L5） |
| L7 / L8 | ✅ 但改动面在共享中间件 / 需要新迁移定口径 | — | 留待 |

### 5.12.2 D1：两个**活指标**读的是**冻结族** ⇒ 一个恒 0、一个恒 NULL（且无任何错误信号）

**缺陷本体**（真库读数，非推理）：

| # | 指标 | 窗口 | 原读源 | 后果 |
|---|---|---|---|---|
| 1 | `customer_cost_view.active_concurrent` | **5 分钟** | `_request_logs` | **恒 0**（任何环境都恒 0，见下） |
| 2 | `model_cost_per_task_view.avg_latency_ms` | **7 天** | `_request_logs` | 只能读到**停写那一刻**的快照值（7 天后为 NULL） |

三个决定性读数：

```bash
source /tmp/lgw-env.sh
PSQL="$HOME/kaixuan/pg18/bin/psql"
# ① v1 族在退役后是空的（本地）
"$PSQL" "$DATABASE_URL" -tAc "SELECT count(*) FROM public._request_logs;"        # 0
"$PSQL" "$DATABASE_URL" -tAc "SELECT count(*) FROM public._request_logs_hot;"    # 0
# ② 它**从未**被写入过 —— ⚠ 用「触发器 + 行数」判定，**不要**用 n_tup_ins（见下方订正）
"$PSQL" "$DATABASE_URL" -tAc "SELECT count(*) FROM public._request_logs;"        # 0
"$PSQL" "$DATABASE_URL" -tAc "SELECT t.tgname, c.relname FROM pg_trigger t
  JOIN pg_class c ON c.oid=t.tgrelid WHERE NOT t.tgisinternal
    AND c.relname IN ('_request_logs','_request_logs_hot');"                     # 0 行
# ③ 端点现状
curl -s -H "Authorization: Bearer $ADMIN_TOKEN" \
  http://127.0.0.1:8782/api/admin/auto-route/cost/customer                      # []
```

> **⚠ 订正（本轮实测发现的一条「判据污染度量」）**：这条证据最初写的是
> `pg_stat_user_tables.n_tup_ins` = 0（当时确实读数 0，因为**门还没写**）。
> 但本轮把真库门加上之后，同一个读数变成了 **13**，而表里**仍然是 0 行**、
> `n_tup_del=0` ⇒ 看起来像「表被写过了、又被删了」。
> ⇒ 根因：**PG 的 `n_tup_ins` 计入「回滚事务里的插入」**（本轮受控实验实测：
> `BEGIN; INSERT; ROLLBACK;` 之后 `n_tup_ins` 确实 +1），而本轮的真库门
> `admin/auto_route_cost_views_session_source_test.go` 正是
> `defer tx.Rollback()` 的**夹具插入**（`api_key_model_cost` 那行）⇒
> **门把自己的度量污染了**。
> ⇒ 决定性复现：连跑该门 3 次，`n_tup_ins` 13 → 16（**恰好 +3**），
> 表行数**始终 0** ⇒ 那 13 次插入**全部是门自己的回滚夹具**，
> 该表**从未被生产流量写过**。
> ⇒ **判别动作：要证明「某表没被写过」，用「触发器在不在 + 现在几行」，
> 不要用 `n_tup_ins`**——后者会被任何跑过夹具的测试污染，且
> 「回滚也算」这个语义不看文档根本想不到。

**为什么「恒 0」是普遍结论而不是「本机没数据」**：`active_concurrent` 的窗口是
**5 分钟**，而 v1 族自 2026-10-13 停写 ⇒ **冻结族里永远没有「最近 5 分钟」的行**
⇒ 这个计数在**任何环境**都恒 0（哪怕那台环境有三年历史数据）。它衡量的是
「此刻有多少个 auto 请求在跑」，恒 0 ⇒ 并发压力看板与由它派生的
`pressure_ratio` / `avg_pressure_1h` 全部失去意义，**且不报任何错**。

**语义映射**（逐列核对，不是机械替换）：

| v1 列 | 会话族对应列 | 依据 |
|---|---|---|
| `outbound_model` | **`raw_model_name`** | `domains/streaming/executors/executor.go` 的 `candidateRawModel(c)` ⇒ 候选的**上游原始模型**；`session_turns.model` 是**客户端**模型，语义不同 |
| `api_key_id` | `api_key_id` | ⚠ **类型不同**：会话族是 **text**（写侧 `APIKeyID string`，落库是十进制文本 `'2'`），`api_key_model_cost.api_key_id` 是 **integer** ⇒ 必须显式 `::text`，否则建视图时报 `operator does not exist: text = integer`（本轮真实踩到） |
| `is_auto_request` / `success` / `ts` / `latency_ms` | 同名列 | 语义一致 |

**两条腿的选型**（各有依据，不是随手挑）：

- `active_concurrent`（5 分钟）= **hot 单腿** `session_turns_hot`
  · **完整**：在线写路径只写 hot（`domains/session/v2/turn_writer.go`），
    晋级延迟远大于 5 分钟 ⇒ 窗口内的行必然在 hot；
  · **无重复**：计数指标不能被数两次（下面 ② 的 UNION ALL 才有那个问题）；
  · **便宜**：本仓先例 —— `db/request_logs_view_schema.go` 的
    `SessionFamilyTurnsHotSourceSQL` 明写「短窗仪表盘用 hot-only，免得父表
    分区扫描与并发 MV REFRESH 把 `/api/logs/top-models` 顶爆 10s 预算
    （2026-10-15 生产 500 实证）」。
- `avg_latency_ms`（7 天）= **hot ∪ parent 两条腿 UNION ALL**
  · 7 天窗跨过 hot 保留期（名义 8h）⇒ 只带 hot 会**整段丢失** 7 天前到 8 小时前；
  · 形态与 `SessionFamilyTurnsSourceSQL` 同款；promote 会排空 hot ⇒ 不会重复；
  · 且它是**均值**，对偶发重复不敏感（计数指标对重复敏感，所以那条债是另一回事）。
  · **不能**复用现成的 `session_turns_with_current_month`：它不投影
    `raw_model_name`（实测 information_schema），而本子查询需要它。

**改法与登记**（迁移 883）：

| 落点 | 内容 |
|---|---|
| `sql/migrations/startup/883_session_family_cost_view_metrics.sql` (+`.down.sql`) | 两条 `CREATE OR REPLACE VIEW`（列名/列序/列类型不变 ⇒ 可直接替换）+ `lock_timeout=60s` + `statement_timeout=5min` + `POST_CONDITION` |
| `sql/objects/views/{customer_cost_view,model_cost_per_task_view}.sql` | 正典定义体同步 |
| `sql/schema/01-schema.sql` **与** `deploy/sql/schemas/baseline/01-schema.sql` | 两份**逐字节相同**（`deploy/sql/verify-migration.sh` 会 diff 它们）⇒ 新装环境与存量环境形状一致 |
| `installer/cmd/llm-gw-installer/main.go` | `go:embed` + startup map 条目 |
| `installer/cmd/llm-gw-installer/embeddata/startup/883_*.sql` | embeddata 副本（与 canonical **SHA 现场一致**：`84a614746d06…`） |
| `installer/internal/dbinit/runner.go` | `StartupFiles` 条目 |
| `scripts/apply-db-revision-sequence.sh` | 部署通道 `files=()` 条目 |
| `sql/schema/installed_startup_migrations.tsv` | **用生成器重建**（`go test -run TestStartupManifest -update`）⇒ 883 @ 位置 278（序号不写死） |

**判据（2 道，都做过变异验证）**：

| 门 | 钉什么 | 变异 → 转红 |
|---|---|---|
| `admin/auto_route_cost_views_session_source_test.go`（**真库 + 造夹具**） | 两个指标真的取到**会话族**的值：造 `api_key_model_cost` 行 + 3 条会话行，断言 `active_concurrent == 1`、`avg_latency_ms == 夹具延迟` | **M1** 改回读 v1 ⇒ `active_concurrent` 变 0；**M2** 把匹配键换成 `model` ⇒ `avg_latency_ms` 变 NULL |
| `sql/migrations/startup/migration_883_..._test.go`（契约门，5 道） | 迁移体不读 v1 + 两条腿的选型 + `::text` + down 可回滚 + canonical/baseline 同步 + embeddata 一致 + **三处登记都在** | **M3** 把 883 从部署通道摘掉 ⇒ 登记门红；**M4** 去掉 `::text` ⇒ 形态门红 |

⇒ 门里还配了**前提断言**（`public._request_logs` 当前确实是空的 ⇒「指标非零」
只可能由会话族贡献）与**阴性侧断言**（非 auto 行、`success IS NULL` 行都**不得**
被计入 ⇒ 防止「为了让门过，把谓词删掉」）。

**⚠ 端点仍然返回 `[]` —— 这不是本轮没修好，是另一件事**（见 §5.12.3）。

### 5.12.3 D2（新遗留 **L11**）：成本聚合链**从未接线**（不是退役造成的）

`api_key_model_cost` 是两张视图的**外层**聚合表。它的填充依赖 trigger
`trg_update_api_key_model_cost`，而：

- 全仓搜 `CREATE TRIGGER ... api_key_model_cost` ⇒ **只在一份归档 feature doc
  里**（`deploy/sql/docs/features/2026-06-15-auto-route-mode-cost-table.sql`，
  建在 `request_logs` 上）⇒ **正典与全部 startup 迁移里都没有它**；
- **真库清点（本轮实测，2026-10-20 部署后）**：
  `SELECT t.tgname, c.relname FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid
   WHERE NOT t.tgisinternal;`
  ⇒ 全库非内部触发器里**没有** `trg_update_api_key_model_cost`，
  `api_key_model_cost` 身上**也没有**任何触发器；
  退役族上只剩两个**别的**触发器（`trg_session_tenant_binding` 挂
  `_request_logs`、`trg_update_session_summary` 挂 `_request_logs_hot`）。
  ⇒ 函数 `update_api_key_model_cost()` **确实存在于库里**（归档 feature doc 建过），
  但**没有任何触发器调用它** ⇒ 它**一次都没跑过**。
- `SELECT count(*) FROM public.api_key_model_cost;` = **0 行**
  （⚠ **不要**用 `n_tup_ins` 当「有没有被写过」的证据——**本轮的门自己会污染它**，
  见 §5.12.2 的订正框：PG 计入回滚事务的插入，夹具 `defer tx.Rollback()` 会让
  计数只涨不落，连跑 3 次门实测 13→16、行数恒 0）；
- `api_key_model_cost` **一个索引都没有** ⇒ 原函数里的
  `ON CONFLICT (bucket, api_key_id, raw_model)` 会直接报
  `42P10: there is no unique or exclusion constraint matching the ON CONFLICT specification`
  ⇒ 就算把 trigger 挂回去，**第一行就会失败**。

⇒ **结论：这两个成本端点从功能设计出来那天起就一直是 `[]`**，与 request_logs
退役**无关**（不是退役造成的回归）。「端点空」和「读源错」是**两件事**：
本轮修的是后者（读源），前者是**新功能缺口**。

**复活它的前置顺序**（缺一不可，按依赖排）：

1. **写侧**：会话族的 `canonical_id` 实测 **0/NULL**（`session_turns` 与
   `_hot` 都是）⇒ `model_cost_per_task_view` 的分组键会一直是 NULL；
   `cost_usd` 虽有值但本机全为 0（本地凭据无计价）⇒ 成本列要真实，得先确认
   写侧在有计价的环境里会落非零值。
2. **唯一索引**：`api_key_model_cost (bucket, api_key_id, raw_model)` UNIQUE
   （否则 `ON CONFLICT` 无法工作）。
3. **会话族 trigger**：在 `session_turns_hot` 上挂会话族版聚合函数
   （会话族的列名是 `model`/`raw_model_name`，v1 版的 `outbound_model`/
   `client_model` 不存在 ⇒ 不能直接复用原函数体）。
4. **读源切换**：**本轮已完成**（迁移 883）。

⇒ 本轮**不做** 1–3：那是**新功能**（要动写侧 + 新增唯一索引 + 挂在写热路径上的
trigger），不在「退役双写」的目标范围内，且会显著放大本轮的风险面。
⇒ **判别动作**：看到「某个端点返回空」时，**先问「它外层的聚合表有没有在增长」**
（`pg_stat_user_tables.n_tup_ins`），再问「读源对不对」——两者独立，混在一起
就会把「新功能缺口」误当成「退役残留」。

### 5.12.4 D3：列表端点对同一行集扫**两遍** → 扫一遍

上一轮把「列表端点三扫」记成 L3，理由是「**在本地小数据集上无法证明收益**」。
本轮换了量法：**量执行次数，不量墙钟/buffers**。

**为什么旧量法量不出来**（真库 EXPLAIN ANALYZE, BUFFERS，24h 窗，三种写法）：

| 写法 | Buffers | Execution |
|---|---|---|
| A：旧 COUNT | shared hit=137 | 0.30ms |
| B：旧 SUM | shared hit=137 | 0.30ms |
| C：合并后 | shared hit=137 | 0.30ms |

⇒ 本地只有 ~1.2k 行，**第二趟全命中缓存** ⇒ buffers 完全相同、**量不出差别**。
⇒ 但**执行次数**是**结构性**、与数据量无关的事实：同一行集被独立扫了两次。

**改法**（`admin/logs.go` 的 `listLogs`）：COUNT 与各 SUM 合并成**一条**聚合
（`SELECT COUNT(*), COALESCE(SUM(...)), ... FROM <同一 FROM/WHERE>`），
执行次数 2 → 1。

**失败语义必须保持**（旧行为是「SUM 失败 ⇒ 聚合字段回零、页照常出；
COUNT 失败才 500」）：

- 合并查询失败 ⇒ 退回**单独**的 COUNT 查询（count 与列表仍然可用，聚合字段保持零值）；
- COUNT 也失败 ⇒ 500（与旧行为一致）。

**口径不变**：COUNT 与 SUM 用的是同一 FROM/WHERE 与同一组参数
（tenant_admin 路径两者都带 `api_keys` 的 LEFT JOIN；super_admin 路径两者都不带）
⇒ 合并查询里的 `COUNT(*)` 与旧 COUNT 查询**逐字等价**。

**判据**（`admin/logs_single_scan_test.go`，真库 + **pgx QueryTracer** 数查询）：

- 判据 ①：统计「读日志源 + 带聚合 + **无 GROUP BY**」的查询条数 ⇒ 必须 **1**
  （旧写法是 2；`by_model` 的 GROUP BY 不计入）；
- 判据 ②：响应的 `count` 与各 SUM 与**旧两次扫描的参考口径**（测试自己用同一源、
  同一对 from/to 重算）**逐值相等**。
- 变异验证：把 COUNT 拆回独立查询 ⇒ 判据 ① 转红（报 2 次扫描并打印录到的 SQL）；
  恢复后转绿。

**活体读回**（部署后）：`/api/logs?page=1&page_size=3` ⇒ 200，
`count=324`、`aggregate.total_requests=324`、`prompt_tokens=13109`、`by_model` 20 项。

### 5.12.5 本轮新增的一条判据纪律

**夹具里被比较的两个键必须「故意不同」，否则门分不出键漂移。**
第一版真库门把会话行的 `model` 与 `raw_model_name` 设成同一个值 ⇒ 变异 M2
（把匹配键从 `raw_model_name` 换成 `model`）**没让门变红** ⇒ 门是「看着在测、
其实测不出来」的那种。改成两个不同的字符串后，同一个变异立刻转红。
⇒ **判别动作：写夹具时问一句「我要防的那个漂移，会让这两个值不同吗」**；
若不会，夹具就钉不住它。

### 5.12.6 部署后读回（`2.5.8.2757`，`VERIFY_PASS=1`）

**版本身份**（部署脚本自己验的，不是我事后看目录猜的）：

```
[verify] 8782:…/bin/2.5.8.2757: ok (version=2.5.8-ddc2f336-20261020-2757 build_seq=2757)
VERIFY_PASS=1   VERIFY_RELEASE=2.5.8.2757   VERIFY_PORT=8782
```

⇒ 运行中的二进制身份 = `ddc2f336` = 本轮代码提交 `ddc2f3362`（**sha 对得上**，
不是「当前 release 目录里有没有」那种弱证据）。

**D3 的活体读回**（列表端点，合并聚合之后）：

| 读数 | 部署前（老二进制 + 新视图） | 部署后（新二进制） | 判定 |
|---|---|---|---|
| HTTP / 耗时 | 200 | **200 / 82ms** | ✅ |
| `count` | 324 | **324** | ✅ 与旧 COUNT 逐值一致 |
| `aggregate.total_requests` | 324 | **324** | ✅ 与 `count` 自洽 |
| `aggregate.prompt_tokens` | 13109 | **13109** | ✅ 逐值一致 |
| `aggregate.completion_tokens` | — | **11875** | — |
| `aggregate.total_tokens` | — | **24984** | = 13109 + 11875 ✅ |
| `aggregate.by_model` | 20 项 | **20 项** | ✅ |
| `items` | 3 | **3** | ✅ |

⇒ **口径没变**：合并后的单次聚合与旧的两扫（独立 COUNT + 独立 SUM）
**逐值相等**（判据里也用旧口径重算做过对账，§5.12.4）。
⇒ **但「扫一遍」这件事在活体上是读不出来的**（本机 ~1.2k 行，buffers 量不出，
见 §5.12.4）⇒ 它的证据是**结构性**的（QueryTracer 数查询 = 1），
不是**运行时**的。别把「读数一样」当成「少扫了一遍」的证据。

**D1 的活体直查**（真库，逐条形状对照）：

| 形状 | 读数 |
|---|---|
| **旧读源**（v1 冻结族，同一条子查询的形状） | `v1_active_concurrent = 0`、`v1_avg_latency_7d = NULL`、`_request_logs` **0 行** |
| **新读源**（会话族 7 天窗，hot ∪ parent） | `deepseek-v4-flash` 54 行 / **avg 4046.1ms**；`glm-5.2` 29 行 / **avg 6395.8ms** |
| **视图内子查询**（外层行存在且键匹配时） | `deepseek-v4-flash` → **4046.1**、`glm-5.2` → **6395.8**、`grok-4.6` → NULL（该模型 7 天内确无成功行 ⇒ NULL 是**正确**的） |

⇒ 决定性点：**旧读源在这三条上全是 0 / NULL**（冻结族里没有数据），
新读源能取到**真实延迟** ⇒ 读源确实切到了会话族，**且键空间是对的**
（`raw_model_name` 而不是 `model`）。

**⚠ 但 `active_concurrent` 的活体值仍然是 0 —— 这也是正确的**：
会话族里 `is_auto_request` **全是 false**（hot 310 行 / parent 877 行，
auto 行数 **0 / 0**）⇒ 没有任何 auto 路由流量 ⇒ 5 分钟窗里自然没有可计数的行。
`is_auto_request` 来自 **`X-Gw-Is-Auto` 头或 `model="auto"`**
（`domains/streaming/handler.go:7006`）⇒ 本机这轮测试走的是**指定模型直连**，
不是 auto 路由。
⇒ **判别动作：看到 `active_concurrent = 0`，先问「这台机器有没有 auto 流量」
（`count(*) FILTER (WHERE is_auto_request)`），再问「读源对不对」**——
前者为 0 时，读源再对也是 0。**「0」有两种成因，必须分开。**

**端点与台账终态**：

```
/api/admin/auto-route/cost/customer → 200 []      （L11：外层聚合表 0 行）
/api/admin/auto-route/cost/model    → 200 []      （同上）
customer_cost_view 定义体含 _request_logs?          → false
model_cost_per_task_view 定义体含 _request_logs?   → false
customer_cost_view 含 FROM session_turns_hot st?    → true
model_cost_per_task_view 含 FROM session_turns st_par? → true
台账 session-summary-and-integrity-2026-09:883_session_family_cost_view_metrics.sql
     → content_sha256 = 84a614746d06…（与 canonical 副本一致）
退役族 _request_logs 行数 = 0 ；session_turns_hot = 310 ；session_turns = 877
```

**⚠ 端点仍 `[]` 是 L11，不是本轮修坏了**：外层聚合表 `api_key_model_cost`
**0 行**且**无触发器**（§5.12.3 的清点）⇒ 视图的相关子查询**压根不会被执行**
⇒ 两个端点必然返回 `[]`。**「读源已修」与「端点有数」是两件事。**

> **⚠ 读数口径提醒：`count` 是「活」的、会自增。** 上表的 `count=324` 是
> **某一刻**的读数；后续每有请求进日志源它就会变（复验期间涨到 325，
> 逐页扫描还写入了一条**空模型/失败**的会话行：`ts=09:11:36`、
> `model=''`、`success=f`、`latency=2ms`）。
> ⇒ 实测 **`/api/logs` 不自我计入**：连打 3 次该端点后 `count` **不动**
> （325 → 325）⇒ 漂移来自**别的**请求进日志源，不是端点自观测。
> ⇒ **判别动作：报告 `count` 时必须带上「读取时刻」**，否则下一个读者
> 复算得到不同数字会以为口径漂了。**口径一致性的证据是
> `count == aggregate.total_requests` 与各 SUM 逐值相等**，不是 `count` 本身。

### 5.12.7 ★ 部署后复验（API 全量 + 104 页逐页），并抓到一个**量具污染**

部署 `2.5.8.2757` 后用上一轮那两套**可复现**脚本重跑（不是手工抽查）：

| 复验 | 工具 | 结果 |
|---|---|---|
| API 全量 | `scripts/` 外的 `/tmp/llmgw-retire-20261020/api-sweep.sh`（路由**从源码抽**，不靠记忆列） | **511 条路由**、**0 个 >2s**、**0 个 5xx**；状态分布 200×269 / 405×114 / 400×67 / 404×50 / 401×6 / 503×5 / ERR(3)×35 / ERR(28)×4（4 个是 SSE 常驻流，`curl -m 25` 超时属**预期**） |
| 逐页 104 页 | `/tmp/llmgw-retire-20261020/pw/sweep.mjs`（无头 Chromium，**独占**跑） | 文档状态 **101×200 + 1×503**（`/maintain`，本机未部署该服务）；**0 个 JS 运行时异常**；**0 个慢 API**；**页面加载 ≥2000ms 的页面 0 个**（最慢 `/dashboard` **1965ms**） |
| 与 2756 基线逐条对比 | 两个 TSV 按路由 join | **路由集合完全相同**（0 增 0 删）；**状态变化 4 处，全部是 `500 → 200`**（`data-lifecycle/storage/tables`、`prompt-injection/detections`、`prompt-injection/stats`、`work-types/stats`）⇒ **0 处回退**；页面标记（`LOADING`/`ZERO_ROWS`/`ERR_BANNER`/`NOT_FOUND`）集合**与基线完全一致**（0 新增、0 消失） |

**本轮改动端点的稳态耗时**（`/api/logs?page=1&page_size=20`，8 次采样）：
**26–48ms（中位 ~35ms），全部 200** ⇒ 合并聚合没有把端点变慢。

**★ 抓到的量具污染（本轮最值得记的一条）**：第一次跑逐页扫描时，我把
**API 全量扫与逐页扫并发**跑了（同���个网关）⇒ `/dashboard` 读数
**2098ms（基线）→ 2920ms**，差点被当成「本轮改动把仪表盘拖慢了 40%」。
⇒ **重跑逐页扫（这次独占）= 1965ms**，比基线**还快** ⇒ 2920ms 是
**两台压测互相踩**出来的**量具读数**，不是产品事实。
⇒ 同一份代码、同一套脚本，三次读数：基线 2756 **2098ms** / 并发 **2920ms** /
独占 **1965ms**；全站 `navMs` 中位数三次分别是 1128 / 1120 / 1122ms
（**中位数完全不动** ⇒ 只有长尾页被踩）。
⇒ **判别动作：量页面耗时前，先确认「没有别的压测在同一个被测服务上」**；
并发跑两个压测器时，**长尾页的读数必被污染**，而中位数不会暴露这件事
（所以不能只看中位数就下结论）。
⇒ 附带一条：`/live` 那一页偶发 `NOT_FOUND` 标记是**文本启发式的假阳性**
（标记是 `/404|页面不存在|Not Found/i` 对页面文本做正则，而实时拓扑页的
文本在「算标记」与「落盘文本」之间会变，动态 id 里出现过 `404` 子串）⇒
该页另两次读数标记为空、`doc=200`、0 JS 异常 ⇒ **不是缺陷**，是**量具**。

**结论**：本轮 D1/D3 两条改动在**活体**上**没有引入任何回退**；且上一轮
修掉的 4 个 500 在新版本上确认已转 200。

**本轮最后一个发现（写进判据纪律）**：`n_tup_ins` 的读数被**门自己**污染了
（13 → 连跑 3 次门 → 16），根因是 PG 计入回滚事务的插入。
⇒ 这条已写进 §5.12.2 的订正框与 §5.12.3 的证据链，**证据已换成「触发器 + 行数」**。

## 5.13 ★ 续做收口：3 张 v1 裸母表分析视图改读会话族（迁移 884）+ 一次**量具订正**

### 5.13.1 ★ 先说本轮最大的发现：**上一轮（和我自己）用了一把坏尺子**

上一轮 §4.2 的结论是「**5 张** `v_*` 视图仍读 v1 裸母表」，判定写法是：

```sql
pg_get_viewdef(v) LIKE '%_request_logs%'
```

⇒ **SQL 的 `LIKE` 里 `_` 是「单字符通配符」**（不是字面下划线）⇒ 这条判据会把
**canonical 视图名 `request_logs_with_current_month`** 也判成命中
（`%` + `_` 匹配掉 `request_logs` 前面那个空格）⇒ **假阳性**。

用**精确仪器**（`strpos` 精确子串 / 正则 + 词边界）重判全部 13 张触及
`request_logs` 族的视图，真库读数：

| 视图 | 精确：含 `_request_logs` 字面量 | 精确：含 `session_turns` | 旧 LIKE 判据 | 结论 |
|---|---|---|---|---|
| `request_logs_with_current_month` | **t** | t | t | 真读 v1（**canonical，本就该有 v1 臂**） |
| `request_logs_with_current_month_without_customer_id` | **t** | f | t | 真读 v1（**有意建成**的 v1-only 中间层） |
| `request_logs_with_current_month_without_request_class_due_at` | **t** | f | t | 同上 |
| `request_logs_bodies_with_current_month` | **t** | f | t | 同上 |
| `request_logs_bodies_progress` | **t** | f | t | 同上（v1 正文回填进度，冻结族上**恒 0 是正确读数**） |
| **`v_continuation_effectiveness`** | **t** | f | t | **真读 v1 裸母表** |
| **`v_node_switch_analysis`** | **t** | f | t | **真读 v1 裸母表** |
| **`v_timeout_effectiveness`** | **t** | f | t | **真读 v1 裸母表** |
| `routing_analytics_source` | f | f | t | 读 canonical ⇒ **已切**（假阳性） |
| `v_model_health_dashboard` | f | f | **t** | **假阳性**（只提到 canonical 名） |
| `v_probe_system_health` | f | f | **t** | **假阳性**（同上） |
| `customer_cost_view` / `model_cost_per_task_view` | f | t | f | 本轮 883 已切 |

⇒ **订正**：上一轮说「5 张」，**实为 3 张**；`v_model_health_dashboard` 与
`v_probe_system_health` 是**假阳性**（它们只提到 canonical 视图名）。
⇒ 而上一轮**自己写的 883 POST_CONDITION 用的就是这条坏判据**
⇒ 本轮一并订正（迁移 883 的内容指纹因此变化，通道按契约**自动重放**：
`content replay (ledger c005c02cb438 -> file cb0da6adfb7c)`，幂等、视图体不变）。
⇒ **本仓自己的门 `internal/sqlreadguard` 用的是正则 + 词边界
（`(?i)\b(FROM|JOIN)\s+(public\.)?_?request_logs\b`）⇒ 那个门是对的**，
坏的只有我们临时写的 ad-hoc 读数与文档里的命令。
⇒ **判别动作：判「某个名字出现了没有」时，别用 `LIKE` 配 `_`**——
`_` 是通配符；要精确就用 `strpos` / `position` / 正则 + 词边界。

### 5.13.2 这 3 张视图为什么值得收口（缺陷本体）

| # | 视图 | 原读源 | 后果 |
|---|---|---|---|
| 1 | `v_continuation_effectiveness` | `FROM _request_logs`（24h 窗） | **恒 0 行**、无错误信号 |
| 2 | `v_node_switch_analysis` | `FROM _request_logs`（24h 窗） | **恒 0 行**、无错误信号 |
| 3 | `v_timeout_effectiveness` | `FROM _request_logs`（24h 窗） | **恒 0 行**、无错误信号 |

真库读数（改读源前）：`_request_logs` **0 行**；三张视图 **0 / 0 / 0 行**。

**两条额外的理由**（都不在「读源」这一层，但正是它们让这批优先）：

1. **它们是「后期删除 `_request_logs`」的依赖阻塞项。**
   `pg_depend` 真库清点（2026-10-20）：

   | v1 表 | 被哪些对象依赖 |
   |---|---|
   | `_request_logs` | **7 个视图**：`request_logs_with_current_month`（canonical）、`..._without_customer_id`、`..._without_request_class_due_at`、`request_logs_bodies_progress`、**`v_continuation_effectiveness`**、**`v_node_switch_analysis`**、**`v_timeout_effectiveness`** |
   | `_request_logs_hot` | canonical + 2 个 `without_*` 视图 |
   | `_request_logs_bodies` | `request_logs_bodies_progress`、`request_logs_bodies_with_current_month` |
   | `_request_logs_bodies_hot` | `request_logs_bodies_with_current_month` |

   ⇒ **没有任何表 / 物化视图 / 函数**依赖 v1 族 ⇒ 阻塞项**全是视图**。
2. **这 3 张是唯一「没有任何 API/Go 代码消费」的 v1 读面**
   （全仓 grep 只命中 `internal/sqlreadguard` 的 DEBT 登记与文档）
   ⇒ 收口它们**零 API 影响**。

⇒ 而剩下 4 个阻塞项（canonical 与 3 张 v1-only 中间层）是**有意建成**的
（`cmd/gateway/request_logs_view_session_arm_pin_test.go` 把它们登记在
`v1OnlyRequestLogsViews`，并有双向门钉住形状）⇒ 真正的最后一步是
**canonical 的 v1 臂（臂3）**，那是另一件事、且必须与那 4 张一起动
⇒ 本轮**不碰**，登记为后续（§5.13.6）。

### 5.13.3 能改读**且不丢能力**（逐列查类型，不是靠记忆）

会话族的 6 个「细节语义列」在 **`session_turn_details` / `_hot`** 上**都有**
（真库 `information_schema` 逐列核过），且与 v1 **类型完全一致**
⇒ `CREATE OR REPLACE VIEW` 可直接替换（列名/列序/类型都不变）：

| 语义列 | v1 `_request_logs` | 会话族 details | 一致 |
|---|---|---|---|
| `is_continuation` | boolean | boolean | ✓ |
| `cached_response_id` | bigint | bigint | ✓ |
| `context_size_tokens` | integer | integer | ✓ |
| `node_switch_count` | integer | integer | ✓ |
| `timeout_mode` | character varying | character varying | ✓ |
| `effective_timeout_seconds` | integer | integer | ✓ |
| `error_kind` / `latency_ms` / `success` / `ts` | text / integer / boolean / timestamptz | 同（**turns 侧**） | ✓ |

⚠ **canonical 视图不能复用**：`request_logs_with_current_month` 的 118 列里
**不投影**这 6 列（实测）⇒ 只能用会话族**直连**形态
（`session_turns*` LEFT JOIN `session_turn_details*`）。
⇒ details 覆盖率真库实测 **100%**（877/877 parent、312/312 hot，无一行缺 details）。

**两条腿的选择**：`hot ∪ parent UNION ALL`，join 键与 canonical 视图的会话臂
**逐字相同**（`d.tenant_id::text = t.tenant_id::text AND d.request_id = t.request_id
AND d.partition_date = t.partition_date`）。
⇒ 前提（真库实测，也是门里的前提断言）：**hot ∩ parent 无重叠**（turns 0 行、
details 0 行）⇒ 这 3 张都是**计数型**指标，UNION ALL **不会**把同一行数两次
（这与 5 分钟窗那种指标只取 hot 单腿是同一个理由）。

### 5.13.4 改完之后：一张**活了**，两张仍空但**成因可证明**

| 视图 | 改前 | 改后 | 成因 |
|---|---|---|---|
| `v_node_switch_analysis` | **0 行** | **16 行**（16 个小时桶、314 个请求、成功率与延迟齐全） | ✅ **读源通了** |
| `v_continuation_effectiveness` | 0 行 | 0 行 | 底层 `session_turn_details` 里 `is_continuation` **0 行** ⇒ **本来就没有这种流量**（不是读源错） |
| `v_timeout_effectiveness` | 0 行 | 0 行 | 底层 `effective_timeout_seconds` 非 NULL **0 行** ⇒ **本来就没有这种流量** |

⇒ 这是 §10.6「0 的两种成因」的**第三个实例**：
**「空」从「读源错」变成了「没有这种流量」，而且这次后者是**可证明**的**
（底层列的计数直接给出了证据）⇒ 改读源把「不知道为什么空」变成了「知道为什么空」。

### 5.13.5 判据（2 道）+ 4 处变异全部转红

| 门 | 钉什么 | 变异 → 转红 |
|---|---|---|
| `admin/v_analytics_views_session_source_test.go`（**真库 + 造夹具**） | 三张视图真的取到会话族的值；5 条前提断言（v1 族为空、定义体无 v1 引用、夹具不在 parent 腿、夹具每行都有 details、两个桶里除夹具外无别的行）；**阴性侧**：窗口外（>24h）的行不得被计入 | **M1** 改回读 v1 ⇒ 前提断言红；**M2** 删掉 24h 窗口 ⇒ 阴性侧红；**M3** 断 details join ⇒ 红；**M4** 只留 hot 腿 ⇒ 红（**parent 腿夹具缺失**） |
| `sql/migrations/startup/migration_884_..._test.go`（契约门 6 道） | 迁移体读会话族 + 两条腿 + join 键 + 每张视图自己那几列（**并集**覆盖 6 列）+ 24h 窗口；down 用 **860 之后的物理名** `_request_logs`；**4 份文件正文逐字相同**；embeddata 一致；**三处登记**；**★ 第 0 条：判据的判据** | 把 883 的好写法改回坏 `LIKE` ⇒ 红；去掉任一登记 ⇒ 红 |

**★ 夹具的三个坑（本门实测踩出来的，写在这里给下一个人）**：

1. **桶会被真实流量污染** ⇒ 断言只能写成「≥」⇒ 那是弱判据。
   ⇒ 解法：夹具放到**未来**的整点（视图窗口 `ts > now()-24h` **没有上界**
   ⇒ 未来行在窗内，而真实流量不可能有未来时间戳）⇒ 该桶**独占** ⇒ 断言可精确。
2. **窗口外的桶不能写死**（本门第一版写死「now-30h」）⇒ 那个桶里**恰好有 1 行
   真实数据** ⇒「该桶为空」的前提不成立 ⇒ 阴性侧断言分不清是「窗口条件被删坏」
   还是「那个桶本来就有数据」。
   ⇒ 解法：**动态找**一个「证明为空」的窗口外小时桶（从 now-28h 往后逐小时试，
   找第一个两条腿都没行的）。
3. **只往 hot 腿插夹具 ⇒ 「漏掉 parent 腿」的变异抓不到**（夹具在 hot，
   hot 单腿照样能取到它）⇒ 必须再插一条**只在 parent 腿**的夹具
   （本门用 `switches=994` 做哨兵）。

**M4 变异为什么值得单独做**：它是三条断言里唯一能证明「两条腿都在」的；
没有它，视图被改成 hot 单腿（父表 877 行全丢）这门**照样绿**。

### 5.13.6 遗留：真正的「最后一步」是 canonical 的 v1 臂（臂3）

收口这 3 张之后，`_request_logs` 还被 **4 个视图**依赖：
`request_logs_with_current_month`（canonical 的**臂3**）、
`..._without_customer_id`、`..._without_request_class_due_at`、
`request_logs_bodies_progress`。
⇒ 那 3 张 v1-only 中间层是**有意建成**的（`request_logs_view_session_arm_pin_test.go`
双向钉住形状与代码清单）⇒ **不能单独删**。
⇒ 真正要删 v1 表时，**臂3 必须与它们一起走**（臂3 现在恒空 ⇒ 删掉它不改变读数，
但会改变 canonical 视图的形状 ⇒ 撞上好几道形状门）。
⇒ 登记为后续项（**L12**），本轮**不做**（属最终删除步骤，牵动 canonical 视图形状）。

### 5.13.7 ★ 我自己引入的一个**新装路径**缺陷，以及为此立的**基线自洽门**

**缺陷本体**：884 把 3 张视图的读源换成会话族（`session_turns_hot` / `session_turn_details*`），
而我在**同一轮**顺手把**安装器实际使用的那份基线**
（`installer/cmd/llm-gw-installer/embeddata/01-schema.sql`）**也**改了
⇒ 884 的视图体里出现了 `FROM public.session_turns_hot` / `FROM public.session_turn_details_hot`。

而这些关系**基线并不创建**：

| 关系 | 谁建 | 基线里有吗 |
|---|---|---|
| `public.session_turns`（parent） | **基线自建** | ✅ 有 |
| `public.session_turns_hot` | 启动序列 `session_turns_hot_bootstrap.sql`（TSV index 9） | ❌ **没有** |
| `public.session_turn_details` / `_hot` | 迁移 733 / 802（启动序列） | ❌ **没有** |

基线是**逐语句**应用的，而 `CREATE VIEW` **急切校验**它引用的关系 ⇒ 新装在基线里报：

```
ERROR:  relation "public.session_turns_hot" does not exist
LINE 24:            FROM public.session_turns_hot st
```

并**中止整个安装**（真库临时库实测，2026-10-20）。
而**升级路径是绿的**（那些表早就存在）⇒ **「升级路径绿」≠「新装路径绿」**。

> 取数命令（复现口径）：
> ```bash
> SCRATCH=$(echo "$DATABASE_URL" | sed 's#/[^/]*$#/scratch_fresh3#')
> psql "$DATABASE_URL" -c "CREATE DATABASE scratch_fresh3;"
> psql "$SCRATCH" -v ON_ERROR_STOP=1 -q -f installer/cmd/llm-gw-installer/embeddata/01-schema.sql
> ```
> 覆盖范围：本机 PG 18.10（127.0.0.1:54318）；**只跑基线**（不跑启动序列），
> 因为缺陷就发生在基线时点。启动序列里 `session_turns_hot` 由
> `session_turns_hot_bootstrap.sql`（TSV index 9）建 ⇒ 基线时点还不存在。

**纠正**：5 张视图（2 成本 + 3 分析）的 **4 份副本**（canonical `sql/schema/01-schema.sql`、
`deploy/sql/schemas/baseline/01-schema.sql`、installer `embeddata/01-schema.sql`、
以及 `sql/objects/views/*.sql` 的说明头）全部回到 **v1 fallback 体**：
基线自建 `public.request_logs` / `public.request_logs_hot` ⇒ 该体在基线时点**有效**；
**部署形态由迁移 883 / 884 换上** ⇒ 两条路径跑完启动序列后**形态一致**。

> 实测（2026-10-20，新装链跑完 vs 存量升级后的真库，逐字同形）：
> ```
> SELECT v.relname, strpos(pg_get_viewdef(v.oid,true),'session_turns')>0 AS sess,
>        strpos(pg_get_viewdef(v.oid,true),'_request_logs')>0 AS lit_v1
> FROM pg_class v JOIN pg_namespace n ON n.oid=v.relnamespace
> WHERE v.relkind='v' AND n.nspname='public' AND v.relname IN
>  ('customer_cost_view','model_cost_per_task_view','v_continuation_effectiveness',
>   'v_node_switch_analysis','v_timeout_effectiveness') ORDER BY v.relname;
> ```
> 两条路径 5 张视图全部 `sess=t, lit_v1=f` ⇒ 同形。

> 顺带查明：883 只改了 canonical + deploy-baseline 两份（**没改**安装器那份）
> ⇒ 883 留下 3 份副本分叉，但**没有破新装**（883 的读源 `session_turns` 是基线自建的）。

**判据**：`sql/schema/baseline_view_refs_test.go` —— **基线自洽门**（静态，常驻）：

1. 采集基线**创建**的对象（TABLE / VIEW / MATERIALIZED VIEW / SEQUENCE / TYPE /
   FUNCTION / PROCEDURE / DOMAIN / EXTENSION / FOREIGN TABLE）；
2. 逐个 `CREATE VIEW` 的定义体，抓 `FROM` / `JOIN` 后面的关系名；
3. 排除 **CTE**（`WITH x AS (…)` / `, x AS (…)`）—— CTE 不是关系；
4. 每个被引用的关系**必须**在 ① 里 ⇒ 否则报出来。

| 变异 | 结果 |
|---|---|
| 把基线里 `public.request_logs rl` 改成 `public.session_turns_hot rl` | **红**，并点名 `public.customer_cost_view → session_turns_hot` |
| 恢复 | 绿 |

⚠ **这道门自己踩的量具坑（必须写下来）**：Go 的 `regexp` 里第一版漏了 `m` 标志
⇒ `^` 只匹配**整串开头** ⇒ **扫到 0 个视图** ⇒ 门会「安静通过」
（0 个视图自然没有违规，**空集合同样满足「没有违规」**）
⇒ 门自带两条 Fatal：「采集到 0 个已创建对象」/「扫到 0 个视图」
⇒ 这是判据纪律第 3 条（「没被阻断」有两种成因，必须把「看不见」单独判掉）的又一形态，
**在门自己身上**发生了一次。

### 5.13.8 ★ 新装链本身断在 853（**既有缺陷**，与本轮无关，但挡住了「新装可跑通」这个交付物）

**现象**：把链容错跑完（每条迁移独立事务、失败不遮后面）后，新装链在
`853_credential_probe_queue_automatic.sql` 报：

```
ERROR:  relation "public.credential_probe_queue" does not exist
```

⇒ 安装器 `InitSchema` 遇错即返回 ⇒ **整个新装中止**。

**根因**：853 的**文件头自己声明**依赖「489/490_credential_probe_queue_*（队列表本体）」，
而**那两条从未登记进 `StartupFiles`** ⇒ 队列表在新装环境里根本不存在。
853 的 runner 注释本来就写着「Fresh installs hit 42703 … **once 489/490 create the queue**」
——**事实是它们没被登记**。

**既有性证明**（pristine HEAD 工作树，AGENTS.md 纪律：证「既有失败与我无关」用强证据）：

```bash
git worktree add /tmp/lgw-pristine-20261020 HEAD        # = 7c8aad856
# 用同一套集成测试、同样的临时库跑 pristine
```

⇒ pristine **同样在 853 失败** ⇒ **既有缺陷，与本轮无关**。

**修法**（3 处登记 + 1 处生成，489/490 都幂等且自包含：不引用其它关系）：

| 点位 | 动作 |
|---|---|
| `installer/cmd/llm-gw-installer/embeddata/startup/489_*.sql`、`490_*.sql` | 从 canonical **逐字节**复制（`cmp` 验过） |
| `installer/cmd/llm-gw-installer/main.go` | `go:embed` ×2 + embed map ×2 |
| `installer/internal/dbinit/runner.go` | `StartupFiles` ×2 —— 必须排在 853 **之前**（853 是 `ALTER TABLE` 那张表） |
| `sql/schema/installed_startup_migrations.tsv` | **用生成器**产出（`cd installer/internal/dbinit && go test -run TestStartupManifest -update`），序号不写死 |

**顺带修了链门自己的量具**（`startup_chain_gate_test.go`）：它的 `runInstallerPSQL`
无条件加 `--single-transaction`，而真实安装器（`dbinit.Runner.applySQL`）会把带
`dbinit:no-transaction` 标记的文件送到**非事务通道**
⇒ 718/719/807/822/845/871/877 这 7 条含 `DROP/CREATE INDEX CONCURRENTLY` 的迁移
在**这个门里**被误报成「链上新破坏」
⇒ **量具比被测对象更严 ⇒ 造假阳性**（§10.9）。
修正后这 7 条全绿 ⇒ 原 18 条「已审阅失败项」里有 **10 条早已不再失败**（只是没人跑这个门）
⇒ 按该表自带的收缩规则逐条删除 ⇒ **本表清空**
⇒ 门的含义变成最锐的一条：**「整条 fresh-install 链必须一条不落地应用成功」**。

**顺带修了 2 条过期 pin**（都是被 853 中止掩盖、853 一通就露出来的）：

| 断言 | 旧值 | 新值 | 依据（实测） |
|---|---|---|---|
| `session_turns_hot` 列数 | 104（2026-10-01 的 pin） | **106** | 821 加 `is_abandoned`、823 加 `request_status`；**新装链临时库 106**、**生产真库 106**（两者一致 ⇒ 钉的是「新装与生产同形」） |
| 「734 视图链可查」查的表 | `public.request_logs` | `public._request_logs` | 迁移 **860**（`860_rename_request_logs_family_pending_delete`，链内 index 257）把族整体改名成 `_` 待删标志形态；**部署通道也跑 860** ⇒ 生产真库同样是 renamed 代（实测 `_request_logs` 在、`request_logs` 不在） |

**结果**（真库 PG 18.x 临时库，**真实 `psql` 逐条跑**，不是静态分析）：

| 门 | 修前 | 修后 |
|---|---|---|
| `TestStartupChainGapsMatchReviewedSet` | 8/280 失败 + 18 条过期项 ⇒ **红**（且真缺陷与 7 条假破坏混在一起） | **0 of 282 fail，0 expected ⇒ 绿** |
| `TestFreshInstallerSessionTurnsHotBootstrap` | **在 853 中止**（新装根本不成立） | **PASS**（42.6s，整链跑完 + 形状断言全过） |
| 新装后 5 张视图读源 | — | 与存量真库**逐字同形**（`sess=t, lit_v1=f`） |

**未做（并说明为什么）**：489/490 **不**进部署通道
（`scripts/apply-db-revision-sequence.sh` 的 `files=()`）
——生产真库已有该表（33 列 + `automatic` 列、173 行，2026-10-20 实测）⇒ 对存量库是 no-op；
进通道只会在生产上新建一个用不到的对象。

### 5.13.9 ★ 部署后读回（`2.5.8.2758`，`VERIFY_PASS=1`）

发布产物身份：`2.5.8-1c09647e-20261020-2758`（`git_sha=1c09647e`，`build_seq=2758`）
⇒ 构建时工作区干净且 HEAD==`1c09647e`（版本号类提交不改 Go 树）。

> 取数命令（口径与覆盖范围）：
> ```bash
> # 视图读源（**精确仪器**：strpos，不用 LIKE 配 `_`）
> psql "$DATABASE_URL" -tAF'|' -c "
> SELECT v.relname, strpos(pg_get_viewdef(v.oid,true),'session_turns')>0 AS sess,
>        strpos(pg_get_viewdef(v.oid,true),'_request_logs')>0 AS lit_v1
> FROM pg_class v JOIN pg_namespace n ON n.oid=v.relnamespace
> WHERE v.relkind='v' AND n.nspname='public' AND v.relname IN
>  ('customer_cost_view','model_cost_per_task_view','v_continuation_effectiveness',
>   'v_node_switch_analysis','v_timeout_effectiveness') ORDER BY v.relname;"
> # 退役族行数：_request_logs / _request_logs_hot / _request_logs_bodies / _request_logs_bodies_hot
> # 端点：curl -H "Authorization: Bearer $ADMIN_TOKEN" "http://127.0.0.1:8782/api/logs?page=1&page_size=3"
> ```
> 覆盖范围：本机 PG 18.10（127.0.0.1:54318）+ 本机网关 8782；**只写实测到的**。

| 项 | 读数 | 判定 |
|---|---|---|
| `readyz` | 8782 = **200**（8781 无监听 = 只启了活跃槽） | ✅ |
| 5 张视图读源 | 全部 `sess=t` / `lit_v1=f` | ✅ 会话族形态 |
| `v_node_switch_analysis` | **16 行** | ✅ 与改后口径一致（改前 0 行） |
| `v_continuation_effectiveness` / `v_timeout_effectiveness` | 0 / 0 | ✅ 成因可证明（底层列确实 0 行，§5.13.4） |
| 退役族 4 张表行数 | **全 0** | ✅ 停写仍然成立 |
| `/api/logs?page=1&page_size=3` | **200 / 86ms**；`count=327`、`total_requests=327`、`by_model` **20 项** | ✅ 与 2757 同口径（324→327 的 3 条增量是**失败的探针请求**：`is_auto_request=f`、24–31 prompt tokens、0 completion，非用户流量） |
| 部署通道台账 | 883 = `cb0da6adfb7c`、884 = `5d7efd281152` | ✅ 与当前文件 sha256 一致（无需内容重放） |

**模型请求与写入测试（部署后）**：3 轮累积式对话（`ping` / `say OK.` / 中文指令）⇒
**第 1 轮 200**（`deepseek-v4-flash`，`prompt=88 completion=37`）且**落库**
（`session_turns_hot` 10:01:04 `success=t`）⇒ **部署后写侧可用**；
第 2/3 轮 **503**，失败请求**同样落库**（`status_code=503`）⇒ 失败也留痕。

**503 的归因（逐条可证，不是推测）**：`no_candidate` ⇒ 三个候选绑定**各自**不可用：

| 绑定 | 凭据 | `is_routable` | `unavailable_reason` |
|---|---|---|---|
| `deepseek-v4-flash` | 1（provider 2453 `token.sensenova.cn`） | f | `binding_unavailable`（上游 **429** → 熔断，§L9 族） |
| `z-ai/glm-5.3-flash` | 2（provider 2452） | f | `availability_cooling`（我的请求同样把它 429 进了冷却） |
| `deepseek-v4-flash-0731` | 3（provider 2454） | f | `availability_suspended` |

⇒ 且 `proxy_nodes` **0 行** ⇒ **没有第二候选** ⇒ 503 是环境的**真实状态**，
**不是仓库缺陷**（网关在该状态下如实返回 503 并把失败写进会话族）。

### 5.13.10 ★ 部署后复验（2758）：API 全量 + 104 页逐页 + 累积式多轮，并修掉**页扫标记量具的假阳性**

> 复现命令（**独占**跑，§10.7：并发会污染长尾页读数）：
> ```bash
> # API 全量（路由从源码抽）
> cd /tmp/llmgw-retire-20261020 && TOKEN="$(cat admin.token)" OUT=api_sweep_2758.tsv bash api-sweep.sh
> # 104 页逐页（无头 Chromium）
> cd /tmp/llmgw-retire-20261020/pw && node sweep.mjs
> # 累积式多轮（5 轮，每轮带全量历史）
> API_KEY=… MODELS=glm-5.2 ROUNDS=5 ROUND_DELAY=12 OUT_DIR=… bash real-model-multiround-accum.sh
> # 写回读回（**独立**读侧，与发请求的脚本互不污染）
> bash verify-model-writeback.sh <session_id> 5
> ```
> 覆盖范围：本机 PG 18.10（127.0.0.1:54318）+ 本机网关 8782 + 无头 Chromium。

| 复验 | 结果 |
|---|---|
| **API 全量** | **511 条路由、0 个 >2s、0 个 5xx**；状态分布 200×269 / 405×114 / 400×67 / 404×50 / 401×6 / 503×5 / ERR(3)×35 / ERR(28)×4（4 个 SSE 常驻流，`curl -m 25` 超时**预期**） |
| **API 与 2757 逐条 join** | 路由集合 **0 增 0 删**、**状态变化 0 处** ⇒ **0 回退** |
| **API 与 2756 基线 join** | 状态变化 **4 处，全部是 `500 → 200`**（上一轮修的 4 个端点）⇒ 0 处回退 |
| **104 页逐页** | 文档状态 **101×200 + 1×503**（`/maintain`，本机未部署该服务）；**0 个真 JS 未捕获异常**（`pageErr`）；**0 个真 5xx**（排除 `/maintain-api/*` 的预期 503 共 101 次）；**0 个慢 API（≥2000ms）**；**0 个慢页面（≥2000ms）**；`navMs` 中位 **1122**、最慢 **1965**（与 2757 独占跑的 1122/1965 **逐位相同**） |
| **累积式多轮（5 轮）** | **5/5 = 200**（`glm-5.2`，会话头 `gw_` 前缀）；`prompt_tokens` **13 → 32 → 46 → 314 → 1066 严格单调** ⇒ 多轮上下文确实累积 |
| **写回读回（独立脚本）** | **全项 PASS**：① `turn_no` 连续 **1..5**；② `prompt_tokens` 单调不减；③ 正文三列 `jsonb_typeof` **全是 `array`**（5 行 × 3 列）；④ 退役族零新增 |
| 退役族 4 张表 | 行数 **全 0**（复验期间新增流量后仍为 0） |

#### ★ 本轮又抓到一次**量具造假阳性**：页扫的 `NOT_FOUND` 标记会命中「数字里的 404」

- **现象**：2758 的页扫比 2757 多了 1 个 `NOT_FOUND` 标记（`/admin/data-lifecycle`），
  差点被当成「本轮改动把该页搞坏了」。
- **真相**：该页正文是「PostgreSQL 数据库 v17.10 **404.2 MB** 总占用」——
  **库大小恰好是 404.2 MB** ⇒ 标记正则 `/404|页面不存在|Not Found/i` 把
  **数字**当成了 404 状态码 ⇒ 纯**假阳性**（同页 `docStatus=200`、无真 5xx、无 JS 异常）。
- **同一类的另外两处**（本轮一并查清）：
  ① UUID 里的 `…**8404**…`（`/admin/sessions/gw_0114f95e-266f-4eb4-8404-…`）——
  `/404/i` 会命中**子串**；② 标记测 `text.slice(0,200)` 而落盘只存 `text.slice(0,160)`
  ⇒ 命中可能落在**没落盘**的区间里 ⇒ **事后无法复现**「为什么这一页被判成 NOT_FOUND」。
- **修法（改尺子，不是改产品）**：
  ① 404 必须有**词边界** `404`（排除 `8404` 这类前后都是词字符的子串）；
  ② **且**后面不跟小数/数字 `(?![.\d])`（排除 `404.2 MB`）；
  ③ **且**不跟单位（`MB/GB/KB/B/bytes/ms/行/条/个/次/秒`）；
  ④ 标记窗口与落盘窗口**统一为 200 字符** ⇒ 事后可确定性重算。
- **修完的读数**：带标记页面 19 → **10**，且残留的 7 个 `NOT_FOUND` **逐个核实是真态**
  （`key not found` / `client not found` / `task not found` / `request journey not found` /
  `approval not found` / `order not found` ×2 —— 路由表用的是**合成 id**，空态正确）；
  另 3 个 `ZERO_ROWS` 是**数据态**（该表本来就没数据）。
  ⇒ **2758 的标记集合是 2757 的子集** ⇒ 0 处回退。
- **判定纪律**（本条是第 2 次出现 ⇒ 晋级为规则）：
  **「用裸数字做文本启发式匹配」必须配词边界 + 单位/小数排除**；
  且**标记的判定窗口必须与落盘窗口一致**，否则事后不可复现 ⇒ 判据与证据必须同源。

#### ★ 另一个**坏尺子**：写回验证脚本在 macOS 系统 bash 3.2 下「检查没跑成却打印 PASS」

`verify-model-writeback.sh` 用了 `mapfile`（bash **4+**），而 macOS 系统 bash 是 **3.2**
⇒ 段落函数因 `mapfile: command not found` + `set -u` 的 `unbound variable` **静默失败**，
脚本却继续走到末尾打印「=== 结果：PASS ===」⇒ **一个没跑过的检查报了 PASS**。
⇒ 修法：用 `~/sdk/bash5/bin/bash` 跑（本仓已有，见 AGENTS.md「本机工具链」）；
⇒ **纪律**：`set -e` 缺席时，检查失败必须**显式置失败位**并让脚本**非零退出**，
不能让「检查没执行」与「检查通过」长得一样。

### 5.13.11 ★ 定时任务补跑：三模型累积式多轮 + 凭据状态读数（+ 又抓到 4 处**量具**缺陷）

> 触发：定时任务要求「凭据 1 的 `availability_recover_at` 到点后重跑累积式多轮」。
> 取数命令（口径与覆盖范围）：
> ```bash
> API_KEY=… MODELS="deepseek-v4-flash,deepseek-flash,glm-5.2" ROUNDS=5 ROUND_DELAY=12 \
>   OUT_DIR=… bash /tmp/llmgw-retire-20261020/real-model-multiround-accum.sh
> for sid in <三个 session_id>; do
>   ~/sdk/bash5/bin/bash /tmp/llmgw-retire-20261020/verify-model-writeback.sh "$sid" 5
> done
> # 凭据/绑定状态（读侧，独立于发请求的脚本）
> psql "$DATABASE_URL" -tAF'|' -c "SELECT id, availability_state FROM credentials ORDER BY id;"
> psql "$DATABASE_URL" -tAF'|' -c "
> SELECT raw_model_name, credential_id, is_routable, unavailable_reason
> FROM public.v_routable_credential_models
> WHERE raw_model_name IN ('deepseek-v4-flash','deepseek-flash','glm-5.2') ORDER BY 1;"
> ```
> 覆盖范围：本机 PG 18.10 + 本机网关 8782；三个模型 × 5 轮累积式对话。

**结果（15 次请求 / 3 个会话）**：

| 模型 | 轮次 | `prompt_tokens` | 落库 | 写回读回 |
|---|---|---|---|---|
| `glm-5.2` | **5/5 = 200** | **13 → 28 → 42 → 307 → 577 严格单调** | 5 行 `success=t` | **全项 PASS**（轮次连续 1..5、正文三列全 `array`、退役族零新增） |
| `deepseek-v4-flash` | 5/5 = **503** | 28→28→46→**71**→**56**（见下方口径） | 5 行 `success=f` | PASS（失败也落库，B 判据按前提 SKIP） |
| `deepseek-flash` | 5/5 = **503** | 27→28→45→**70**→**55** | 5 行 `success=f` | PASS（同上） |

> ⚠ **503 的轮次 `prompt_tokens` 为什么「变小」**：脚本只在 **200** 时把 assistant 回复并进
> history ⇒ 全失败会话里 history **不推进** ⇒ 每轮请求只带**本轮**提示词
> ⇒ `prompt_tokens` 只反映**提示词长度**（第 5 轮的中文提示词比第 4 轮短）
> ⇒ **「变小」是预期，不是「多轮退化」**。这条判据的**前提**是「至少有成功轮次」。

**凭据与绑定状态读数（2026-10-20 10:28–10:30 实测）**：

| 对象 | 读数 |
|---|---|
| `credentials` | 1 = `ready`、2 = `ready`、3 = `suspended`（`availability_recover_at` 已为空 ⇒ 08:17 的恢复点已过） |
| `deepseek-v4-flash`（凭据 1 / provider 2453） | `is_routable = f`，`unavailable_reason = **node_probe_failed**` |
| `deepseek-flash`（凭据 1 / provider 2453） | `is_routable = f`，`unavailable_reason = **node_probe_failed**` |
| `glm-5.2`（凭据 1 / provider 2453） | `is_routable = **t**` ✔ |
| `glm-5.2`（凭据 3 / provider 2454） | `is_routable = f`，`unavailable_reason = availability_suspended` |
| `proxy_nodes` | **0 行** ⇒ 没有第二候选 |

⇒ 两个 deepseek 模型的 503 **不是**凭据冷却（凭据 1 是 `ready`），而是**这两个模型绑定的
探针状态是 `node_probe_failed`** ⇒ 路由层直接判定「无可用候选」。
⇒ 这是**环境状态**，不是仓库缺陷；且**失败请求同样落库**（`success=f`、`status_code=503`）
⇒ 审计链完整。

**退役与数据终态**：退役族 4 张表行数 **全 0**（15 次请求之后仍为 0）；
会话族 `session_turns` 881 / `session_turns_hot` 344 / `session_bodies_hot` 344。

#### ★ 本轮又抓到 **4 处量具缺陷**（全在写回验证脚本里，全都不是产品问题）

| # | 现象 | 根因 | 修法 |
|---|---|---|---|
| 1 | 验证脚本在 `~/sdk/bash5/bin/bash` 下报 `p?: unbound variable` ⇒ **B 段整段不执行** | 脚本里写的是 `... -> $p（说明对话没累积）`——**变量名后紧跟全角括号**；bash 在 `set -u` 下把非 ASCII 字节并进了变量名（`p` + 0xEF…）⇒ 变量名变成 `p?` | 全文正则扫「`$var` 紧跟非 ASCII」一律改成 `${var}` |
| 2 | B 段既打印「**不适用（SKIP）**」又报「`FAIL: prompt_tokens 变小`」 | 判据**缺前提**：全失败会话里 history 不推进 ⇒ 「变小」是预期 | B 段加前提：无成功轮次 ⇒ **整个单调性循环跳过**（不是只打印一行 SKIP） |
| 3 | C 段对**失败轮次**报「`outbound_body` 的 jsonb_typeof='null`，不是 array」 | 判据**缺前提**：失败轮次没到上游 ⇒ 出站正文与响应增量**合法地是 null** | C 段只判**成功轮次**；失败轮次打印「不适用：没到上游，出站正文本就是 null」 |
| 4 | C 段报了一堆 FAIL，末尾却仍打印「**C: 正文三列全部是 JSON 数组 OK**」 | **结论行无条件打印** ⇒ 结论与失败自相矛盾 | 结论行改为「按本段失败位是否增加来选文案」 |

⇒ 与 §10.13 同一族，但这次更细一层：**「检查没跑成」「检查跑成但前提不成立」「检查跑成且结论为负」
是三件不同的事**，而量具把它们都印成了相似的东西。
⇒ **防复发检查（新增）**：判据脚本每一段都要有
「**前提**（什么时候这条判据适用）」「**本段结论**（按本段失败位选文案）」「**段级 SKIP**（前提不成立时明说）」三样。

---

## 6. ★ 真删轮：迁移 890（v1 族**最终删除**）+ **四个**致命缺陷的根修 + 退役前必须做的三件事

> 本节是本轮（2026-10-20 复核轮续做）的收口章节。前 5 节讲的是「**怎么把写入和读面切到会话族**」；
> 本节讲的是「**把切完的 v1 族真删掉**」，以及在删之前/删之后**实测抓到的四个致命缺陷**。
> 四者都不是推理出来的，是在本机真库上**复现出来的**（其中 F4 是**新装链实测**逼出来的）。

### 6.0 结论先行

| # | 结论 | 证据位置 |
|---|---|---|
| 1 | v1 族**真删**由迁移 890 一次做掉：6 个关系、4 张 v1-only 视图、canonical 视图的第三臂、**20 个函数/过程 + 2 个触发器** | §6.1、§6.3 |
| 2 | ★ **致命缺陷 F1**：上游把停写键的**代码默认**翻成 false ⇒ 三条 bg 扫描的「可比性前提」永久失效 ⇒ 在任何**未显式配置该键**的环境上**永久停摆**（门关着的理由已过期、后果却不可逆） | §6.5 |
| 3 | ★ **致命缺陷 F2**：882 的负成本索引建在**会被 890 删掉的表**上 ⇒ 索引随表消亡 ⇒ 会话族**无任何 `cost_usd` 索引** ⇒ 检查退回**全分区 Seq Scan** | §6.6 |
| 4 | ★★ **致命缺陷 F4**：890 一度**破掉新装路径** —— 两条**只在升级路径上看不见**的 v1 依赖边（`routing_analytics_source` 视图 + 两条 RESTRICTIVE `*_owner_filter` 策略）⇒ PostgreSQL 直接拒绝 `DROP TABLE` ⇒ **新装被 890 中止**；按仓库既有决策 D9（先改写策略、再删表）修好后，新装链 **0 of 284 fail** | §6.4 |
| 5 | ★ **致命缺陷 F3**：890 的 down **只复原 4/20 个函数**，且那 4 个的函数体取自**陈旧正典** ⇒ 回滚得到「表回来了、promote/TTL 函数没回来」的**半活**族 | §6.7 |
| 6 | 我自己的一条裁决被推翻：`sql/objects/` **不是**「部署后形态的 SSOT」，它是「**从不应用、但对基线承重**」的第四份表示 ⇒ 改了它会**破掉新装路径** | §6.8 |
| 7 | **部署顺序**是硬要求：本地已按「**先停旧容器、再落 schema**」执行；生产（211/252）必须「**先上新二进制（890 延后）再落 schema**」或安排维护窗口 | §6.10 |
| 8 | **211/252 若还有停写前的历史，890 会拒绝执行**（非空闸）⇒ 必须先归档/核账，否则删表即删历史（down 只能重建骨架） | §6.11 |

### 6.1 爆炸半径：890 到底删掉了什么

**删掉的对象**（全部来自 `sql/migrations/startup/890_drop_request_logs_v1_family.sql`）：

| 类别 | 数量 | 明细 |
|---|---|---|
| 关系 | 6 | `_request_logs`（分区母表）/ `_request_logs_hot` / `_request_logs_bodies` / `_request_logs_bodies_hot` / `_request_logs_archive` / `_request_logs_id_seq` |
| v1-only 视图 | 4 | `request_logs_with_current_month_without_customer_id` / `request_logs_with_current_month_without_request_class_due_at` / `request_logs_bodies_with_current_month` / `request_logs_bodies_progress` |
| canonical 视图的臂 | 1 | `request_logs_with_current_month` 的**第三臂**（v1 冻结体）——视图本身**活着**，只是从三臂变两臂 |
| 函数/过程 | 18 | 分区管理（`ensure_*` / `promote_*` / `create_next_month_partitions` / `ensure_next_month_archive_partition` / `repair_request_logs_detached_partitions`）、归档（`archive_request_logs` / `archive_request_logs_default`）、回填与清理（`backfill_request_logs_bodies` / `cleanup_stale_in_progress_requests` / `drop_old_request_logs_bodies_partitions`）、读面（`credential_most_used_model` ×2 / `get_last_successful_request` / `update_api_key_model_cost`） |
| 触发器函数 | 2 | `update_session_tenant_binding` / `update_session_summary`（实测这两个触发器**只**挂在 v1 表上；它们写的 `session_tenant_binding` / `session_summaries` 反而是**会话族**的表 ⇒ v1 停写后它们早已无输入） |
| 触发器 | 2 | `trg_session_tenant_binding`（挂 `_request_logs`）/ `trg_update_session_summary`（挂 `_request_logs_hot`）——**随表消失** |

**替换（不是删除）的两个函数**：`recent_success_rate`、`system_health_status` —— 它们的函数体从「静态读 v1」换成会话族形态（理由见 §6.3）。

**读侧**：全仓 v1 读面归零，由仓库自带的审计器证明（`internal/sqlreadguard`：v1 桶 **0 文件 / 0 处**）。
**写侧**：v1 的三个流量写点全部挂停写门（§3.1），本地 v1 族 6 个关系**实测全 0 行**。

### 6.2 ★ boot 路径的「存在性闸」：删表之后**必须**先问「对象在不在」

这一节承接上一轮（§5.13 之后）的实测，是**真删表**的第一块多米诺：
它不发生在迁移里，而发生在**网关启动**时。

**缺陷本体（真库实测，非推理）**：启动路径上三个 ensure 都用同一把尺子——**catalog 短路**，
算的是「**缺多少**」：

```sql
-- 列项：表在且列不在 ⇒ 计 1
WHERE to_regclass('public._request_logs') IS NOT NULL AND NOT EXISTS (...)
-- 索引项：按名字数
+ WHERE NOT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = want.name)
```

退役后：列项因 `to_regclass` 为 NULL 而计 **0**（"没缺列"），**索引项却计 13**
（索引随表一起消失 ⇒ "缺 13 个索引"）⇒ `missing = 13 ≠ 0` ⇒ **短路不触发**
⇒ 继续跑 `ALTER TABLE _request_logs …` ⇒ **42P01**。

**放大成事故的那一步**：`42P01` 命中 `db.IsSchemaMismatchError` ⇒ 启动快速失败
⇒ `db.Open` 返回 `(nil, err)` ⇒ `dbConn == nil` ⇒ `SetDepsOptional(true)`
⇒ **`/readyz` 仍返 200** ⇒ 部署安全网放行 ⇒ 网关「**就绪但没有数据库**」。
⇒ 一个 DDL 形状问题，最终表现成「部署成功但服务是空壳」。

**修法（三处，早退优先于短路）**：

| 位置 | 早退条件 | 落地形态 |
|---|---|---|
| `db/db.go:2613` | `_request_logs` / `_request_logs_hot` 都不在 | `slog.Info("v1 request_logs family is retired (890) — nothing to ensure; skipping the v1 schema DDL")` |
| `db/db.go:4527` | `_request_logs` 不在 | work_type 列/索引 DDL 跳过 |
| `db/maas_schema.go:75` | `_request_logs` 不在 | maas `credits_charged` DDL 跳过 |

⚠ 两条纪律：**① 「存在性」当前提的 ensure，判据必须先问「对象在不在」，再问「缺什么」**
（顺序反了就会在对象消失时短路失效）；**② 任何「按名字数」的项（索引/触发器/约束），
必须确认这些名字与被删对象同生共死**——否则删对象会让计数失真（此处：13）。

### 6.3 迁移 890（up）：三条设计决定

1. **非空闸**：v1 族任一表有行 ⇒ `RAISE EXCEPTION`。理由：v1 的行是会话族镜像建立**之前**的历史，删表即删历史，而 down **只能重建骨架**（救不回任何一行）⇒ 把「要不要丢历史」变成必须显式回答的问题，而不是迁移的副作用。
   实现上用**动态 SQL**（`FOREACH` + `to_regclass`）而不是写死表名 —— 写死表名会被投递通道的词法改写改掉，而「两种名字都试」在动态 SQL 里天然成立。
2. **命名代际：两种物理名都写**。投递通道对「改名后」的处理**不一致** —— 部署通道 `scripts/apply-db-revision-sequence.sh` 做词法改写（`request_logs` → `_request_logs`，正则 `(?<![\w$])…(?![\w$])` 防双前缀），而**安装器** `installer/internal/dbinit/runner.go` **逐字执行、不改写** ⇒ 890 把两种名字都写进删表清单 ⇒ 三条通道都正确。
3. **canonical 视图改两臂**（`session_turns_hot ∪ session_turns` + details 特征层 LEFT JOIN），投影与 710/734/813/815/816/817/824 装出来的**逐字相同** ⇒ **118 列同名同序** ⇒ 约 40 个读方**零改动**（真库实测：改前改后行数 1267 一致）。
   ⚠ **顺序硬约束**：必须**先**换 canonical 视图、**后**删两张中层包装视图（旧 canonical 的第三臂依赖它们 ⇒ 顺序反了会被依赖挡住）。

两个「静态读 v1」的函数必须换，原因不是洁癖：

- `recent_success_rate` 进的是 provider 候选查询的 `CROSS JOIN LATERAL` ⇒ **带 v1 腿就是 100% 流量失败**；而 `CREATE OR REPLACE FUNCTION` 对 SQL 函数体**不在创建时解析** ⇒ 建得成功、**首次调用才炸** ⇒ 症状与「上游全挂」同形。
- `system_health_status` 是 30s 窗口读 `_request_logs_hot` ⇒ 42P01 ⇒ 健康徽章永久 suspect + 每 30s 一条 WARN。

### 6.4 ★★ 致命缺陷 F4：890 一度**破掉新装路径** —— 两条只在升级路径上看不见的 v1 依赖边

这是本轮**最重**的一条：它不是「某个功能坏了」，而是「**新装直接装不上**」，
而且在**升级路径上完全看不出来**（升级路径是绿的）。

**发现方式**：跑仓库自带的**新装链门**（`installer/cmd/llm-gw-installer/startup_chain_gate_test.go`
的 `TestStartupChainGapsMatchReviewedSet`，逐条把整条 fresh-install 链应用到**一次性库**上）。
它报：

```
ERROR:  cannot drop table _request_logs_hot because other objects depend on it
DETAIL: policy session_turns_owner_filter on table session_turns depends on table _request_logs_hot
startup chain: 1 of 284 registered migrations fail, 0 expected
```

**根因**：两条**指向 v1 族**的依赖边，而它们**只在升级路径上不可见**：

| # | 依赖边 | 为什么升级路径看不到 | 新装路径的形态 |
|---|---|---|---|
| ① | `routing_analytics_source` 视图 | 它由**迁移 649** 建（body 直读 `_request_logs_hot`），但生产上 2026-10-13（S3 波 2 停写切换）被**应用启动期**的 `CREATE OR REPLACE VIEW` 换成了 canonical 形态（`db/db.go` 的 `routingAnalyticsMVSQL`）⇒ **生产早已不依赖 v1** | 安装器**只跑迁移、从不启动应用** ⇒ 应用的那次自愈**永远不会发生** ⇒ 它仍是 649 的 v1 直读形态 |
| ② | `session_turns_owner_filter` / `session_turns_hot_owner_filter`（**RESTRICTIVE**） | 策略表达式**内部** UNION ALL 读 `_request_logs_hot` ∪ `_request_logs` 取最早那行的 `owner_user`；**生产**上这两条策略**已经不在**（实测本地真库 0 条 `owner_filter` 策略）⇒ 生产无依赖 | 457/526 建的策略**仍在** ⇒ 依赖边仍在 |

⇒ 这两条边都由**历史迁移**建立，而历史迁移的字节**不可变**（sha 台账 + 「历史 SQL 字节不可变」）
⇒ **只能由退役迁移自己切断**。

**修法（按仓库已有的权威决策，不是临时发明）**：`docs/audit/2026-10-04-session-migration-decision-sheet.md`
的 **D9**（援引审计 §9.137 / §9.139 的真库实测）早已写明整改路径**三步、顺序不能换**：

> ① `sessions.owner_user` 已存在（§9.141.1）⇒ 不需要新增字段；
> ② **把 `*_owner_filter` 策略改写成读 `sessions.owner_user`，不再引用 `request_logs`**；
> ③ **最后才 `DROP TABLE request_logs`**。
> ⚠ 「只做 2+3 步会把 owner 过滤**直接删掉**（不是迁移，是丢失）」。

⇒ 890 补两步：

- **2b 步**：把 `routing_analytics_source` 换成**与生产逐字相同**的 canonical 形态
  （15 列、列名/类型/次序与旧体逐位一致 ⇒ `CREATE OR REPLACE` 的列契约成立）。
  生产上这一步是幂等 no-op（body 已相同）。
- **2c 步**：把两条 `*_owner_filter` 策略改写成读 `public.sessions.owner_user`
  （`sessions.session_id` 与 `session_turns.session_id` **同型 text** ⇒ 可直接等值连接）。
  **只在策略存在时改写** ⇒ 生产形态（本来就没有）是 no-op ⇒ **不凭空造出**生产从来没有的策略。

⚠ **安全语义没有被悄悄改变**（逐条核对）：改写**前** owner 来自**恒空**的 v1 族 ⇒ `EXISTS` 恒假 ⇒
**对非超管一律不可见**（fail-closed）；改写**后** `app.current_user` 在**生产里零设置点**
（审计 R38 `agent-guc-surface.md` 实测：仅测试 `set_config`）⇒ `current_setting('app.current_user', true)` 为 NULL ⇒
比较结果 NULL ⇒ `EXISTS` 仍恒假 ⇒ **仍然 fail-closed**。⇒ **今天的可见性完全不变**，
本步只把 owner 的来源从一张**即将被删的表**换成会话族自己。超管旁路原样保留。

**down 侧的对称复原**：回滚时把两条策略**还原成 v1 形态**（逐字取自真库定义），
且**只在「策略存在且其表达式不再引用 v1」时**才还原 ⇒ 生产形态仍是 no-op ⇒
既不让回滚后的库与回滚前不等价，也不凭空造策略。

**复验（全部实测）**：

| 验证 | 结果 |
|---|---|
| 新装链门（284 条逐条跑真库） | **0 of 284 fail**（修前：1 of 284 fail，890 中止整条链） |
| 真库往返：up 后 | v1 函数 0 / v1 表 0 / **仍依赖 v1 的视图 0** / **仍依赖 v1 的策略 0** |
| 真库往返：down 后 | **20/20** 函数逐字一致、2 个触发器复原、11 个 v1 关系回来 |
| 真库往返：策略复原（在新装形态的库上） | 两条策略**与 890 之前逐字一致** |
| 门 + 变异 | 新立 2 道门；9 项变异全部转红、源文件按字节还原 |

⇒ **教训的形状**：「升级路径绿」≠「新装路径绿」这一条，本轮**第三次**兑现
（前两次：5 张分析视图、2 张中层包装视图）。但这次的形态**更隐蔽**：
前两次是**基线里的视图定义**与生产不一致，而这次是**「生产靠应用自愈修好了、新装没人自愈」**
⇒ ⇒ **凡是靠应用启动期自愈维持的形态，新装路径上都不存在** ⇒
**退役类迁移必须假设「新装形态 = 历史迁移原样」**，而不是「= 生产当前形态」。

### 6.5 ★ 致命缺陷 F1：停写键的**代码默认**被翻转 ⇒ 三条扫描永久停摆

**触发源**：上游提交 `c2fae70ea`（审计 D0）把 `storage.request_logs_write_enabled` 的**代码默认**由 `true` 改成 `false`
（`settings/key_request_logs_write_enabled.go:44` = `GetPlatformBool(KeyRequestLogsWriteEnabled, false)`）。

**为什么这是致命缺陷而不是「改了默认值」**：890 之后，bg 里三条扫描的**证据源**都已随 v1 族迁到会话族（⇒ 源在**无条件前进**），但它们的**前提守卫**仍读那个停写键 —— 「证据源不再前进 ⇒ 不可判定 ⇒ 跳过」。于是：

- 在**显式**配置了 `storage.request_logs_write_enabled=false` 的环境（本地、生产）⇒ 跳过是**对的**（源确实冻结了）；
- 在**没有显式配置**该键的环境（**新装 / 克隆 / 灾备**）⇒ 代码默认 false ⇒ **永久跳过** ⇒
  「**门关着的理由已过期、后果却不可逆**」：源明明在前进，扫描却永远不跑，且**没有任何错误信号**（跳过被 `s4ScanSkipMetrics` 记成正常的 skip）。

三条被波及的扫描与本轮的处置：

| 扫描 | 原前提守卫 | 处置 | 为什么这样处置是安全的 |
|---|---|---|---|
| credential recovery 的 36h 回看 | `lookbackComparability`（读停写键） | **退役该守卫**，36h 回看**恒执行** | 该扫描有**自己独立**的启用开关 `lookbackScanIntervalLocked()`（interval=0 是禁用哨兵）⇒ 退役守卫**不会**自己产生探测流量 |
| routing health 的 `recorded_cost_is_negative` | `recordedCostComparability`（读停写键） | **退役该守卫**，检查**恒执行** | `SkipWhen` 的**机制**保留（将来某条检查的源又不再前进时仍可用）；闭集 reason 里 `s4_stop_write` 也保留（指标序列连续性，gauge 恒 0） |
| ledger reconciliation 的 `usage_credit` 比对 | `usageCreditComparability`（开关式） | 换成**数据驱动**判据 | 新增纯函数 `usageCreditComparable(usageArmRows, diffRows)`：diffRows=0 ⇒ 可判定；**usage 臂为 0 且有差异行** ⇒ 不可判定 ⇒ `markSkipped("usage_arm_empty")` 且**不产生 finding**（SQL 增列 `(SELECT count(*)::int FROM usage) AS usage_arm_rows`） |

⇒ 处置原则：**不是「保留旧行为」，而是「把每条扫描自己的开关/判据留下，把已经过期的全局前提退役」**。

**判据**：三道新门（`TestLookbackScanIsNotGatedByRetiredV1Key` / `TestUsageCreditComparabilityIsDataDriven` /
`TestUsageCreditCheckIsNotGatedByRetiredV1Key` / `TestRecordedCostCheckCarriesNoS4Guard`），
其中「Go 侧有没有读那个键」用 **`go/ast` 取字符串字面量**判定（不用正则扫全文：注释会把「退役说明」顶成读点），
并配**阳性对照**（合成检查覆盖 `SkipWhen` 机制的两条腿）。

### 6.6 ★ 致命缺陷 F2：882 的索引随表消亡 ⇒ 慢 SQL 回归

**缺陷本体**（实测，非推理）：

- 882 建的 `idx_request_logs_cost_negative` 挂在 **`public._request_logs`** 上 ⇒ 890 的 `DROP TABLE` **级联删索引** ⇒
  会话族**没有任何 `cost_usd` 索引**。
- 实测 `EXPLAIN (ANALYZE, BUFFERS)`：负成本检查对 `session_turns_2026_07/08/10/11` **各一次 Seq Scan**（本地 1ms；生产是真表 ⇒ 全表扫描）。
- **第二个成因**（容易被漏掉）：检查用的是**宽源** `db.SessionFamilyTurnsSourceSQL()` —— 那是给「日志列表」型读方准备的
  **118 列投影 + 与 `session_turn_details*` 的 LEFT JOIN**。对本检查（一个 6 列、按 2 列分组的窄聚合）它是纯浪费：
  ① 宽投影把行宽推到 ~677 字节；② **details 的 LEFT JOIN 更贵** —— 即使 turns 侧被过滤到 0 行，哈希连接仍要**扫完整个 details 面**建哈希表
  ⇒ **慢查的真正来源不是 turns 表，而是 details 表**。

**根修（两件，缺一不可）**：

1. 检查改用**窄源**：直接 `session_turns_hot ∪ session_turns`，只取本检查真正用到的 **6 列**，去掉 118 列投影与 details JOIN
   （同款窄读的先例：`domains/streaming/anomaly_harvester.go:228`）。
2. 会话族自己的 **`cost_usd < 0` 部分索引**（迁移 891）：父表级 + hot 表，键 `(ts, credential_id, raw_model_name)`、
   `INCLUDE (cost_usd, cache_read_tokens, prompt_tokens)`、`WHERE cost_usd < 0` ⇒ 从 Seq Scan 变 Index Only Scan。

**判据**：882 的门从 4 道改成 **7 道** —— 形态门/down 门保留（迁移文件自身没被改坏），cross-pin 门**反转**
（原门钉「检查侧读 `public._request_logs r`」，现在检查侧已迁会话族 ⇒ 改为**负向**断言「不再读 v1 族」+ **正向**断言「读会话族窄源、谓词仍是 `cost_usd < 0` / 30 天」），
另加两道新门：「v1 族真删 ⇒ 882 的索引**必然**随表消亡」「会话族**必须**有自己的 `cost_usd < 0` 部分索引」。
⚠ 负向断言配了**阳性对照**（判据自检：同一判据必须能对真 v1 读点报红）与**阴性对照**
（必须对 canonical 视图名 `request_logs_with_current_month` **不**报红 —— 用 `strings.Contains` 会假红）。
3 项变异（重新引入 v1 读点 / 改掉会话族源 / 把视图名塞进 v1 清单）**全部转红**，源文件按字节还原。

### 6.7 ★ 致命缺陷 F3：890 的 down 只复原 4/20 个函数 ⇒ 半活族

**这一条是在「退役前自查」里抓到的，机制上比 F1/F2 更隐蔽**：它不影响前向，只影响**回滚**。

**事实一（文件头与实际不符）**：890 的 down 文件头写着「那些函数**也**被 890 删了，所以本 down 一并把它们装回去」，
而 890 up 删的是 **20 个**函数/过程 + 2 个触发器函数，down 实际只复原了 **4 个**
（`ensure_request_logs_partition` / `ensure_request_logs_bodies_partition` / `create_next_month_partitions` / `ensure_next_month_archive_partition`）。
⇒ 回滚后 v1 族「**表回来了、promote / TTL / 归档函数没回来**」= **半活**：表在、分区不产、正文不清理。

**事实二（更严重：已复原的那 4 个函数体也是陈旧的）**：它们取自正典 `sql/objects/functions/`，
与**真库部署版**逐字比对后**实质不同** —— 真库版多了 advisory lock 串行、默认分区约束处理、legacy shell 重挂等分支
⇒ 回滚会装回一个**更弱**的分区函数。

**事实三（回滚通常是「代码也一起回滚」）**：旧版 `bg/partition_manager.go` 会调用 promote / archive / TTL 这些函数 ⇒
只恢复表不恢复函数 ⇒ 旧代码**每 tick 42P01**。

**修法**：按 **890 执行前的真库定义**逐字复原 —— 来源 `pg_get_functiondef` / `pg_get_triggerdef` / `obj_description`，
2026-10-20 于本地真库采样 ⇒ **20 个函数/过程 + 2 个触发器全部装回**，顺序与 up 的 DROP 顺序一致。
⚠ 已核对：本节所有函数体内**不含裸 `request_logs` 标识符**（只有 `_request_logs*`）⇒ 两条投递通道（runtime 词法改写 / installer 原样）装出来的库完全一致。

**真库往返实测**（临时库 = 本地库的 schema-only 克隆）：

| 阶段 | 实测 |
|---|---|
| up 前 | 快照 20 个函数定义 |
| up 后 | v1 族函数 **0** 个、v1 族表 **0** 张 |
| down 后 | **20/20 函数定义与 up 前逐字一致**（`pg_get_functiondef` 全等比较）、**2 个触发器**复原、11 个 v1 关系回来（**分区不在 down 范围**，文件头已声明） |

**判据**：三道新门 —— `TestMigration890DownRestoresEveryDroppedFunction`（up 的 DROP **集合** ⇔ down 的 CREATE 集合；
**重载按条数比**（`credential_most_used_model` 有两个重载 ⇒ 20 个对象只有 19 个名字，只比名字集合会漏掉「少复原一个重载」））、
`TestMigration890DownRestoresTheTwoTriggers`、`TestMigration890DownEmbeddataCopyIsByteIdentical`。
3 项变异（删一个函数体 / 删一个触发器 / 改一个函数名）**全部转红**，源文件按字节还原。

**有意不还原的一处**：`recent_success_rate` / `system_health_status` 是 890「**替换函数体**」而非「删除对象」，
本 down **保留会话族形态**（会话族形态不引用 v1 表 ⇒ 回滚后不 42P01；v1 空表时 v1 腿口径与三臂 canonical 视图不自洽）——
理由写在 down 文件头的 3c 条，避免下一个人把它当成漏项去「修」。

### 6.8 ★ 一条被我自己推翻的裁决：`sql/objects/` 是「第四份表示」

**我一度把 `sql/objects/` 当成「部署后形态的 SSOT」**，把 5 张分析视图改成了会话族形态
（`customer_cost_view` / `model_cost_per_task_view` / `v_continuation_effectiveness` / `v_node_switch_analysis` / `v_timeout_effectiveness`）。

**结果**：883/884 的门红了 —— 因为**新装基线**（4 份副本）引用了这 5 张视图，而新装路径上 `session_turns_hot`
由**启动序列**建、**基线不建** ⇒ 基线里的 `CREATE VIEW` 急切校验它引用的关系 ⇒ 新装报 **42P01 中止整个安装**
（2026-10-20 真库临时库实测）—— ⚠ 而**升级路径是绿的**（升级路径上启动序列先跑）。

**裁决依据**（仓库自己写的）：`scripts/audit/baseline-reconcile.py` 明写 `sql/objects/` 是
「**第四份表示、从不应用**（唯一引用是 `deploy/sql/sync-objects.sh` 的单向拷贝）、但**对基线承重**（部分基线对象的唯一出处）」；
`sql/schema/objects_registry_test.go` 同款结论。

⇒ **回退**这 5 张视图到 v1 fallback 体（4 份基线副本全回 v1 体），**部署形态交给迁移 883/884 换上**
（两条路径跑完启动序列后形态一致），并立**基线自洽门**（`sql/schema/baseline_view_refs_test.go`：「升级路径绿」≠「新装路径绿」）。
⇒ **保留** `sql/objects/views/request_logs_with_current_month.sql` 的会话两臂体
（`db/request_logs_view_dump_generation_test.go` 要求它与 Go composer 同代，且与 883/884 的 5 张**不重叠**；3 份基线副本保持 v1 体，890 在两条路径上换上）。
⇒ `internal/sqlreadguard` 白名单从 1 条增到 **6 条 LEGIT**（5 条「基线 fallback 溯源体」+ 1 条散文误报），
并改写了那段**已经写错的**注释（原文说「5 张分析视图已改会话族」）⇒ `debtBaseline` 仍为空（零债棘轮保持）。

### 6.9 本轮的门禁与变异验证清单

| 门 | 守什么 | 变异验证 |
|---|---|---|
| `TestMigration882_IndexShapeIsStable` | 882 迁移文件自身没被改坏（索引虽已消亡，文件仍会跑） | — |
| `TestMigration882_DownDropsSameIndex` | down 删同名索引 | — |
| `TestMigration882_IndexDiesWithTheV1Family` | 890 真的删了 882 索引挂着的那张表 | — |
| `TestMigration882_CheckSideMovedToSessionFamily` | 检查侧**不再读 v1 族**（负向）+ 读会话族窄源与原谓词（正向） | ✅ 3 项全红 |
| `TestMigration882_DetectorSelfCheck` | **判据自身**：必须能对真 v1 读点报红、且对 canonical 视图名不报红 | ✅ |
| `TestMigration882_NegativeCostIndexNowOnSessionFamily` | 会话族**必须**有自己的 `cost_usd < 0` 部分索引（按内容发现，不写死文件名） | ⏳ 待 891 落地后补 |
| `TestMigration890DownRestoresEveryDroppedFunction` | up 的 DROP 集合 ⇔ down 的 CREATE 集合（条数 + 名字集合） | ✅ 3 项全红 |
| `TestMigration890DownRestoresTheTwoTriggers` | 两个 v1 触发器复原 | ✅ |
| `TestMigration890DownEmbeddataCopyIsByteIdentical` | down 的两份副本逐字节相同 | ✅ |
| bg 的 4 道 F1 门 | 三条扫描不再被退役键门控；`usage_credit` 判据数据驱动 | ✅（worker 报告 9 项变异全红） |
| `internal/sqlreadguard` | v1 读面归零 + 6 条 LEGIT 溯源体 | ✅ |
| `internal/rowsguard` | 3 条豁免键改键（同一循环体漂移）+ 1 条删除（循环已退役） | ✅ |

### 6.10 ★ 部署顺序：先上新二进制，还是先落 schema？

**窗口风险**：旧二进制跑在新 schema 上。

- 缓解一半：890 把 `recent_success_rate` / `system_health_status` 的**函数体**换掉了 ⇒ 旧二进制调这两个**函数**拿到的是新体（不 42P01）。
- 但**直接读 v1 关系**的路径会 42P01。**实测证据**：当前本地在跑的容器 `llm-gateway-local-8782`
  （`2.5.8-70a30467-20261020-2764`，git_sha `70a30467`，**来自另一份 checkout `llm-gateway-go-3`**）的源码里
  `admin/analytics.go:821 FROM _request_logs_hot`、`admin/analytics.go:827 FROM _request_logs`、
  `admin/auto_route_outcome_freshness.go` 等**生产文件**直接读 v1 关系 ⇒ 890 之后这些端点会 42P01。

⇒ **本地**：**先停旧容器，再落 schema**（本轮已按此执行；停服窗口 = 停机 + 部署时长）。
⇒ **生产（211/252）**：**先上新二进制（890 延后），再落 schema**；若要一次做完，安排维护窗口。
   ⚠ `deploy-local.sh` 的默认顺序是「**先迁移后上新二进制**」⇒ 直接跑会在迁移窗口里让旧容器带着 v1 腿撞新 schema。
⚠ 另一条协调事实：本地部署根 `~/kaixuan/llm-gateway-go-v2` 是**多会话共享**的，而当前在跑的容器来自**另一份 checkout**
⇒ 本地部署前要确认没有别的会话正在部署（否则会互抢部署根与端口）。

### 6.11 ★ 211/252 的归档与核账要求（非空闸会拒绝执行）

890 的非空闸在 v1 族任一表有行时 `RAISE EXCEPTION` ⇒ **211/252 若还有 2026-10-16 停写前的历史，890 会拒绝执行**。
这不是保护伞式的「拦一下」，而是**必须显式回答「这些历史要不要丢」**：

1. **逐表计数**（6 个关系，含两种物理名）：
   `SELECT 'public.'||relname, n_live_tup FROM pg_stat_user_tables WHERE relname LIKE '%request_logs%'`
   ⇒ 再用 `count(*)` 复核（`n_live_tup` 是估计值）。
2. **最后写入时间**：确认 v1 族的最后写入时间 **早于** 会话族镜像建立的起点 ⇒ 镜像链之前的行就是「历史」。
3. **归档**（若确认要留）：`pg_dump --data-only --table='public._request_logs*'`，**落盘后再跑 890**；
   ⚠ down 只能重建**骨架**，救不回任何一行 ⇒ 归档是唯一的救法。
4. **核账**：把 v1 族的行数/金额与会话族对应区间对齐（口径差异要写进 211/252 的核账记录，不要静默丢弃）。

### 6.12 遗留与风险登记（本轮**没有**解决的，诚实列出）

| # | 遗留 | 证据 | 归属 |
|---|---|---|---|
| 1 | **质量标记真实丢失**：`session_turn_details` 有 `quality_flags` / `quality_fix_actions` / `quality_score` 列，镜像代码也映射了，但真库 **904 行全部 NULL** ⇒ 上游算出的质量标记**目前没落库**（lite/SQLite sink 仍落） | 真库实测 | 需决定：接线落库，或**明确登记为口径损失** |
| 2 | **v1 的 7 天正文 TTL 没有等价物**：841 的 `llm_gateway_partition_retention` 出厂为空且全仓无 INSERT ⇒ 会话族月分区**只增不减** | 全仓检索 | 待办（与本轮退役无直接因果） |
| 3 | 两个 settings 键已成**死键**：`lifecycle.request_logs_ttl_days` / `lifecycle.request_logs_bodies_ttl_days` | 检索 | 清理或登记 |
| 4 | `resolvePromoteConfig` 的 `case "_request_logs_bodies":` 与 `bg/metrics.go` 的 `hotTableTSColumn` 里 `_request_logs*` label 现在**不可达**（刻意保留） | 代码审读 | 低危 |
| 5 | `updateRequestLog` **丢弃 `CommandTag`** ⇒ 0 行 UPDATE 是**静默 no-op**（无错、无日志、无指标） | 代码审读 | 与本轮无直接因果 |
| 6 | `quality_flags` 无 PG 写目标 + `requestClassArg`（`client.go:2749`）无生产调用方 | 代码审读 | 同上 |
| 7 | canonical 视图**第三臂消失** ⇒ **镜像链建立之前的历史**不再由该视图提供（v1 族已删，视图无从引用） | 设计必然 | 已被非空闸的前置归档要求覆盖 |
| 8 | `admin/` 包的「v1 bodies 读方为 0 ⇒ 量具坏了」类门：主体退役后这类判据需要换题（正在处理） | 本轮实测 | 处理中 |

---

### 6.13 ★ 致命缺陷 F5：`deploy-local.sh` 的共享目录判定把**软链**误判成「未绑定」⇒ 部署整体中止

**症状**：本地部署在 `migrate_existing_pg_to_shared()` 这一腿失败，
`docker run` 报 `Bind for 127.0.0.1:5432 failed: port is already allocated`，
脚本「已恢复原容器」后退出，**网关根本没起来**。

**根因（三层）**：

1. **宿主机实况**：`llm-gateway-pg` 的 bind source 是
   `~/kaixuan/postgres-citus`（citus+pgvector 镜像 `kx-citus-pg17:13.3.0-vector-arm64`），
   而脚本约定的共享目录是 `~/kaixuan/postgres` ⇒ **字符串不相等**。
2. **判定缺陷**：那条腿用**字符串**（仅压缩斜杠）比较两条路径 ⇒ 判定「未绑定共享目录」
   ⇒ 触发**几百 MB 级数据搬迁 + 容器重建**。
3. **固定端口**（**本轮改动前的代码**）：重建用的 `docker run` **硬编码** `-p 127.0.0.1:5432:5432`
   （同仓的 compose 模板却是 `${LLM_GATEWAY_PG_PORT:-5432}`）⇒ 本机 5432 已被别的实例
   （`acc-pg17`）占用 ⇒ **重建必失败** ⇒ 整条部署中止。
   ⇒ 即便判定正确，这台机器上这条腿也**跑不通**。

**修法（最小改动 + 可测）**：

- 新增 `scripts/deploy-local-lib.sh` 的 `dl_physical_path()` / `dl_same_physical_dir()`：
  判定改用**物理路径**（`cd … && pwd -P`）⇒ **同一份数据**就跳过搬迁；
  目录不存在时回退到旧的斜杠归一化（不静默返回空串）。
- `scripts/deploy-local.sh` 的那条腿改调 `dl_same_physical_dir "$DL_PG_SOURCE" "$shared"`。
- 宿主机侧把共享目录做成软链：`~/kaixuan/postgres -> postgres-citus`
  ⇒ 物理路径相同 ⇒ 腿直接跳过，**活数据原地不动、容器不重建、零停机**。
- 契约门（`tests/deploy_local_contract_test.sh`）：软链与目标判为同一目录、不同目录判为不同、
  空输入判为不同、不存在路径回退非空、且脚本**必须**走 `dl_same_physical_dir`
  （并禁止再出现旧的字符串归一化写法）。
- 变异验证 2 项全红：M1 把判定退回纯字符串比较 ⇒ 「must use dl_same_physical_dir」红；
  M2 去掉 `pwd -P` ⇒ 「dl_physical_path must resolve symlinks」红。两次均按 sha256 还原。

★ **订正（合并上游后）**：上面第 3 层「重建**固定占用 5432**」描述的是**本轮改动前**
的代码（`-p "127.0.0.1:5432:5432"` 硬编码）。合并进来的上游提交**已经把它改掉了**：
重建现在用 `dl_pg_publish_spec "$pg_bind_ip" "$pg_host_port"` ⇒ **沿用旧容器已发布的
host 端口**（本机即 54318），并支持 `LLM_GATEWAY_PG_PORT_BIND` 覆盖
（`scripts/deploy-local-lib.sh:208-217`）⇒ **「端口被占 ⇒ 重建必失败」这一层在合并后
已不存在**。本轮那次失败发生在**合并前**的代码上；两条修法（物理路径判定 + 沿用端口）
合起来才是完整解。

### 6.14 ★ 本地部署与全量验收（实测数据，2026-10-20）

**部署**：**两次**，两次均 `VERIFY_PASS=1`、端口 8782、`VERIFY_DATABASE=external`：

| # | release | git_sha | 触发原因 |
|---|---|---|---|
| ① | `2.5.8-8531596a-20261020-2766` | `8531596a` | 本轮改动 + 当时尚未提交的部署脚本修复（§6.13） |
| ② | **`2.5.8-20c814d7-20261020-2768`** | `20c814d7` | **合并上游并改号后重部署**（`20c814d7` = 已推送的合并提交） |

⇒ **§6.14 以下的全部验收数字都是第二次部署（合并后）实测的**，
且与合并前**逐项一致**：写链路（3/21 成功、正文 JSON array）、
533 端点扫（**500 = 0**，p50 1.3ms / p95 17.9ms）、105 页扫（**无 >2s**，max 1519ms）。
顺序按 §6.10：**先停旧容器 → 落 890/891 → 上新二进制 → 起网关**。

**落库事实**（真库 `llm_gateway` @ 127.0.0.1:54318）：

| 项 | 结果 |
|---|---|
| v1 族关系数 | **0**（`_request_logs*` 全删；`recent_success_rate` / `system_health_status` 仍在——它们是**被替换**不是被删，按 §6.7 3c 有意不还原 v1 腿） |
| 会话族 `cost_usd < 0` 部分索引 | **2** 条（父表 + hot 表，即迁移 891） |
| 新装链 | **0 of 284 fail**（`TestStartupChainGapsMatchReviewedSet`，真库逐条跑） |

**模型多轮请求**（`ping` / `say OK.`，7 个模型 × 3 轮 = 21 次）：

- **只有 `deepseek-v4-flash` 本机有活凭据** ⇒ 3/3 返回 200（`pong` / `OK.` / `OK.`）；
  其余模型 503 `no_candidate` 或 400 `invalid_model` ⇒ **环境既有状态，不是本轮回归**
  （部署前的 `gateway-8782.log` 里已有 52 条 `no_candidate`；凭据池只有 3 条、全在 deepseek）。
- **写链路完好**：21 次请求 → `session_turns_hot` 22 行（含 1 条后台流量），
  状态码逐条正确（3×200 / 16×503 / 3×400），200 行的 `is_final_success=true`；
  正文落在 `session_bodies_hot.outbound_body`，**是 JSON array**（不是 null），
  且长度随轮次增长（37 → 119 → 200 字符）⇒ 多轮上下文确实累积。
  ⚠ 口径澄清：`session_turns.request_delta/response_delta` 是 **null**，但**部署前的历史 200 行
  （08:02~08:27）同样是 null** ⇒ 不是回归；本形态的正文存在 `session_bodies*` 族。

**逐 API 扫描**（533 条 GET 路由，口径同 2026-10-16 验收轮）：

| 状态 | 数量 | 判定 |
|---|---|---|
| 200 | 281 | ✅ |
| 405 | 122 | 扫描口径（GET 打只读 POST 路由；方法前缀由 Go 1.22 ServeMux 在**进 handler 前**拦截 ⇒ 零副作用） |
| 400 | 68 | 缺/错参数 |
| 404 | 50 | **前缀分发器按设计要子路径**（如 `/api/admin/model-integrity` 要 `summary`/`events`/`fingerprint-drift`）+ handler 级 `not found` |
| 503 | 7 | 子系统未启用（identity-pool / mobile-node / lite / memora）+ 运维平台未配 |
| 401 | 5 | OpenAI 兼容腿（`/v1/models` 等）要 **API key**，本扫用的是 admin JWT |
| **500** | **0** | ★ 上一轮验收轮的 **12 个 500 全部清零** |

延迟：p50 **1.4ms** / p95 **14.4ms**。
⚠ 扫出两条「>2s」：`/api/free-pool/stream`（89.7s）、`/api/admin/system-monitor/stream`（51.1s）。
**它们不是慢查询**——用 `curl -w time_starttransfer` 实测 **TTFB = 1.6ms / 1.8ms**，
量到的是 **SSE 流的持续时长**（客户端等流结束），不是响应延迟。
⇒ 口径教训见 §12.2。

**逐页浏览器验证**（headless Chromium + **真实 UI 登录** + 真实后端，105 个页面）：

| 指标 | 值 |
|---|---|
| 页面数 | **105**（`web/src/router.ts` 的 107 条 `path:` 去掉 2 条 catch-all） |
| 页面 HTTP 非 200 | **1**：`/maintain` = 503「运维平台不可用」（按设计，本机没配运维平台） |
| 耗时 | p50 **928ms** / p95 **1256ms** / max **1640ms** ⇒ **无页面 >2s** |
| 页面内业务 5xx | **0**；仅有 `/maintain-api/*`（运维平台未配）与 `/api/free-discovery/*`（子系统未启用）的 503 |
| 页面内 4xx | 全是**扫描器用错 ID** 造成的：用真实 ID 复测即 200（`/api/keys/10` 200、`/api/admin/sessions/<真实 session_id>/turns` 200、`/api/admin/session-analytics/clients/grok-4.6` 200） |

⇒ **上一轮验收文档明确留下的缺口（「每一个页面这一条尚未满足」）在本轮关闭**。

⚠ **「Maestro」的口径说明**：仓库内没有 Maestro 脚手架；本轮与历史轮次用
**headless Chromium 逐页扫 + 逐 API 扫**等价覆盖（真实浏览器 + 真实登录 + 真实后端），
差异只在录制/断言框架，不在覆盖面。

### 6.15 ★ 211/252 的执行单（SQL 变更记录 + 顺序 + 归档 + 回滚）

**本轮新增的 SQL（两个文件，两套）**：

| 编号 | 文件 | 作用 | 幂等/可逆 |
|---|---|---|---|
| 890 | `sql/migrations/startup/890_drop_request_logs_v1_family.sql` | **真删** v1 族：6 个关系 + 4 张 v1-only 视图 + 20 个函数/过程 + 2 个触发器；canonical 视图改两臂；`routing_analytics_source` 换 canonical 形态；两条 owner_filter 改读 `sessions.owner_user` | 一次性；非空闸会拒绝在有数据的库上执行；`.down.sql` 可逐字复原 20 函数 + 2 触发器 + 11 个关系（**救不回任何一行数据**） |
| 891 | `sql/migrations/startup/891_session_turns_cost_negative_partial_idx.sql` | 会话族 `cost_usd < 0` 部分索引（父表 + hot 表），键 `(ts, credential_id, raw_model_name)` INCLUDE `(cost_usd, cache_read_tokens, prompt_tokens)` | `CREATE INDEX IF NOT EXISTS`；`.down.sql` 可删 |

★ **编号更正（合并上游时改号）**：这两条迁移最初编为 **885 / 886**；
合并 `origin/main`（59 个提交）时发现**上游已占用 885~889**
（`885_session_turns_host_tags` / `886_session_bodies_storage_comment` /
`887_session_turns_search_text_fts` / `888_session_family_join_index_gaps` /
`889_session_turns_tools_population`），且**已随 154 的 2765 部署**
⇒ 上游的编号**不可改** ⇒ 本分支的这两条**改号为 890 / 891**。
改动面：canonical + embeddata 四个文件、两个契约门、installer's go:embed + map +
`StartupFiles` + TSV 清单、部署通道 `files=()`、changelog 登记行（含 sha 重算）、
本文与 LESSONS 的散文编号。**内容只改了编号**（文件头/注释里的编号与错误信息文案）。
⚠ 本地那条部署记录里的「885/886」是**改号前**的文件名（内容相同）；本地库上
「v1 族已删」这个**事实**不受改号影响。
⚠ 改号后的**幂等性已实测（两条路径）**：① 在「v1 族已删」的库克隆上重跑 890 → EXIT=0；② **第二次本地部署时，通道在活库上真的重跑了 890 与 891**（`run/db-revision-sequence.log`：`applying …/890_drop_request_logs_v1_family.sql`、`applying …/891_…sql` ⇒ 随后 `database revision sequence completed successfully`）⇒ 改号**没有**在真实投递路径上炸掉。
（非空闸用 `to_regclass` 判存在 ⇒ 缺表即跳过；canonical/函数/policy 全部
`CREATE OR REPLACE` 或「仅在存在时改写」）；891 连跑两次 → 两条索引仍在、EXIT=0。

**登记的六处**（缺一处就装不上）：canonical `sql/migrations/startup/`、
`installer/cmd/llm-gw-installer/embeddata/startup/` 两份副本、
`installer/cmd/llm-gateway-go-installer/main.go`（go:embed + embeddedSQLFiles map）、`installer/internal/dbinit/runner.go`（StartupFiles）、
`sql/schema/installed_startup_migrations.tsv`（用 `cd installer && go test ./internal/dbinit/ -run TestStartupManifest -update` 生成）、
`scripts/apply-db-revision-sequence.sh` 的 `files=()`（891 排在 890 之后、序列末尾）。

**执行顺序（生产，与本地相反）**：

1. **先上新二进制**（含 890/891 的二进制必须先就位），
   或安排**维护窗口**（本地是「先停旧容器」，生产是「先上新二进制」——见 §6.10）。
2. 跑 `scripts/apply-db-revision-sequence.sh`（含 890 → 891）。
   ⚠ 通道里 890 之前的历史文件对 v1 族做**词法改名适配**（`_request_logs`）——
   库处于「已改名」形态时适配才生效；**混态（`ambiguous`/`mixed`）会直接报错停下**，需人工处理。
3. 890 的**非空闸**：v1 族任一表有行 ⇒ `RAISE EXCEPTION` ⇒ **先按 §6.11 完成归档与核账**。

**回滚**：`.down.sql` 逐字复原 20 函数 + 2 触发器 + 11 个 v1 关系（分区不在 down 范围）；
但**落进会话族的数据不会回流到 v1 族** ⇒ 回滚只是「让旧二进制还能跑」，
不是「把数据搬回去」。

---


## 7. 本轮新增的经验

### 7.1 「执行快」不等于「语句快」：EXPLAIN 的 Execution Time 会骗人

`EXPLAIN ANALYZE` 报 `Execution Time: 0.03ms` 而 `Planning Time: 1195ms`。
只看执行时间 ⇒ 一切正常。
⇒ **判别动作：看慢 SQL 时，`Planning Time` 与 `Execution Time` 必须一起读。**
本轮的全部三个症状（写超时、正文不落、trace 丢）都指向规划，而规划慢的根因是
**共享缓冲太小 ⇒ catalog 页进不去缓存**，跟 SQL 本身无关。

### 7.2 「修好了」的注释不等于「修生效了」：查解析顺序

`hook.go` 的注释写着「raise the default budget to 2000ms」，代码也真的改了常量。
但 `GetPlatformInt` 的解析顺序让 `spec.Default`（500）**先命中**，代码里的
fallback 根本走不到。
⇒ 判别动作：**改「某个值的来源」时，先把它的解析顺序列出来**（DB → env →
spec.Default → 代码 fallback），再确认自己改的那一层**真的在链上**。
⇒ 通用式：**注释与代码一致 ≠ 生效**；生效的是「链上第一个命中的那一层」。

### 7.3 「给旁路单独开一个 ctx」不等于「预算分开了」

第一版修法（维度写用自己的 ctx）**被自己的判据当场抓住**：轮次写拿到的剩余预算是
−0.8ms。因为两个 `WithTimeout(bg, timeout)` 的 deadline 是**同一个绝对时刻**。
⇒ 判别动作：**要让两段各用各的预算，后一段的 deadline 必须从「后一段开始」起算**，
而不是「再开一个同长的 ctx」。
⇒ 通用式：**预算分离 = 计时起点分离**，不只是「换个 ctx 对象」。

### 7.4 部署验证通过 ≠ 数据可用

`VERIFY_PASS=1` 探的是进程/端口/版本。本轮实测：部署全绿、探活全绿、迁移全绿，
但**一行请求数据都没落库**。
⇒ 判别动作：任何「数据可用」的结论，必须有一份**读回**的证据
（`jsonb_typeof` 读正文、读 `turn_no` 连续性、读 `trace_events` 非空），
不能拿「接口 200」当落库证据。

### 7.5 端口会骗人：先问「这个端口是谁的」

迁移脚本的 docker 通道里出现 `postgresql://…@127.0.0.1:5432/…`，而本机 5432
恰好是**另一个项目**（`acc-pg17`）映射出来的。差点误判成「迁移打到别的库去了」。
实际无害——DSN 在**容器内部**解析，127.0.0.1:5432 就是 `llm-gateway-pg` 自己。
⇒ 判别动作：看到端口先问「这个端口是谁的」，再问「这个连接在哪一侧解析」。

### 7.6 「同一张表有两套形状」时，先问「哪一份是正典」

`prompt_injection_detections` 同时存在两套互不兼容的形状声明：
`sql/migrations/startup/315_*.sql` 声明一套（`detection_score` + `matched_rules` …），
`sql/schema/01-schema.sql` 与 `sql/objects/tables/*.sql` 声明另一套（`risk_level integer` …）。

**正典是后者**（基线 schema + 正典对象文件），不是迁移文件。
致命的一步是 315 用的 `CREATE TABLE IF NOT EXISTS`：**在已有正典表的库上是空操作**
⇒ 315 声明的那几列**从未被创建** ⇒ 查它们的代码恒 42703。
⇒ 判别动作：**查列/表时，「正典形状」取自 `sql/schema/01-schema.sql` 与
`sql/objects/tables/`，不取自迁移文件**；迁移文件可能是**从未生效**的声明
（`IF NOT EXISTS` 让它静默变成空操作）。
⇒ 推论：看到 `42703 column ... does not exist` 时，先去正典里核这一列**在不在**，
再去核「是哪条迁移声明的」——后者很可能就是**没生效**的那条。

### 7.7 形状错配会同时毒化读侧与**写侧**，而写侧常被降级成 WARN

同一张表的形状错配让读侧 500（可见），也让**写侧**每次 INSERT 都失败
（`domains/promptinjection/detector.go`）。但写侧失败只落一行 `slog.Warn`
⇒ **检测照常生效/拦截，只是审计记录一条不留**
⇒ 页面/报表上「这批请求没有注入命中」成了**假结论**。
⇒ 判别动作：**凡是「写不进去只记 WARN」的写点，优先怀疑「列/类型对不上」而不是「偶发失败」**；
把这类 WARN 升级为 Error（本轮已改），让「审计链断掉」在告警层可见。

### 7.8 真库门能抓到 mock 抓不到的东西

本轮 6 道新判据里，有 **4 个是我自己的错误**被真库门当场抓住：
`AS c` 把列别名写成了表别名；`FROM` 跑到了子查询**外面**；
`categories` 绑 JSON 字节（PG 数组字面量 22P02）；判据自己把数组扫进 `*string`。
**这四条在 mock 下全绿**（pgxmock 对参数类型不敏感，只认「有一条 INSERT 发生过」）。
⇒ 判别动作：**凡是 SQL 的形状本身是缺陷（列名、数组线格式、类型转换），
判据必须让 PG 自己解析**——把生产用的那条 SQL 提成包级常量，让判据**逐字**跑它，
而不是让判据自己抄一份（抄的那份会与生产漂移，且漂移了没人知道）。

### 7.9 「端点坏」与「端点被取消」要用两条证据分开

同一端点 500 有两种完全不同的成因：**服务端有缺陷** vs **客户端跑了**。
本轮的 `/api/logs`、`/api/admin/turns/sessions` 都被误判过一次。
⇒ 判别动作（两条一起看，缺一不可）：
  1. **同一请求逐字 curl 重放** ⇒ 200 ⇒ 服务端没问题（是客户端取消）；
  2. **服务端日志里 `context canceled`** ⇒ Go 的 `http.Server` 在客户端断开时
     取消 request ctx ⇒ 佐证。
⇒ 补充：**同端点同标签页时好时坏**（本轮 500/200/500）本身就是争抢的指纹 ——
真缺陷是**稳定**的。

### 7.10 工具的连接池上限会伪造出「数据恒为空」

内嵌 Browser 每标签页常驻一条 SSE，而 Chromium 对同源 HTTP/1.1 只有 **6 条**连接
（整个浏览器共享）⇒ 标签页一多，新请求被**客户端放弃** ⇒ 服务端 `context canceled`
⇒ 页面「完整、无报错、数据恒为空」。本轮一开始把它当成应用缺陷查了很久。
⇒ 判别动作：**做页面级验收前先问「这个工具每标签页占几条连接」**；
不满足时用**单页顺序访问的无头脚本**（本轮用的 playwright-core + Chrome for Testing），
它天然没有多标签页争抢。

---

## 8. 量具纪律（本轮复用的四条）

1. **正文是否落库用 `jsonb_typeof(col)='array'` 判**，不用 `IS NOT NULL`
   （JSON `null` 也满足 `IS NOT NULL` ⇒ 假绿）。
2. **正文写入目标是 `session_bodies_hot`**（独立表，`pg_inherits` 继承行数 = 0），
   `count(*) FROM session_bodies`（父表）**不含**它。晋级有 8h 延迟，
   父表最新时间戳停在几小时前**本身不构成停写证据**。
3. **会话头必须 `gw_` / `gt_` / `gs_` 前缀**，否则被 `sanitizeGwSessionHeader`
   静默丢弃 ⇒「多轮」退化成 N 次独立单轮而不报任何错。判别：`prompt_tokens`
   随轮次单调增长（实测 glm-5.2：13 → 15 → 19）。
4. **「同一个数字连续报三次」时先写探针**。本轮 `turn_no` 与时间序错位，
   靠逐轮 `turn_no/prompt_tokens/ts` 三列并排才定位到是**重放顺序**而非写顺序。

---

## 9. §5.11 之后新增的经验（本轮最后一段）

### 9.1 ★ 量具坏了的现象与服务端坏了**同形**

BSD `sed` 不认 LHS 的 `\n` ⇒ 标记没剥掉 ⇒ `json.loads` 失败 ⇒ **history 被悄悄
置空** ⇒ 下一轮请求体变成空串 ⇒ 服务端回 `400 sanitization_failed: Invalid
request body`。服务端日志写的是「请求体非法」，**看起来 100% 像产品缺陷**，
实际根因在量具（macOS 默认 `sed` 是 BSD 版，`\n` 只在 GNU sed 里是换行）。
⇒ **判别动作：看到「服务端说我的请求体非法」，先看落盘的 payload 对不对**——
本例落盘文件里还带着 `__HTTP__200__T1.5` 标记，一眼就能看出。
⇒ 配套纪律：量具的中间变量更新失败时**必须显式中止**，
不能让失败值（空串）继续流进下一轮 —— 「静默清空」比「直接报错」贵得多。

### 9.2 ★ 探活一次只碰**一个**模型

一次探 3 个模型，其中第 2 个撞上上游 429 ⇒ 凭据被冷却 3 分钟 ⇒ 第 3 个模型
（以及第 1 个）全部变成 `no_candidate` ⇒ 读起来像「这个族一个都没了」，
实际是**一个模型的问题被放大成了整个凭据**。
⇒ **判别动作：探活失败时先查 `credentials.availability_state`（凭据级），
再查 `credential_model_bindings.available`（模型级）**；两者的原因与修法完全不同
（见 §5.11.4 与 L9）。

### 9.3 ★ 「目录」不等于「可路由清单」

`/v1/models` 的返回**随可用凭据变化**（本机实测 89 → 77，正好等于另一个凭据的
provider 的模型数）。所以「模型不在目录里」这句话**既不成立也不准确**：
它可能只是**那个凭据当时不可用**。真正的归属要去
`provider_models` → `credentials` → `credential_model_bindings` → 探针/配额状态
逐层查（§5.11.6 的表就是这么列出来的）。
⇒ **推论：写「某模型不存在」之前，先确认目录是**谁**的目录。**

### 9.4 ★ 熔断的连坐范围要与写侧粒度对齐着看

同一个失败事件里，**两条粒度不同的路径同时在写**：
写侧 `WriteOnError` 对 `KindConcurrent` 走 `writeModelLevelFailureOnly`
（只冷自己那一个 (凭据, 模型) 绑定，`writer.go:518` 明写「✅ DO NOT update
credentials.availability_state」）；
熔断侧 `nodehealth` 的 `EffectSetCredentialUnavailable` 却把范围放大到**整个凭据**。
⇒ **判别动作：看到「一个模型的故障连坐了兄弟模型」，不要只看写侧那一条路径**——
`credential_model_bindings.unavailable_reason` 的**前缀**直接告诉你谁写的：
`auto_<kind>` = 模型级，`auto_credential_<kind>` = 凭据级。
本例 10 个绑定里 9 个是 `auto_credential_rate_limit`、1 个是 `auto_rate_limit`，
一眼就能看出放大发生在哪条路径。

### 9.5 ★ 同一件事在不同协议腿上的规则要逐腿核对

「Responses 腿接受 `status=incomplete`+`max_output_tokens` 作正向证据、
chat 腿不接受」——这类**协议腿之间的规则不对称**，靠读单个文件是发现不了的
（两个判据分散在 `bg/probe_http_validation.go` 和
`bg/credential_probe_response_validation.go`）。
⇒ **判别动作：凡是「按证据强弱定生死」的规则，列出它覆盖的**全部**腿**，
逐腿对照「同一现象在这条腿上判什么」**——本例是 Responses 放行、chat 定罪。
⇒ 也提醒：**不要为了让某条腿通过就改判据**——本仓的
「2xx 不等于健康、必须有正向证据」是刻意立的规矩（见 L10 的处理方式：
**记为遗留、单独一轮再议**，不在别人的任务里顺手改掉）。

---

## 10. §5.12 之后新增的经验（本轮续做）

### 10.1 ★ 「量不出来」是对**量法**的判断，不是对**事实**的判断

上一轮把 L3 记成「在本地小数据集上无法证明收益」。这句话本身没错，但它
**掩盖了一个可证的事实**：同一行集被独立扫了两次。
本轮换了量法就出来了 —— **执行次数**是结构性的、与数据量无关；
而 buffers / 墙钟在小数据集上**原理上就分辨不出**（第二趟全命中缓存，
实测三种写法都是 137 hit）。
⇒ **判别动作：说「量不出」之前，先问「我选的量具有没有分辨这件事的能力」**。
量具分辨不出的量（分辨率不足）≠ 事实不存在。
⇒ 推论：找**结构性**的量（执行次数、扫描的行集条数、依赖图形状），
而不是**连续**的量（耗时、buffers、CPU）—— 前者在小环境也能证伪。

### 10.2 ★ 夹具里被比较的两个键必须「故意不同」，否则门分不出键漂移

第一版真库门把会话行的 `model` 与 `raw_model_name` 设成**同一个值** ⇒
变异「把匹配键从 `raw_model_name` 换成 `model`」**没让门变红** ⇒
门「看着在测、其实测不出来」。
改成两个不同的字符串后，同一个变异立刻转红。
⇒ **判别动作：写夹具时问「我要防的那个漂移，会让这两个值不同吗」**；
若不会，夹具就钉不住它。
⇒ 这与「允许侧判据要配前提断言」是同一族：**门必须能分辨「对的」与「错的」**，
而不只是「有值」与「没值」。

### 10.3 ★ 「端点空」与「读源错」是**两件独立的事**

`/api/admin/auto-route/cost/customer` 返回 `[]`，原因有**两层**：
读源错（活指标读冻结族）+ 聚合链从未接线（`n_tup_ins=0`）。
只修读源 ⇒ 端点**仍然** `[]`（因为相关子查询在外层表零行时压根不执行）。
⇒ **判别动作：看到空结果，按这个顺序查**：
① 外层聚合表**有没有在增长** ⇒ 用 **`count(*)` + `max(bucket)`**
（⚠ **不要**用 `pg_stat_user_tables.n_tup_ins` 当「有没有被写」的证据——
它**会计入回滚事务的插入**，被测试夹具污染，见 §10.5）→ 决定「有没有数据」
② 读源对不对（视图/查询的源表）→ 决定「数据取对了吗」
③ 相关子查询在空表上**不执行** ⇒ 要证明读源，必须**造外层行**（夹具），
不能靠「视图建出来了」或「端点不报错」。

### 10.4 ★ 类型要查 `information_schema`，不要靠记忆

会话族 `api_key_id` 是 **text**、v1 是 **bigint**、聚合表是 **integer**
—— 三个值三种类型。凭印象写比较 ⇒ 建视图直接报
`operator does not exist: text = integer`。
⇒ **判别动作：跨族映射列时，逐列查类型**（一张 `information_schema` 查询就够），
并把这个差异**写进迁移注释**（它是下一个人的坑）。

### 10.5 ★ 判据会**污染它自己要用的度量**：先问「这个量会不会被我自己写高」

本轮用来证明「`api_key_model_cost` 从未被生产流量写过」的证据是
`pg_stat_user_tables.n_tup_ins = 0`。加完真库门之后，同一个读数变成 **13**，
而表里**0 行**、`n_tup_del=0` ⇒ 字面读数是「被写进 13 行、又删干净了」。
⇒ 根因（受控实验实测，不是猜的）：**PG 的 `n_tup_ins` 计入回滚事务里的插入**
——`BEGIN; INSERT; ROLLBACK;` 之后计数确实 +1；而本轮的真库门正是
`defer tx.Rollback()` 的夹具插入 ⇒ **门把自己的度量写高了**。
⇒ 决定性复现：连跑该门 3 次 ⇒ 计数 13 → 16（**恰好 +3**）、行数**恒 0**
⇒ 那 13 次插入**全部**是门自己的夹具。
⇒ **判别动作**：
① 用一个量下结论前，先问「**这个量会不会被我自己（的测试/任务）写高**」——
本例若不追问，就会把「测试跑过 13 次」误读成「生产写过 13 行」，
进而把一条正确结论（L11：聚合链从未接线）**整个推翻**；
② **可复现的判据**是「跑 N 次 ⇒ 计数恰好 +N、行数不变」，
这比任何一次读数都有说服力；
③ 判「某表有没有被写过」时，优先用**结构事实**（触发器在不在、现在几行、
`max(bucket)`），而不是**计数器**——计数器会被任何跑过夹具的测试抬高。

### 10.6 ★ 「0」有两种成因：**读源错** 与 **本来就没有这种流量**

`active_concurrent` 修完读源后，活体读数**仍然是 0**。
差点又写一次「修好了但读数还是 0 ⇒ 没修好」。
⇒ 查下去是**第二种成因**：会话族里 `is_auto_request` **全是 false**
（hot 310 行 / parent 877 行，auto 行数 0 / 0）⇒ 本机这轮测试走的是
**指定模型直连**，**压根没有 auto 路由流量** ⇒ 5 分钟窗里没有可计数的行，
**0 是正确的**。
（`is_auto_request` 来自 **`X-Gw-Is-Auto` 头或 `model="auto"`**，
见 `domains/streaming/handler.go:7006`。）
⇒ 7 天窗那个指标（`avg_latency_ms`）**同一时刻**取到了真实值
（4046.1ms / 6395.8ms）⇒ **同一个视图里两个指标一空一有数**，
这个不对称本身就是「读源已通、只是这条腿上没流量」的证据。
⇒ **判别动作：看到 0，先分它是「取错了」还是「没有可取的」**——
查**上游集合的大小**（`count(*) FILTER (WHERE is_auto_request)`、
`count(*) FILTER (WHERE success)`），而不是反复重跑同一个查询。
⇒ 这与 §10.3 是同一族但**方向相反**：那里是「端点空 ≠ 读源错」，
这里是「读数 0 ≠ 没修好」⇒ **合起来一条：任何单个读数都不能同时定两件事**。

### 10.7 ★ 量页面耗时前，先确认「没有别的压测在同一个被测服务上」

- 场景：部署后复验，我把 **API 全量扫**（511 条路由）和 **104 页逐页扫**
  **并发**跑在同一个网关上 ⇒ `/dashboard` 读数从基线 2098ms 变成 **2920ms**
  ⇒ 差点写成「本轮改动把仪表盘拖慢了 40%」。
- 根因：两个压测器**互相踩**。逐页扫独占重跑 = **1965ms**（比基线还快）⇒
  2920ms 是**量具读数**，不是产品事实。诡异之处：全站 `navMs` **中位数
  三次几乎不动**（1128 / 1120 / 1122ms）⇒ **只有长尾页被污染**，中位数
  完全不暴露这件事 ⇒ 只看中位数会漏掉，只看单页最大值会被骗。
- 防复发检查：① 测页面/接口耗时前，**确认没有第二个压测在同一个被测服务上**
  （本轮是「顺手并行」造成的，最容易发生）；② 怀疑读数异常时，**先重跑一次
  独占**，再下结论；③ 跨版本比耗时**必须同条件**（同并发度、同是否独占、
  同机器负载）——本轮三次读数里只有「独占 vs 独占」可比；④ 报耗时结论时
  **把测量条件写进结论**（「独占跑的，API 全量扫未同时进行」），
  否则下一轮的人会拿不同条件的数字来对比。
- 证据：同一份 `sweep.mjs`、同一份代码，三次读数：基线 2756 **2098ms** /
  并发 **2920ms** / 独占 **1965ms**；`/dashboard` 的 `slow` 列三次都为空
  （**没有任何单个 API ≥2000ms**），其自身端点最大 221ms
  （`admin/dashboard/swim-lane-init`）⇒ 页面耗时是 87 个并行请求的聚合 + SPA 渲染。
- 溯源：request_logs→session_turns 复核轮 §5.12.7；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.12.7。

### 10.8 ★ 判「某个名字出现了没有」时，别用 `LIKE` 配 `_`：`_` 是**单字符通配符**

- 场景：上一轮判定「哪些视图还读 v1 裸母表」用的写法是
  `pg_get_viewdef(v) LIKE '%_request_logs%'` ⇒ 结论是「**5 张** `v_*` 视图读 v1」。
  本轮用精确仪器重判 ⇒ **实为 3 张**，另两张（`v_model_health_dashboard` /
  `v_probe_system_health`）是**假阳性**。
- 根因：SQL 的 `LIKE` 里 **`_` 匹配任意单字符**（不是字面下划线）⇒
  `'%_request_logs%'` 会把 `request_logs_with_current_month`（**canonical 视图名**）
  也判成命中（`%` + `_` 吃掉了 `request_logs` 前面那个空格）⇒
  **「v1 族」与「canonical」被混为一谈**。
  ⇒ 而本轮**我自己新写的 883 POST_CONDITION 用的也是这条坏判据** ⇒
  同一把尺子在一轮内坑了两次（上一轮的结论 + 本轮的迁移自检）。
- 防复发检查：① 判「某个标识符出现了没有」**不用 `LIKE` 配 `_`**：用
  `strpos(x, '_request_logs') > 0`（精确子串）、`position(...)`、
  或**正则 + 词边界**（`\b` 尾巴是关键：`request_logs_with_current_month`
  里的 `_` 是词字符 ⇒ `\b` 不成立 ⇒ 不会误判）；
  ② **先确认判据能分辨「要判的那个」与「最像它的那个」**——本例最像的是
  canonical 视图名；③ 判据自己错了时，它钉的一切都是假的 ⇒
  **给判据配一条「判据的判据」**（本轮 `TestMigration884_JudgmentInstrumentDistinguishesV1FromCanonical`
  就是这条：断言精确仪器命中 v1 字面量、**不**命中 canonical 名，并核对仓库自己的门里
  仍然是那条正则）；④ 上一轮写下的**数量型结论**（「5 张」）在下一轮要**重算**，
  不要直接引用——本轮就是靠重算才发现上一轮多算了 2 张。
- 证据：真库 `pg_get_viewdef` 精确重判 13 张视图（§5.13.1 的表）：3 张真读 v1、
  2 张假阳性；`internal/sqlreadguard` 的正则 `(?i)\b(FROM|JOIN)\s+(public\.)?_?request_logs\b`
  对两者的判定分别是「命中 / 不命中」⇒ 仓库的门是对的。
- 溯源：request_logs→session_turns 复核轮 §5.13.1 / §10.8；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.13.1。

### 10.9 ★ **量具比被测对象更严 ⇒ 造假阳性**：修尺子要先于下结论

- 场景：仓库自带的 fresh-install 链门 `TestStartupChainGapsMatchReviewedSet`
  报告「8 条新破坏」，其中 7 条的错误都是
  `DROP/CREATE INDEX CONCURRENTLY cannot run inside a transaction block`。
- 根因：**门自己的执行通道**（`runInstallerPSQL`）无条件加 `--single-transaction`，
  而**真实安装器**（`dbinit.Runner.applySQL` 与同包的 `applyInstallerSQL`）
  会把带 `dbinit:no-transaction` 标记的文件送到**非事务通道**
  ⇒ 这 7 条在**门里**必然失败，在**真安装器里**其实成功
  ⇒ **量具比被测对象更严 ⇒ 量具造的假阳性**。
- 危害比「假绿」更隐蔽：门的失败集合里**混着真缺陷**（853 的队列表缺失）
  与**假破坏**（7 条事务包裹）⇒ **信号被淹没**，真缺陷没人看得见；
  而且门长期红着 ⇒ 没人相信它 ⇒ 它彻底退化成摆设。
- 防复发检查：① **量具的执行通道必须与被测系统的执行通道逐字一致**
  （同一套标记分流、同一套事务语义）；② 门的失败集合要**分类**看：
  「真破坏」与「量具造成的破坏」必须分开记账，不能混进同一张 allowlist；
  ③ 门红了先问「**是我的尺子坏了吗**」，再问「被测对象坏了吗」；
  ④ 修完尺子要**重跑**并把 allowlist 按该门自带的收缩规则清理干净
  （本轮清空 18 条 ⇒ 门从「8 坏 / 18 期望」变成「0 坏 / 0 期望」）。
- 证据：本机 PG 18.10 临时库逐条实跑 —— 修前 `8 of 280 fail`，
  修后 `0 of 282 fail, 0 expected`（§5.13.8 的结果表）。
- 溯源：request_logs→session_turns 复核轮 §5.13.8 / §10.9。

### 10.10 ★ **「升级路径绿」≠「新装路径绿」**：基线必须自洽

- 场景：迁移 884 改读会话族后，**升级路径**（真库）一路绿、部署后读数也对；
  而**新装路径**在基线第 18647 行就报 42P01 并**中止整个安装**。
- 根因：基线（`01-schema.sql`）是**逐语句**应用的，而 `CREATE VIEW` 会
  **急切校验**它引用的关系 ⇒ 只要基线里写了
  `FROM public.session_turns_hot`（这个关系由**启动序列**建、基线不建），
  新装就必然在这里死。**升级路径看不出这个问题**（那些表早就存在）。
- 防复发检查：① 改视图/改表时，**先问「这个关系在基线时点存在吗」**
  ——基线自建的是「基线自己 CREATE 出来的」，启动序列建的是「基线时点还没有」；
  ② 视图的**部署形态**交给**迁移**换上，**基线保留 fallback 体**
  （基线自建 `request_logs` ⇒ fallback 体在基线时点有效）⇒ 两条路径最终同形；
  ③ 立一道**基线自洽门**（静态、��驻）把「基线引用了基线不创建的关系」这条
  变成**常驻**约束，而不是靠某次新装撞出来；
  ④ 门自己也要防「扫到 0 个对象就安静通过」（§5.13.7 的量具坑）。
- 证据：新装临时库实跑（基线 exit=0 修复后）；新装链 282 条全绿；新装后 5 张视图
  与存量真库**逐字同形**（§5.13.7 / §5.13.8）。
- 溯源：request_logs→session_turns 复核轮 §5.13.7 / §10.10。

### 10.11 ★ 判「这条迁移会不会在链上跑」时，看**它自己声明的依赖**是否在链里

- 场景：853 在新装链上报「队列表不存在」。读 853 的 runner 注释，
  它明明白白写着「once **489/490** create the queue」
  ⇒ **作者以为 489/490 在链里**，实际那两条**从未登记**。
- 通式：一条迁移**自己文件头里声明的依赖**，就是它最可信的依赖清单
  ⇒ 链上断链时，**先拿被断的那条的声明去查链**，比翻全链快得多。
  顺带：对同一个对象的「创建者」往往有好几条（这里 489 建表、490 补列、
  853 加 `automatic` 列）⇒ 断链常常是**整族没登记**，不是单条漏登记。
- 防复发检查：① 登记一条启动迁移时，把它的**声明依赖**一起核对是否在 `StartupFiles` 里；
  ② 链上顺序要**按依赖排**（489/490 必须在 853 之前）；
  ③ 登记后**重跑整链**并对比失败集合（`knownStartupChainGaps` 这类 allowlist
  能给出「改前 vs 改后」的精确差集）。
- 证据：853 文件头 + runner 注释 + pristine 工作树复现（既有性）+ 补登后 0/282 失败（§5.13.8）。
- 溯源：request_logs→session_turns 复核轮 §5.13.8 / §10.11。

### 10.12 ★ 用**裸数字**做文本启发式 ⇒ 一定要配词边界 + 单位/小数排除

- 场景：页扫的 `NOT_FOUND` 标记比上一轮多了 1 页（`/admin/data-lifecycle`），
  差点被当成「本轮改动把该页搞坏了」。
- 根因：标记正则 `/404|页面不存在|Not Found/i` 命中的是页面正文里的
  「PostgreSQL 数据库 v17.10 **404.2 MB** 总占用」—— **库大小**恰为 404.2 MB。
  同类还有 UUID 里的 `…**8404**…`（子串命中）与
  「标记测 200 字符、落盘只存 160 字符」造成的**事后不可复现**。
- 防复发检查：① **裸数字**（404/500/403 之类）做匹配时，
  必须同时有 `\b` 词边界 + `(?!\.\d)`（小数）+ `(?!\s*(?:MB|GB|KB|bytes|ms|行|条|个))`（单位）；
  ② 更稳的做法是**只匹配明确措辞**（`页面不存在` / `Not Found` / `HTTP 404`），
  数字状态码一律靠 `docStatus` 字段判（那条是**结构化**的，不受正文影响）；
  ③ **标记的判定窗口必须与落盘窗口一致** ⇒ 判据与证据同源、可离线重算；
  ④ 判「某标记集合变没变」前，**先把新出现的标记逐个证伪**（本轮证伪结果：
  `docStatus=200` + 无真 5xx + 无 JS 异常 ⇒ 假阳性）。
- 证据：2758 页扫 19 → 修完 10；残留 7 个 `NOT_FOUND` 逐个核实为**真态**
  （`key/client/task/journey/approval/order not found`，路由表用合成 id）。
- 溯源：request_logs→session_turns 复核轮 §5.13.10 / §10.12。

### 10.13 ★ 「检查没跑成」与「检查通过」必须长得不一样

- 场景：`verify-model-writeback.sh` 用了 `mapfile`（bash **4+**），macOS 系统 bash 是 **3.2**
  ⇒ 段落函数因 `mapfile: command not found` + `set -u` 的 `unbound variable` **静默失败**，
  脚本却一路走到末尾打印「=== 结果：PASS ===」
  ⇒ **一个根本没执行的检查报了 PASS**。
- 根因：脚本没有 `set -e`，且失败位 `fail` 只由 `bad()` 设置；
  段落函数整体挂掉时既不置失败位也不非零退出 ⇒ 「没跑」与「通过」在输出上无法区分。
- 防复发检查：① 凡依赖 bash 4+ 特性（`mapfile` / 关联数组 / `${var^^}`）的脚本，
  **必须用 `~/sdk/bash5/bin/bash`**（本仓已有，见 AGENTS.md「本机工具链」），
  且在脚本头注明所需 bash 版本；② 判据脚本**每一段**执行完要留下「跑过了」的痕迹
  （打印段标题 + 计数），缺哪段一眼能看出来；
  ③ 「结论行」只能在**所有段都跑过**之后打印，且任一段异常 ⇒ 非零退出；
  ④ 复跑前先确认解释器：`bash --version`（macOS 自带 3.2，Homebrew 5.x 在 `~/sdk/bash5/bin`）。
- 证据：系统 bash 跑 → 3 处 `mapfile: command not found` + `unbound variable`，
  末尾仍打印「PASS」；`~/sdk/bash5/bin/bash` 跑 → A/B/C/D 四段全部执行、**全项 PASS**
  （turn_no 1..5、prompt_tokens 13→32→46→314→1066 单调、正文三列全 `array`、退役族零新增）。
- 溯源：request_logs→session_turns 复核轮 §5.13.10 / §10.13。

### 10.14 ★ 判据的「前提 / 段级 SKIP / 段级结论」三件套（Seen: 1/3）

- 场景：定时补跑三模型累积式多轮时，写回验证脚本连续暴露 4 个问题，
  其中 3 个会让**产品结论被写错**。
- 根因（四处各不相同，但都是「量具的缺陷」）：
  ① 脚本里 `... -> $p（说明…）`——**变量名后紧跟全角括号**，bash 5.2 在 `set -u` 下
  把非 ASCII 字节并进变量名 ⇒ `p?: unbound variable` ⇒ **B 段整段不执行**；
  ② B 段判据**缺前提**（全失败会话 history 不推进 ⇒ 「token 变小」是预期），
  且只打印 SKIP 却仍让循环体报 FAIL ⇒ 输出自相矛盾；
  ③ C 段判据**缺前提**（失败轮次没到上游 ⇒ `outbound_body`/`response_delta` 合法地是 null）；
  ④ C 段的**结论行无条件打印「OK」**，即使刚报了一堆 FAIL。
- 防复发检查：① 写含中文的 bash 脚本时，**变量引用一律带花括号** `${var}`
  （bash 会把 `$var` 后的非 ASCII 字节并进变量名）；② 每条判据都要写清**前提**
  （什么时候适用）与**不适用时怎么办**（明说 SKIP，不要含糊放过）；
  ③ **段级结论必须按本段失败位选文案**，不能无条件打印；
  ④ 「检查没跑成」「前提不成立」「结论为负」三者必须在输出上**可区分**。
- 证据：修正后三个会话全部 **PASS**：`glm-5.2` 5/5=200（token 13→28→42→307→577 单调、
  正文三列全 `array`）、两个 deepseek 5/5=503（`success=f` 落库、B 按前提 SKIP、
  C 按前提 SKIP）、退役族零新增。
- 溯源：request_logs→session_turns 复核轮 §5.13.11 / §10.14；[复盘](docs/audit/2026-10-20-request-logs-session-turns-复核轮.md) §5.13.11 / §10.14。

---

## 11. `admin/` 包收口轮：v1 退役后「台账型门」全部翻转（2026-10-20）

> 范围：**只改 `admin/` 包**（生产代码零改动）。`bg/`、`db/`、`sql/`、`internal/sqlreadguard/`
> 由本轮之外的上游收口；本节只报告它们的状态。

### 11.1 为什么台账型门会集体变红

迁移 890 把 v1 族**物理删除**之后，`admin/` 里那批「台账型」门的前提整体失效：

| 台账 | 原来守什么 | 890 之后的事实 |
|---|---|---|
| `requestLogsReadInventory` | 读 `_request_logs*` 的生产文件清单（57 文件 / 109 调用点） | **0**（canonical 视图不是 v1；SQLite 存储面是同名不同物） |
| `indirectRequestLogsReaders` | 间接读 v1 的文件（3 条） | **0**（两个切换层都恒为会话族） |
| `requestLogsStopWriteClassification` | 停写分级（63 条） | **0** |
| `v1BodiesReaders` / `v1BodiesReaderShapes` / `v1BodiesReadPaths` | bodies 读方登记（24 / 24 / 3 路径） | **0** |
| `retirementBreakers` / `retirementReattributed` | 删 v1 会坏掉的文件 | **0** |
| `viewArmCutoverReaders` | 「切换那一刻会静默降级」的清单 | **0**（canonical 视图没有 v1 臂了） |
| `requestLogsControlPlaneReaders` | 控制面依赖 | **0** |
| `v1ReaderRetirementTiering` / `v1ArmViewNamesRuntimeChecked` / `v1ArmLiteralBlindSpot` / `v1ReaderIndirectPopulation` | 分档域 / v1 臂视图 / 工具盲区 / 第二总体 | **0** |

⇒ **处置不是删门**：这些门守的是「不许出现新的 v1 读方」这条不变式，
删掉就没人守了。⇒ 统一翻成 **「清单必须为空 + 实测必须为空 + 新增即红」**，
并给每条负向断言配**阳性对照**（合成条目 / 合成源码），
避免「表空了 ⇒ 循环不执行 ⇒ 恒绿」。

### 11.2 「canonical 视图不是 v1」是这一轮的口径分水岭

890 之后 `request_logs_with_current_month` **仍然存在**（内容换成会话族两臂体，118 列同名同序）。

⇒ 一切「v1 读方」口径必须排除它，否则 55 个视图读方会被误算成 v1 读方。
本轮把它收窄成**已退役关系的闭合集**（`retiredV1RelationNames`：
5 张底表 × 两种拼写 + 4 张 v1-only 视图），并在
`admin/session_family_sql_shape_test.go` 里用**标识符边界**判定
（`public.request_logs` 是 `public.request_logs_with_current_month` 的前缀 ⇒
朴素 `Contains` 会把「必须在场的 canonical 视图」判成「必须不在场的 v1 族」）。

### 11.3 已知的生产缺陷：**4 个 admin 生产文件仍读 890 已 DROP 的视图**

`890_drop_request_logs_v1_family.sql` DROP 了
`request_logs_with_current_month_without_customer_id` 与
`request_logs_with_current_month_without_request_class_due_at`，
而下列文件仍读它们 ⇒ **运行时 42P01**：

| 文件 | 行 | 读的关系 |
|---|---|---|
| `admin/usage_trend_series.go` | 407 / 588 | `…_without_customer_id` |
| `admin/auto_route.go` | 158 | `…_without_customer_id` |
| `admin/attempt_quality_api.go` | 139 | `…_without_request_class_due_at` |
| `admin/auto_route_correlations.go` | 129 / 150 / 171 / 193 / 246 | `…_without_request_class_due_at` |

⇒ 本轮**只报告不改**（生产文件由上游收口）；但它们被**逐个具名**登记在
`admin/session_family_sql_shape_test.go` 的 `retiredV1ReferenceExceptions` 里，
并由 `TestRetiredV1ReferencesAreAllAccountedFor` 守着
（**新面孔即红** + 例外只许缩小 + 每项必须有非空理由）。
⇒ 「v1 读方总体为空」这句话因此**是逐个核对过的**，不是「扫不到所以为空」。

### 11.4 同名不同物：`storage/sqlite/request_log_store.go`

该文件里的 `request_logs` 是它**自己的 SQLite 表**（列是 request_id / tenant_id /
session_id / ts / method / path / status_code / duration_ms / has_body，占位符 `?`），
与 PG 的 v1 族无关。⇒ 扫描器按**路径**排除 `storage/sqlite/`（不是按「有没有 `?`」
那种启发式 —— 后者一个 PG SQL 里出现 `?` 也可能是 JSON 操作符）。

### 11.5 处置三分法（本轮 22 道红用例）

- **(a) 反转断言**（主体还在、形状变了 ⇒ 「必须读 v1」改成「必须读会话族」+
  负向断言「不得再出现 v1 族关系名」）：tenant stats / data lifecycle /
  cross-table request_id / bodies 批量取数 / outcome 新鲜度 / 压缩计数 /
  telemetry 占位符与停写门 / PGBodyReader 七件套 等。
- **(b) 删测试留注释**：**本轮没有用到**。原先预留这一类是因为「主体真退役且无等价物」，
  实际每道红用例都找到了等价不变量（要么是「会话族两腿在场」，要么是
  「只许缩小 / 只许新增」这类**形状不变量**）⇒ 全部走 (a) 或 (c)。
- **(c) 换成守新不变量的门**：
  `TestBodiesSwitchLayerIsAlwaysSessionFamily`（切换层恒等于会话族）、
  `TestTenantStatsLogsTable_ProjectionAndSourceUntouched`、
  `TestTenantStatsLogsTable_BothBranchesAreIdentical`、
  `TestResetCredentialSuccessRateRowsIsANoOpAfterV1Retirement`、
  `TestResetCredentialSuccessRateRowsNeverTouchesTheSessionFamily`、
  `TestSessionKeyedReaderSourceGuardHasTeeth`、`TestSessionLegOnlyGuardHasTeeth`、
  `TestTelemetryIngestHasNoV1Writes`，
  以及本轮 22 道台账门全部翻成的「清单为空 + 实测为空 + 新增即红」。

**门名改动 5 处**（语义已变、名字会误导；均在 `auditGateDocs` 映射之外 ⇒ 门总数仍 17）：

| 旧名 | 新名 |
|---|---|
| `TestBodiesSwitchDefaultsToV1` | `TestBodiesSwitchLayerIsAlwaysSessionFamily` |
| `TestResetCredentialSuccessRateRowsRunsInsideSuperAdminGUCTx` | `TestResetCredentialSuccessRateRowsIsANoOpAfterV1Retirement` |
| `TestResetCredentialSuccessRateRowsKeepsGUCGuardPredicate` | `TestResetCredentialSuccessRateRowsNeverTouchesTheSessionFamily` |
| `TestTenantStatsLogsTable_V1LegsUntouched` | `TestTenantStatsLogsTable_ProjectionAndSourceUntouched` |
| `TestTenantStatsLogsTable_StopWriteDropsTwinAntiJoin` | `TestTenantStatsLogsTable_BothBranchesAreIdentical` |

### 11.6 同步更新的文档

- `docs/audit/2026-09-30-session-request-data-re-audit.md`：
  「灰度前必须处理的静默档：**56** 条」→ **0** 条，并追加 2026-10-20 订正
  （v1 族真删 ⇒ 停写分级表清空 ⇒ 三个静默档条数归零；**口径未变**，
  数字仍由 `countSilentStopWriteEffects` 从登记表实时计算）。

### 11.7 变异验证（8 条，全部「变红 → 按字节还原 → sha256 一致」）

| # | 变异 | 变红的门 | 还原 |
|---|---|---|---|
| M1 | `db.SessionBodiesSourceSQL()` 退回 v1 臂 | `TestIndirectRequestLogsReadersAreStillIndirect` / `TestBodiesSwitchPairIsPinnedAndPaired` | sha256 一致 ✓ |
| M2 | `bg.settleSourceFor` 的 `TurnsTable` 退回 `_request_logs_hot` | `TestIndirectRequestLogsReadersAreStillIndirect` | sha256 一致 ✓ |
| M3 | `db.SessionFamilyTurnsSourceSQL()` 少一条腿（hot ∪ hot） | `TestSessionFamilySourceShapeMatchersHaveTeeth` | sha256 一致 ✓ |
| M4 | `admin/tenant_stats_logs_table.go` 源退回 `_request_logs_hot` | `TestRequestLogsReadInventoryIsComplete` / `TestRetiredV1ReferencesAreAllAccountedFor` | sha256 一致 ✓ |
| M5 | `admin/telemetry.go` 写回 `_request_logs_hot` | `TestTelemetryIngestHasNoV1Writes` | sha256 一致 ✓ |
| M6 | `admin/usage_enhanced.go` 压缩计数源退回 `_request_logs` | `TestCompressedCountReadsParentNotCanonicalView` | sha256 一致 ✓ |
| M7 | 去掉 SQLite 存储面排除（同名假阳性未被识别） | `TestRequestLogsReadInventoryIsComplete` / `TestRetiredV1ReferencesAreAllAccountedFor` | sha256 一致 ✓ |
| M8 | 新增一个**未具名**的 v1 读方 | `TestRetiredV1ReferencesAreAllAccountedFor` / `TestRequestLogsReadInventoryIsComplete` | sha256 一致 ✓ |

M8 是本轮最关键的一条：它证明「台账为空 + 新增即红」这条不变式**真的在跑**，
而不是「扫描器坏了所以看起来空」。

### 11.8 本轮发现、**没有改**的问题

1. **4 个 admin 生产文件仍读 890 已 DROP 的视图** ⇒ 运行时 42P01（§11.3）。
2. **`storage.session_bodies_native_read` 是僵尸配置项**：读端开关已无读者
   （`db.SessionBodiesSourceSQL()` 恒等于会话族），但键仍登记在
   `settings/spec_storage.go` 的 settings spec 里 ⇒ 运维改它不会有任何效果。
3. **写端 `storage.session_turns_bodies_enabled` 仍在**（控制 `session_turns` 的
   `request_delta` / `response_delta` 两列是否写入）。**会话族的 `session_bodies`
   正文是无条件写的**（`bodiesRec`），所以正文读链没问题；但
   `session_turns` 宽表那两列在生产上恒 NULL —— 与 v1 退役无关，是既有状态。
4. `admin/usage_trend_series.go` 的 `detail` 档读的是被 DROP 的包装视图，
   而它本来就想「带 api_key 过滤的 detail 档」⇒ 修复方向是改读 canonical 视图
   （890 之后是会话族两臂体、列名同序），不是重建包装视图。

---

## 12. 部署与验收轮新增的经验

### 12.1 ★ 「字符串相等」≠「同一份数据」：路径判定必须按**物理路径**

**现象**：`deploy-local.sh` 的「容器数据目录 == 共享目录吗」判定用字符串比，
宿主机上共享目录是软链（`~/kaixuan/postgres -> postgres-citus`）⇒ 判成「未绑定」
⇒ 触发几百 MB 级数据搬迁 + 容器重建 ⇒（**本轮改动前的**）重建固定占 5432、
该端口已被别的实例占住 ⇒ **整条部署中止**，网关根本没起来。
（端口那一层在**合并上游后已修**——重建改为沿用旧容器已发布的 host 端口；见 §6.13 订正段。）

**根因**：判定问的是「**路径写法**一样吗」，而要问的是「**是不是同一份数据**」。
软链、绑定挂载、`/private/var` 与 `/var` 别名，都是「同物异名」。

**防复发检查**：凡是「这两个路径是不是同一个东西」的判定，都用**物理路径**
（`cd … && pwd -P`）而不是字符串；比较前先问「有没有可能是同物异名」。
本轮已把判定抽成 `dl_same_physical_dir()`（可 source、可单测）并配契约门 + 2 项变异。

**证据**：`scripts/deploy-local.sh`（判定改造）、`scripts/deploy-local-lib.sh`
（`dl_physical_path` / `dl_same_physical_dir`）、`tests/deploy_local_contract_test.sh`
（新门）、宿主机 `~/kaixuan/postgres -> postgres-citus`。
**溯源**：§6.13。

### 12.2 ★ 长流的「耗时」是**流时长**，不是响应延迟

**现象**：逐 API 扫描报出 `/api/free-pool/stream` 89.7s、`/api/admin/system-monitor/stream` 51.1s，
按「>2s 必须定位并优化」的规则本应去查慢 SQL——但这是**假慢**。

**根因**：SSE/长轮询端点的响应**不结束**，客户端的「等待响应」时长等于**流的持续时间**；
首字节其实 1.6ms 就到了。用 `curl -w time_starttransfer` 实测 TTFB = 1.6ms / 1.8ms。

**防复发检查**：扫出「慢端点」时，先问「这个端点返回的是**一次性响应**还是**流**」；
流式端点一律改用 **TTFB**（`time_starttransfer`）量，别用「读到 EOF」的总时长。
⇒ 判据纪律的又一个形态：**量具的计时边界必须与被测语义对齐**。

**证据**：§6.14 的 API 扫表 + `curl -w` 复测。
**溯源**：§6.14。

### 12.3 ★ 扫出 404，先怀疑**量具用错了 ID**

**现象**：逐页扫里 `/api/keys/1`、`/api/admin/sessions/1/turns`、
`/api/admin/session-analytics/clients/1` 都是 404，看着像「端点坏了」。

**根因**：扫描器把路径里的 `:id` 一律替换成 `1`，而这些 ID 空间里 **1 不存在**
（真 key id = 10、session id = `gw_…`、client id 是模型名）。

**防复发检查**：动态段替换出来的 404/4xx，**必须用真实 ID 复测一次**再下结论；
真实 ID 从库里取（最近的 `request_id` / `session_id` / 主键），
并把「用真实 ID 复测 → 200」写进验收记录。
⇒ 这与 §11.7「零命中要用合成样本自证」是同一族：**没命中可能是「看不见」，也可能是「本来就没有」**。

**证据**：`/api/keys/10` 200、`/api/admin/sessions/gw_ca40172f…/turns` 200、
`/api/admin/session-analytics/clients/grok-4.6` 200。
**溯源**：§6.14。

### 12.4 ★ 路由 404 有三类成因，别混成「端点不存在」

**现象**：50 条 404 混着三种完全不同的东西。

**根因与分类**：
1. **Go 默认 mux 的 404**（正文 `404 page not found`）：路径没被注册；
2. **前缀分发器的按设计 404**：`/api/admin/model-integrity` 是**前缀**，
   要子路径（`summary` / `events` / `fingerprint-drift`），裸路径必然 404；
3. **handler 级 not found**（JSON 正文）：路由在，但资源/子资源不存在。

**防复发检查**：判「端点不存在」前，先看 **404 正文形态**（纯文本 vs JSON）
与**路由是不是前缀**；前缀型路由要扫它的**真实子路径**才算覆盖。

**证据**：`admin/handler.go:979-980`（前缀分发器）、`admin/model_integrity.go:66`
（`http.NotFound`）。**溯源**：§6.14。

### 12.5 ★ 写链路验收要看**正文类型**，不是「有没有落行」

**现象**：v1 族真删后做写链路验收，第一眼看到
`session_turns.request_delta/response_delta` 全是 JSON **null**，差点判成「正文丢了」。

**根因**：本形态的正文存在 `session_bodies*` 族（`outbound_body`，JSON **array**），
而 `session_turns` 那两列是**另一个开关控制的可选写入**（见 §11.8 第 3 条）。

**防复发检查**：判「正文是否落库」必须先确认**正文的目标表**与**类型**：
`jsonb_typeof(...)` 是 array 才算有正文；并且要拿**部署前的同形态历史行**做对照，
否则分不清「本轮回归」与「既有形态」。

**证据**：本轮 200 行的 `session_bodies_hot.outbound_body` = `array`，长度 37 → 119 → 200
（随多轮增长）；部署前的历史 200 行 `session_turns.request_delta` 同样是 null ⇒ 非回归。
**溯源**：§6.14。

### 12.6 ★ 自己新建的迁移编号，可能与**并进来的上游编号**撞车

**现象**：本轮的「v1 族真删」编为 885、「会话族负成本索引」编为 886；
合并 `origin/main`（59 个提交）时发现上游已占用 **885~889** 且**已部署到 154**。

**根因**：迁移编号是**全局唯一**资源，而本分支的编号基于**合并前**的 main 选；
上游并行期继续往下编号 ⇒ 两条分支的编号空间相交。撞号的后果不是编译失败，
而是**两个不同迁移共用一个编号** ⇒ 台账/清单/通道数组出现两行同号、文档指代错对象。

**防复发检查**：① 选编号前先看远端最新号；合并后第一件事是**核编号占用**；
② 已部署的编号不可改 ⇒ 只能改自己的，且改号要连带改**全套登记**
（canonical + embeddata、契约门、go:embed + map、`StartupFiles`、**TSV 用 `-update` 重生成**、
通道 `files=()`、changelog 行**重算 sha**、散文编号）；
③ 改号只动编号 ⇒ 但**幂等性必须重测**（本地那条部署记录用的是旧文件名 ⇒ 通道下次会再跑一次新编号的文件）。

**证据**：890/891（原 885/886）；TSV 重生成后 294 条；新装链 0 of 294 fail；
890 在「v1 已删」的库克隆上重跑 EXIT=0、891 连跑两次 EXIT=0 且两条索引仍在。
**溯源**：§6.15。
