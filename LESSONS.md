# 项目经验队列（LESSONS）

> 这里只记录可复用的返工、审查、验证或生产问题，并附可核验的证据。开工时先读相关条目；收尾时只新增可行动的发现，常规成功不必记一条。证据和晋级标准见 `.agents/rules/66-skill-evolution.md`。

## 待观察经验

## 2026-10-15 — 安全工具链升级需同步 vendor 与实际构建入口（Seen: 1/3）
- 任务 / 缺陷：[Magpie第二轮审计](docs/audit/2026-10-15-magpie第二轮审计与完善.md)；Go1.27.1与x/net可达漏洞门。
- 基线 / 修复 SHA：集成基线 `d63d9dcf959aa06143923e33cf637c4da047df1a`，本轮安全收口待提交。
- 场景与影响：Go测试/构建通过后，govulncheck仍检出13项可达标准库漏洞；只改mod会被旧tracked vendor拒绝，旧CI补丁版与GOTOOLCHAIN=local也不会自动切新SDK。
- 根因：源码依赖、vendor、编译器与CI/镜像入口是独立约束，任一旧层都能阻断或偏离安全升级。
- 防复发检查：同步MVS最小依赖与vendor，生成两遍比哈希；更新活动CI并显式选择修复SDK；扫描默认vendor模式及独立模块，部署后读实际产物go version -m。
- 证据：两模块Go1.27.2扫描均0可达；vendor两次5853文件哈希一致；x/net0.60与配套版本由官方模块依赖边确认，未运行tidy。
- 状态：已修复待观察

## 2026-10-15 — 验证矩阵继承父环境，主门每次都在错误分支早退（Seen: 1/3）
- 任务 / 缺陷：[Magpie第二轮审计](docs/audit/2026-10-15-magpie第二轮审计与完善.md)；verify cgo契约。
- 基线 / 修复 SHA：基线 `c708260d0514befacacfdd18f83429f80bfc98f6`，本轮实现待提交。
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

## 晋级记录

将已晋级条目移到这里，并链接目标规则、清单或技能。同一模式只按不同工作日计 `Seen`；出现三天后晋级。经证据确认的安全、隐私、数据丢失或生产事故，若已有可执行的防复发检查，可立即晋级。
