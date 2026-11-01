# 24h 审计轮 —— prompt-injection 时间戳 scan 族根修 + 48h 轮遗留三项复核（2026-11-01）

> 审计窗口：**2026-10-31 18:00 → 2026-11-01 18:30**（goal 四步：合并编译 /
> 48h 轮遗留复核 / 三台日志与 PG 例行 / 有修正则提交部署）。
> 起点：本地 main `70930583a` == origin/main（零落后）。
> 终点：`9d3b190de` 推送；245/154 部署；**211 亦已部署**（用户目标明确要求
> 「部署到 211 上并测试验证」⇒ 覆盖本档初稿的「211 保 tick 锚点不重部署」
> 决策，见 §5 决策表与 §5.1 的 211 行）。

---

## 〇、一句话结论

48h 轮遗留三项全绿（tick 未到、dropped_events 全零、queue full 零复发）；
例行日志审计抓到一条**潜伏 4 个月的真缺陷**——prompt-injection 管理面
5 个读端点把 timestamptz 扫进 Go string，pgx binary 路径下有行即 500
（今天 18:04 被首次访问触发）。修复过程与并行会话实时撞车，逐文件调和
落地（`9d3b190de`，其 `*time.Time` 方向胜出，我方 `::text` 方案丢弃）；
245/154 已部署；**211 随后亦按用户明确要求完成部署与验证**
（`2929-bb020177`，槽 b@8783 → 槽 a@8781），代价是放大类闭案的 24h tick
锚点从 11-02 14:38 顺延到 **11-02 18:42**（≈4h，顺延非取消）。

---

## 一、四步①：合并、编译与测试

- `git fetch` ⇒ origin/main 零新提交（HEAD 已同步），工作树起点干净。
- `go build ./...` ✅ rc=0。
- `make test-short` 三跑：**351 包 ok，唯 admin 三次全在 120.2s 上下超时**
  （120.137/120.114/120.276s）；admin **单独跑三次全绿**（103.2/103.9/105.9s）。
  ⇒ 定性：**结构性边缘**，非本轮回归——make test-short 的包级
  `-timeout=120s`（Makefile:42）对 admin 的 ~104s 基线只剩 ~16s 余量，
  全量并行时其它包抢 CPU 即越线。10-31 拦截归因轮（"admin 95.7s"）、
  10-31 48h 轮（"admin 120s 超时=负载假红"）是同一曲线的早段。处置：
  如实登记，不改 Makefile 口径（AGENTS.md 既有纪律）；机器空闲时
  （如 worktree 钉树）全量可绿，48h 轮已有先例读数。

## 二、四步②：48h 轮遗留三项复核

### 2.1 ① 放大类 dup 判据 —— tick 未到，中间读数干净 + 两处易误判形态定性

- **锚点事实**：211 当前槽 b（8783）StartedAt `2026-11-01T06:38:21Z` =
  **14:38:21+08**；24h tick = **11-02 14:38+08**，本轮（17:5x）读数时刻
  **物理未到** ⇒ 不写闭案（24h 轮 §603 纪律重申）。
- **中间读数**（窗口 14:38 → 18:0x，3.4h）：model_iq_runs 383 行
  （failed 372 / partial 5 / success 6）。
- **lt10min = 4 行/4 对**（非零！）——逐条溯源后定性为**部署交接尾巴**，
  不是放大复发：
  - 4 对全部落在 14:38:24–14:47:30（新槽启动头 9 分钟）；
  - 旧槽 a 活到 14:38:35（ExitCode 137，蓝绿重叠 14 秒），14:38:24 的
    cred3 首行是旧进程尾巴；其余 3 对是新槽启动序列（启动 recovery 批
    14:39:0x × 3 目标 + 首轮挑靶 14:39:56–14:47:30）；
  - **14:48 之后 lt10min 再无新增**（17 时 263 行大扫荡每目标仅 1 行）。
  - 对照：上一槽（10-31 20:59 起算）启动窗口 lt10min=0/0（§603）——
    本次非零纯粹因为旧槽尾巴恰好写进了新槽窗口内。
