# 项目经验队列（LESSONS）

> 这里只记录可复用的返工、审查、验证或生产问题，并附可核验的证据。开工时先读相关条目；收尾时只新增可行动的发现，常规成功不必记一条。证据和晋级标准见 `.agents/rules/66-skill-evolution.md`。

## 待观察经验

## 2026-10-16 — 探针必须保留真实状态与模型作用域（Seen: 1/3）
- 任务 / 缺陷：211 grok-4.6 路由诊断与节点状态修正；基线 `587af94bdf29b942c7d7051f4562955d83b12115`。首版 `7fc04b6e18dd3759ca9855e9b4316675ad672b40` 独立审查失败，追加隔离写回与余额门后重新审查，不沿用旧批准。
- 根因：生成探针把 401/403 合并成同一前缀，分类先按403判鉴权；具名404报文也未命中连续的 model not found。额度/模型权限被误挂为凭据级故障。resolve遗漏了dispatch仍在使用的credential circuit gate；健康写通的no-op条件遗漏被清空的错误字段。
- 防复发检查：保留协议与实际HTTP状态；额度、模型绑定、真正鉴权及模糊403分别配正负对照；排序测试先证明Router确实保留待过滤节点；SQL清理条件覆盖每个被清空字段。不以Ping其他模型成功证明目标模型可用。
- 证据：`TestCredentialProbe211Classification` 54个HTTP本地回放子例；撤额度识别6条红、过度豁免24条红后还原；`TestApplyURSMOverlayAppliesCredentialGate` 与 `TestLivePlanOrderFiltersCredentialCircuitAndCompactsRanks`；`TestHealthyCredentialSQLGuardCoversEveryClearedColumn`。初始211快照18个陈旧错误凭据，后续读数28；有效数据面请求未复现（现有凭据401），不能把模拟回放写成公网恢复。
- 状态：修复待观察；线上永久额度枯竭不因分类变更自动变成可用。

## 2026-10-15 — 本地化验收要覆盖底层解析异常（Seen: 1/3）
- 任务 / 缺陷：Magpie第二轮browser-use验收；非法URL透出浏览器原生英文，语义拒绝又透出工具函数硬编码中文。
- 基线 / 修复 SHA：部署 `369817bd2` / 2658 实测发现；修复 `98eeb903cdb13b1db05f05ee79cc1c5fdf53a8f0`。
- 根因：组件翻译了自身错误，但共享校验器与原生URL异常仍用自由文本；仅toThrow断言证明拒绝，不能证明错误语义/语言。
- 防复发检查：共享校验用稳定错误键；畸形URL与安全语义拒绝分别断言精确键；切语言重绘既有错误且保留输入；同时确认未revealKey/生成产物。
- 证据：URL6例/provider5例修前精确键断言红；修后39项绿；去解析映射及恢复provider明文变异分别红；独立浏览器实际交互发现遗漏。
- 状态：已修复待观察

## 2026-10-15 — 安全工具链升级需同步 vendor 与实际构建入口（Seen: 1/3）
- 任务 / 缺陷：[Magpie第二轮审计](docs/audit/2026-10-15-magpie第二轮审计与完善.md)；Go1.27.1与x/net可达漏洞门。
- 基线 / 修复 SHA：集成基线 `d63d9dcf959aa06143923e33cf637c4da047df1a`，修复 `76579bef134c762c88dd577baf2c16a5d072bf12`。
- 场景与影响：Go测试/构建通过后，govulncheck仍检出13项可达标准库漏洞；只改mod会被旧tracked vendor拒绝，旧CI补丁版与GOTOOLCHAIN=local也不会自动切新SDK。
- 根因：源码依赖、vendor、编译器与CI/镜像入口是独立约束，任一旧层都能阻断或偏离安全升级。
- 防复发检查：同步MVS最小依赖与vendor，生成两遍比哈希；更新活动CI并显式选择修复SDK；扫描默认vendor模式及独立模块，部署后读实际产物go version -m。
- 证据：两模块Go1.27.2扫描均0可达；vendor两次5853文件哈希一致；x/net0.60与配套版本由官方模块依赖边确认，未运行tidy。
- 状态：已修复待观察

## 2026-10-15 — 验证矩阵继承父环境，主门每次都在错误分支早退（Seen: 1/3）
- 任务 / 缺陷：[Magpie第二轮审计](docs/audit/2026-10-15-magpie第二轮审计与完善.md)；verify cgo契约。
- 基线 / 修复 SHA：基线 `c708260d0514befacacfdd18f83429f80bfc98f6`，修复 `9257aefa5f165d78e9786456f7d0454858a09b78`。
- 场景与影响：verify先export CGO_ENABLED再调用契约测试，默认入口在Linux模拟分支红，强制cgo入口在Darwin默认分支红，后续全量验证从未开始。
- 根因：单例测试未先清父环境；另用nocgo依赖图查cgo专属边界，前置错误修掉后才暴露第二层假红。
- 防复发检查：每个矩阵子shell先清目标变量再显式预设；动态依赖查询显式指定被登记形态；父环境0/1污染与自然完成对照都要在场。
- 证据：`tests/verify_cgo_convention_test.sh` 五种环境完整门RC0；去unset与去go-list形态钉扎变异分别在污染/空依赖断言红。SDK26.5+cgo1真实SQLite测试RC0，不能把旧SDK不可用当永久前提。
- 状态：已修复待观察

