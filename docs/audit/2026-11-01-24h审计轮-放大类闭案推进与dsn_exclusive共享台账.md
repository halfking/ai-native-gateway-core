# 2026-11-01 24h 审计轮 —— 放大类闭案推进（tick 未到）与 dsn_exclusive 共享台账落地

> 会话：goal 会话（2026-10-31 23:22 起）。承接 48h 轮（`28e0076ef` + `7f0881aee`）全部遗留。
> 固定四步：① origin/main 合并 + worktree 钉提交全量读数 → ② 优先收口项 → ③/④ 结构项与调查卡 → ⑤ 常规巡检/部署 → ⑥ 文档收口。

## §0 一句话结论

**① test-short 351 包全绿（钉 `73c8c8fd5`）+ guards 抓到并修正一处上游真红（admin 路由快照漂移，只在 ursmcheck 慢守卫包可见）；② 放大类闭案判据（新槽 24h tick）今晚物理未到（tick = 11-01 20:59），部分窗读数干净（4 行启动探针 lt10min=0/0），自动化 `automation-43481e79` 保持不动、明晚 23:30 验收；③ dsn_exclusive 共享台账（TSV）落地（`e619cef44`），双向对账 + 变异臂 4/4 检出；④ all-0-candidates 调查卡闭卡：4 候选 = `v_routable_credential_models.is_routable` 视图闸的输出，无候选上限、无 URSM 截断，35 笔失败是整池真红的池可用性窗，fail-fast 行为正确，无需动路由语义；⑤ 三台本轮不重部署（运行树与 HEAD 服务端代码零增量 + 211 重部署会重置闭案锚点），偏离理由如实登记。**

## §1 四步① 全量读数（worktree 钉 `73c8c8fd5`）

- 共享树开工时 `git status` 干净 ✓（HEAD `7f0881aee`）；`git fetch` 快进合并 origin/main 5 提交 → `73c8c8fd5`（内容：`cmd/tools/backfill-session-opened` 新工具 + `deploy-seamless-docker.sh` build_seq 冻结根修 + 两份审计档，**服务端代码零变更**）。
- worktree：`/Users/niuer/workspace/ai-native-tools/llm-gateway/wt-20261101-audit`（detached @ `73c8c8fd5`）。
- **`make test-short`：351 包全绿，RC=0**（后台跑；坑：worktree 里 make 的 PATH 不带 `~/sdk/go/bin` ⇒ 首跑 `go: command not found` RC=127，显式 `export PATH="$HOME/sdk/go/bin:$PATH"` 后重跑通过）。
- **guards 首跑抓到一处真红（pristine `73c8c8fd5`）**：`TestAdminRoutesSnapshotMatchesGenerator`——`/api/admin/annotations` 一行 handler.go:1524（生成器）vs 1519（快照）。归属：`426393659`（10-31 拦截归因轮）改 `admin/handler.go` 未重生成快照（快照停在 `111c2c1be`，10-30 第 8 轮续）。**为什么 test-short 没看见**：该门只在 `scripts/ursmcheck`（test-short 显式排除的慢守卫包）⇒ 红从 10-31 上午潜伏到现在，guards 一跑就显形。修法 = 按生成器口径重生成快照（`e619cef44`：290 行 file:line 漂移对齐，数据行数 598 不变，头部自述一致）；重生成后快照三门绿。
- guards 慢腿口径教训：ursmcheck 全包实测 ~894.8s（worktree 1800s 口径），自设 `-timeout=580s` 会把 `TestStructuredRegistryHollowoutReadersRejectEmptyRegistry`（子进程密集型）掐成假红 panic——**慢守卫包的超时必须用 Makefile 口径（1800s）**，自设超时的「红」先对照已知全量口径再定性。
- worktree `web/` 先 `pnpm install --frozen-lockfile`（PNPM_RC=0，2.9s）再 `make guards`。
- ★ 读数分两档注记：**基线全量读数（test-short 351 绿 + guards 抓红）在钉 `73c8c8fd5` 的干净 worktree 取得**；主 checkout 在本轮中段出现**并行会话 WIP**（`domains/streaming/` 3 文件 + 2，后由并行会话自己提交为 `9485f53bc`/`d12ebe07b`/`3c4f9d522` 并推送）⇒ 主 checkout 只跑定向门验证（§3/§5.2），如实标注。终态树（含并行 3 提交 + 本轮 `e619cef44`）的**全量复测**在 worktree@`e619cef44` 上重跑，读数见 §6。
- ★ 本轮读数有效性注记：全量读数全部在钉住提交的干净 worktree 上取得；主 checkout 在本轮中段出现**并行会话 WIP**（`domains/streaming/{messages,request_meta,responses,native_pair_test}.go` + `web/public/menu-config.json` 5 文件，非本轮改动，不碰不还原）⇒ 主 checkout 上只跑了受影响两包的定向验证（§3），并如实标注「跑在含并行 WIP 的树上」。

