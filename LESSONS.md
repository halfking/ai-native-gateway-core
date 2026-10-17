# 项目经验队列（LESSONS）

> 这里只记录可复用的返工、审查、验证或生产问题，并附可核验的证据。开工时先读相关条目；收尾时只新增可行动的发现，常规成功不必记一条。证据和晋级标准见 `.agents/rules/66-skill-evolution.md`。

## 待观察经验

## 2026-10-17 — Bash 路径里的版本四元组不是外传目的地（Seen: 1/3）
- 任务 / 缺陷：ZCode/MiniMax 装 JDK/Maestro 时 Bash 参数含 `jdk-17.0.20.1+1`，整条 `output_policy_blocked`（request `ecb14454…`）。
- 基线 / 修复 SHA：生产取证在 2693；修复 `c07632314`（r1033），154=`2695-c0763231`（8781）。
- 根因：`server_ip` 的 `\b` 在 `-`/`+` 处仍成词界，把版本号当成 Apple 公网段 IP；r1020b 曾登记为残留。
- 防复发检查：邻接字符豁免（前邻 token 粘连或后邻 `+`）；正负对照裸公网 IP/URL 主机位仍拦；变异撤 `isVersionTokenGlue` 前邻分支必须红。
- 证据：`TestR1033*`；154 E2E `tool_choice=Bash` + JDK 路径流式 `finish_reason=tool_calls` 无阻断；二进制含 `isPathOrVersionEmbeddedAddress`。
- 状态：已修复待观察

## 2026-10-17 — Magpie 空回复要对齐 chat 与 Responses 两腿（Seen: 1/3）
- 任务 / 缺陷：154 `minimax-m3` / `empty_model_response`；Responses 桥已有 Magpie 语义，chat 腿仍把空 `tool_calls` 幻影当成功。
- 基线 / 修复 SHA：生产 2682 复现；修复 `85f81dba9`，部署 154=`2684-85f81dba`（8781）。
- 根因：`ChatChunkHasOutput` 对任意非空 `tool_calls` 数组计语义 → 空流 early-empty 计数被重置 → benign-EOF 伪成功；客户端报 `empty_model_response`，网关不换候选。
- 防复发检查：幻影（空 name/arguments）与具名 tool 正负对照；chat gate EOF（finish 无 [DONE]）必须 `KindEmptyResponse`；Responses 与 chat 两腿同测。
- 证据：`TestChatChunkHasOutput/phantom`、`TestRunEmptyStreamGatePhantomToolCallsTriggerEarlyEmpty`、`TestR1028ChatPhantomToolCallsFinishWithoutDoneFailsOver`；二进制含 `Magpie emptyReply failover`。
- 状态：已修复待观察

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

## 2026-10-16 — 工具候选的授权与缓冲上限要贯穿完整流式链（Seen: 1/3）
- 任务 / 缺陷：[Magpie第四轮](docs/audit/2026-10-16-magpie-round4-tool-contracts-errors.md)；bool-only纠正会凭空造工具名，失败候选又可能被后置MiniMax清理吞掉，choice上限也挡不住无换行物理行。
- 基线 / 修复 SHA：基线 `755c4aecf277e1045fbdf79bdcb3e87deda2dffe`；修复 `ffd9cc6a7`，冻结38文件清单摘要 `d7cd107bdc2b8a4e8a101e887504a55ad7934336d5e61f86fa425c7265303cba`。
- 根因：解析成功不等于客户端授权；局部parser修好后，后置清理、buffer和terminal仍可破坏语义。
- 防复发检查：由真实request取得不可变精确function名称；未知/不完整候选穿过实际hook仍为原文；同时限制choice、候选文本和物理行，验证EOF/data+error/LF/UTF8，不能凭空创建finish。
- 证据：真实三入口与供应商known/inferred路径均有判据；reader五个隔离变异命中预期断言；规格、代码、Go审查覆盖最终38文件。
- 状态：已修复待观察

## 2026-10-16 — 工具历史去重需证明镜像且保留原位置和元数据（Seen: 1/3）
- 任务 / 缺陷：[Magpie第四轮](docs/audit/2026-10-16-magpie-round4-tool-contracts-errors.md)；ID-only去重会吞不同名称/参数的调用，重建镜像会丢cache_control或改变content顺序。
- 基线 / 修复 SHA：基线 `755c4aecf277e1045fbdf79bdcb3e87deda2dffe`；修复未提交，冻结清单同上。
- 根因：消除重复与保持信息等价是两条独立约束；顶层无重复键也不能证明被编辑的nested content无歧义。
- 防复发检查：局部batch配对；镜像需ID+name+args及一对一次数相等；保留原content块位置和metadata，只抑制对应canonical副本；每个实际field editor拒绝重复目标字段。
- 证据：三客户端×三上游×流/非流18路和6路实际重试通过，34原黄金未改；metadata/order/name/args反例及typed歧义拒绝有独立测试。
- 状态：已修复待观察

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

## 2026-10-16 — 页面空白先证「数据请求发出去没有」，再谈后端；同一 token 跨端点对照排自己的脚本（Seen: 1/3）

- 任务 / 缺陷：退役轮页面验收。`/request-logs` 渲染完整（顶栏、列头、已登录为超级管理员）但显示「共 0 条」「无记录」，而库里 session_turns 族有 574 行。
- 基线 / 修复 SHA：观察于 `e4cb0c558`（页面侧问题不在本轮退役改动集内，`git diff HEAD~2 HEAD` 对 `web/` 零命中）。
- 根因一（差点误判成产品缺陷）：验收脚本里 `tok = d.get("token") or (d.get("data") or {}).get("token")` **漏了 `access_token` 分支** ⇒ 发出 `Bearer None` ⇒ `/api/logs` 返 401。同一脚本里 `/api/admin/sessions` 却是 200（走的是另一条 token 解析路径），差点得出「admin JWT 读不了 /api/logs」的产品结论。补上分支后同一 token 下 `/api/logs` 返回 **200 + 170KB 数据**。
- 根因二（页面侧，待定）：浏览器 `network` 层显示页面**根本没发出** `/api/logs` 请求，只有 3 个 pending 的辅助请求（`/api/system/background-tasks`、`/api/routing/available-models`、`/api/keys`）；点「刷新」275s 超时。已排除服务端 hang（curl 69ms）、DB、静态资源、网关进程、产物不一致，停在客户端主线程。
- 防复发检查：(a) **页面空白 ≠ 后端无数据**——先用 network 层确认数据请求是否发出，发都没发就别去查后端；(b) 同一响应异常要**跨端点对照**：`/api/admin/sessions` 200 与 `/api/logs` 401 并存时，先怀疑自己的 token 解析，而不是给后端定罪；(c) token 解析写成 `d.get("token") or d.get("access_token") or …` 三段式，别省。
- 证据：补分支后同 token `/api/admin/sessions?limit=2` → 200、`/api/logs` → 200（170246B）、`/api/logs?page=1&page_size=5` → 200（10840B）；页面侧 `inspect` 20ms 正常但 `click`/`query(network)` 275s 超时。
- 状态：观察中（页面侧根因未定位，需能读浏览器 Console 的环境）