## 2026-10-15 — 脱敏工具脚本与人工凭据声明必须区分作用域（Seen: 1/3）
- 任务 / 缺陷：r1034，MiniMax 合法 sed 脱敏命令被 output_policy_blocked；原始人工账号/密码组合漏脱敏。
- 基线 / 溯源：`14d9fed9c0b801a5ef2d71feae6417835fa77903`；`docs/superpowers/specs/2026-10-15-minimax-policy-credentials-design.md`。
- 根因：凭据赋值正则把 sed 匹配表达式当真值；前置守卫单测未覆盖后置检查器对 JSON 转义和半截参数的处理；去引号后问句/变量豁免又可能误放真实字面口令。
- 防复发检查：匹配段与替换段均证明无秘密才屏蔽；人工自然语言规则只进输入；断言映射含完整真值；保留原始引号语义并匹配完整分隔词，避免“改为”被后缀“为”当问句。工具JSON解码不得静默丢弃重复键。允许侧同时跑真实合规全链及秘密/数字/键的反向样本，校验同属主策略。
- 证据：`TestR1034Production1971ByteArguments`、`TestR1034ThreeProtocolStreamsRestoreHumanInput`、`TestR1034HumanCredentialDeclarationsRoundTrip`、`TestR1034ToolShapeRedactionHonorsOwnerPolicy`；撤除关键修复会在对应断言变红。
- 状态：观察中

## 2026-10-15 — probeOne 进程级 MemoryFlight + holdTimeout TTL → 包测串味（Seen: 1/3）
- 任务 / 缺陷：`TestExecutor_NoCandidateProbe_HonorsClientCancel` 单独绿、连跑红
- 基线 / 修复 SHA：合入 main 的 24h 审计提交（基线 magpie `9ea45f7f8`）
- 场景与影响：同包先跑 NoCandidateProbe 后，Cancel 测报 ProbeSync never called
- 根因：`defaultMemoryProbeOneFlight` 进程单例；`TryAcquire(model, holdTimeout)` 无 Release
- 防复发检查：单飞默认 per-Executor 惰性 memory；钉子 `TestDefaultProbeOneFlightIsPerExecutor`
- 证据：连跑组修前红 / 修后 `go test ./domains/streaming/executors/ -count=1` RC=0
- 状态：观察中

## 2026-10-15 — session_online ⋈ 停写后视图 → EXPLAIN 52s/0 行（Seen: 1/3）
- 任务 / 缺陷：dashboard `/api/admin/sessions/online` 5s→500
- 基线 / 修复 SHA：本轮 24h 审计（生产 252 EXPLAIN）
- 场景与影响：视图臂 `id` 对 session 行为 NULL；`session_last_requests` 0 行仍付规划代价
- 根因：S4 后仍 JOIN `request_logs_with_current_month` 取 tenant
- 防复发检查：改读 `session_summaries`；钉子禁 `request_logs_with_current_month` / `session_last_requests`
- 证据：旧 JOIN 52.8s/0 行；summaries 近窗 0.76ms/180 行
- 状态：观察中

## 2026-10-14 — miniredis「跨实例」测用了不同 Redis key 前缀 → 假红（Seen: 1/3）
- 任务 / 缺陷：`p0b_rawlogs_miniredis_convergence_test` 首跑 LearnedParams=[]
- 基线 / 修复 SHA：未提交（基线 `ed620001e`）
- 场景与影响：Diagnose/RecordTerminal 正确，实例 B 永远读空；单独 debug 子测却绿
- 根因：`newTestRedisStore` 前缀 `llmgw:reqanom:test`，实例 B 误用 `…:rawlogs`——同 miniredis 不同 key 空间
- 防复发检查：跨实例夹具断言两侧 Store 前缀字符串相等；先 `storeA.List` 再读 B
- 证据：改同前缀后 rawlogs 两子测 + Diagnose error.loc 绿；M6 去 error.loc 臂双红
- 状态：观察中

## 2026-10-14 — macOS `strings|grep -q` 在 pipefail 下把命中判成指纹假 MISS（Seen: 1/3）
- 任务 / 缺陷：magpie P0 部署观测脚本 `scripts/observe-magpie-p0-postdeploy.sh`
- 基线 / 修复 SHA：未提交（基线 `ed620001e`）；工作区已改脚本为 python 字节搜索
- 场景与影响：Go 二进制里确实有 `probeOne single-flight` / `llmgw:probe_one:`，但脚本全报 MISS；裸 `strings|grep -F` 却 OK
- 根因：`set -o pipefail` + `grep -q` 提前关管 → `strings` 收 SIGPIPE 非 0 → `if` 整段失败；另：const 标识符名不进 rodata，须钉字面量并经 `init` 保活防 DCE
- 防复发检查：指纹扫描用 `python3` 字节 `in`；契约类指纹用 `init` 赋值到 package var；审计文档写明禁止 `strings|grep -q`
- 证据：`TARGET=local BIN=/tmp/magpie-p0-bin/gateway bash scripts/observe-magpie-p0-postdeploy.sh` → 三指纹 OK（OBS=0）；修前 OBS=1 全 MISS
- 状态：观察中

