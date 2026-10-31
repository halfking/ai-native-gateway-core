# 2026-10-31 第 11 轮遗留收口轮 —— sanitize 审计攒批 + resolver best_rank 部署判据 + AGENTS 归档核对

> 会话目标（goal 三项）：① 部署 resolver best_rank 分层到 211（干净 worktree + LLM_GATEWAY_ROOT 钉根；
> 部署判据=候选集合对拍：glm-5.3-flash 的 selected=z-ai/glm-5.3-flash 且 WARN 静默，不预设 WARN=0；
> 同族 minimax-m2.7-highspeed 一并复核）；② 共享主树同步到 ≥59274e2db 后核对 AGENTS 归档拆分无内容丢失；
> ③ 第 11 轮遗留（sanitize audit queue full 44250/3h 真实丢事件、同伴对比 1.00 家）择一收口。
> **三项全部落地**；③ 选了 sanitize queue full（同伴对比 1.00 家是凭据池结构问题，见 §7 遗留）。

## §0 结论总览

| 项 | 结论 | 证据 |
|---|---|---|
| ② AGENTS 归档拆分 | **无内容丢失**（字节级） | old[3941:16698] == archive[5:12762]（diff rc=0，12758 行逐字一致）；old[1:3940]==new[1:3940]；archive@拆分提交 == archive@HEAD（后续提交只增不改） |
| ① 部署 | **d953b55e3 已上线 211，槽 b(8783) 接管** | 版本三件套：活跃槽 b + `2.5.8-d953b55e-20261031-211` + runtime_role=active；干净 worktree `/tmp/lgw-deploy-bestrank-1031` + `LLM_GATEWAY_ROOT` 钉根 |
| ① 部署判据 | **候选集合对拍四臂全 PASS** | 同源探针（本仓 `modelbinding.ResolveRawBinding`）对 211 真库：cred 19 glm-5.3-flash → selected=`z-ai/glm-5.3-flash` + warns=0 + 特异性 Debug=1；cred 62 裸名 exact 不变；cred 21 minimax-m2.7 → **warns=1（真歧义保留，未预设 WARN=0）**；cred 21 minimax-m2.7-highspeed → selected=`MiniMax-M2.7-highspeed` + warns=0 |
| ③ sanitize queue full | **攒批修复已上线** | 部署后 12m+ 窗口 `sanitize rule audit queue full` = **0**（部署前 44250 条/3h）；审计事件持续落库（20min 窗口 26 fingerprint 更新）；dropped 计数已上 status API |
| 提交 | `d953b55e3`（攒批本体）+ `7eb19712d`（dropped_events 去 omitempty），均推 origin/main | 变异臂 6/6 检出；sanitizerules/security/sanitize/admin/outputcompliance 四包绿；-race 绿 |

## §1 ② AGENTS 归档拆分核对（字节级，三重证据）

拆分发生在 `dfcb1bc36`（AGENTS.md 16698 → 3942 行，归档文件 12762 行）。核对法（不信任「拆分提交自己声称无丢失」）：

1. **移出段 == 归档正文**：`diff <(sed -n '3941,16698p' old_ag.md) <(sed -n '5,12762p' archive_at_split.md)` → **rc=0**。
   区间锚定：diff hunk `@@ -3938,12761 +3938,5 @@` 的前 3 行是上下文，真正移除起点是 old 的 3941 行；
   归档文件头 4 行（标题 + 空行 + 拆分说明引用块）后正文从第 5 行开始。12758 行逐字一致。
2. **保留段零改动**：`diff <(sed -n '1,3940p' old) <(sed -n '1,3940p' new)` → rc=0；new 尾部只追加归档指针注记。
3. **归档在拆分后未被改动**：`archive_at_split == archive_at_HEAD`（12762 行 byte 级相等；
   后续 `3b22ad922`「R 标记索引+体积棘轮门」与 `07a5ac941`「补记维护轮结论」都只写在 AGENTS.md 当前状态段）。