- **17 时 263 行全失败** —— 定性为**每日 6h anomaly 节律的正常一拍**，
  非新事件：anomaly 全池扫荡在 05/11/17/23 时反复出现（10-30 起
  255/262/253/267/225/263/257/264/255/266/263），每目标仅 1 行
  （trigger_kind=anomaly ×263），error 分类 404×167 / 410×74 / 429×12 /
  400×7 / 403×2 / 500×1 = 存量下线模型族（openrouter cred8 的
  dbrx/deplot/fuyu/starcoder2 等）。与前日同拍（10-31 17 时 257 行）
  量级持平 ⇒ 无回归。
- **今晚 23:30 自动化 `43481e79`**：按 48h 轮 L3 登记预期会报「未达」
  （它按旧锚点 11-01 20:59 判 tick；实际锚点已因 14:38 部署重置到
  11-02 14:38）——**读数时按新锚点解读，勿误判回归**。
  （本轮写作时该自动化尚未 fire，其输出由下一轮复核。）

### 2.2 ② dropped_events（新计数器）—— 全零 ✓

`GET /api/admin/sanitize-rules/instances`（admin token 289B）：166 个
实例 **total_dropped=0**，活跃实例（6caf0524b79a，last_seen 10:07Z 即读数
当刻）dropped_events=0 显式零。批写失败退路（c267433cd）部署后未触发 ⇒
写失败路径零增长。

### 2.3 ③ sanitize audit queue full / WARN 风暴 —— 零复发 ✓

槽 b 11.5h 日志：`sanitize audit queue full` WARN = **0**（攒批+4096+节流
三增量持续生效）；日志中 `dropped` 相关零条。首轮 grep "queue.*full" 命中
的 12 条全是 **probe_necessity 的 INFO**（探针必要性自检"removed from
queue"，健康行为）——正则误匹配，换精确口径后归零。

## 三、四步③：三台日志与 PG 例行审计

### 3.1 211（槽 b，06:38Z 起 11.5h）

- ERROR 411 = `http_request` 359（探针 503 族，上游可用性窗，24h 轮已
  定性 fail-fast 正确）+ `executor` 44（stream_interrupted/429 上游族）+
  **`admin` 6（本轮真缺陷，§四）** + `provider` 2。
- WARN 3491：top 族 node 1430 / telemetry 481 / trace 395 /
  validate_and_fix 223（既有 role alternation 形态）/ stale 121 /
  pending_capture_overflow 45（~1MiB 捕获上限按设计）——全部既有形态，
  无新噪声族。

### 3.2 245 / 154（蓝绿槽 @8782，06:30 起窗）

- 245（llmgo-245-canary@8782）：ERROR **0** / WARN **0** ✓。
- 154（llm-gateway-go-canary@8782）：ERROR **0** / WARN **0** ✓；
  acc-nginx 仍 Restarting（既有登记：内部容器无端口绑定，不擅动）。
- 量具坑（本轮实录）：三台 ssh 别名全部走 **Port 25022**（~/.ssh/config），
  直连 IP 默认 22 端口会被远端立即关闭——报 "Connection closed" 先查
  端口再下「机器挂了」的结论。

### 3.3 PG

- **wa-pg17（共享实例）**：24h 错误 5 条全为外部工具手搓 SQL（零长标识符 /
  sales_intel / " | " 列名 = kb-importer 族既有形态）⇒ 非本仓。
- **acc-pg17（网关库）**：24h 错误 14 条，**仓内代码零引用（grep 全空）⇒
  全部外部客户端**：
  - 17:09 波 11 条：`llm_usage_stats` / `llm_optimization_reports` /
    `llm_optimization_records` / `cl.task_type` 不存在——某外部工具把
    DSN 配到了网关库（这些表属另一产品）；
  - 18 点 3 条：`prompt_injection_attack_vectors`（注意：本仓 handler 查的
    是**不带前缀**的 `injection_attack_vectors`）/ `output_compliance_keywords`
    / 一条 syntax error——同族外部探测。
  - 处置：登记为用户侧/运维动作（配置纠偏属外部工具 owner），不擅动。