## §2 四步② 优先收口：放大类闭案（tick 未到，自动化保持）

- `automation-43481e79`（明晚 23:30 触发，一次性）**runCount=0 未执行**——按 goal 授权自己跑同款 lag 分桶 SQL。
- 211 活跃槽：`llm-gateway-211-a`（唯一容器，healthy），StartedAt = **2026-10-31 12:59:40 UTC = 北京 20:59:40**（48h 轮部署锚点），运行镜像 `llm-gateway-211`（= `2.5.8-28e0076e`，见 48h 档 §6 部署读数）。
- **部分窗读数**（窗口 = 新槽起跑 10-31 20:59:40+08 起，`trigger_kind='scheduled'`，lag `over (partition by credential_id, raw_model_name order by created_at)`，同第 12 轮/48h 轮口径）：
  - total **4 行**（全部 20:59:42–53 的启动探针）：cred 4 `deepseek-v4-pro`（404）/ cred 8 `z-ai/glm-5.3`（410）/ cred 19 `deepseek-ai/deepseek-v4.1-flash`（429）/ cred 49 `deepseek-v4-pro`（404）；
  - **lt10min = 0 行 / 0 对** ✓；10-60min / 1-4h / ≥4h 桶全 0（窗内尚无第二轮）。
- 近 24h 对照（10-30 20:59 → 10-31 20:59）：小时分布与 48h 档 §2.3 **逐字一致**（30/3/115/17/15/29/39/1/16/45），零新增异常；20 时尾桶 7 行 = 旧槽尾 3 行 + 新槽启动探针 4 行。
- **判定的诚实边界**：闭案判据是「**当前槽 24h tick**（11-01 ~20:59）dup=0」——启动探针 ≠ 24h tick（第 12 轮原文判据）。tick 明晚才打，**本轮不写「放大类闭案」**；部分窗干净如实入档，终验由 `automation-43481e79`（明晚 23:30，tick 后 2.5h，无需等待循环）执行。启动探针目标数 30→16→4 的逐轮收缩与本轮归因：探针调度按「last_run+间隔到期」挑靶，19 时重开轮刚扫过大部 ⇒ 到期靶自然少；与放大类无关（lt10min=0）。

## §3 四步③ dsn_exclusive 结构项：共享台账（TSV）落地

**背景**（48h 轮遗留 2）：dsn_exclusive 族散在两份 Go 文件里——ursmcheck 的 `datasourceEnvExemptions`（14 条，混着非 DSN 名）与 sql/schema 的 `destructiveGuardedDB`（2 条）；`TestDestructiveDSNsRegisteredDsnExclusiveInURSM` 只能做单向正则对账（本门 ⊆ ursmcheck），反方向「免语义判断自动化」需要共享数据文件。

**本轮落地**：

1. **新共享台账 `scripts/audit/dsn_exclusive_registry.tsv`**：dsn_exclusive 族 9 条（TRACE_RETIREMENT / TEST_MAAS_BILLS / GATEWAY_CANONICAL / LLM_GATEWAY_FEATURE_STATS / TEST_SESSION_INGEST / GUARD_IT / ROUTEINCIDENT / REPORT_E2E / TEST_SESSION_SETTINGS），每行 `name<TAB>reason`，头部注释写明两侧消费方与「增删必须两侧同改」纪律。
2. **ursmcheck 侧**（`scripts/ursmcheck/datasource_env_test.go`）：字面量 map 缩成 `builtinEnvExemptions`（5 条非 DSN/特殊接线名）；`loadDsnExclusiveRegistry` 装载 TSV（**牙齿**：文件缺失/格式坏/名字形状/重名/空理由/行数下限 ≥5 ⇒ t.Fatal）；`datasourceEnvExemptions(t)` = 字面量 ∪ TSV（4 个消费点统一走它 ⇒ TSV 行自动享受「与事实源不相交 + 理由非空」既有纪律）；新门 `TestDsnExclusiveRegistrySharedSpine`（装载牙齿 + 与事实源不相交）；门 1 增**死条目棘轮**（TSV 名字仓内零真库腿引用 ⇒ 红——堵「删 fixture 忘删台账」）。
3. **sql/schema 侧**（`sql/schema/integration_gate_test.go`）：旧 `TestDestructiveDSNsRegisteredDsnExclusiveInURSM`（Go 源码正则单向）重写为 `TestDestructiveDBsRegisteredInSharedDsnExclusiveTSV`，**双向集合对账**：(a) `destructiveGuardedDB ⊆ TSV`；(b) `TSV ⊆ lanes`（lanes = destructiveGuardedDB ∪ dedicatedSessionDB 两条非注入车道）——(b) 正是 2026-10-31 第三次反向报红（MAAS 台账记错边）的机器化。

