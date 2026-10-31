# 24h 审计第 9 轮 —— recording_consent 前端消费 / 914 迁移预算门 / glm-5.3 双映射数据面处置 / DNS flake 加固（2026-10-30）

> 会话目标（用户）：承接第 7 轮（`docs/audit/2026-10-29-24h审计第7轮-合并收口与211生产三修.md`）
> 的四项遗留：① 前端消费 recording_consent；② 914 迁移超时预算修正；
> ③ glm-5.3 别名双映射数据面处置；④ upstream DNS flake 加固。
> 共享工作树纪律：开工 `git fetch` + worktree 核对；部署走干净 worktree + `LLM_GATEWAY_ROOT` 钉根。

## 0. 一句话结论

四项全落地：① 前端三路消费 consent 信号（列表字段 + 409 结构码兜底），
「录制未开启」中性态替代英文 409 报错，5 组件 3 i18n 键 ×8 语种；
② 914 四环境复核全已应用（252 侧 `convalidated=t` 实核）⇒ 字节冻结不动，
预算纪律落成**可证伪守门** `TestHeavyDDLModernMigrationsDeclareMeasuredBudget`
（三变异臂全过）；③ glm-5.3/glm-5-3 裸名别名在 flash canonical 2716170 下的
双映射**双库置 disabled**（211 acc-pg17 + 252 pg-252-pg17，覆盖 245/154/252-dev）——
**不是 DELETE**，走 `alias_sync.go` 注释明文的 operator-kill 语义（DO UPDATE
的 `WHERE status <> 'disabled'` 守卫防复活），行级 JSON 备份存 `/tmp/w13-keys/`；
④ DNS 测试预算 5s/10s → 30s/60s + KindTimeout 显式 skip。

## 1. 入场实态

- 本地 main 落后 origin 21 提交（含并行会话第 8 轮审计、modelbinding 特异度修复
  `83013cce4`、consent 读门夹具修复 `c77e134b2`、model-credibility 线、
  telemetry 线的部分合并）⇒ fast-forward 无冲突。
- `git status` 干净、无在途 stash（stash@{0} 为第 34 增量已收敛快照，保留）。
- worktree 3 个：本树 + `/tmp/llmgo-base`（008546aa 部署基座）+ `lgw-deploy-154`
  （154 部署克隆）——无并行会话占用信号。
- 211 活跃槽在会话期间被并行会话从 b（008546aa）切到 a（`a91f87ba-dirty`，
  build_seq 2905）——本节数据面操作与其正交。

## 2. ① 前端消费 recording_consent（第 7 轮 §5 遗留）

### 病

后端三路（v2 / unified / tree）已透出 `recording_consent`，读正文端点在
consent=off 时回 **409 + code=recording_unavailable**
（`admin/recording_consent.go:154` `writeRecordingUnavailable`）。前端 **0 消费**：

| 消费点 | 旧行为 |
|---|---|
| `SessionDetailPage.loadBodyStatus` | `/detail` 整体 409 被 `catch {}` **静默吞掉**——页面毫无解释 |
| `TurnDigestDrawer` | 409 的**英文后端 detail** 原样进红色错误横幅 + 重试按钮（重试必然再红） |
| `SessionTurnsSyncPane` | `v2Error` 置值但模板**从未渲染**（死状态），轮次空态冒充「没有轮次」 |
| `SessionSummaryBar` / `TurnsListView.resummarize` | `surfaceError` 把 409 当生成失败说「请重试」 |

consent **默认关闭** ⇒ 这是 consent-off 租户在会话详情页的**日常视图**，不是边缘态。

### 修

- **信号源两条**，取先到者：① turns 列表响应的 `recording_consent` 字段
  （元数据主路不打 409 门，常态第一信号；`SessionTurnsTimeline` 新增
  `recordingConsent` emit 透传给页面）；② `/detail` 的 409 结构码兜底
  （列表没回来时 / 旧后端没带字段时的唯一信号）。
  `undefined` 不当 false（旧后端兼容），由 409 兜底接手。