- 慢 SQL：`log_min_duration_statement=-1` + pg_stat_statements 未开 ⇒
  **维持不可观测**（L1 属主裁决项延续）。

## 四、真缺陷：prompt-injection 管理面 timestamptz scan 族（本轮核心）

### 4.1 发现与定位

18:04–18:05+08（部署后 3.4h）211 槽 b 出现 6 条新形态 ERROR：

```
ERROR admin handler internal error op="Failed to get policy"
  caller=prompt_injection_handler.go:241
  err="can't scan into dest[31] (col: created_at): cannot scan timestamptz
       (OID 1184) in binary format into *string"
```

定位链：
1. `admin/prompt_injection_handler.go` 5 处查询（policy / rules×2 共用投影 /
   engines×2 / canary / attack-vectors）把 **11 个 timestamptz 列**投影后
   scan 进 `string` / `*string` 结构体字段；
2. pgx v5 prepared statement（QueryExecMode 默认）对 timestamptz 回
   **binary** 格式，`*string` 目标只放行 text/varchar OID ⇒ **必炸**；
3. handler 起源 `ac8437a33`（**2026-06-29**）；真库 rules 23 行自
   **06-30** 起、policies 1 行自 07-19 起存在 ⇒ **该管理面 5 个读端点
   自上线起每次访问必 500，潜伏 4 个月**——engines/canary/vectors
   生产 0 行（走 ErrNoRows 分支返回默认对象）所以从未显形；今天 18:04
   被首次访问抓到。

### 4.2 与并行会话的实时撞车与调和（本轮工程主体）

我方 18:0x 先落 `::text` 投影修复 + 隔离 schema 真库测试（变异臂 2/2）；
**18:19 并行会话落盘同缺陷的测试文件**（23KB，三件套：前提门/字段门/
AST 门），其方向与我方**相反**（struct 改 `*time.Time`），且 AST 门
**明文禁止** `时间戳列::text` 写法。逐文件调和裁决（沿 §610 撞车纪律）：

| 面 | 裁决 | 理由 |
|---|---|---|
| handler 修法 | **并行版 `*time.Time` 胜出**，我方 `::text` 撤回 | 并行版有两条实质论据：① PG text 输出（空格分隔、无 T）**不是 RFC3339**，移动端 Safari `new Date()` 解析失败；② 可空列 `::text` 得 NULL 扫进非指针 string 仍是 500。我方 `::text` 只保住桌面端 JSON 形态，治标 |
| rules/canary 投影 COALESCE 加固 | 两版并集（description/enabled/case_sensitive/leak_action/notify_on_leak） | NULL 族与 timestamp 族同根（我方夹具的 NULL description 行先暴露此族——生产 23 行恰好全非 NULL 故线上只见 timestamp 族） |
| PUT policy 写载荷 | 并行版 `PromptInjectionPolicyUpdate` 分离 | 响应体时间戳字段不再绑架解码（GET 原样回 PUT 在 *time.Time 下会因非 RFC3339 整包 400——分离后零约束） |
| 真库腿 | **我方隔离 schema 骨架胜出**，并行版测试并入 | LIKE 真表克隆 + 隔离 schema（从不写 public）对生产库安全；并行版并入后 7 子测试（含全 NULL 列、PUT 忽略时间戳） |
| 静态门 | 并行版独立文件 | 字段指针型 reflect 门 + AST 禁 ::text 门 + RFC3339 JSON 门——空库不假绿 |

撞车时序实录（供复用）：并行会话 18:19 落测试 → 18:22 起改 handler →
18:24 把自己的 23KB 测试**精简为纯静态门**（9KB）→ 18:25 **扩充我方的
realdb 测试**（8.9KB→18.7KB，并入其前提门/update 体）——两个会话在
共享 checkout 上**互相看见了对方的文件并自行分工**，无需通信。

### 4.3 验证读数