## 2026-10-14 — `/usr/bin/env bash` 在 macOS 落 bash 3.2，`declare -A` 直接炸（Seen: 1/3）
- 任务 / 缺陷：`observe-magpie-p0-postdeploy.sh` 基线对照段首加 `declare -A COUNTS`
- 基线 / 修复 SHA：未提交；已改为普通 `for` + python 打基线 diff（无 assoc array）
- 场景与影响：本机 `bash scripts/observe-…` 在指纹 OK 后 `declare: -A: invalid option`，基线 diff 整段跳过
- 根因：shebang/`env bash` ≠ `~/sdk/bash5`；deploy-lib 族已要求 bash5，观测脚本却假定 4+
- 防复发检查：观测/部署辅助脚本禁止 `declare -A` / `${var^^}`，或显式用 `$HOME/sdk/bash5/bin/bash`；改完用裸 `/usr/bin/env bash` 复跑
- 证据：修前 OBS 中段炸；修后对照 `magpie-p0-154-baseline-20261014.json` 三指纹 `CHANGED`/`OK expected`
- 状态：观察中

## 2026-10-14 — 154 观测脚本默认 `gateway` + 裸 journal unit + grep 零命中 → 假 FAIL/中途死（Seen: 1/3）
- 任务 / 缺陷：`observe-magpie-p0-postdeploy.sh` 对 154 现网只读烟测
- 基线 / 修复 SHA：未提交；已改双候选 `llm-gateway-go|gateway`、canary@port 日志/unit、`grep|wc || echo 0`
- 场景与影响：BIN 默认 `/opt/.../gateway` → FileNotFound；`journalctl -u llm-gateway-go` 几乎空；`set -o pipefail` 下首个 0 计数 `grep` exit 1 打死脚本（指纹 MISS 后看不到 count/baseline）
- 根因：多 checkout 文档混用二进制名；蓝绿 canary unit 未进脚本；零命中被当成管道失败
- 防复发检查：观测脚本必须解析两名；优先 `gateway-canary-$port.log`；凡 `grep|wc` 计数加 `|| echo 0`；改后对 154 现网（故意 MISS 指纹）跑通到 baseline compare
- 证据：修后 `OBS_154` BIN 解析成功、readyz ok、count[*]=0、baseline same/MISS 打印完整
- 状态：观察中

## 2026-10-16 — 已登记迁移不等于物理契约成立（Seen: 1/3）
- 任务 / 缺陷：[Magpie第三轮](docs/audit/2026-10-15-magpie第三轮字节与会话一致性.md)；708有marker/hash，但body parent/hot/unified均缺kind，12条补偿报42703。
- 基线 / 修复 SHA：基线 `493d24ebcdaf45d792130f32b42e6724cb667905`；修复 `8b04755c13e67546670b9dae27db64d067591e63`。
- 根因：只检查ledger无法证明真实列、默认值、索引、视图和权限；历史成因不能从单一marker倒推。
- 防复发检查：新编号增量修复，保留旧迁移原字节；升级通道即使marker齐全也跑可复用postcondition；真实PG保留原行、NULL拒绝整笔回滚、writer完整hook均需在场。
- 证据：独立PG17 ingest四族与settings三组通过；删除物理断言的隔离变异准确红；独立DB review通过017653d81。实际业务部署另见审计，不把夹具结果冒称现网结果。
- 状态：已修复待观察

## 2026-10-16 — 成功黄金矩阵必须配失败终态和思考语义（Seen: 1/3）
- 任务 / 缺陷：[Magpie第三轮](docs/audit/2026-10-15-magpie第三轮字节与会话一致性.md)；24成功交换全绿仍漏response.failed被当stop及thinking经Chat中转丢失。
- 基线 / 修复 SHA：发现于 `3643d9db9ac876c7d8d15365e4b1da0b6d024fc0`；修复终点 `7c7102af0378fe246c967bf62f684ecc8c4d2360`。
- 根因：只约束最终请求体和成功回复，无法覆盖错误外显；中间协议的序列化不是无损容器。
- 防复发检查：真实handler→Executor桥加入失败、unknown incomplete、EOF/read error、取消及thinking enabled/adaptive；完整独立语义断言先于黄金录制。
- 证据：真实failed流原黄金用例PASS、新判据红；修后14负向/截断路径和8思考黄金通过，7对应变异准确红；原26黄金字节保持。
- 状态：已修复待观察

## 2026-10-16 — Redis原子执行不提供错误回滚，清理失败也不撤销已确认提交（Seen: 1/3）
- 任务 / 缺陷：[Magpie第三轮](docs/audit/2026-10-15-magpie第三轮字节与会话一致性.md)；ACL拒绝PEXPIRE后原pair已改变；release-only失败使strip/tasks从4/7重复累计为5/9。
- 基线 / 修复 SHA：本轮缓存实现，基线 `d357bf985ec58f728632a9fb6a0146c4be8a8543`；最终提交见审计精确SHA。
- 根因：Lua不会回滚先前DEL/HSET；把cleanup错误覆盖primary ack会让调用方保留已生效的待累计增量。
- 防复发检查：同脚本第一次持久修改前按实际参数检查所有写权限；真实Redis逐项拒绝DEL/HSET/PEXPIRE并校验完整旧hash及绝对TTL；独立有界token cleanup只记录故障，primary结果保持。
- 证据：真实ACL负控原实现准确红；修后真实strict race通过；release-only仓内红→绿，updateCache→CommitFinal保持4/7；撤ack保护/吞主错误/去token保护均命中正确断言。
- 状态：已修复待观察