目标书写的「12441 行」是第 9 轮审计时点的 WIP 读数；落库终态 12762 行，以上述三重对拍为准。

## §2 ① 部署经过（含两次并行冲突的正确处置）

1. **开工确认**：fetch 后共享主树 = `47e4f552c`（≥59274e2db，is-ancestor 实证），工作树干净；
   三条并行 WIP 已收进 stash（stash@{0} = r107-inflight-sync-20261031）。
2. **第一次部署尝试 FATAL**：本地部署锁 `/tmp/kx-llm-gateway-deploy-211.lock` 被并行会话持有
   （pid 11781 存活，树 `/tmp/lgw-round-1031c`，commit=13149a8af）。按锁纪律**等待、不抢锁**。
   ⚠ 外层 `echo DEPLOY_EXIT=$?` 让复合命令 exit=0——**部署 FATAL 与命令 exit 0 同时发生**，
   靠读日志抓到（教训：后台命令的 rc 必须来自部署脚本本身，不是外层 echo）。
3. **并行部署结果**：`13149a8af` = 我的 HEAD + 1 个 web 收口提交（**包含 resolver 修复**），
   同规格部署（干净 worktree + 钉根）成功，槽 a 接管。**此刻不再重部署我的 47e4f552c**
   （真子集，零差量 churn）——等 ③ 修复合入后一次部署同时覆盖 ①③。
4. **③ 落地推送后**：从我的 worktree（branch fix/sanitize-audit-batch = d953b55e3，
   干净 + 钉根）部署 → **槽 b 接管**，迁移 probe 含 927 全过。
   蓝绿状态机自身完成候选健康验收后切流。

## §3 ① 部署判据：候选集合对拍（两层证据）

### 3.1 数据层：211 真库候选 SQL（resolver 同款 rank 投影，全凭据扫描）

- **glm-5.3-flash 族**（变体 `['glm-5.3-flash','glm-5-3-flash','glm-5.3','glm-5-3']`，
  与 `modelname.NormalizeRouteKeyAliases` 实测输出一致）：19 个凭据有候选；
  **每一个双候选凭据都是 `{特异=rank1, 宽泛=rank3}` 形态**（含审计主角 cred 19：
  `z-ai/glm-5.3-flash=rank1, z-ai/glm-5.3=rank3`）⇒ 首位严格更特异 ⇒ 全族静默。
  唯一例外 cred 61：三个 rank1 候选（`glm-5.3-flash`/`free/glm-5.3-flash`/`glm/5.3-flash`）
  ⇒ 同分 ⇒ **仍 WARN（真歧义，正确保留）**。
- **minimax-m2.7 族**（变体 `['minimax-m2.7','minimax-m2-7']`）：双绑定凭据（21/41/42/75）
  = `{MiniMax-M2.7=rank1, MiniMax-M2.7-highspeed=rank1}` ⇒ 同分 ⇒ WARN 保留——
  与立项卡 §2.6 边界预测逐字一致（「若两候选 rank 相同则仍应 WARN（真歧义，不同模型名）」）。
- **minimax-m2.7-highspeed 请求位**（变体含剥壳 `'minimax-m2.7'` 落 rank3）：
  `{MiniMax-M2.7-highspeed=rank1, MiniMax-M2.7=rank3}` ⇒ 特异胜出 ⇒ 静默，
  **同族随分层自动受益**如立项卡所判。

### 3.2 行为层：同源探针（本仓 `modelbinding.ResolveRawBinding` × 211 真库，slog 捕获）

| cred | requested | selected | WARN | 特异性 Debug | 判定 |
|---|---|---|---|---|---|
| 19 | glm-5.3-flash | **z-ai/glm-5.3-flash** | **0** | 1 | **PASS**（判据本体：selected 正确 + WARN 静默 + Debug 留痕证明走的是新分支） |
| 62 | glm-5.3-flash | glm-5.3-flash（exact） | 0 | 0 | PASS（裸名 exact 路径零变化） |
| 21 | minimax-m2.7 | MiniMax-M2.7 | **1** | 0 | PASS（**真歧义 WARN 保留**——证明「不预设 WARN=0」的另一半：机器没坏，是判据分化了） |
| 21 | minimax-m2.7-highspeed | **MiniMax-M2.7-highspeed** | 0 | 1 | PASS（同族静默 + selected 正确） |