## 2026-10-16 — 「写入持续失败」的日志关键字比报错文本更早暴露孤儿迁移（Seen: 1/3）

- 任务 / 缺陷：退役轮追查 5/7 目标模型不可用。根因链有四层：`/v1/models` 少了它们 → 绑定它们的 cred=1/cred=3 是 `quota_state=permanently_exhausted` → 路由候选 SQL（`provider/client.go` 的 `quota_state NOT IN ('permanently_exhausted',…)`）把它们全部排除 → 而这个状态之所以**永远翻不回来**，是因为探针写回一直在失败。
- 基线 / 修复 SHA：修复 `apply-db-revision-sequence.sh` 补登 044（见下条提交）。
- 根因：`sql/migrations/startup/044_health_source_probe_now.sql` 早就在仓库里，文件头注释把症状原样写了出来（`ERROR: … violates check constraint "chk_credentials_health_source" (SQLSTATE 23514)`），但它**从未进入任何部署入口**——不在 `apply-db-revision-sequence.sh` 的 files=()，也不在 `installer/internal/dbinit/runner.go` 的 StartupFiles（后者只收 478+，0xx 段被 baseline 吸收，而 **baseline 里的约束同样是旧形态**，所以全新安装也复现）。于是 `chk_credentials_health_source` 永远停在缺 `probe_now` 的旧白名单，而 `bg/credential_probe_v2.go:2180/2198` 的 ProbeNow 快路径固定写 `probe_now` ⇒ **每一次探测都写不进去**。
- 定位手法（可复用）：不要从「模型为什么不可用」这种结果往回猜，直接在日志里 grep **写回失败的日志关键字**——`hard-quota bookkeeping update failed` 一命中就把 SQLSTATE 23514 和约束名摆出来了，四层根因链在一次 grep 里塌缩成一条。四层归因（列表缺 → 凭据态 → 候选 SQL → 写回失败）如果逐层猜，会在中途误判成「额度真的用完了」。
- 防复发检查：迁移文件存在 ≠ 会被执行。判断一个迁移是否真正在通道里，要查**两个入口 + baseline**：`scripts/apply-db-revision-sequence.sh` 的 files=()、`installer/internal/dbinit/runner.go` 的 StartupFiles、以及 baseline 是否已吸收其效果。`sql/migrations/startup/` 有 495 个编号而 installer 只登记 264 个 ⇒ 孤儿是常态不是意外。
- 证据：修复前 cred=3 在 22 分钟内 23514 失败 22 次（累计 40 次，最后一次 21:33:20）；补登并经通道执行后（`gateway_db_revision_sequences` 记 content_sha256 == execution_sha256、`runtime_adaptation=request-logs-physical-v1`），cred=1 与 cred=3 的 `health_source` 都成功写成 `probe_now`，**23514 计数不再增长**，且 cred=1 的 `quota_state` 从 `permanently_exhausted` 自行翻回 `ok`。迁移序列契约门 9/9 RC0。
- **但不要过度宣称**：模型可用性并未因此恢复——cred=3 仍是 `availability_state=rate_limited`（真实上游限流），`/v1/models` 从 95 变成 93。数据库写回恢复只是**必要条件**，真实限流/额度仍需上游侧解决。cred=1 的 `quota_state` 翻回 `ok` 但其模型未回到列表，说明还有 `model_probe_state` / 探测态等第二层闸门未过。
- 状态：观察中（044 已进通道；生产 154/245 是否同样受影响**未核实**，需在生产侧确认约束白名单）

## 2026-10-16 — 「页面空白 vs 后端无数据」用网关访问日志定案，别用 CDP 快照（Seen: 1/3）

- 任务 / 缺陷：`/request-logs` 渲染完整、已登录为超级管理员，但恒显「共 0 条 / 无记录」；而 `GET /api/logs` 直接 curl 返回 `count=576` + 37KB。
- 基线 / 修复 SHA：观察于 `c3abfbca1`（不在退役改动集内，`git diff HEAD~2 HEAD` 对 `web/` 零命中）。
- 方法一（错的那条）：浏览器 `network` 快照里只看到 `/api/keys`、`/api/routing/available-models`、`/api/keys` 三条 **pending**，据此判「页面根本没发 `/api/logs`」。**这个推论不成立**——服务端实测这三条分别只要 3ms/35ms/51ms，快到完全可能在快照瞬间处于 pending。差点把"快照里没看到"当成"没发生"。
- 方法二（对的那条，且不依赖浏览器工具）：**查网关自己的 `http_request` 访问日志**。它是服务端事实，不受 CDP 采样/超时影响。
- 网关日志给出的事实链：浏览器时代的请求序列是 `/api/keys` → `/api/providers` → `/api/routing/available-models` → `/api/system/bootstrap/status`，**唯独没有 `/api/logs`**；`/api/providers/{id}/credentials` 在 21:13 之后再未出现；而 `/api/providers` 自身**出现过 500**（21:18:44 / 21:23:33，夹在 200 之间）。
- 代码侧定位到可疑结构：`onMounted` 是**串行 await 链** —— `await loadKeys()` → `await loadCredentialOptions()` → `await load()`。第三段里的 `loadCredentialOptions()` 又对**每个** provider 并发 `await getProviderCredentials(p.id)`（`Promise.all`，provider 表有几十行）。这条链上**任何一环不 resolve，`load()` 就永不执行**，而模板已经渲染完，于是正好表现为「页面完整但 0 条」——服务端无责。
- 已排除：后端 `/api/keys` 3ms、`/api/routing/available-models` 35ms、`/api/logs?page=1&page_size=20` 51ms（结构为 `{items,count,aggregate}`，与前端 `resp.items`/`resp.count` **完全匹配**）；时间范围过滤无关（带 from/to 的四种时区组合均返回 576）；`timeRange()` 是纯同步无循环；`loadCredentialOptions()` 的 catch 分支会正常 `return`。
- 防复发检查：(a) 页面空白**先查服务端访问日志**判断"请求发没发"，再谈渲染；(b) CDP/network 是**采样**不是事实，pending 不等于没发生，慢请求必然出现 pending；(c) 前端 `onMounted` 里的串行 `await` 链要当"单点故障链"审：任一环挂起 = 整个页面的数据区永久空白，而 UI 毫无报错。
- 状态：观察中（确切卡点仍需能读浏览器 Console 的环境；已把范围从"整个前端"收窄到 `loadKeys/loadCredentialOptions` 这条链，且指向 `/api/providers` 的 500 与不稳定的 per-provider credentials 扇出）