## 2026-10-16 — 跨入口会话验收须分开响应契约与异步落库（Seen: 1/3）
- 任务 / 缺陷：[Magpie第三轮验收](docs/audit/2026-10-15-magpie第三轮字节与会话一致性.md)；脚本把裸UUID发canonical头、要求Chat回同名头，随后又用5秒同步可见性判异步镜像，连续两轮假红。
- 基线 / 修复 SHA：发布源码`76e185e71cebe87611746d03cea56bbc352a4d7b`未变；修正验收脚本，不据此修改生产。
- 根因：同为会话ID不代表三入口的header值域/响应行为相同；HTTP完成不代表后台镜像/补偿已同步完成。
- 防复发检查：先读真实入口契约；用实际返回request_id且要求互异，按tenant查canonical，分别比较显式ID和存在的响应头；独立核turn/body/date/终态/双delta，有界等待并记录elapsed，不重发请求或放宽告示阈值。
- 证据：最终18HTTP与17唯一pair通过；8类验收负控正确拒绝；首条旧5秒窗口后的可见性不足以证明冷启动或补偿，原RC1保留。
- 状态：已修复待观察

## 2026-10-16 — `docker exec` 丢 `-i` 后 stdin SQL 静默空跑（Seen: 1/3）
- 任务 / 缺陷：stream_mode 轮（docs/audit/2026-10-16-credential-model-stream-mode.md）；211 库目录查询连续四轮返回空，险些误判「库无 glm 模型、域名后端另有其人」。
- 基线 / 修复 SHA：未提交（基线 `59477eb4c`）。
- 场景与影响：`docker exec [-e …] psql` 不带 `-i` 时 stdin 不进容器，psql 读 EOF 后 rc=0、零输出——与「查询无结果」完全同形；`-c` 形态正常，更迷惑。
- 根因：docker exec 默认不接管 stdin；psql 无 `-c` 时从 stdin 读脚本，空 stdin 即空执行。
- 防复发检查：经 stdin 传 SQL 的 `docker exec` 一律带 `-i`；远端取证脚本先跑 stdin 形态 `SELECT 1` 自证通道再跑业务查询；rc=0 且空输出不得当「无数据」结论。
- 证据：同机 `-c "SELECT 1;"` 出 `1`，stdin 形态 `SELECT 2;` 空（rc 均 0）；补 `-i` 后同一 SQL 立即返回 10 行模型目录。
- 状态：观察中

<!-- 只有出现具体问题时才新增条目。 -->

```markdown
## <日期> — <一句话模式>（Seen: 1/3 个不同工作日）
- 任务 / 缺陷：<链接或编号>
- 基线 / 修复 SHA：<精确 SHA；未提交时注明“未提交”并记录基线 SHA>
- 场景与影响：<可观察事实>
- 根因：<缺失了哪条约束或验证>
- 防复发检查：<规则 / 回归用例 / 命令>
- 证据：`<命令>` → <实际结果；未运行项和原因>
- 状态：观察中 | 已修复待观察 | 已晋级 → <目标链接>
```

## 2026-10-15 — 版本化配置必须接管全部执行链（Seen: 1/3 个不同工作日）
- 任务 / 缺陷：`codex/sensitive-rule-management`；管理页面停用规则不能仍由旧PII/凭据字段/环境动作执行。
- 基线 / 修复 SHA：基线 `493d24ebcdaf45d792130f32b42e6724cb667905`；本任务代码提交前记录。
- 根因：配置CRUD、语义匹配、协议入口与最终写回的所有权不一致；Chat、Messages、Responses并不共享ServeHTTP入口。
- 防复发检查：规则启停、log/mask/block、重叠豁免、可信还原、三协议输入开/关均在真实执行入口断言；write-time阻断须核HTTP和失败审计，不能只测字节。
- 证据：`TestManagedRuleDisablingStopsCredentialFieldChain`、`TestManagedOverlappingRulesKeepStrictBlock`、`TestManagedOutputTrustAndExplicitScopeActions`、`TestAllProtocolRequestsPinBeforeInputAndWithoutInput`通过；撤还原/去重/双协议Pin/Type限制五项overlay变异均在断言红。
- 状态：已修复待观察

## 2026-10-15 — Redis连接成功不等于订阅成功（Seen: 1/3 个不同工作日）
- 任务 / 缺陷：同任务；SUBSCRIBE ACK黑洞曾阻断数据库补偿轮询和退出。
- 基线 / 修复 SHA：同上；本任务代码提交前记录。
- 根因：启动同步等待pubsub ACK，Receive默认无真实读取期限；立即报错及正常miniredis测试都看不见该失败。
- 防复发检查：订阅建立与DB轮询解耦；真实socket握手期限；专测接受TCP、吞掉ACK、取消退出。
- 证据：独立代理探针修前DB v2/实例v1且退出阻塞；修后v2收敛并正常退出；`TestRedisWithheldSubscribeACKHasDeadline -race -count=3`通过。
- 状态：已修复待观察