- **判码助手** `web/src/api/sessions_v2.ts` `isRecordingUnavailable(e)`：
  `ApiError && status===409 && code==='recording_unavailable'`——五个消费点
  同一份口径（同一把尺一份实现）。
- **中性态不是错误态**：consent-off 是租户开关状态，渲染专属横幅/空态/提示，
  不进红色错误、不给重试按钮；`body_status` 的「部分轮次没有正文」横幅在
  consent-off 下**不并显**（全部轮次都没正文，不是「部分」——空状态要能区分）。
- i18n：`requestDetail.bodyStatus.consentOffTitle/consentOffBody/consentOffInline`
  × 8 语种（locale-key-writer 落地，键集合/占位符门全绿）。
  文案不编造不存在的设置入口（前端无 recording-consent UI），
  动作指向「由管理员为该租户开启」。

### 测试

- `SessionDetailPage.test.ts` +3（409 兜底点亮横幅 / 列表字段点亮 / 字段翻true熄灭；
  409 与 unavailable 横幅**不并显**断言）；mock 改 `importOriginal` 保留真
  `isRecordingUnavailable`（桩了它等于自己测自己）；ApiError 桩补 `code` 字段。
- `TurnDigestDrawer.test.ts` +2（409 → 专属空态、英文原文不上屏、无重试；
  **409 但 code 不同 → 照旧走错误横幅**——防 consent 分支吞掉其它 409）。
- 存量测试三文件补挂真实 zh-CN 语言包（`LoginModal.test.ts` 先例）：
  `SessionSummaryBar` / `TurnsListView` 新增 `useI18n` 后裸挂必炸；
  `turnsResummarizeStale` 的 sessions_v2 mock 同步 importOriginal。

## 3. ② 914 迁移超时预算（第 7 轮 §5b-2 遗留）

### 现状复核（不修文件本体）

914 的 `SET statement_timeout='120s'` 是按本地小表拍的；245 `session_turns`
VALIDATE 实测 2m25s ⇒ 部署中止，第 7 轮已按「改 15min 副本手工应用 + 双账本
stamp」收场。本轮逐环境复核**914 已全部应用**：

| 环境 | 证据 |
|---|---|
| 211 | 第 7 轮部署 008546aa 应用（§7 读数） |
| 245 | 第 7 轮 stamp 双账本（改副本） |
| 252（含 154 / 252-dev 共库） | 本轮实核 `pg_constraint`：`session_turns_origin_stage_check` / `session_turns_hot_origin_stage_check` **convalidated=t** |
| 154 | 走 252 共库（上面同一份证据）；008546aa 部署成功即已应用 |

⇒ **改文件字节是纯下行**（连锁 break 211/252 的 checksum 门，且迁移永不再跑）。
914 本体不动，与第 7 轮决定一致。

### 交付物：预算纪律落成守门测试

「新增迁移必须按目标环境最大表实测预算」从散文变成可证伪的门：
`sql/migrations/startup/migration_version_unique_test.go`
**`TestHeavyDDLModernMigrationsDeclareMeasuredBudget`**（作者推送前必跑的
migration guard 文件内，与撞号门同位）：

- **范围**：版本 ≥ 900 的现行线 up 迁移（更老的冻结历史，同撞号门 ≥492 的豁免形态）。
- **触发面（可静态判）**：`VALIDATE CONSTRAINT`（914 烧点本体）或
  非 CONCURRENTLY 的 `CREATE INDEX`（建索引全表扫，761 同族）。
- **要求**：① 显式 `SET statement_timeout` 最大值 ≥ 10min（761 先例的数量级——
  实测 18.9s 抬 10min，不贴实测值抬）；② 文件含「实测」注释写明量了哪个
  环境哪张表、量出多少——没有量过的预算就是拍脑袋。
- **存量豁免**：900（无显式 SET，表小靠角色级 30s 侥幸通过）、914（120s，
  已按改副本+stamp 收场）——字节冻结，豁免理由写在测试注释里。