## 2026-10-16 — 无节制并发扇出会饿死主数据请求，且症状是「无报错的空白页」（Seen: 1/3）

- 任务 / 缺陷：`/request-logs` 恒显「共 0 条 / 无记录」，而 `GET /api/logs` 直接 curl 返回 `count=576`。页面完整渲染、已登录为超级管理员、**控制台无任何报错**。
- 基线 / 修复 SHA：修复在 `RequestLogsView.vue` 的 `onMounted`（`await loadCredentialOptions()` → `void loadCredentialOptions()`）。
- 根因：`onMounted` 是串行 await 链 —— `loadKeys()` → `loadCredentialOptions()` → `load()`。第三段里 `loadCredentialOptions()` 对**每个** provider 并发 `await getProviderCredentials(p.id)`（`Promise.all`）。本地 `providers` 表 47 行 ⇒ 首屏一次性扇出 **47 个同源请求**；浏览器对同源 HTTP/1.1 只有约 **6 条**并发连接，主数据请求 `/api/logs` 被挤到队尾并最终被客户端取消。
- 服务端留下的铁证：`admin listLogs count query failed: context canceled`（`caller: logs.go:646`）。**注意这条在 `http_request` 访问日志里查不到** —— 请求被取消后没走到记录访问日志那一步，只在 handler 的错误分支留痕。只看访问日志会得出「前端根本没请求」的错误结论（我第一轮就是这么错的）。
- 讽刺之处：这段实现**违反了自己上方 40 行的设计约定**——注释原文写着「任一来源失败都不阻塞日志列表渲染，下拉回退为空」，但代码用 `await` 把下拉加载变成了日志列表的硬前置。修复即回归该约定：`void loadCredentialOptions()`，主列表先发，下拉后台补齐，失败仍由内部兜底并在筛选区显示，语义不变。
- 为什么这类症状极难定位：① `/api/logs` 在访问日志里**缺席**，容易被误判成"前端没发"；② 手动 curl 一切正常，服务端零责任；③ 前端无报错，UI 完全正常只是没数据；④ 触发条件是**数据量**（47 个 provider），provider 少的环境永远复现不了。
- 防复发检查：(a) 排查"页面空白"时，**服务端 handler 错误日志要先于访问日志**看——`context canceled` / `deadline exceeded` 只在前者；(b) 前端对列表做 `Promise.all` 扇出前先看**基数**，并考虑分批或加并发上限；(c) `onMounted` 里的串行 await 链要当单点故障链审，任一环挂起 = 整页数据区永久空白；(d) 注释写下的设计约定要与实现对账，注释说"不阻塞"而代码 `await` 了，就是 bug。
- 证据：修复前网关日志 21:44:04 可见 `/api/providers` 200 后仅 6 个 credentials 请求（其中 5 个 500），`/api/logs` 只在 handler 错误分支留 `context canceled`；`/api/providers` 实返 47 条。手动复现 6 个 credentials 端点全部 200（说明 500 也是并发挤压的伴生现象，非独立缺陷）。修复后 `vue-tsc -b` RC0。
- 状态：观察中

## 2026-10-16 — 「并发超限」要用 duration 算 in-flight，用请求条数会高估（Seen: 1/3）

- 任务 / 缺陷：`/request-logs` 首屏恒 0 条。排查中我一度用「任意 100ms 窗口内的请求条数」当作并发数，得出「峰值并发 13、超浏览器上限 6 一倍多」，并据此又加了两轮修复。**这个口径是错的**。
- 基线 / 修复 SHA：三轮修复 `c4212904b`（扇出分批）、`d6c79ba22`（全局闸门）、`8ab468fc6`（原生 fetch 纳入）。
- 口径纠正：服务端 `http_request` 日志带 `duration_ms`，用它把每个请求展开成 `[开始, 结束]` 区间再数重叠，才是**真实 in-flight 并发**。改口径后实测峰值 **4**（正好等于闸门上限），**从未超过浏览器的 6**。用请求条数会把「10ms 内各完成一个的 10 个请求」误读成「10 路并发」。
- 决定性对照：16 线程并发 curl 复刻整个首屏（含 `/api/logs` + 全部 per-provider credentials）共 32 个请求，**32/32 全 200**；同一时刻浏览器侧仍被取消。据此判定最后一层原因是**浏览器环境**——非活动标签页被 Chromium 冻结，飞行中的 fetch 被 abort，服务端如实记 `context canceled` 而前端拿不到可处理的错误。旁证：被取消的 `/api/providers/16/credentials` 耗时 **0.0ms**（连接被直接重置，不是排队超时）。
- 仍然成立的修复（三轮都已上线且可验证）：47 路 provider 扇出 → 分批 4；首屏 20 个请求 → 15；`bootstrap/status` 由 useAppNav 与 router 各调一次合并为 1 次；真实 in-flight 并发受控。**这些是实打实的首屏改进，只是它们不是 0 条的最终原因。**
- 防复发检查：(a) 并发判断必须用 duration 区间重叠数，**不要用时间窗内的请求条数**；(b) 判断「是不是服务端的锅」要有一组**同条件并发对照**（并发 curl 复刻同一批端点），32/32 成功就别再往服务端找；(c) 三轮修复后症状仍在，要敢于把最后一层归到环境上并**明说**，而不是继续加码改动面。
- 证据：`vue-tsc -b` RC0（三轮）；前端全量 **237 文件 / 2484 用例全 PASS**（37~40s，三轮）；duration 重算峰值 in-flight = 4；并发 curl 32/32。
- 状态：观察中（应用侧已修完；页面最终复验需在**可交互**的浏览器环境进行，本会话的 Electron 浏览器标签多次冻结不可靠）