## 2026-10-15 — 翻译文件齐全不证明运行时已注册（Seen: 1/3 个不同工作日）
- 任务 / 缺陷：同任务；8语言111键源文件齐全，6个index只import未export。
- 基线 / 修复 SHA：同上；本任务代码提交前记录。
- 根因：测试只检查独立字典文件，没有从真实locale入口装配解析器。
- 防复发检查：加载真实index导出，对每个语言namespace逐键执行i18n解析；同时跑前端全套断点门。
- 证据：真实bundle测试先在6语言undefined断言红；修复后实际locale及i18n逐键解析通过，前端2474测试、类型及构建通过。
- 状态：已修复待观察

## 2026-10-16 — 字符串内容像JSON不代表它是参数容器（Seen: 1/3 个不同工作日）

- 根因：工具根arguments/input和真实嵌套字符串共用JSON解码入口；解码清空字段标签，导致人工配置auth_blob等规则及内置password规则被绕过。
- 防复发：仅协议根容器允许解码；实际字符串叶子先沿配置字段规则处理并保留完整还原边界。覆盖内置与自定义字段、JSON样对象/数组串、普通根容器子键的正负判据。
- 证据：48个真实上游载体+Redis完整映射通过；独立公开API探针在password/auth_blob的普通和JSON样串均仅看到占位符；撤guard变异变红。
- 溯源：本任务基线493d24ebcdaf45d792130f32b42e6724cb667905；docs/audit/2026-10-16-sensitive-rule-management-validation.md。

## 2026-10-16 — 路由插入还会改变移动端的源码引用（Seen: 1/3 个不同工作日）

- 任务 / 基线：敏感规则管理；493d24ebcdaf45d792130f32b42e6724cb667905。
- 根因：新增管理路由后只关注后端行为与桌面UI，移动端的Go行号注释及精确断言同样依赖被插入的源文件。
- 防复发：修改路由注册块后运行移动端全树Go引用扫描及真实源码断言；按实际注册处订正注释和断言，不扩大漂移/误报名单。
- 证据：桌面236文件2476测试绿后，移动端原门仍有6文件10条行号失败；完整修复及验证见本任务交付报告。
- 状态：已修复待观察。

## 2026-10-16 — 熔断观测镜像不能抢占恢复准入（Seen: 1/3 个不同工作日）

- 任务 / 基线：Vapeur grok-4.6 经154失败；会话 `mvs_4bfb2af7bdd34b2487a7d70d8785cbf0`，分支 `fix/vapeur-circuit-mirror-20261016`，基线 `dd0d824866`。
- 根因：SQL视图禁用异步 `circuit_state` 并不足够；Go Candidate.UnavailableReason 又用镜像摘流，重启/丢失closed事件时请求无法抵达内存熔断器的半开恢复门。
- 防复发：镜像保留诊断但不做候选准入；同时验证无pin的stale-open候选能到真实forward、live-open/quarantine零上游调用，权威鉴权/额度/人工停用仍拒绝；新门须先红再绿并反向变异。
- 证据：154 `f9fc8b1b97b1ce39d8a1a717a8867f77` 非流式503；`9eb908939f3f0df745042055222d9a29` 流式244秒空正文后失败（不是HTTP200成功）。provider镜像回归修前红；恢复镜像摘流与跳过真实Allow两种变异均命中相应断言；恢复后provider/executors/credential/routeguard全包绿。端到端部署验收另行记录实际结果，不把本地测试当现网成功。
- 上线验收（2026-10-16 15:41–15:47）：以现网基线 `6aaefb57`（154 当时 2674 的构建源）+ 本修复 cherry-pick 出聚焦发布树 `ca502c14`，经 `scripts/deploy-154.sh` 上线 154 = `2669-ca502c14`（`verified:true`，active 8781，`/readyz` ready，旧实例 2674 已 drain）。上线后经 154 网关实测 grok-4.6 仍失败，但失败面已定位清楚：非流式 1.1s 503「No available provider」，日志 `[candidate_diag] db_empty plan_count=0 reason_hint=no_catalog_rows_for_model`；流式 240.7s 后 `gateway_survival_fail_closed / deadline_exceeded`，正文空——HTTP200 不算成功。判别实验：同凭据 grok-4.3 同样 0 候选、健康模型 glm-4.5-flash 200 且正文含 `VAPEUR_OK` → 排除发生在**凭据层**（quota/availability 权威门），不是模型名映射、也不是熔断镜像。直连 Vapeur 双模式均 `429 budget_exceeded / Organization balance insufficient`；凭据 63 为 `quota_state=permanently_exhausted`、`circuit_state=closed`。
- 状态：代码已修并已在 154 上线（`2669-ca502c14`）；**端到端成功验收待 owner 充值 Vapeur 组织余额**，充值后需 force_enable 凭据 63 再跑流式/非流式 E2E。充值前不存在「Grok 恢复成功」这一事实。