**结构要点（本轮踩出来的，第一版被实测打红）**：TSV 的对账目标集不能是任何单一注册表——① session ingest/settings 两名走 `dedicatedSessionDB`（专用 runner）车道而非 `destructiveGuardedDB`；② 更关键的是 sql/schema 主门扫描器的形状表（`isDBCredentialEnvName`）只认 `_DATABASE_URL/_DB_URL/_PG_URL/_ISOLATED_DB_URL` 后缀 + `TEST_PG_DSN`，台账里 5 条 `_DSN` 后缀的名字（TRACE_RETIREMENT_TEST_DSN 等）**对主门不可见**、从未需要车道。第一版「TSV ⊆ lanes」全量断言当场被这 5 条打红 ⇒ 正确形状是「**TSV ∩ 扫描器形状 ⊆ 车道并集**」，且形状判定抽包级 `isDBCredentialEnvName` 与主门共用同一把尺（形状表一旦扩到 `_DSN`，对账门当场报红强制补车道——形状规则与台账的耦合被显式钉住）。

**验证（主 checkout 定向，含并行 WIP 树上）**：vet ✓；快照三门 ✓；`TestDestructiveDBsRegisteredInSharedDsnExclusiveTSV` + `TestGateInjectsEveryDBCredentialName` ✓；DSN 覆盖门族（`TestRealDBLegsUseDeclaredDSNEnvNames` / `TestDsnExclusiveRegistrySharedSpine` / `TestDatasourceEnvHasNoDeadEntries` / `TestDSNEnvMatcherSelfCheck`）✓；`go build ./...` ✓。变异臂 4/4 见 §5.2。终态树全量复测见 §6。

## §4 四步④ all-0-candidates 调查卡：闭卡（无需动路由语义）

**卡面**（48h 档遗留 3）：deepseek-v4.1-flash 库内 ~8 条 available=t 绑定（cred 10/19/23/43/49/50/25/61），resolve 只出 4 候选——4 从哪来（候选上限？视图过滤？策略？）。

**取证**（2026-10-31 23:3x 211 实测，`docker exec acc-pg17`）：

1. 候选链路代码定位：`resolveCandidatesForRequest` → `GetCandidatesByModality` → `candidateQuerySQLBaseTemplate`（`provider/client.go`）；URSM 过滤是第二道门（`domains/streaming/executors/router.go:437` 全拒 WARN）。「4」产生在**第一道 DB 候选层**。
2. DB 候选层的唯一变化闸 = **`v_routable_credential_models.is_routable`**（视图定义实测）：折叠 8 道闸——provider enabled/manual、credential status/lifecycle/manual、`availability_state='ready'`、quota 三态、pm.available、cmb.available、`unavailable_reason != 'manual'`、health_status、以及 **node_probe_state 活跃冷却窗**（`last_direct_ok=false AND next_retry_at > now()`）。其余闸（tenant=default、routing_scope=all、canonical active、mps broken_confirmed 无行）对该模型全部恒绿。
3. 15 条匹配绑定的逐条归因（当前时刻）：**is_routable=t 仅 3 条**（cred 3/49/50）；其余按 reason 分桶——`node_probe_failed` ×3（cred 8/18/19，探针 429 冷却窗内）、`availability_rate_limited` ×1（cred 4）、`availability_suspended` ×2（cred 61/63）、`binding_unavailable` ×1（cred 25）、`provider_manual_disabled` ×3（cred 10/11×2）、`credential_manual_disabled` ×1（cred 43）、`lifecycle_disabled` ×1（cred 23）。
4. **「4 从哪来」定案**：不是候选上限（候选 SQL 无 LIMIT）、不是 routing_policy（927 单例，不截候选）、不是 URSM 截断（URSM 在 DB 层之后）——**是 DB 候选层视图闸的时变输出**：事故窗（08:37–11:18）视图还认为 4 条绿（其中至少 1 条此后被健康闸收红，如 cred 4 的 rate_limited / cred 8/18/19 的探针冷却窗），到本轮读数时刻只剩 3 条。容器日志已随蓝绿替换湮灭（第 12/48 轮同款），4 条的具体名单不可回溯，但机理链完整闭合。
5. **判定**：available=t 只是八个闸之一，把「绑定在目录里」误当「绑定可路由」是卡面的措辞偏差；那 35 笔失败是**整池真红的池可用性窗**（健康闸拦下的是真实 429/403/suspended 证据 + 人工/生命周期决断），executor fail-fast 行为正确。**「availability 过滤后再截断」的语义变更评估：前提不成立，不做**（原卡已预见需独立授权——现在连提案都不需要）。观测面可选改进（留给属主，不动路由语义）：routing_resolve 的零候选/全拒路径把每条被 DB 层排除绑定的 `unavailable_reason` 带进日志（URSM 全拒 WARN 已有 view_sample，DB 层排除发生在更早、无日志）——下次同类窗口可自解释。