## 2026-10-16 — 性能数字先证伪再采信：行过滤会把报错耗时当成提速（Seen: 2/3）

- 任务 / 缺陷：给 `GET /api/admin/data-lifecycle/storage/tables`（本机实测 1286 / 2500 / 1734ms，唯一还 >2s 的 admin GET）找优化手段。测出把 TOAST 的相关子查询 `SELECT t.oid FROM pg_class t WHERE t.reltoastrelid = c.oid` 改写成 `LEFT JOIN pg_class tc ON tc.oid = c.reltoastrelid` 后只要 **6–9ms**（原写法 1170 / 1197 / 3463ms），提速两个数量级，差点直接采用。
- 根因：测量命令是 `psql -f q.sql 2>&1 | grep -i '^Time'`。**`\timing` 对失败的语句同样打印耗时**。而 PG 17 的 `pg_stat_user_tables` 里**根本没有** `total_size / indexes_size / toast_size` 列（`information_schema.columns` 查 `%size%` 返回 0 行），查询直接 `ERROR: column s.total_size does not exist` —— 那 8ms 是**报错耗时**。是同批跑的对照查询（没做行过滤）把同一个错误暴露了出来。
- 补充口径纠正：真正的慢因不是 TOAST 子查询，而是 `ORDER BY pg_total_relation_size(c.oid) DESC` —— 排序必须对**全部 440 张表**逐个算大小，`LIMIT 20` 省不掉这笔钱。改成 OID 索引查找实测**没有收益**（1170/1197/3463 vs 1696/1254/1455ms），所以最终走短 TTL 快照。
- 防复发检查：(a) 性能对比命令**不允许只用 `grep Time` 这类行过滤**，至少保留 `ERROR` 或校验返回行数；(b) 报"提速"前先跑对照查询确认两版都真的返回了数据行，而不是只看耗时；(c) "新写法快很多"必须能指出旧写法慢在哪一段的 EXPLAIN 证据，否则先怀疑自己测错了东西。
- 证据：上文三次实测序列；`\timing` 对 ERROR 语句仍输出 `Time:`（复现命令见本条"根因"）；最终实现 `admin/data_lifecycle_storage.go` 的 `tableSizesSnapshot`，冷 3.47s → 热 14.3µs。
- 补记（当日第 2 次，同源）：查 `pg_class` 确认 `session_bodies_hot` 是否存在时，`ORDER BY relname` 的输出被 `head -25` 截断，该行恰好排在所有 `session_bodies_2026_10_*` 之后，于是又得出一次「表不存在」的错判。**行截断与行过滤是同一类错误**：都只保留让人看到的那部分，却按「全部」来推理。见下条。
- 状态：已落地

## 2026-10-16 — 缓存切片类改动的真正风险是口径漂移，不是缓存（Seen: 1/3）

- 任务 / 缺陷：`/storage/tables` 从"每次按 `limit` 查库"改成"一次采满 `tableSizesMaxLimit=200` 行、再按请求的 `limit` 切片"。
- 根因：新引入的 `sliceTableSizes` 必须按**本次 limit** 重算 `totalBytes` 与 `PercentOfDB`。若沿用采满时的值，**不会报任何错** —— 百分比照样落在 0–100，页面照常渲染，只是 `limit=20` 显示的变成"对 200 行求和的占比"，与优化前口径不同。这类缺陷没有任何运行时信号，只能靠判据挡住。
- 连带发现：**Top-N 是 Top-M 的前缀，这个推理要求排序确定性**。原 SQL 只有 `ORDER BY 4 DESC`，字节数并列时 PG 的排序不稳定，前缀性质只能算"大概率成立"。补 `, 1`（`relname`）作次键后严格成立，对耗时无影响。
- 防复发检查：(a) 占比 / 均值这类**相对量**，判据要用**响应自身的分母**复算，不要用绝对常量 —— 绝对常量会让"分母错了但分子恰好也错"的实现蒙混过关；(b) 引入缓存或切片时，逐字段列出"哪些值依赖 limit / 参数"，对**每一个**写断言，并先断言区分力前提（如 Top-5 之和确实小于 Top-200 之和）；(c) 任何"前缀 / 子集"推理，先确认底层排序有确定性 tie-break。
- 判据：`admin/data_lifecycle_storage_tables_perf_test.go` 的 `TestDataLifecycleTableSizesSliceRecomputesTotals`（变异验证：切片忽略 limit → 红；占比不重算 → 红）。
- 状态：已落地

## 2026-10-16 — 判据覆盖了被测常量，会让该常量的变异不可见（Seen: 2/3）