## 2026-10-16 — `deploy-154.sh` 的 `exec bash` 会绕过 bash5 入口（Seen: 1/3 个不同工作日）

- 任务 / 基线：154 聚焦部署；工作树 `/Users/huangxt/workspace/llm-gateway-go-grok46`，基线 `6aaefb57` + `ca502c14`。
- 根因：`scripts/deploy-154.sh` 末尾是 `exec bash "$SCRIPT_DIR/deploy-seamless.sh" ...`，这个 `bash` 走 PATH；只用 `~/sdk/bash5/bin/bash scripts/deploy-154.sh` 启动并不能让后续链路跑在 bash5 上，系统 bash 3.2 立刻在 `${target^^}` 报 `bad substitution`、在空数组上抛 `extra_flags[@]: unbound variable`。
- 防复发：调用前把 bash5 目录放到 PATH 最前（`PATH="$HOME/sdk/bash5/bin:$PATH"`），不要只换入口可执行文件；失败后先按 `lock/metadata` 的 `started_at` + 持有 pid 是否存活判陈旧再清锁——该脚本在报错前已把 `deploy.lock` 建在目标机上，留着会让下一次部署直接失败。
- 证据：首跑失败并在 154 留下 `deploy.lock`（持有 pid 75015 已死、`commit=ca502c145`）；判陈旧后清理，PATH 前置重跑通过（总 131s、切换 25s、凭据解密冒烟 `failed=0`）。

## 2026-10-16 — 部署输出过管道会掩盖退出码（Seen: 1/3 个不同工作日）

- 任务 / 基线：同上，154 聚焦部署首跑。
- 根因：`deploy-154.sh ... 2>&1 | tail -80` 的退出码取自 `tail`，脚本中途 `exit 1` 仍呈现 `Command exited with code 0`，差点被当成部署成功。
- 防复发：部署类命令不接管道，重定向到日志文件后单独读文件并显式回显 `RC=$?`；先看回执里的「✓ 部署完成 / verified」再回看退出码。
- 证据：首跑真实失败（bash3.2 报错）；二次运行 `DEPLOY_RC=0`，回执含 `blue-green handoff complete (old=2674-6aaefb57, new=2669-ca502c14, active_port=8781)`、`providers=14,18,13092 creds=17 failed=0`、`verified_at` 已写。

## 2026-10-16 — 「无候选」要先分清凭据层排除还是模型名问题（Seen: 1/3 个不同工作日）

- 任务 / 基线：同上；154 `2669-ca502c14`，库 252 `llm_gateway`。
- 根因：日志里的 `reason_hint=no_catalog_rows_for_model` / `plan_count=0` 读起来像「模型目录里没这个模型」，真实原因却是该凭据被权威 quota/availability 门挡在计划之外——整个 provider 的所有模型一起从候选里消失。
- 防复发：见到 db_empty 就做一次判别实验（同凭据另一模型 vs 另一健康凭据的模型）；只有「同凭据模型全空 + 健康模型正常」才能归因凭据层，再去查 `quota_state`/`availability_state` 与上游真实响应，不要去改模型名映射或目录播种。
- 证据：grok-4.6 与 grok-4.3（凭据 63）均 `plan_count=0`；glm-4.5-flash 200 且正文含 `VAPEUR_OK`；直连上游双模式 429 `budget_exceeded`。

## 2026-10-16 — 线格式学习键必须绑定运行时端点而非声明协议（Seen: 1/3）

- 任务 / 缺陷：[magpie P0 终态审计](docs/audit/2026-10-16-magpie-p0终态审计-13项缺陷五分片收口.md)；reqprobe 学习跨 provider/wire 污染 + Chat 腿误删 stream_options。
- 基线 / 修复 SHA：基线 `dd0d82486`；修复 `86459be8b`。
- 根因：学习键退化为 (providerCode, model) 且用 cand.Protocol（声明协议）——同一 catalog 多 provider 实例互相生效；responses 声明但能力闸门失败实际走 chat 的请求按 responses 记/查，Chat 必需骨架被误删。
- 防复发检查：学习/记忆类键一律 (providerID, outboundModel, actualWire)；actualWire 由运行时端点决策映射（native→responses/anthropic/否则 chat），不得从候选声明反推；空学习快照不缓存（异步学习落地后下一请求立即可见）；量具用目标 wire 的骨架字段（stream_options），不用会被其它裁剪器删掉的字段（reasoning_effort）。
- 证据：declared-wire 变异让 chat 丢 stream_options 红；空缓存变异让异步学习 60s 不可见红；exact-key 隔离测试覆盖同 catalog 双 provider/双 wire。
- 状态：观察中

## 2026-10-16 — 交互 deadline 必须派生 context，durable 未提交即交接（Seen: 1/3）