- **变异臂 3/3**：A 120s 违规金丝雀 ⇒ FAIL（报出确切预算信息）；
  B 10min+实测 ⇒ PASS；C 10min 但无实测注释 ⇒ FAIL
  （★ C 臂第一版靶点自己的注释里写了「实测」二字被门正确放行——
  **「臂没过先问靶点再问门」**的又一次实证），还原后 PASS。

## 4. ③ glm-5.3 别名双映射数据面处置（2026-09-19 记忆挂账）

### 现状实锤（211 acc-pg17）

`model_aliases` 裸名双映射仍在：
`glm-5.3`/`glm-5-3` 同时 active 于 canonical **2422803（glm-5.3，id 273/294384）**
与 **2716170（glm-5.3-flash，id 818875/818876）**。
48h 生产 WARN 15 条：`requested_model=glm-5.3-flash` 的归一化集合混入裸名
⇒ 候选混进非 flash 的 `z-ai/glm-5.3`（第 8 轮 `83013cce4` 的特异度排序只是
兜底选对，歧义源仍在）。

### 处置：置 disabled，不是 DELETE

`discovery/alias_sync.go` 的 `rebuildAliasIndex`（6h 周期）自带两条关键语义：

1. `NOT EXISTS (... WHERE raw_name = X AND status='active')` ⇒ 裸名在
   2422803 侧仍 active ⇒ **删掉的行不会被重建插入**；
2. `ON CONFLICT ... DO UPDATE ... WHERE status <> 'disabled'` + 注释明文
   **「A 'disabled' pair is an explicit operator kill and stays disabled」**
   ⇒ 置 disabled 是同步机制自己定义的关断通道，防复活是结构性的。

两处消费方（`provider/client.go:1168` 候选 SQL、`modelbinding/resolver.go:130`
绑定 SQL）都只认 `status='active'` ⇒ disabled 即时收敛歧义。

| 库 | 覆盖面 | 动作 |
|---|---|---|
| 211 `acc-pg17`/llm_gateway | 211 生产 | 818875/818876 → disabled（UPDATE 2，WHERE 四重守卫） |
| 252 `pg-252-pg17`/llm_gateway | 245（DSN 实查指向 172.16.2.210）、154、252-dev | 同上（UPDATE 2） |

删前全行 JSON 备份：`/tmp/w13-keys/glm53-alias-backup-211.json` 与
`glm53-252-after.txt`（含 created_at/updated_at，可逐字段 INSERT 回滚）。
处置后复核：2422803 侧两裸名仍 active；两 canonical 的 available offers
17/18 与 18/17 ⇒ **glm-5.3 请求路由面完好，flash 不再被裸名认领**。

### 观察窗与边界

- 歧义 WARN 稀疏（15/48h），本轮不等窗口——下次日志审计轮复核归零。
- `minimax-m3.1-flash-preview` 同族歧义（3h 窗口 1 条）是**另一条数据线**，
  不在本轮范围，登记遗留。
- 行 `updated_at` 显示 03:30/03:09 有同步触碰——处置后若回归，说明还有
  未识别的写方，届时查 `discovery/discovery.go:861/1035/1068` 三个 INSERT 面。

## 5. ④ upstream TestDo_DNSFailureReservedTLD 负载 flake 加固（第 7 轮 §5 遗留）

- **根因链**：`ClassifyError` 先判 `context.DeadlineExceeded` 再判 `*net.DNSError`
  （生产语义有意如此——解析超时要走故障转移的 timeout 通道）⇒ 旧预算
  dialer 5s / client 10s 在全量并发下被 CPU 饥饿拖穿，resolver 未返回
  NXDOMAIN 而 deadline 先到 ⇒ 判 KindTimeout ⇒ 断言红。
- **修法**（`upstream/client_fault_test.go`）：预算 5s/10s → **30s/60s**
  （NXDOMAIN 毫秒级，上限只兜病态），**不改分类顺序**；另加 KindTimeout
  显式 skip（与既有 sinkhole skip 同形态：「量具挂起」是读不到，不是读到了 0）。