- 任务 / 缺陷：给 `storageOverviewTTL` / `tableSizesTTL` 写"TTL 到期会重新取数"的判据。第一版写法是把 TTL 临时压到 60ms 再等过期。
- 根因：这版判据**把被测常量本身覆盖掉了**，于是"把 TTL 改成 365 天"这个变异在判据视野里不可见 —— 过期用例照样全绿（因为它自己把 TTL 设成了 60ms），只有显式失效路径会变，而显式失效不看 TTL。结果是数据可以永久陈旧而判据无感。
- 修法：加一条**独立的默认值上界断言**（`tableSizesTTL ∈ (0, 60s]`），放在临时覆盖**之前**执行，让生产默认值本身进入判据范围。TTL 改成 365 天时红在 `:343`。
- 防复发检查：(a) 任何判据如果临时修改了被测常量，必须另有一条**不改常量**的断言覆盖它的默认值；(b) "过期刷新"类判据若只覆盖常量，就只能证明"覆盖后的值会过期"，不能证明"生产值会过期"。
- 证据：`TestDataLifecycleTableSizesTTLExpiryRefetches:343`；总览侧同名判据 `TestDataLifecycleStorageTTLExpiryRefetches:399` 同构补齐。**同一模式当日出现两次（总览轮 + Top-N 轮），第三次出现即晋级为规则。**
- 状态：观察中

## 2026-10-16 — 「停写/数据丢失」先怀疑自己数错了表：写入目标是 hot 表不是分区父表（Seen: 1/3）

- 任务 / 缺陷：退役验收查「新流量的 session 正文有没有落库」。`SELECT count(*) FROM session_bodies` = 550，`max(ts)` = **2026-10-16 16:22**，近 90 分钟 **0 行**；而同期 `session_turns_hot` 落了 47 行、其中 27 个成功轮。差点判成「正文写入在 16:22 停了 9 小时 = 一类数据丢失」。
- 根因：**写入目标根本不是 `session_bodies`**。`domains/session/v2/bodies_writer.go:310,368` 写的是 `public.session_bodies_hot`（8 小时窗口表），由 PartitionManager 事后提升成 `session_bodies_yyyy_mm` 月度分区。而 `session_bodies_hot` 是**独立表**（`pg_inherits` 里对它的继承行数 = 0），所以 `count(*) FROM session_bodies`（分区父表）**不包含** hot 表的任何行。实测 hot 表 76 行、近 90 分钟 48 行、`max(ts)` 就是当时——正文一直在正常写。
- 连带一个同源的假回归：查 `pg_class` 想确认 hot 表在不在，结果 `ORDER BY relname` + `head -25` 把 `session_bodies_hot` 截掉了（ASCII 序里 `session_bodies_2026_10_*` 全排在 `session_bodies_hot` 之前），于是又得出一次「hot 表不存在」的错判。**同一天第二次被行截断/过滤吃掉证据。**
- 防复发检查：(a) 判断「某类数据是否还在写」时，**先从写入代码里 grep 出它 INSERT 的那张表**，再去数那张表，不要按表名"看起来像父表"去猜；(b) 分区体系里要分清**分区父表 / hot 窗口表 / 已提升的月度分区**三者的包含关系，`count(*)` 只在父子关系成立时才等价；(c) 提升是延迟的（此处 8 小时），所以"父表最新时间戳停在几小时前"本身完全正常，不构成停写证据；(d) 任何 `head -N` / `grep` 截断的输出，若要据此下"某对象不存在"的结论，必须先确认被截断的区间不包含它。
- 证据：`session_bodies`=550（max ts 16:22）/ `session_bodies_hot`=76（max ts 与当时一致）；27 个成功轮的正文覆盖率用 hot 表算是 **27/27**，用父表算是 0/27。
- 状态：已落地（结论改写：正文持久化无缺陷）

## 2026-10-16 — 会话 id 只认 `gw_`/`gt_`/`gs_` 前缀，自造 id 会被静默丢弃（Seen: 1/3）

- 任务 / 缺陷：做「5 轮真实多轮会话」验证时，客户端发 `X-Session-Id: retire-verify-<hex>`，结果库里每个会话都只有 **1 turn**，会话 id 全是网关新生成的 `gw_*`。
- 根因：`domains/streaming/handler.go:581 sanitizeGwSessionHeader` 只接受 `gw_`（主会话）/ `gt_`（自动标题分支）/ `gs_`（自动摘要分支）三种前缀，其余一律当"客户端元数据"丢弃（`cmd/gateway/main_pipeline.go:772` 的 `X-Gw-Session-Id` → `X-Session-Id` 回退只解决大小写与命名优先级，不解决前缀校验）。所以**多轮验证在结构上根本没发生**，5 次请求被拆成 5 个独立单轮会话。
- 判别方法：`prompt_tokens` 是否随轮次增长。改正前缀后 glm-5.2 的 ptok 为 33 → 62 → 97，说明上文确实带上了；改正前每轮 ptok 恒定。
- 防复发检查：(a) 自造客户端标识前先读校验函数，别假设"任意字符串都会被采纳"；(b) 「多轮」验证必须有一个**独立于响应**的证据（库里同一 session 下 turn_no 连续递增、或 ptok 单调增长），否则 5 次独立请求会被误当成 5 轮会话；(c) 断言里不要用客户端自己传的字段做过滤条件——网关可能根本没用它。
- 状态：已落地

## 2026-10-17 — 「模型不可用」先看错误码分层：no_candidate vs All candidates failed（Seen: 1/3）

- 任务 / 缺陷：验收「7 个模型」时，6 个不可用。我一度归因为
  「4 个在 `provider_models` 没登记 + 建议给它们绑一条到健康凭据上」。
  **两个结论都是错的**，而且方向相反——真实阻塞是上游容量。
- 决定性证据（错误码分层）：请求 `minimax-m2.7` 时拿到的是
  `model_not_found: **All 3 candidates failed**`，而 `minimax-m3` /
  `glm-5.3-flash` 拿到 `no_candidate`。
  - `no_candidate` = 候选**构造不出来** ⇒ 目录里查不到这个模型（登记/映射问题）
  - `All N candidates failed` = 候选**找到了、逐个尝试过、都失败** ⇒ 解析链路正常，
    是上游在拒绝
  同一个模型在两次调用里给出不同错误码，恰恰说明**它可解析、只是上游在抖**。
- 命名错配这个假设是被**代码**否掉的，不是被数据：`provider_models` 用两列表达同一模型
  （`raw_model_name` 带 provider 前缀、`standardized_name` 不带），而候选解析
  `provider/client.go:1151,1225,1693` 是 `OR mo.standardized_name = $1`，
  **两列都匹配**。所以 `minimaxai/minimax-m3` 这种带前缀登记**不影响**对外可达。