探针为临时程序（部署后即删），但它是**与事实源同源的量具**（直接调用部署树里的 resolver 实现
对生产库执行），四臂断言含「selected 值、WARN 计数、Debug 在场」三面。复现法已写入本档 §3。

### 3.3 部署后生产日志（12m 窗口）

- `ambiguous normalized model binding` = **0**；
- `sanitize rule audit queue full` = **0**（部署前 ≈246/min）；
- glm-5.3-flash 真实流量 100 行，`audit: request completed … success=true` 实证
  （当前流量走 cred 62 裸名 exact 路径，行为面不变）。

## §4 ③ sanitize audit queue full 收口（攒批写）

### 4.1 根因（比第 11 轮文档的「单条写入速率低」更具体）

`PostgresStore.RecordEvent` 每条事件付一次：`BEGIN` → **跨实例 advisory 锁
`pg_advisory_xact_lock(741968280105)`** → upsert → **保留期 DELETE
（`ORDER BY last_seen DESC,id DESC OFFSET $1`，O(保留水位) 扫描）** → `COMMIT`。
`observeRule` 每命中 1~2 条 ⇒ 生产速率结构性高于排水速率 ⇒ 有界队列（默认 256）持续满
⇒ `Record` 非阻塞丢弃（`dropped++` + WARN）。队列大小不是瓶颈，**排水路径的单条事务成本才是**。

### 4.2 修法（语义不变，只提排水速率）

- `Manager.auditWorker` 攒批：`drainBatch` 非阻塞取（首条 + 立即可取的至多 127 条），
  洪峰每轮整批、稀疏流量零延迟增量（单条形状与旧路径完全一致）；
- 批路径经**可选接口** `BatchEventWriter.RecordEvents`：PostgresStore 单事务
  （一锁 + multi-VALUES upsert + **每批一次**保留期 DELETE）；MemoryStore 同语义；
  第三方 Store 无该接口 ⇒ 回落逐条（既有行为零变化，桩测试钉住）；
- **批内重复 fingerprint 先聚合**（count=sum、last_seen=max、first_seen=min）再 upsert；
  ON CONFLICT 增量 = `count + EXCLUDED.count`（等价于逐条 count+1 的批形态）；
- `writeEvent` 的 AuditEnabled 逐条过滤语义原样保留（关审计时已入队事件在写时丢弃）；
- `Instance.DroppedEvents` 进 `Status()` ⇒ admin status API 可观测（此前只能翻日志）；
  `7eb19712d` 去 omitempty：**显式 0 证明计数已接线**（缺席与静默零不可分辨）。

### 4.3 落地中抓到的真缺陷（变异臂 M6 的由来）

首版 Postgres 批 INSERT **没有把 `count` 放进 INSERT 列表**——表定义
`count BIGINT NOT NULL DEFAULT 1`（migration 866）⇒ 新 fingerprint 的聚合值 k 在
fresh-INSERT 分支被默认值 1 吃掉、conflict 分支 `+EXCLUDED.count` 也只加到默认 1
⇒ **批内同 fingerprint 的 k-1 次命中无声丢失**。触发链：M3 变异臂（memory 聚合退化）
首轮 rc=0 假绿 ⇒ 逼出「聚合语义必须落 store 直测」⇒ 写直测时顺藤发现 Postgres 侧缺 count 列
⇒ 修法 = INSERT 列表带 count（12 参/行）+ sqlmock 断言聚合参数（5+1 两行）。

### 4.4 测试与变异