- 任务 / 缺陷：同审计；survival 240s 只是循环检查点，阻塞 executor 可远超预算；durable 流被 deadline 永久 FailTerminal。
- 基线 / 修复 SHA：基线 `dd0d82486`；修复 `175f987fa`。
- 根因：deadline 存为 time.Time 只在循环边界比较；attempt ctx 未派生 deadline（WithoutCancel 又把父 deadline 一起丢掉）；settlement 只认 client_disconnected 一个交接条件。
- 防复发检查：任何「有界等待」必须 `context.WithTimeoutCause` 派生到执行 ctx（fake Now 与真 wall-clock 解耦）；父 deadline 优先且不渲染 synthetic terminal；durable 在 `deadline_exceeded && !ContentCommitted` 时 ReleaseToWorker，已提交内容绝不重放；恢复提示（Retry-After）沿 AttemptRecord/tracker→CandidateOutcome→Detail 全链传递，判据禁止手构 CandidateOutcome。
- 证据：阻塞 executor 40ms 预算实测 ~40ms 返回；撤 ctx 派生同测超时红；durable resched 断言；1140s hint 端到端出现在 deadline detail。
- 状态：观察中

## 2026-10-16 — 子任务不得清理不属于它的目录；「性能修正」必须与「什么都不做」对照（Seen: 1/3）

- 任务 / 缺陷：request_logs 退役轮（`df9a213de`）。两个独立问题，都发生在同一天。
- 基线 / 修复 SHA：基线 `2f2c163ee`；修复 `df9a213de`。
- 根因一（证据丢失）：子任务为满足「隔离残留为 0」，把整个 `.audit-validation/` 当残留清理——那里是本轮证据目录（416 端点扫描基线、扫描脚本、admin token、deploy 日志）。**清理范围应限于自己创建的临时物，且删除前逐条确认归属。**
- 根因二（优化制造了更大的问题）：`queryTableSizes` 同一天早些时候被加了一层 `FROM ( ... ) sized` 派生表，注释报「1504ms → 527ms」。派生表**挡住了 LIMIT 下推**，toast 相关子查询从 20 次变成 440 次（每次扫 3881 行 ≈ 170 万行）。真实 EXPLAIN：4571ms vs 无派生表 1104ms。那次修正**只测了改后版本、没与「什么都不套」这个更简单的选项对照**，于是把 4571ms 当成了改进。
- 防复发检查：子任务的删除动作限定在自己 `mktemp` 出来的路径，并在报告里单列「我删除了什么」；删除他人目录前必须先 `ls` 并在交接里说明。性能修正必须留**三方对照**（改前 / 改后 / 不改），且对照项是「最简单的替代方案」而不只是「更复杂的等价写法」。派生表 / 子查询包裹这类结构改动，先 `EXPLAIN (ANALYZE)` 看被它挡住的东西（Limit 是否下推、SubPlan 的 loops 变化），再决定留不留。
- 证据：EXPLAIN 对照 —— 带派生表 `SubPlan 1 loops=440 / Execution Time 4571ms`，不带 `loops=20 / 1104–1289ms`；修复后端点 4.3–7.5s → 0.77–1.00s。
- 状态：观察中

## 2026-10-16 — 判据「静默 SKIP」与「未执行分支」是两种不同的假绿（Seen: 1/3）

- 任务 / 缺陷：同退役轮。6 条 storage 判据在**每一个**真实环境里 SKIP；另一条判据的「列存在」分支因前提从未成立而从未跑过。
- 基线 / 修复 SHA：基线 `2f2c163ee`；修复 `df9a213de`。
- 根因一：跳过条件只认 `TEST_DATABASE_URL`，而本仓真库来源恒为 `LLM_GATEWAY_DATABASE_URL`。同族判据 `admin500DSN` 早有三级回退，唯独这个文件没有 ⇒ 优化在 CI 与任何标准环境里**没有守护**，却显示 PASS。
- 根因二：`titleCol=", "` 与 `titleVal=", 'Cold..'"` 各带一个逗号，拼成 `'..', , '..'` ⇒ 42601。该分支因真库一直没有 `sessions.title` 而从未执行；补登迁移让它第一次跑起来才暴露。
- 防复发检查：判据的 DSN / 环境变量取值必须与同族判据**同构**（同一份回退链），不允许各写各的；提交前用「该环境变量在本仓标准入口下确实存在」跑一遍，确认无 SKIP。凡是有「前提在场 / 前提不在场」两条分支的判据，必须分别实跑过——未跑过的分支等于未测试。
- 证据：修 DSN 回退后 6 条判据由全 SKIP 变为 6/6 PASS（cold=2.754s → hot=7.792µs），并完成缓存 TTL 变异（短路 `time.Now().Before(snap.expiresAt)` → `TTLExpiryRefetches` 红在「TTL 形同虚设」）。
- 状态：观察中

## 2026-10-16 — 「既有失败」的归因要用 worktree 强证据，不用 git stash（Seen: 1/3）

- 任务 / 缺陷：同退役轮。子任务报「6 个 admin 测试失败与我无关，追溯到退役轮 staged 改动」。
- 基线 / 修复 SHA：基线 `2f2c163ee`；修复 `df9a213de`。
- 根因：子任务用 `git stash` 做对照——(a) stash 只覆盖它自己的改动，不等于 pristine HEAD；(b) 它自己承认 stash 破坏了 staged/unstaged 区分，把 869 的改动从 staged 变成 unstaged。结论「与本轮无关」因此没有证据支撑。
- 防复发检查：判定「既有失败与我无关」一律用 `git worktree add --detach <tmp> origin/main` 在**同一数据库**下复跑（唯一变量是代码）。禁止用 `git stash` 做对照实验；若确需 stash，事后必须逐文件核对 staged 状态是否复原。
- 证据：`TestCrossTableRequestIDRepresentation` 在 pristine origin/main **PASS**、在本 worktree **FAIL**，同一数据库 ⇒ 确由本轮改动引起，随后白名单项被正确移除。
- 状态：观察中