- 「绑到健康凭据上」这条路本身就不成立：`credentials.provider_id` 决定归属
  （cred=1→2453、cred=2→2452、cred=3→2454），把 2454 的模型绑到只服务 2453 的
  凭据是跨厂商端点的语义错误。
- 连带观测：`/v1/models` 公布数随熔断状态在 **10 ↔ 92/94** 之间摆动
  （cred=2 熔断 60s 期间只公布 10 个）。**公布 ≠ 当前可服务**。
- 防复发检查：(a) 排查"某模型不可用"要**先分层**：错误码说 `no_candidate` 才去查登记，
  说 `All N candidates failed` 就去查凭据健康/上游；(b) 动手改数据绑定前先确认
  `credentials.provider_id` 的归属语义，别以为凭据可以跨 provider 复用；
  (c) 归因"未登记"前先查 `standardized_name`，只看 `raw_model_name` 会漏掉整类模型；
  (d) 同类样本要取**多个**再下结论——单次抽样会把瞬时熔断误读成静态缺失。
- 证据：熔断恢复前后 `/v1/models` = 92；`minimax-m2.7` → `All 3 candidates failed`
  vs `minimax-m3`/`glm-5.3-flash` → `no_candidate`；cred=2 `circuit_state` 于
  01:23:42 open → 01:24:42 closed（60s 自愈）。
- 状态：已落地（写入 `docs/05-testing/02-test-plans/会话轮次退役/退役验证记录.md` §8.1）

## 2026-10-17 — 计数型判据要断言「结果可见」，不能只等计数增长（Seen: 1/3）

- 任务 / 缺陷：把 `/storage/tables` 改成 stale-while-revalidate 后，要改判据
  `TestDataLifecycleTableSizesTTLExpiryRefetches`（原契约是"TTL 到期同步重查"，
  新契约是"返回旧读数 + 后台刷新"）。我第一版新判据是：等
  `tableSizesCollectCount() >= 2` 就算刷新发生，再取一次断言新值。
- 根因：`collect` 计数在 **查询开始前**就 `Add(1)`。所以轮询到 2 时，
  后台采集**可能还在跑、快照尚未写回**，于是随后的取值仍拿到旧读数——
  用例红在 `刷新后 TotalBytes = 52649984, want > 52649984`。
  **计数增长 ≠ 工作完成。**
- 修法：契约是"刷新最终让新读数可见"，就**按结果轮询**（带 deadline），
  而不是按计数器轮询。这同时把"刷新没发生"和"刷新太慢"两种失败都收进同一条断言。
- 防复发检查：(a) 任何以 `atomic` 计数器/布尔标志作为判据条件的用例，
  必须确认该计数是在**工作完成后**才自增；若是在开始前自增，就不能拿它当"完成"信号；
  (b) 后台/异步化的改动，判据要断言**用户可见的结果**（新值出现），而不是内部信号；
  (c) 异步化后原同步判据必然失效，要连同"非阻塞"这条一起补上（本次补了
  <300ms + 返回旧读数两段）。
- 证据：`data_lifecycle_storage_tables_perf_test.go:391`（M5）与 `:417`（M6）两处变红；
  修好后日志 `过期响应=52723712(202µs) 刷新后=60325888`。
- 状态：已落地

## 2026-10-17 — deploy-local 切流失败的根因是端口，不是新代码：pidfile 可与真实持有者脱节（Seen: 1/3）

- 任务 / 缺陷：用 `scripts/deploy-local.sh deploy` 部署含预热改动的新版本，
  连续 3 次 `readiness gate failed`，最终 `active cutover failed; previous
  release was restarted`。表面看像是"新代码起不来"。
- 真因（**与新代码无关**）：日志里是
  `ERROR gateway listen failed: listen tcp :8782: bind: address already in use`。
  启动流程一路走到 `CHECKPOINT: HTTP server configured`，**没有任何 panic**——
  新二进制本身是好的，只是抢不到端口。
  现场取证：真正持有 8782 的是 PID 87330（01:59 启动的 2687，`PPID=1` 孤儿），
  而 `run/gateway-8782.pid` 里写的是 **21247**（早已死亡的另一个 pid），
  脚本 `stop_instance` 按 pidfile 去停 19769，**压根没碰到 87330**。
- 附带发现：`/tmp/lgw-env.sh:14` 有 `export LLM_GATEWAY_ROOT=...`。
  在**它之后**再 `export LLM_GATEWAY_ROOT="$(pwd)"` 会被静默覆盖；
  而 `LLM_GATEWAY_ROOT` 只是部署根（`~/kaixuan/llm-gateway-go-v2`，非 git 仓库），
  源码根是脚本自身的 `PROJECT_ROOT`（`SCRIPT_DIR/..`）。
- 防复发检查：
  (a) 切流失败先看 `gateway-*.log` 里的 `gateway listen failed` —— 有这行就是
      **端口/孤儿问题**，不是代码问题，别去改代码；
  (b) 部署前先 `lsof -nP -iTCP:8781 -iTCP:8782 -sTCP:LISTEN` 与
      `run/*.pid` 对账，二者不一致就说明有孤儿，先按 TERM→轮询→KILL 规范清掉；
  (c) 每次部署都必须 `export LLM_GATEWAY_ROOT`，且**在 `source /tmp/lgw-env.sh`
      之后**执行，否则等于没设；
  (d) `reap_stray_gateways` 是**模式锚定部署根**的，本仓进程实际跑在 worktree 的
      `bin/2.5.8.NNNN/gateway` 下，锚点不同就可能清不到——不能指望它兜底。
- 证据：`~/kaixuan/llm-gateway-go-v2/logs/gateway-8782.log` 尾部
  `listen tcp :8782: bind: address already in use`；`lsof` 87330 vs pidfile 21247；
  停掉 87330 后重部成功。
- 状态：已落地（本地开发网关；同一模式在 154/245 用 deploy-seamless，不适用本条）

## 2026-10-17 — 部署纪律：切流失败先看端口；更隐蔽的是「部署了错误的代码树」（Seen: 1/3）