| 门 | 结果 |
|---|---|
| `go build ./admin/` + `go vet` | ✅ |
| gofmt（三个改动文件） | ✅（并行版两文件先 gofmt -w 再提交） |
| 静态门 3 个（字段指针 / 禁 ::text / RFC3339 JSON） | ✅ PASS |
| 真库腿（211 acc-pg17 隔离 schema，ssh 隧道 TEST_ADMIN_500_DSN） | ✅ 7/7 子测试 |
| admin 全包 `-short` | ✅ 105.9s（单独口径） |
| 变异臂（调和后版本） | ✅ 2/2：字段回退 string ⇒ **编译红**（RFC3339 JSON 门构造即类型错）+ 字段门红；投影回退 ::text ⇒ AST 门红；还原 md5 逐字一致 |
| 我方 `::text` 版变异臂（弃用前） | ✅ 2/2（policy ::text 撤回 ⇒ 真库红、错误与线上逐字同族；description COALESCE 撤回 ⇒ NULL 族红）——留档证明被丢弃方案也曾被门钉住 |

### 4.4 提交

`9d3b190de`（3 文件，+793/−41）已推送 origin/main。

## 五、四步④：部署与验收

| 台 | 决策 | 理由与读数 |
|---|---|---|
| 211 | **已部署 `2929-bb020177`**（蓝绿：槽 b@8783 → **槽 a@8781**） | ★ **决策翻转**：初稿决策为「本轮不重部署」（理由＝保住放大类闭案的 24h tick 锚点 11-02 14:38）。**但用户目标明确要求「使用脚本部署到 211 上，并进行测试验证」**，且该 500 是**线上当前活跃缺陷**（不部署 ⇒ 缺陷继续在线，网页继续报错）⇒ 按用户指令执行 211 重部署。**代价（诚实记）**：新槽 StartedAt `2026-11-01T10:42:11Z` = **18:42:11+08** ⇒ 放大类闭案的 tick 锚点从 11-02 14:38 顺延到 **11-02 18:42**（≈4h，**顺延非取消**，验收仍在明日轮）。验收读数见 §5.1 的 211 行 |
| 245 | 部署 `9d3b190de`（seamless，seq+1） | 见 §5.1 部署记录 |
| 154 | 部署 `9d3b190de`（seamless 经 252） | 见 §5.1 部署记录 |

### 5.1 部署记录（跑后回填）

（见文末回填段）

## 六、遗留风险与登记

| # | 级别 | 事项 | 处置 |
|---|---|---|---|
| L1 | P2 | PG 慢 SQL 不可观测（双仪表全关） | 属主裁决项延续（48h 轮 L1） |
| L2 | P3 | acc-pg17 外部工具打库（llm_usage_stats 族 17:09 波 11 条 + 18 点 3 条，仓内零引用） | 用户侧/运维：外部工具 DSN 配置纠偏 |
| L3 | P3 | 今晚 23:30 自动化 `43481e79` 将按旧锚点报「未达」 | 预期行为，按新锚点（11-02 14:38）解读；下一轮复核其输出 |
| L4 | P3 | 211 放大类闭案：tick ~~11-02 14:38~~ → **11-02 18:42**（211 已按用户要求重部署，新槽 StartedAt 18:42:11+08）后复核 dup=0 | 明日轮验收（**顺延约 4h，顺延非取消**）；本轮中间读数干净（交接尾巴 4 对已定性） |
| L5 | P3 | ~~211 运行树 2ffe5495 落后 HEAD 一个修复提交~~ → **已收敛**：211 运行树 = `2929-bb020177`（含 `9d3b190de`） | 已闭合（§5.1 的 211 行） |
| L6 | P3 | make test-short 的 admin 120s 结构性边缘 | 全量并行时越线、单独跑绿；不改口径，登记；如需根治属 Makefile 调整提案 |
| L7 | P3 | 154 acc-nginx Restarting | 既有登记维持，不擅动 |
| L8 | P2 | **output-compliance 族同根 scan 缺陷（潜伏）** | 见下方展开；本轮**只登记不修**（不同特性域，且当前不显形） |