## 2026-10-16 — 切流失败报「version mismatch」时，先查端口被**哪个部署根**占着（Seen: 1/3）

- 任务 / 缺陷：session-audit 修复上线本地网关。`deploy-local.sh` 连续 3 次 `[cutover] readiness gate failed`，每次都是 `version mismatch {… build_seq: 2677 …} 2.5.8-…-2678`，最后 `error: active cutover failed; previous release was restarted`。
- 基线 / 修复 SHA：基线 `abdd4c752`；修复 `a3acb8bb3`。
- 根因：8782 被**另一个 runtime root**（`~/kaixuan/llm-gateway-go-v2`，非 git 仓库的纯部署根）的进程占着。`deploy-local` 的 pidfile 是 **root 局部**的（`<root>/run/gateway-<port>.pid`），从 worktree 部署时看不见对方的 pidfile ⇒ 停不掉它 ⇒ 新进程 `bind: address already in use` 退出 ⇒ readiness gate 读到的 `/version` 仍是旧进程的 2677 ⇒ 报成「版本不匹配」。
- **回滚不自愈**：脚本的 `previous release was restarted` 只是重启旧 release，它同样 bind 不了那个端口。所以修复永远上不去，而现象看上去像「新构建产物有问题」——本轮差点被引去查二进制。
- 诊断入口：部署 **stdout 不会直说**，只说 version mismatch；真因在 `logs/shutdown.log` 的 `abnormal_exit / exit_type=listen_error / details=listen tcp :8782: bind: address already in use`。
- 防复发检查：部署前先确认 active 端口的占用者属于哪个 root —— `cat <root>/run/active-port` + `cat <root>/run/gateway-<port>.pid` + `ps -p <pid> -o command` 看 binary 路径前缀。若占用者不属本次 root，先用**那个 root 的** run 目录停它；停时按 zombie-aware 判活（本轮 TERM 15s 未退才 KILL，不能只看 pidfile 删除成功就当已停）。另：本机 `lsof -iTCP:<port> -sTCP:LISTEN` 会卡住，用 `lsof -nP -p <pid> | grep LISTEN` 更快。
- 证据：三次 `listen_error`（pid 48486 / 48686）；停掉占用者 8620（v2 root 的 2677）后重部署，`VERIFY_PASS=1`，2679 在 8782 通过 `[verify]` 与凭据解密冒烟。
- 状态：观察中

## 2026-10-16 — 验收脚本的路由与凭据口径必须与被测链同源，且异常路径要清理（Seen: 1/3）

- 任务 / 缺陷：session-audit 端到端验收脚本首跑连挂两次——先 `HTTP 404`，修完变 `HTTP 401`，两次都在共享真库留下 2 行探针数据。
- 基线 / 修复 SHA：基线 `abdd4c752`；修复 `a3acb8bb3`（脚本在 `.audit-validation/`，不入库）。
- 根因一（404）：按 REST 惯例猜成 `/api/auth/login`，实际注册是 `/api/auth/token`（`admin/handler.go:922`）。`grep '"/api/auth/login"'` 零命中本可提前发现。
- 根因二（401）：`.env.local` 的值是**单引号包裹**的，`cut -d= -f2-` 把引号一起带上 ⇒ 拿带引号的串去比对 bcrypt。而部署脚本的 dotenv 解析会去掉定界引号 ⇒ 两边口径不一致。错误文案是「口令与 users 表不匹配」，极易误判成「口令失效、需要找用户重置」，实际只是解析口径问题。
- 防复发检查：(a) 路由名以 `HandleFunc` 注册处为准，不按惯例猜；(b) 复用部署链凭据时，口令走**同一份 dotenv 解析**（配对引号去界），不要自己 cut、也不要落到临时文件再 strip（多一次中间态）；(c) 验收脚本对真库的写入一律放 `finally` 清理——首跑 404 抛异常绕过了清理，探针行留在共享真库污染后续判据计数，只能手工 SQL 清；(d) 端到端断言要**双向**：除了「前提在场时 stats.total≥2」，还要「删掉探针后 total 正好回落 2」的相对对照，否则 `total=0` 既可能是「修好了且真没数据」也可能是「静默失败的老 bug」，响应上无法区分。
- 证据：修正后 12/12 PASS；`finally` 生效的那一轮登录失败也打出「验收数据已清理 — 残留 0 行」；前后对照 `total=2 → 0`，`by_status={need_approval:1, pass:1}`、`avg_score=50`。
- 状态：观察中

## 晋级记录

将已晋级条目移到这里，并链接目标规则、清单或技能。同一模式只按不同工作日计 `Seen`；出现三天后晋级。经证据确认的安全、隐私、数据丢失或生产事故，若已有可执行的防复发检查，可立即晋级。