- 任务 / 缺陷：用 `scripts/deploy-local.sh deploy` 部署含预热改动的新版本。
  **连续 4 次部署（2688–2691）全部 `VERIFY_PASS=1`，但一次都没部署到我的代码。**
- 真因（**我自己犯的**，AGENTS.md 早就写过「source 后必须立刻 cd 回 worktree」）：
  `/tmp/lgw-env.sh` 第 15 行是 `cd /Users/.../llm-gateway-go || exit 1`。
  我写的是 `cd <worktree> && source /tmp/lgw-env.sh && deploy`，于是 source 把我
  **切回主 checkout**，deploy 脚本随之解析到主 checkout 的副本，
  `PROJECT_ROOT` 变成主 checkout，编出来的是并发会话那棵树（git_sha 9406de65）。
- 静默性来源：`deploy-local.sh` 自带 verify，`VERIFY_PASS=1` 只证明
  「新产物起来了且 /version 与 bundle 元数据自洽」，**不证明产物来自我想要的源码**。
  `version.json` 里的 git_sha 取自**仓库里那个陈旧的 version.json 文件**，
  本次它就写着 9406de65——所以连版本号都在**配合**这个错误，一眼看不出来。
- 抓到的证据（都是"多看一眼"才抓到的）：
  (a) `strings <bundle>/gateway | grep "startup prewarm of overview failed"` = 0，
      而同参数自建的有 1；
  (b) 2688/2690/2691 三个 bundle 的 `gateway` **md5 完全相同**（`f4e954ef…`）——
      输入不变则产物不变，而我的源码明明变了；
  (c) `go env GOMOD` 指向主 checkout 的 `go.mod`（正常应是 worktree 的）。
- 防复发检查：
  (a) **source 之后必须立刻 `cd` 回目标 worktree**，顺序写成
      `source /tmp/lgw-env.sh && cd <worktree> && export LLM_GATEWAY_ROOT=... && deploy`；
  (b) 部署后**验产物内容**，不只看 `VERIFY_PASS`：至少挑一个本次新增的字符串常量
      `strings` 一下，或比对 bundle 二进制 md5 是否随源码变化；
  (c) `VERIFY_PORT`/`VERIFY_RELEASE` 要一起看，只 grep PASS 会漏掉端口错位；
  (d) 切流失败先看 `gateway-*.log` 的 `gateway listen failed`——有这行就是**端口/
      孤儿**问题，不是代码问题（本次另有一次：`run/gateway-8782.pid` 写 21247、
      真实持有者是 PID 87330 的孤儿进程，脚本按 pidfile 去停另一个 pid，
      新进程 `bind: address already in use` 连续 3 次 readiness gate 失败）。
- 证据：自建 md5 `3bbae51c…`（含 DBG 埋点）vs 部署 md5 `f4e954ef…`（无）；
  修正顺序后 2692 的 git_sha = `1233e793`（= 本 worktree HEAD）且
  `strings` 命中预热常量。
- 状态：已落地（本地开发网关；154/245 用 deploy-seamless，不适用本条）

## 2026-10-17 — 会自愈的状态不能写成静态结论；先找那个"已经归好名"的视图（Seen: 1/3）

- 任务 / 缺陷：验证 7 个目标模型在本地网关上是否可用。
  01:4x 那轮我写下的结论是「三条凭据全部额度耗尽，本地已无任何上游容量」。
- 根因（结论本身错，不是数据错）：凭据状态**会自愈**。02:4x 在同一台机器重测，
  cred=1 已完全恢复（视图判定 10/10 `is_routable`），
  `deepseek-v4-flash` 与 `glm-5.2` 端到端 200。把某一时刻的读数写成结论，
  等于把快照当常量。
- 真正的分层（此前被"全是额度问题"这句话盖住了）：三个凭据三种**互不相同**的原因——
  cred=1 已恢复；cred=2 `availability_state=ready` 却因 `node_probe_state` 里
  8 行 `last_direct_ok=false AND next_retry_at > now()` 而贡献 **0 个候选**；
  cred=3 标着 `permanently_exhausted` 却带着每次都往前滚的 `availability_recover_at`。
- 防复发检查：
  (a) 报告"某能力不可用"前，**必须当场重测一次**，且把结论写成
      「时刻 T 的观测 + 会自愈」而不是「不可用」；
  (b) **先找那个已经把失败原因归好名的视图**再手写 SQL。本轮
      `v_routable_credential_models.unavailable_reason` 直接给出
      `node_probe_failed` / `availability_rate_limited`，
      一步就把三层原因分开了——手写候选 SQL 只能看到"0 行"，看不出是哪一条；
  (c) 区分"整体不通"与"某个凭据独占的模型不通"要靠**判别实验**：
      另取 3 个同属该凭据独占的模型实跑，全 503 ⇒ 是凭据级问题；
      本轮正是这一步否掉了"只有这几个模型坏了"的猜测；
  (d) `health_status=healthy` **不代表能路由**（cred=2 就是反例）。
- 附带查出的独立缺陷：`quota_state='permanently_exhausted'` 与
  `availability_recover_at` 每次前推 ~90s 自相矛盾，而候选 SQL 用
  `quota_state NOT IN ('permanently_exhausted',…)` 做**硬排除** ⇒
  **143 个本可每 90s 服务一次的模型被当成永久不可用**。命名与语义不符，
  待 owner 决定改状态还是改语义（见验证记录 §8.1）。
- 证据：`v_routable_credential_models` 按凭据统计
  `cred=1 routable=10/10`、`cred=2 routable=0/89 (node_probe_failed)`、
  `cred=3 routable=0/148 (availability_rate_limited)`；
  三次 25s 间隔采样下 cred=3 的 `availability_recover_at`
  02:49:13 → 02:50:43 → 02:50:43。
- 状态：取证已落地（`docs/05-testing/02-test-plans/会话轮次退役/退役验证记录.md` §8.1），
  状态语义修正待 owner 拍板

## 2026-10-17 — 同一个错误码在两条代码路径里可以差 360 倍；先问"错误类别参与了决策吗"（Seen: 1/3）