### L8 展开：output-compliance 族的同根 scan 缺陷（2026-11-01 实测，**潜伏未显形**）

★ 与本轮修的 prompt-injection 族**同根**（pgx timestamptz → binary → Go
string 拒扫），但**落在另一个特性域**，故本轮**登记不修**（避免把两个域的
改动混在一次部署里）。实测读数：

| 端点 | 缺陷面 | 当前是否显形 |
|---|---|---|
| `GET /api/admin/output-compliance/policy` | `scanOutputCompliancePolicy`（:222-242）把 `created_at`/`updated_at`（timestamptz）扫进 Go `string`（:119-120）⇒ 同族 500；`last_check_at` 扫进 `sql.NullString`（:224）⇒ **更隐蔽**：pgx 的 `scanPlanSQLScanner` 在**二进制**格式下把**原始 8 字节**喂给 Scanner ⇒ **不报错，而是把裸时间戳字节当成字符串**（静默乱码） | **否**——`output_compliance_policies` 生产 **0 行** ⇒ 走 ErrNoRows 分支返回默认策略 ⇒ 200。★ 但 `PUT /api/admin/output-compliance/policy`（:280 `updatePolicy` → :300 INSERT）**能造出第一行** ⇒ 造出后该 GET 立刻 500 |
| `GET /api/admin/output-compliance/feedback` | `listFeedback`（:700）把 `created_at` 扫进 Go `string`（:162） | **否**——`output_compliance_feedback` **0 行** |
| `GET /api/admin/output-compliance/keywords` | `scanKeyword`（:451）把 `created_at`/`updated_at` 扫进 Go `string`（:133-134） | **否**——`output_compliance_custom_keywords` **0 行** |
| `GET /api/admin/output-compliance/review-queue` | `listReviewQueue`（:577-580）把 `created_at` 扫进 Go `string`、`reviewed_at` 扫进 `sql.NullString`（:608）⇒ 同族 | **否**——`output_compliance_review_queue` **0 行** |

★ 四张表生产**全部 0 行** ⇒ 与本族 prompt-injection 的
engines / canary-tokens / attack-vectors **同款「空表 ⇒ 缺陷不显形」**
⇒ 与 §七第 3 条通用式互为印证。

★ 修法方向（**与本轮同款**）：时间戳字段改 `*time.Time`（JSON 仍 RFC3339
字符串、可空列如实 null）；`sql.NullString` 那两处必须改 `*time.Time`
（`sql.Scanner` 在 binary 格式下**不报错但静默乱码**，比 500 更难发现）；
其余可空列投影侧 COALESCE 成 schema DEFAULT。
⚠️ 处置时注意 `output_compliance_policies` 的**写路径**（PUT）已存在 ⇒
改字段类型会同时约束 PUT 的解码 ⇒ 宜与本轮一样做**写载荷与响应结构体分离**。


## 七、通用式（收编 LESSONS §611）

1. ★★★★ **pgx v5 的 timestamptz 在 prepared-statement 路径下以 binary
   格式返回，扫进 Go string 必炸**——「列存在、查询对、结构体像对」的
   读路径可以因**扫描计划**这一层整体 500；判据是真库腿（有行）而不是
   结构体形状检查。
2. ★★★★ **潜伏 4 个月的 500 与「今天坏了」的区分靠数据考古**：handler
   起源提交号（06-29）+ 表行 created_at 最小值（06-30）+ 旧槽日志同形态
   零条（页面 4 个月无人访问）——三证合一才敢写「自上线即坏」。
3. ★★★ **空表读端点走 ErrNoRows 分支返回默认对象 ⇒ 缺陷不显形**：
   engines/canary/vectors 生产 0 行所以从未被发现；真库腿必须**铺行**
   （每表至少一行、NULL 与非 NULL 两种形态都盖到）。