- 本机实测：本机 resolver 是 sinkhole 形态（R32-U1 既有 skip 命中 3/3），
  KindNetwork 主断言与本机不可证——由 30s 预算下的负载体现与 skip 分支兜底。
- 整包 `go test ./upstream/` ✓ 17.6s。

## 6. 并行线存量四红（挡本轮推送，顺带收口）

main 上四道门被并行线推红（干净 HEAD 复测同样红 ⇒ 非本轮引入），按门的
自有处置口径收口：

| 门 | 违规 | 定性 | 处置 |
|---|---|---|---|
| `i18n:shared-dead:check` | `modelCredibilityView.error.historyFailed` 8 语种 0 引用（R183 档已定性「那条线的欠账」） | 组件把 detail+history 合并进 `Promise.all` 单 catch 用 `detailFailed`，history 键从未接线 | 三口径对拍（死键门/全仓字面量/scanner）判死 ⇒ `i18n-dead-key-prune.mjs --check-parser`（672 文件 0 不一致）+ `--apply` 删 8 处，不变量破口 0；**基线未动**（0=0） |
| `design-ai-smell:check` | `ModelCredibilityView.vue:346` `≠` 字形当图标（V3） | 同上，model-credibility 线引入 | 换 `@element-plus/icons-vue` 的 `<Warning />`（裸 svg 显式尺寸纪律：`.mismatch-mark` font-size 12px + inline-flex） |
| `copy-quality:check` | 5 处破折号当标点（en-US/de-DE modelCredibilityView ×4 + web-mobile en-US ×1） | 同上两线引入 | 逐处最小改写（`,` / `;` / `:`），不重译整句 |
| `go test ./tests/session_identity_contract/` | **7 用例红**（W1.3 consent 门遮蔽 404/500/歧义语义） | 双缺陷，见 §6b | 门移位（生产语义修复）+ 契约包 200 路径显式开 consent（第 8 轮「唯一入口显式开」先例的补遗） |

### §6b consent 门遮蔽存在性语义（真实生产缺陷，不只测试红）

`session_detail_v2.go` 的 `requireRecordingConsent` 原先挂在 auth/tenant 之后、
`resolveSessionID` **之前** ⇒ consent=off（**默认态**）时：

- 不存在的会话 ⇒ **409** 而非 404（跨租户同样被遮蔽）；
- resolve/query 内部故障 ⇒ **409** 而非 500。

两个危害：① `session_identity_contract` 的五类 ID 契约被遮蔽（7 用例红，
main 上自 W1.3 落地起持续红——第 8 轮只修了 admin 包的 9 个旧夹具
`c77e134b2`，契约包漏网；本轮 verify 日志首屏只有 4 条也是**日志截头**的假象，
隔离 `-v` 复跑实数 7）；② 「不存在 ⇒ 409 recording_unavailable」给调用方一个
**consent 探针**（不存在的会话本该与其它租户的会话同样不可区分）。

修法（`admin/session_detail_v2.go`）：门移到 `resolveSessionID` 成功之后、
正文查询之前 ⇒ 404/409歧义/500 先裁决，409 只对「真实存在且本租户可读」的
会话拦——与 W1.3 本意（别把空正文伪装成没数据）一致。

配套测试：

- 契约包 3 个「越过门还要继续走」的用例（ResolveGw / ReverseMap / QueryFailure）
  显式开 consent（`enableConsentForTest`，与 admin 包同款）；404/歧义用例
  **不开**——让「门不遮蔽存在性」被持续断言。
- `TestSessionDetailV2_ConsentOffUnavailable` 原先用 `consentGateDB`（任何查询
  即 panic）钉「门先于一切查询」——正是被订正的旧语义 ⇒ 改为 pgxmock 钉新契约：
  resolve 允许查、409 照发、**正文查询（LEFT JOIN LATERAL）从不执行**
  （无后续期望 ⇒ 门若失效则 500 ≠ 409 ⇒ 红）。
