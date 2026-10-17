# Magpie 第四轮实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. 每项先规格审查再代码/Go审查，持续执行本轮已授权目标。

**Goal:** 先完成offered名称校验和缺失tool_result保序补全，再统一三入口错误外显/重试提示；合并推送、本地部署、完整验证及研究总结。

**Architecture:** 请求scope不可变offer约束既有散文纠正；共享batch planner和RawMessage适配器修复历史，IR删除点保留真实结果；errorsx事实进入协议呈现。

**Tech Stack:** Go1.27.2、现有streaming/executors/IR、Redis/PG，真实HTTP假上游、出站黄金、browser-use。

设计与验收契约见[设计](../specs/2026-10-16-magpie-round4-design.md)。所有路径相对仓库根。

## Task 1：C，offered工具名与原文保留

**Files:** 新`internal/tooloffer/`；修改`domains/streaming/tool_call_xml.go`及其XML/body/early-empty测试，
`domains/streaming/executors/{executor.go,executor_chat.go}`、`cmd/gateway/main.go`、真实handler wiring测试；必要修改`internal/vendorstrip/minimax.go`及专属测试以保留未授权/不完整工具候选原文。
本路worker独占这些文件；history的executor接线由root在本路完成后进行。

- [ ] 用旧公开函数证明未知名和plain tool标签当前会生成调用，保存RC与失败断言。
- [ ] 新增WithNames及不可变Names.Has/Len、FromRequest、WithNames/FromContext；先测无offer/未知/大小写/非法参数/重复歧义字段/原结构化调用。

```go
names := tooloffer.FromRequest([]byte(`{"tools":[{"type":"function","function":{"name":"Read"}}]}`))
if !names.Has("Read") || names.Has("read") { t.Fatal("exact caller offer required") }
```

- [ ] bool-only纠正无授权no-op；非流新增context hook优先执行；stream reader承接真实取消context与名称集。
- [ ] reader按choice缓冲，已知完整调用转换；未知、finish/DONE/EOF/overflow原文释放，状态有界；更新与新契约相反的旧断言并注明原因。
- [ ] 实际三handler×流/非流证实生产hook/context命中与正确工具名参数；原生Anthropic保持原纠正边界。
- [ ] 定向测试、race、vet/build与去membership/清fragment/跳hook等隔离变异，审查精确SHA；checkpoint合并push。

## Task 2：D，工具历史补全

**Files:** 新`internal/toolhistory/`；必要修改`internal/ir/{sanitize_tool_messages.go,validate_and_fix.go,serialize_anthropic.go}`及测试；
root负责`domains/streaming/executors/{executor_chat.go,executor_anthropic.go}`与真实出站矩阵新测试。

- [ ] RED：partial A/B仅B、末轮无答案、三call乱序且缺B、已有空答案、跨轮同ID、连续assistant、mixed notice/image/text。
- [ ] 共享planner输出按call顺序的result索引，缺失为synthetic；同批歧义typed拒绝，无全请求answered map。

```go
fixed, err := toolhistory.Repair(body, "chat")
if err != nil { t.Fatal(err) }
again, err := toolhistory.Repair(fixed, "chat")
if err != nil || !bytes.Equal(fixed, again) { t.Fatal("repair must be idempotent") }
```

- [ ] 完整合法历史原buffer no-op；变更时仅目标数组，已有RawMessage数值/未知字段/转义保留；synthetic失败文案明确且Messages带is_error。
- [ ] IR使用同一配对契约，先稳定缺失ID再reconcile；不能删除真实call/result，不能用后轮答案满足前轮。
- [ ] root在实际HTTP attempt统一接线，覆盖首发Chat/nativeResponses、reactiveResponses重试、Messages最后出站，typed歧义按请求错误外显且不上游/不罚节点。
- [ ] 三客户端×三上游×流/非流严格fake验证缺失数、顺序、已有真值及混合文本；既有34黄金不更新通过。
- [ ] 单包/完整IR/streaming/executors、race、vet/build；去synthetic/全局匹配/现有结果后补/丢is_error/全量marshal变异正确红；审查与checkpoint。

## Task 3：F，错误外显与重试提示

**Files:** 新`domains/streaming/client_error.go`及判据；修改`handler.go`、`messages.go`、`responses.go`、`survival_wiring.go`错误出口，
以及`domains/streaming/executors/executor_dispatch.go`最小kind保真及对应测试；dispatch journey/attempt仅追加观测字段，不把raw hint填入planner已有RetryAfter。前两项完成后本路worker独占这些路径。

- [ ] 从真实typed upstream和dispatch构造context/auth/client/content-filter/rate/quota/overload/timeout/down/model-not-found，记录各入口现有错误和重试提示矛盾。
- [ ] 复用errorsx定义语义决策表及设计中客户端投影；保留原kind与同族真实hint；rate沿用既有客户端429可重试特例，其余普通族依EffectiveRetryable；按需为现有dispatch attempt追加仅观测hint字段，不改变处罚/候选政策，不能再被exhausted通用503/model_not_found覆盖。
- [ ] 非流和头未提交时验证HTTP status/code/Retry-After/X-Should-Retry；已提交SSE验证正确协议失败事件、retry字段、单一失败终态，无成功伪装。
- [ ] survival出口沿用相同语义，等待预算/attempts不退化；归责仍走原矩阵，请求错误不罚节点。
- [ ] 定向真实handler/dispatch测试、race及三入口独立负控，审查精确SHA，checkpoint合并push。

## Task 4：完整门、实际发布与总结

- [ ] 更新研究v5和本轮审计，记录Magpie所长、没有照搬的局限、实际完成边界和下一轮方向；仅追加可复用LESSONS。
- [ ] `GOTOOLCHAIN=go1.27.2 SDKROOT=/Library/Developer/CommandLineTools/SDKs/MacOSX26.5.sdk GOMAXPROCS=1 GOFLAGS=-p=1 VERIFY_CGO_ENABLED=1 bash verify.sh --web`自然RC0；installer在自身目录完整测试/vet/漏洞检查；真实存储门/race通过。
- [ ] 规格/code/Go最终SHA复审，完整Task Stop Summary验证后no-ff merge/push；保留并发main改动并区分冻结发布源。
- [ ] 保存本地当前release/配置与一行回滚，以固定已审SHA执行`/opt/homebrew/bin/bash scripts/deploy-local.sh deploy --root $HOME/kaixuan/llm-gateway-go`，两前端完整构建。
- [ ] 实测active-port/bin-current/version/compiler/readyz/healthz；真实HTTP覆盖offer/补齐/错误与重试矩阵；按返回request_id查canonical pair和失败终态。
- [ ] browser-use真实读取data-horizon主/旧alias、检查请求结果，清理本轮测试key/用户/mock/provider状态与浏览器。
- [ ] 实际结果写回研究/审计/版本元数据，审查提交并推送；保存私有证据，finish归档本轮worktree和删除已合并分支，完成目标。

## 当前限制与协作

本地起始2661/8782，端口/版本部署时重读。源main有其他会话版本WIP，不覆盖、不纳入本轮提交。
ACC token缺失，仓内证据替代无法执行的session外部记录；无`.acc-session-policy`，rule50 opt-in不适用。
