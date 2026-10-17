# Magpie 第四轮：工具契约、历史补齐与错误外显

状态：实施中；本文件不构成本轮完成或部署证明。

## 范围与参考

- 本轮基线：`39c2d6428ad0a35be293e5f7c532826140cf3d97`。
- 参考项目：`~/workspace/ai/magpie`，`591ed58b536b17162a0715aa7210d9b7d0db4c09`，只读。
- 已审批设计/计划：`755c4aecf277e1045fbdf79bdcb3e87deda2dffe`；计划已通过独立审查，并经干净集成 worktree 合入主干 `0ba2e354de02e148f619b29d59bc09d63f00600b`。
- 目标顺序：P1-C offered 名称约束；P1-D 缺失结果按调用顺序补齐；P1-F 三入口错误和恢复提示。DSML、GLM、半 Hermes 的更多语法不在本轮实现范围。
- 本轮仅部署本地；154/245 未列入授权部署范围。源 checkout 的他人版本/菜单改动保持原状。

## 从 Magpie 借鉴及修正

Magpie 的 `textcalls.go` 用实际工具集合约束散文恢复，值得借鉴。网关将实际请求中的名称变为不可变、区分大小写的集合；旧的布尔 `toolsRequested` 无法授权名称，因此旧纠正入口关闭执行。模型输出、历史调用 ID 或内置工具类型不能扩大客户端授权。XML 和完整 Hermes JSON 仅在名称、对象参数和语法都能无歧义确认时恢复；失败候选需要穿过后置供应商清理后仍保持原文。

Magpie 的 `pairToolMessages` / `pairToolBlocks` 能补 interrupted 结果，但已有结果在前、缺失结果在后，会将 `[A,B], resultB` 补为 B,A。其 Responses 全请求 answered map 还可能用后轮相同 ID 的结果满足前轮。本轮采用局部批次配对，补齐时按实际调用顺序放回真实与合成结果，Messages 合成结果带 `is_error:true`。已有完整有效交换保留原字节；只改必要的 `messages/input` 数组，真实参数、空结果、大整数及未知字段不做全量重序列化。

工具历史还必须在 IR 删除孤儿结果之前修复，否则最终出站层无法挽回已删除的答案。最终 HTTP attempt 边界统一执行修补，覆盖 native Responses、模式切换、上下文恢复和重试。Chat→Responses 的主动、被动升级复用同一个按实际客户端协议解析的转换函数，避免将 Messages 原体当 Chat 解析而丢失全部工具历史。

Magpie 的错误词典与客户端协议呈现值得借鉴；本仓继续以既有 `errorsx` 分类、`PenaltyOf` 和恢复决策为权威，错误呈现不得重新决定节点处罚或候选策略。

## 已取得的行为证据

- P1-D 真实三客户端 × 三上游 × 流/非流共 18 路严格 HTTP 矩阵通过；另 6 路实际 Chat→Responses 重试通过。两轮均与原 34 份出站字节 golden 同跑，未更新 golden。
- 首轮出站矩阵在接线前真实失败 12/18；初次测试遇到并发改名造成的 `_testmain` 编译失败未计作语义红。之后使用冻结 Go overlay，避免读取并发写入中的测试名称。
- 6 路重试新增判据发现 Messages 两路重试丢失全部调用/结果；复用真实客户端协议转换后通过，记录 `history-reactive.log` → `history-reactive-fixed.log`。
- 工具历史单包/IR 的 11 个隔离变异均在预期语义断言变红；root 接线的 5 个变异也均变红，覆盖删除 attempt 修补、删除 Anthropic 修补、错用 Chat parser、忽略供应商 ID 字段、丢失 ClientBug 分类。
- root 变异初版使用过时测试名，前两项实际未运行测试，结果无效；已修正为精确 `TestRound4HistoryActualUpstreamMatrix` 并重跑。证据以 `root-wiring-mutations-v2/` 为准。
- 独立规格审查发现 finish-only 帧无 delta、空 finish_reason 及显式 `null`/`"null"` 参数边界；均要求独立反例后修正，不能把 null 升级成可执行的 `{}`。

## C/D 冻结审查与补充边界

C/D 最终 38 个 Go 文件的逐文件 SHA256 清单为 `cd-final-manifest.json`，清单摘要 `b55e7ebe61fd8ebfbee473ce1216e0d1437e4c0bd4bdac6300f510a776656be3`。独立规格、通用代码和 Go 审查均通过；提交后仍需绑定最终 SHA，不把文件级审查冒称已发布。

审查进一步发现 reader 的物理行缓冲可绕过 choice/content 上限，以及恢复成功可能凭空创建 terminal。修正后复用现有 SSE 物理行上限，LF 计入长度；单次 Read 内多条合法短行不会被误拒绝；typed limit error 不被 EOF 覆盖，先前待释放文本先交付。仅真实 terminal 才改写 finish_reason。六类缺失/null/空 finish_reason 与 EOF 组合及行边界通过；五个隔离变异均在预期语义断言变红。

Anthropic 镜像去重同时证明 ID、名称、参数及一对一出现次数，并保留原 Content 位置与 cache_control。RawMessage 编辑的每个实际目标字段都拒绝重复键，不能只校验顶层。Go 独立最终定向测试、race 与根模块 vet 通过。集成主干另在干净 worktree 验证，避免覆盖同期脱敏规则功能；这些改动不会授权新的工具名。

F 独立真实 handler 红测 95 例已建立：66 个实际上游、29 个明确零上游；均编译并到达语义断言，wire/call/audit/reducer 前提缺失 0。此为红测证据，尚不是修复通过证明。

## 验收量具的审查

本地验收 harness 在运行真实服务前独立审查。静态计划为 66 HTTP 例、65 个需要 canonical pair 的请求，其中成功 36、失败 29；伪 key 的早期 401 不要求落库。审查通过纯内存负控发现：历史 oracle 忽略额外未知调用；Chat 错误流没有 DONE 仍绿；落库脚本接受失败且不完整的 HTTP 报告；cleanup 登录失败会跳过本地清理。四项均已修复并经过独立负控复审。后续复核还纠正了 overload 夹具的真实上游状态为 502（503/529 在现有分类器属于 concurrent），保持客户端 503/code/retry 提示断言不变。不能把准备阶段的 selfcheck 称为真实服务验证。

## 验证、发布及边界

最终代码审查 SHA、全量项目门、本地 release、66 HTTP、65 canonical pair、browser-use 和清理证据将在实际执行后补登。当前不声称这些步骤已完成。

ACC 的 session 外部记录因 token 不可用，不能执行 append/pin/archive；仓内文档和私有证据不冒称已归档 Memora。额外运行的 ACC Toolkit 全局 `scripts/verify.sh` 为诊断，返回 11 项错误，涉及其自身 dirty worktree、多个项目 env 注册、缺失技能与过期部署注册；这不是本仓根 `verify.sh --web` 的结果，也未计为通过。项目本轮的发布门仍须实际运行并自然返回 0。