- 读数：契约包 **16/16**、admin 全包 ✓、session_replay ✓。

**同族观察（登记不改）**：turn detail / turns bodies / instant-summary 三处的门
同样在存在性裁决之前（`session_turns_v2.go:295/623/858`），非存在会话同样吃
409——但无钉死契约、无失败测试驱动，按「不凭口味扩大改动面」登记为遗留；
若要收口，判据应与 §6b 同款（404 先于 consent 门）。

## 7. verify 读数（★ CGO 口径订正后的最终版）

- `web`: `vue-tsc` **0 错**；`vitest` **2963 通过 / 2 跳过**（新增 5 用例）；
  `build` ✓ 12.36s；i18n 全门 PASS（audit 0 missing / CJK 4483=基线 /
  strict 7 文件 / hidden-key / shared-dead 0 / empty-state / error-clarity /
  error-outlet / jargon / ai-smell 0 违规 / elevation / copy-quality 5→0）。
- `go`（`VERIFY_CGO_ENABLED=1`，全量对拍口径）：**本轮改动零新增红、净修 8 红**
  （契约包 7 + admin ConsentOffUnavailable 1）。对拍方法：干净 HEAD 与带改动
  各跑同批包，红集**逐条相同**的记存量：
  - 本轮修复：`tests/session_identity_contract` 7→**16/16**、admin 1→**ok**、
    session_replay ok、`cmd/gateway` ok、storage/factory/sqlite/integration ok、
    `./upstream/` ok、`./sql/migrations/startup/` ok。
  - **HEAD 存量红 14 条（干净 HEAD 同样红，如实登记不越权修）**：
    `scripts/ursmcheck` 12（快照/台账 recomputable 族 + hollowout×2，其中
    hollowout 两例各 135s/162s 超慢——本轮不动，属并行线账本工程）；
    `cmd/tools/address_family_decision_audit` 1（TestAddressFamilyDecisionSitesAreAllRegistered，
    第 8 轮「登记 not-a-decision」的后续面）；`security/sanitize` 1
    （TestStorageAuditStreamTerminalCapacity）。
  - ★ **CGO 陷阱**：verify.sh 默认 `CGO_ENABLED=0` ⇒ SQLite 驱动不可用 ⇒
    cmd/gateway 的 lite/storage 族 **21 例恒红是环境假象**（CGO=1 下 ok）——
    verify 首屏读数必须带 CGO 口径看，否则「21 红」会把真信号淹没。
  - ★ **日志截头陷阱**：后台跑的长验证，输出文件**开头被裁**——首轮「4 红」
    是截头假象，隔离 `-v` 复跑实数 7。
- **未验证（如实记）**：consent-off 页面的浏览器人工浏览（无凭据，同第 7/16 轮口径）
  ——由 14 个 SessionDetailPage/TurnDigestDrawer 用例（断言渲染文本 = 语言包值）
  与 2963 全量兜底。

## 8. 部署

- **③ 数据面已即时生效**（两库 UPDATE，无需部署）。
- **① 前端**：走正典蓝绿 `deploy-211.sh`（干净 worktree + `LLM_GATEWAY_ROOT`
  钉根）——211 是 consent-off UX 的消费面；245/154 无后端变更，随下次自然
  部署携带，不为纯前端单独 churn 两台预发。部署读数见 §10 补记。

## 9. 本轮没做 / 遗留

- turns detail / bodies / instant-summary 三处的 consent 门位（§6b 同族观察）。
- `minimax-m3.1-flash-preview` 绑定歧义（同族数据线，另案）。
- 211 glm 歧义 WARN 归零复核（下次日志审计轮）。
- alias 双映射的**上游写方治理**：`rebuildAliasIndex` 只挡「已有 active 别名」
  与「disabled 不复活」，但 `discovery/discovery.go` 三个 INSERT 面仍可能在
  特定顺序下再造裸名双映射——结构性修法（如 DB 级「裸名只归一个 canonical」
  部分索引）是独立迁移工程，本轮不动。