4. ★★★ **两个会话在共享 checkout 上修同一缺陷会实时撞车，且能通过
   文件系统互相看见**：本轮无需通信完成了分工（一方静态门、一方真库腿
   + handler）——前提是后动手的一方先 grep 工作树的未跟踪文件。
5. ★★ **`::text` 投影是治标方案**：保住桌面端 JSON 形态但产出 PG 文本
   格式（非 RFC3339，移动端解析不了）且可空列 NULL 仍炸——正解是类型
   对齐（*time.Time）+ 写载荷与响应结构体分离。
6. ★★ **ssh "Connection closed" 先查端口**：本仓 245/154/252 别名全走
   25022，直连 IP 默认 22 会被立即关闭，易误判「机器挂了」。
7. ★★ **grep "queue.*full" 的正则会误匹配 "removed from queue"**：判定
   队列满复发要用精确日志词（"audit queue full"），零命中前先证明量具
   能命中（§610 量具口径先于读数的又一实例）。

---

## 5.1 部署记录（回填）

| 台 | 结果 |
|---|---|
| 211 | ✅ `2929-bb020177`（蓝绿：活跃 b@8783 → **候选 a@8781**，build_seq 2928→2929；新槽 StartedAt `2026-11-01T10:42:11Z` = 18:42:11+08）。部署脚本自带验收全绿：readyz OK（DB+Redis）/ admin SPA 可达 / 网页面 OK（HTML + 入口 JS MIME）/ nginx upstream 8783→8781 已 reload / **公网版本身份 OK（bb020177，第 3 次探到）** / 公网入口 200 / **登录冒烟 OK（admin 连登 6 次无 429）** / 旧槽 b 已停（容器与镜像保留，可 `docker start` 回滚）。迁移：全部 already applied（本轮**无新迁移**，纯代码修复）。★ **缺陷修复验证（双口径）**：<br>· `127.0.0.1:8781`：`GET /api/admin/prompt-injection/policy` → **200**（881B），`created_at='2026-07-19T13:08:25.836952Z'`（**RFC3339** ✓，UTC 渲染＝与库里的 `2026-07-19 21:08:25.836952+08` 同一瞬间）、`last_detection_at=null`（列可空**如实 null** ✓）、`score_threshold_log=3` / `action_on_high_risk='block'`（COALESCE 的 schema DEFAULT 口径 ✓）；`GET .../rules` → **200**，**count=23**（与真库 23 行一致 ✓）。<br>· 公网 `https://llm.itestu.cn`：同两个端点 **200**（policy 完整 JSON 881B；rules count=23）——即**用户报的 URL 已实测修复**。<br>· 部署后 30min 容器日志：`prompt-injection/policy` 与 `prompt-injection/rules` 各 2 次 **status=200**；**`admin handler internal error` = 0**；**`Failed to get policy` / `Failed to scan rule` = 0**（修复前同一批端点是 500）。<br>· **生产数据零改动**：部署后直读 acc-pg17，policy 行 `id=2 / enabled=f / detection_mode=observe / created_at=2026-07-19 21:08:25.836952+08 / updated_at=同` 与部署前**逐字相同** ⇒ 本轮**零写库**（只读路径修复）。 |
| 245 | ✅ `2927-9d3b190d`（总 204s，切换 25s）。验收：admin 登录 200 / token 289B；`/api/system/version` git_sha=9d3b190d ✓；**修复 5 端点全部 200**（policy 有真行 id=2 且 `created_at=2026-07-19T21:08:25.836952+08:00` RFC3339Nano ✓、`last_detection_at=null` 如实 ✓、rules count=23 与 211 生产真值一致 ✓）。首跑失败实录：后台 shell 未带 `$HOME/sdk/go/bin` ⇒ deploy-seamless `go: command not found` 回退 docker 跑法、三 registry 拉不到 `kx-base/golang:1.27-alpine-amd64` 即拒——**后台跑部署必须显式 export PATH** |
| 154 | ✅ `2928-bb020177`（总 226s，切换 28s，经 252 跳板）。验收：admin 登录 200；version git_sha=bb020177（=部署时刻本地 HEAD：9d3b190de 之后并行会话追加了三个 docs 提交——形态表 74140189a / 本审计档 c87696300 / AGENTS 归档 bb0201774；`merge-base --is-ancestor` 实证 **9d3b190de 在其血统内** ✓）；修复端点 policy/rules 200 ✓。**注意：bb020177 当时未推送**——154 生产运行树一度领先 origin，本轮回填提交后推送收敛 |