## §5 四步⑤ 常规

### 5.1 窗口分支清退与巡检

- 分支：本地+两远端均只有 `main`——**无窗口分支，无清退项**。
- 245：`llmgo-245-canary@8781` active/running，current = `2922-28e0076e` ✓；acc-nginx Up 44h healthy；alertmanager/prometheus 单元 active（webhook 静默属用户侧动作，见 §7）。
- 154：`llm-gateway-go-canary@8781` active/running（8781 在听），current = `2923-28e0076e` ✓；**acc-nginx 仍 Restarting（崩溃循环持续，登记不擅动**——内部容器无主机端口绑定，服务面不受影响，第 12/48 轮同判定）。

### 5.2 变异臂（③ 共享台账）：4/4 检出，还原后 md5 逐字一致

| 臂 | 变异 | ursmcheck | sql/schema | 检出点 |
|---|---|---|---|---|
| M1 | 删 TSV 行（TEST_MAAS_BILLS_DATABASE_URL） | **RC=1** | **RC=1** | 覆盖门「名字失豁免」+ (a)「注册表有它台账没有」 |
| M2 | 删 TSV 文件 | **RC=1** | **RC=1** | 两侧装载器 read error（台账本体消失必须响） |
| M3 | 摘 `destructiveGuardedDB` 的 MAAS 条目 | （不动） | **RC=1** | (b)「TSV 里 MAAS 无车道」——正是 10-31 第三次反向报红的镜像方向 |
| M4 | 加假行 TEST_FAKE_SPINE_DATABASE_URL | **RC=1** | **RC=1** | **死条目棘轮**「仓内零真库腿引用」+ (b)「形状内无车道」 |

还原后三文件 md5 逐字一致 ✓。M4 证明死条目棘轮的必要性：没有它，假行只在 sql/schema 一侧红，ursmcheck 侧的垃圾桶无人看守。

### 5.3 部署决策：本轮三台不重部署（偏离固定流程，理由如下）

- **零增量证明**：`git diff --stat 28e0076ef..73c8c8fd5` = 4 文件（新独立工具 + 2 审计档 + deploy-seamless-docker.sh），`cmd/gateway internal domains provider` 零变更；运行树 = 三台全在 `28e0076e`（211 槽 a / 245 `2922` / 154 `2923`）⇒ 重部署的功能增量 = **零**。
- **211 重部署是负收益**：会把 24h tick 锚点从 11-01 20:59 推到重部署时刻+24h，`automation-43481e79`（明晚 23:30，等 tick 上限 2h）将写「tick 未到」⇒ 放大类闭案整体推迟一天——恰与 goal ② 的优先收口方向相反。
- 本轮唯一代码变更（③ TSV）是**测试基建**，不进任何部署二进制 ⇒ 245/154 重部署为纯搅动。
- `22085ad82`（输出合规修复 + 迁移 926）经 `merge-base --is-ancestor` 实证**已在** 28e0076ef ⇒ 「926 待部署」的旧状态已由 48h 轮消解，无积压。
- 部署产物脏文件：主 checkout 的 `web/public/menu-config.json` 脏是**并行会话 WIP**（非部署产物，不碰）；本轮未执行部署 ⇒ 无部署产物需要还原。

## §6 提交与读数汇总

（读数回填节——见 §5 表）

## §7 遗留与移交

1. **放大类闭案最后一环**（不变）：`automation-43481e79` 明晚 23:30 自动验收 211 当前槽 24h tick（StartedAt+24h = 20:59，2.5h 余量）；dup=0 ⇒ 下轮在审计档与 AGENTS 正式写「放大类闭案」。
2. **用户侧动作（两项，不代做）**：245 alertmanager webhook 静默（48h 档 §7；alertmanager 单元 active 但 webhook 不出网）+ kb-importer 外部工具坏转义+盲插撞主键（48h 轮日志审计登记）。
3. **154 acc-nginx 崩溃循环**：维持「登记不擅动」（本轮实测 Restarting 持续）。
4. **all-0-candidates 观测面可选改进**（§4.5，属主裁决）：DB 层排除原因进日志。
5. 既有待决不动：pg_stat_statements preload（需 DB 重启授权）、goacos 31s vs 25s、dropped_events wire 级复验（需 admin JWT）。

## §8 下一轮提示词

（文末统一给）