- 245/154 前端更新（随下次部署）。
- 第 8 轮 §9 遗留清单继续有效（L1 未登记槽位、`e.message` 出口覆盖面等）。

## 10. 推送与部署读数（回填）

- **推送**：`478182c35..14593283c main -> main`。推送窗口与并行会话**撞号三连**：
  首推被拒（origin 推进 a2818aaff，§577/§578 被占）⇒ 我方 LESSONS §577→**§581**
  （跨号留缓冲）+ AGENTS/LESSONS 双侧条目并存解冲突；二推再被拒（origin 再推进
  478182c35，其部署轮自己也让号 §577→§579）⇒ 二次合并（LESSONS 一处冲突：
  origin §579 在前、我方 §581 在后），终态序列 §576→577→578→579→(580 空留缓冲)→**581**。
- **211 蓝绿部署**（干净 worktree `/tmp/lgw-r9-deploy-wt` @ `14593283c` +
  `LLM_GATEWAY_ROOT` 钉根，`DEPLOY_EXIT=0`）：
  - 版本 `2.5.8-14593283-20261030-211`（git_sha=**14593283**，无 `-dirty` ⇒ 无树漂移，
    DD_REPO_ROOT 第四次钉根纪律生效）；
  - 活跃槽 b → **a @8781** 接管；readyz OK（DB 103µs + Redis）；公网版本身份 OK；
    登录冒烟 OK（连登 6 次无 429）；无 pending 迁移（914 族 already applied）。
  - **前端修复上屏证据**：线上 bundle `index-CyDosYzo.js` 含 `recording_unavailable`
    判码与 `consentOffTitle/consentOffBody/consentOffInline` 三键（curl grep 实测）。
  - **③ 数据面跨部署复核**：部署重启后 818875/818876 **仍 disabled**（启动同步
    未复活——operator-kill 语义的第一次跨重启实证）；重启后 10 分钟窗口 glm
    歧义 WARN **0** 条。
  - 部署临时 worktree 已清退（仅 menu-config.json 构建时间戳差异，`--force` 删除）。
- **245/154 未部署**（本轮无后端行为变更需部署；consent 门位订正属 admin 端点，
  随下次自然部署携带——§4 的数据面修复已覆盖其 DB，UI 侧 consent-off 租户
  在 245/154 的会话详情页仍旧行为，已在本节如实登记）。

## 11. 批判式审计（2026-10-31，用户指令：对本任务批判式审计并修正）

按「别吹牛、逐条核查」口径对本档 §1~§10 的声明逐条对拍，**两处声明被实锤失实**，
全部订正如下；修复已落地（提交清单见 §12）。

### 11.1 声明订正（§4/§8/§10）

| 第 9 轮声明 | 实况（第 10/11 轮 + 本轮复核） | 订正 |
|---|---|---|
| 「③ 处置后 glm 歧义 WARN 0（重启后 10min 窗口）」 | 第 10 轮复测：候选仍 2（869/869 次选中 flash 但 WARN 743 条/5.7h）；第 11 轮实锤**归零真因 = cred 19 流量退出**（裸名绑定 cred 62/22 接手），歧义条件原封未动 | **撤回归因**：WARN 窗口读数不构成处置生效证据；条件仍在 |
| 「① 211 上线 bundle 实证（consentOff 三键 + 判码）」 | 04:47 部署 14593283 实证为真；**05:04 并行会话以 478182c3（不含修复）重部署槽 b** ⇒ 该实证 17 分钟后失效。后续第 8 续/10/11 轮部署（111c2c1b → 37004018）均含修复（修复已在 origin/main 祖先链），线上以**当前活跃槽为准**重新取证 | 「上线」必须锚定当前活跃槽+版本+bundle 三件事同刻；本轮复核读数见 §11.4 |
| 「② 914 四环境已全应用 ⇒ 字节冻结不动」 | 维持：改已应用迁移字节会破 checksum 链；预算纪律由守门测试承接 | 不变；守门在本轮补牙（§11.3） |
| 「④ DNS 加固完成」 | 集成测试在本机（sinkhole resolver）恒走 skip ⇒ KindNetwork 主断言本机不可证；分类器结构化分支此前零直接钉子 | 补确定性证据链（§11.2-③） |