**两轮会话文档合流实录**（本轮收尾时发现）：并行会话 18:33 把我方工作树的审计档（本文件）、LESSONS §611、AGENTS.md 段落**原样纳入**了它的提交链（md5 比对逐字一致）；其净增量为 web/web-mobile 形态表第 15 条（时间戳形状：RFC3339 字符串或 null）与 output-compliance 同族登记（L8：`output_compliance_policies` 的 updated_at → string / reviewed_at → sql.NullString——写路径已存在，改类型会约束 PUT 解码，宜同款写载荷分离，留下一轮）。

### 5.2 211 部署的**两处工具链坑**（本轮实测 → 已根修）

★ 坑一（**闸门在 macOS 上根本跑不起来**）：`scripts/deploy-test-gate.sh` 头部声明
`# requires-bash: 4`（:139 `declare -A` 关联数组）⇒ **macOS 自带 bash 3.2
直接跑必红**：`declare: -A: invalid option` ⇒ 闸门在**开跑之前**就死
（首次实测 18:2x 即中止在闸门行，RC≠0）。

⇒ ★★ **且「用 bash5 跑 deploy-211.sh」救不了**：`scripts/deploy-211.sh` 三处
硬写 `bash`（:164 deploy-doctor / :182 deploy-test-gate / :186
deploy-seamless-docker 的 `exec bash`）⇒ 解释器被**降回 PATH 里的系统
bash（3.2）** ⇒ **调用方的 bash 根本没被继承** ⇒ 运维越"正确"地用 bash5
调用，闸门照样死在 3.2 上。
⇒ 修法（已落地）：三处统一改 `"${BASH:-bash}"` = **继承调用方当前的 bash**
⇒ macOS 用 bash5 调用即通；Linux 上 `BASH=/bin/bash`（≥4）**行为完全不变**；
`BASH` 未设时回落 `bash` ⇒ 与旧行为一致。
⇒ ★ 自查教训：我第一版把调用写成 `bash "${BASH:-bash}" script` ⇒ **多了一层
`bash`** ⇒ bash5 去「执行」bash5 这个二进制 ⇒ `cannot execute binary file`
⇒ 靠**实跑 --status**（而不是 `bash -n` 语法检查）才抓到 ⇒ ⇒
**「语法过」≠「行为对」，改解释器调用必须实跑**。

★ 坑二（后台跑部署必须显式带 PATH）：与 §5.1 的 245 首跑失败同源——
后台 shell 未带 `$HOME/sdk/go/bin` ⇒ deploy-seamless `go: command not found`
⇒ 回退 docker 跑法 ⇒ 三 registry 拉不到基础镜像即拒。⇒ 211 本轮部署
显式 `export PATH=$HOME/sdk/go/bin:$HOME/.nvm/versions/node/v22.23.3/bin:$PATH`
（前端构建也要 pnpm）⇒ 首跑即过。

★ ★ **本轮这条通用式的更大教训**：**部署脚本里的"解释器/工具链入口"是
跨平台陷阱的高发点**——`bash`（版本敏感）、`python3`（版本敏感）、`go`
（PATH 敏感）、`pnpm`（PATH 敏感）四个入口，每个都在本轮/近轮各咬过一次
⇒ ⇒ **凡是要改部署工具链，必须按"目标机 OS × 解释器版本矩阵"逐项实跑**，
不能只靠 `bash -n` / 语法检查。


三台终态：211 = `2929-bb020177`（槽 a@8781）、245 = `2927-9d3b190d`、154 = `2928-bb020177`——服务端代码三台同源（9d3b190de 血统），差异仅文档提交。