- 任务 / 缺陷：排查目标模型 `glm-5.3-flash` 为什么不可路由。
  路由层给的答案是 `node_probe_failed`，指向 `node_probe_state` 的探针退避。
- 第一次归因错了：我写成「节点探针退避不自解，8 个模型持续压制」。
  **错。** 退避阶梯是 `bg/node_probe.go` 硬编码的 `NodeProbeBackoffChain`
  （`5s→30s→60s→5m→1h→2h→6h` 封顶），注释里明写
  "rows keep ticking forever — 'paused' was removed from this mode"。
  它不是卡住，是**每 6 小时仍会重试**。我看到 `next_retry_at` 在未来 4 小时就
  下结论"不自解"，等于把"下一次重试还没到"读成了"不会再来"。
  **采样窗口（50 秒）短于重试周期（6 小时）时，任何"没有恢复"的观察都是无效的。**
- 真正的机制：`bg/node_probe.go:2620` 用
  `ChainBackoffIndex(attempt, NodeProbeBackoffChain)` 取延迟，
  **只用 attempt 索引，`last_err_code` 完全不参与决策**。逐行实况：
  8 行里 4 行是上游 **410「已到生命周期终点」**、1 行 429、1 行 timeout、
  1 行 404、1 行 500。于是：
  - **410（永久）按瞬时处理**，每 6 小时重试、永远重试；
  - **429（瞬时）被升到 6 小时封顶**。
- 而同一仓库的凭据级探针 `classifyProbeFailure`（`bg/credential_probe_v2.go`）
  早就把两者都判对了：410 = **binding 级终态**（兄弟模型照常可路由，
  判据 `TestClassifyProbeFailure_ModelEOLEndOfLifeIsBindingOnly`）；
  429 = **60 秒**恢复窗口。
  **同一个 410 与同一个 429，两条探针路径给出完全不同的结论；
  429 的恢复窗口相差 360 倍（60s vs 21600s）。**
  `bg/node_probe.go` / `bg/probe_policy.go` 里 410/EOL 零引用。
- 防复发检查：
  (a) 看到"某节点不再被尝试"时，先读 `next_retry_at - now()`：
      **采样间隔必须 ≥ 重试周期**，否则"没恢复"这个观察不成立；
  (b) 排查失败分类时，**先确认错误码有没有真的参与决策**——
      找"用了 error/code/status 的那处分支"，而不是只看它被存进了哪个字段；
  (c) 同一仓库里对同一外部信号存在**两条路径**（凭据级 vs 节点级）时，
      把两条的判据并排读一遍；并排一读就能看出分歧，
      单独读任何一条都只会看到"它工作正常"；
  (d) 记录 `last_err_detail` 的**原文**再归因。`http_410` 与 `http_500`
      在码表上都是"失败"，原文里一个写着 end of life、一个写着内部错误，
      处置完全相反。
- 证据：`node_probe_state` cred=2 八行逐行 `last_err_code`/`last_err_detail`
  （4×410 end of life、`glm-5.3-flash`=429、`glm-5.3`=timeout、
  `codestral`=404、`ai-synthetic-video-detector`=500）；
  `bg/probe_backoff.go` 的 `NextDelay(failures int)` 签名只吃失败次数。
- 状态：取证已落地（验证记录 §8.1）；探针策略是否按错误类别分流待 owner 拍板

## 2026-10-17 — 观测工具用错顺序会造出"假缺陷"：先等渲染，再限制体积，再省操作数（Seen: 1/3）

- 任务 / 缺陷：对本地网关做逐页浏览器验证。连续三轮都判成
  「渲染侧无法取证 / 白页 / 观测通道损坏」，并据此把该项标为环境阻塞。
- 真实情况：**页面全部正常**。三个"环境故障"全是我自己的操作序列问题：
  1. `inspect` 不带 `limit` → 结果超 64 KiB 预算（`BROWSER_RESULT_TOO_LARGE`）。
     这是一个**快速失败**，不是超时；带 `limit` 后 `/dashboard` 23ms 成功。
  2. 同一标签**只有头几次操作可用**，之后逐次劣化到 275s 超时。
  3. **`open_tab` 后立刻读，会读到 Vue 尚未挂载的空 `main`（`totalLength: 0`）**——
     我据此把 `/admin/turns` 判成白页；加 `wait(3s)` 后该页完整渲染。
     **观测早于渲染 == 假缺陷。**
- 稳定配方（三步，缺一不可）：
  `open_tab(url)` → `wait(timeout=3000)` → `query(kind=text, selector=body, maxChars=小)`。
  逐页验证必须**每页一个新标签**。
- 防复发检查：
  (a) 工具报 `BROWSER_RESULT_TOO_LARGE` / 超时，**先怀疑自己的请求形状**
      （带 limit 了吗？选窄了吗？），而不是先判"环境坏了"——
      快速失败与超时是两种完全不同的信号，前者说明它**已经采到了东西**；
  (b) 断言"页面空白/渲染失败"之前，**必须先等待并至少复核两次**
      （不同标签 + 不同选择器）。本轮三次误判全部源于"只读了一次就下结论"；
  (c) 长时间连续失败时，换**操作序列**而不是只换页面——
      同一批 481 端点的服务端扫描是绿的、页面也是绿的，只有我这条路径在失败；
  (d) 渲染侧定不下来时，**退回既定口径**（以网关访问日志 + 直测 API 定案），
      并如实标注"渲染未取证"，不要把推断写成结论。
- 证据：`/admin/data-lifecycle` 渲染出完整 Top 20 表与存储读数；
  `/admin/turns` 渲染出四维筛选与时间范围；`/dashboard` `inspect` 23ms / 65 元素；
  `/request-logs` 的 `/api/logs` 直测 200/70ms/11036B，并用其首条
  `request_id` 完成退役闭环（`session_turns_hot`=1、`_request_logs*`=0、
  `request_logs` 已不存在）。
- 状态：已落地（验证记录 §8.0）

## 晋级记录

将已晋级条目移到这里，并链接目标规则、清单或技能。同一模式只按不同工作日计 `Seen`；出现三天后晋级。经证据确认的安全、隐私、数据丢失或生产事故，若已有可执行的防复发检查，可立即晋级。