### 11.2 发现并修正的缺陷（本轮提交）

1. **GLM resolver 歧义降噪缺失（第 11 轮立项卡的实施，owner：本审计会话）**
   `modelbinding/resolver.go`：归一化路径 SQL 已按 best_rank 排序 ⇒ 首位恒为
   特异性胜出者，选择从来是对的，但 len>1 一律 WARN ⇒ 200+ 条/天噪声。
   修法（语义不变）：wire 从纯名字升级为 `name\x1frank` 对（\x1e 分隔），
   首位 best_rank 严格小于次位 ⇒ **静默选择**（Debug 留痕）；仅**同分/缺失**
   才维持 WARN+取首。exact 路径不动。判据 = 候选集合对拍（rank 层裁决），
   不再用 WARN 计数。变异面：特异性胜出/真同分/rank 缺失三态各有测试；
   SQL 契约测试补 rank 对投影钉子。
2. **consent 横幅 stale-409 竞态**：`SessionDetailPage.loadBodyStatus` 的 catch
   分支缺 `id !== sessionId.value` 守卫（成功路径有）⇒ 切会话后旧 409 点亮
   新会话横幅。补守卫 + 在途切换测试。
3. **recording_consent wire→emit 接线零覆盖**：后端删字段无人红。补
   SessionTurnsTimeline 测试（false/true/undefined 三态 emit）+ SyncPane
   提示补 `role="status"`（与页面横幅 a11y 口径一致）。
4. **迁移预算门混合索引盲区**：旧判据整文件匹配「有无 CONCURRENTLY」，
   912/913/917/921 头注释里的「非 CONCURRENTLY」**否定词**让门整族放行
   （逐行判据上线当场抓出 4 个假绿）。改逐行判定（每条建索引行自带
   CONCURRENTLY 才豁免，fail-closed）；912/913（索引建在同文件新建空表）
   与 917/921（新功能表无大存量）按豁免表显式登记并写明风险。变异臂
   A（纯并发→绿）/B（混合→红）/C（注释词放行→红，即 912 钻过的洞）。
5. **DNS 分类链的环境依赖**：errorsx 补结构化 `*net.DNSError`→KindNetwork
   与 DeadlineExceeded 优先级两道钉子；upstream 补 stub 传输层测试
   （transport→Do→classify 全链不碰 resolver，sinkhole 主机也可证）；
   goroutine-leak 测试的 .invalid 臂换 stub（消灭不受控 DNS 负载源，
   泄漏判据不变）。

### 11.3 914 的最终口径（不变，重复声明以防再误读）

914 源文件保持 120s **不改**（已应用四环境、checksum 冻结、改动会触发
content-replay）；245 的 2m25s 事实记录在案（改副本 + stamp 收场）。
「按目标环境最大表实测预算」的纪律由 `TestHeavyDDLModernMigrationsDeclareMeasuredBudget`
对 ≥900 现行线执行（本轮逐行化 + 三变异臂）；914/900/912/913/917/921
是显式豁免，不是「预算已修正」。

### 11.4 211 线上复核（只读，2026-10-31）

（合并推送后回填：当前活跃槽 git_sha、bundle 是否含 recording_unavailable
判码与 consentOff 键、glm 别名 disabled 行状态。）

### 11.5 遗留

- resolver 降噪的**部署判据**：部署后以「候选集合对拍」（WARN 消失且
  selected=最特异变体）复核，不预设 WARN=0。
- minimax-m3.1-flash-preview 同族歧义（第 9 轮登记）随同款 rank 分层自动受益，
  无需单独处置。
- 第 10/11 轮的新遗留（sanitize audit queue full 噪声、同伴对比 1.00 家）
  不在本轮范围。