- `manager_audit_batch_test.go` 9 例：批内聚合（5+3）、洪峰批粒度（≤128 且 >1 且零丢弃）、
  队列满丢弃计数 + Status 面暴露、审计开关逐条过滤、无接口回落路径、
  Postgres SQL 契约（multi-VALUES + `+EXCLUDED.count` + count 列参数 + 保留期 DELETE）、
  空 batch no-op、跨批聚合（3+2=5，单批新键=4）、批大小契约钉；
- **变异臂 6/6 全检出**（M1 批粒度单条化 / M2 conflict 退常量 1 / M3 跨批聚合退化
  `+=`→`++` / M4 drop 不计数 / M5 批路径丢审计开关 / M6 INSERT 丢 count 列），
  每臂还原后 md5 逐字一致；包全绿 + `-race` 绿 + 异步时序测试 -count=10 绿；
- 四依赖包绿：sanitizerules / security/sanitize / admin(62s) / outputcompliance。

### 4.5 生产读数（部署后）

- `sanitize rule audit queue full` = 0（12m+ 窗口）；
- 审计事件持续落库：20min 窗口 26 个 fingerprint 更新（`sum(count)` 口径为累计值，不逐窗口清零）；
- status API（铸 JWT 41 super_admin 通道）：`version=2, error=''`，manager 健康；
  `dropped_events` 字段在部署树=omitempty 形态（0 值缺席），`7eb19712d` 已推 main，
  下次部署起显式 0 上屏。**本轮未再触发部署**（零差量不值得一次蓝绿）。

## §5 一处如实登记：一次原因未定的单次测试 FAIL

`7eb19712d` 提交链里 `go test | tail -1` 报过一次 `FAIL`（细节与 rc 被管道吞掉）；
随后 3 次全包 + 10 次异步压力 + 1 次 `-race` 全绿，未复现。最可能解释：部署刚结束机器高载下
异步轮询 3s deadline 的单次超时。**已按「不假装没发生」登记**；教训（管道吞 rc + 吞细节）进 §7。

## §6 工具与操作记录

- 部署树：`/tmp/lgw-deploy-bestrank-1031`（detached→branch `fix/sanitize-audit-batch`，
  部署时干净、`LLM_GATEWAY_ROOT` 钉根）；共享主树全程未动（只在收尾提交 docs 时经我的 worktree 推送）；
- 候选 SQL：`/tmp/probe-candidates2.sql`（内联数组字面量；CTE 传参形态在本机 psql 路径下
  类型推断出岔 `text = text[]`，内联字面量绕开）；
- 临时探针（已删）：`cmd/probe-resolver`（pgx 直连 + slog 捕获）、`cmd/mint-token`
  （`admin.SignToken(41,…)`，token 不落 transcript）、`modelname/variants_probe_test.go`；
- 隧道：`ssh -L 15432:127.0.0.1:5432 211`（acc-pg17 仅绑 127.0.0.1:5432）；
- 部署构建必脏产物：`web/public/menu-config.json`（时间戳）→ 部署后 `git checkout --` 还原。

## §7 遗留（本轮明确没做）

- **同伴对比 1.00 家（第 11 轮另一遗留）未动**：24h canonical 平均 1.45 家、新窗口 1.00、
  0 个 ≥2 供应商 ⇒ 结构性凭据池问题（245/154 维持不开）。本轮选收 sanitize queue full
  （数据完整性受损 + 可修）；同伴对比需扩凭据池，属运营判断。
- `dropped_events` 显式 0（7eb19712d）已在 main，211 线上仍为 omitempty 形态，下次部署自愈；
- sanitize 审计的批量上限 128 为常量（未做成 Options 可调）——洪峰极端形态如再超排水能力再议；
- 第 11 轮 §3 的「20:39 前后流量/规则形态变化溯源」未做（生产增速触发面取证）。

## §8 通用式（→ LESSONS §596）

见 LESSONS.md §596（六条：排水侧单条写路径 / 聚合列必须进 INSERT / 终态计数分不清聚合与累加 /
可选接口的桩不能两头实现 / 观测面显式零 / 管道吞 rc）。
