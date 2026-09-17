# D17 代码卫生与冗余治理 子代理报告（窗口：876302d5e..67f78247c，定点核实任务）

## 一、发现（候选，待主代理复核）

### R35-R7 两个测试

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P3 | **TestDefaultDispatchFollowUpAppliesAuthHeader 命名误导 + 与既有测试重复**：在 dispatchFollowUpRequest seam 塞 stub，生产函数 defaultDispatchFollowUp 永不执行；与 TestInjectFollowUpCarriesAuthorizationHeader（:92-103）覆盖等价；注释声称 "production dispatcher's contract" 不实 | response_interceptor_helpers_test.go:179-199（stub :186）；seam 选择 helpers.go:149-152 | 改名 TestInjectFollowUpSeamPropagatesAuthHeader + 更正注释，或删除 |
| 2 | P3 | **TestDefaultDispatchFollowUpHitsLiveServer 恒真**：手工 http.NewRequest + 亲手 Set 头 + srv.Client().Do，断言 server 看到自己刚设的值——零生产代码；连带 defaultDispatchFollowUp 全仓零测试覆盖 | test:204-236（手工造请求 :222-224）；零覆盖证据 :202,:215,:220,:313 | 改写为 buildFollowUpRequest + srv.Client().Do，或删除并补真测试 |

### R35-R8 头硬编码与死重试机制

| # | 级别候选 | 发现 | 证据 | 建议处置 |
|---|---|---|---|---|
| 3 | P3 | X-Gw-Follow-Up-Depth 硬编码 "1" 与 context 真实深度（1..15）脱节；唯一读者是发现 #2 的恒真测试 | 写点 helpers.go:237；真实深度链 handler.go:5189/:5215 → helpers.go:206 | 写真实值或随 #5 一并删除 |
| 4 | P3 | X-Gw-Follow-Up-Attempt 恒 "1" 且全仓零读者 | 写点 helpers.go:238；恒 1 实参 helpers.go:154 | 随 #5 一并清理 |
| 5 | P3 | **followUpAuthCandidates 死重试机制：三助手自 initial release（052246236）零调用**。Attempt 语义无风险（seam 的 attempt 形参与 survival Attempt 无交集）。清理面：生产 3 处 + 测试 8 处 stub 签名 + 3 处直接调用 | helpers.go:292-308/:310-325/:327-344 | 整批删除（纯死代码） |
| 6 | P3 | 相邻死接缝 cleanupSessionFollowUps 已带诚实 nolint 标注 | helpers.go:100-103 | 保留即可 |

### R35-R2 附带

| 7 | P3 | task_type_hint:"code_audit" 至今全仓无消费方（行号已漂移至 :793）；分类决策落回通用分类器 | mode_hook.go:793；文案 settings/goal_specs.go:240 | 删一行（接线则需 autoroute 入口读 body metadata） |
|---|---|---|---|---|

### R35-R9 gofmt

| 8 | P3 | **gofmt -l 存量 272 文件（排除 vendor/node_modules），全部为窗口外欠账**；窗口触碰文件交集仅 1 个：survival_no_nodes_e2e_test.go（EOF 缺换行）。完整清单见子代理原始输出（gofmt -l . 可再生） | gofmt -l .；comm -12 对 git diff --name-only | 本轮只修窗口内 1 个；272 存量单独立项分批 gofmt（机械提交勿与功能修复混合） |
|---|---|---|---|---|

### 窗口 4 提交新增文件卫生面

| 9 | P3 | survival_no_nodes_e2e_test.go：noNodesExecutor.attempts 死字段（:74 声明、:80 只写、断言全走 exec.calls）+ :71 注释误导 + EOF 缺换行 | :71-74、:80、:317 | 删字段、更正注释、补 EOF |
| 10 | — | sticky_typednil_test.go 无发现 | :15-34 | 保留 |
| 11 | P3（微） | liveactions_test.go：TestSeqMonotonicPerRequest 创建 emitter 后 `_ = e` 丢弃（:141/:153） | — | 顺手清理，不阻塞 |
| 12 | P3 | deploy-252-gateway.sh：CGO=0→容器回退两级构建为第三份重复实现（与 deploy-seamless.sh:757-799、deploy-local.sh build_backend）；互留指向注释已满足底线 | :52-55、:58-78 | 登记：提取 deploy-lib/ 共享构建函数，本轮不动 |
| 13 | P3 | deploy-local-lib.sh：(a) CORS_ORIGINS 同块写两次默认值不一致（:371 无 localhost、:415 含）；(b) dl_prepare_layout no-op 死语句 :71-73；(c) 自引用行号漂移 :543-544/:503；(d) dl_layout(:60-65)/dl_version_fields(:266-275) 零调用死接缝 | 见左 | (a) 删 :371；(b) 删 no-op；(c) 行号引用改函数名；(d) 标注或删除 |

## 二、核实为健康的面

- follow-up 真实深度闸门链健康（handler.go:5189/:5215 → helpers.go:120-129 → :206 每轮 +1）；硬编码 "1" 仅污染观测头，不构成失控面。
- seam 头契约有活测试钉住（TestFollowUpSourceActorMapping :335-360、TestBuildFollowUpRequestCorrelationHeaders :366-398 直接调 buildFollowUpRequest 本体）。
- sticky_typednil / liveactions 两处 typed-nil 回归均真实钉住。
- survival_no_nodes_e2e_test.go 三个测试本体真实（httptest + SSE 逐 chunk 断言），仅 #9 两处卫生问题。
- deploy-local-lib.sh 对 log/warn 的依赖成立（deploy-local.sh:159-160 提供定义）。
- 窗口触碰 5 文件中仅 1 个进 gofmt 存量；两处 typed-nil 修复均有配套回归。

## 三、未覆盖项与原因

- #5 删除死代码后的编译/测试验证（只读约束，动手方须过三门）。
- gofmt 272 文件逐一定性超出定点核实范围。
- deploy 脚本实机执行路径需真机。
