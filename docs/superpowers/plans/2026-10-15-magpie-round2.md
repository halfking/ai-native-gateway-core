# Magpie 第二轮审计与网关完善 Implementation Plan

> **For agentic workers:** 使用 subagent-driven-development；按文件所有权并行实现，集成后依次做规格审查与代码质量审查。

**Goal:** 以两个仓库当前源码纠正对比文档，补齐已证实的续写安全、工具终态和非 API 响应缺口，提交合并推送并完成本地部署验证。

**Architecture:** 扩展现有执行器、IR/normalizer 与共享 upstream.Client，不新增一套重试或学习机制。保留 Redis/PG 多实例一致性；未来项按证据与依赖排序，不把规划当落地。

**Tech Stack:** Go、httptest、现有 shell 门禁、Vue 管理端、host-first PostgreSQL/Redis 部署。

## 基线与范围

- 网关基线：`c708260d0514befacacfdd18f83429f80bfc98f6`；工作分支 `codex/magpie-round2-20261015`。
- 并发主干集成：已快进11提交至 `14d9fed9c`，恢复本轮修改后重新跑全门；再快进d63d9dcf9保留新筛选页面。
- Magpie：`591ed58b536b17162a0715aa7210d9b7d0db4c09`，只读参考。
- 原始 checkout 干净；独立 worktree 承担本轮编辑；仅本地部署在授权范围。
- P0-A/B/C/D 已有后续收口，必须逐项查接线与判据，不重复实现。
- 本轮优先落实 P1-A/E/H 及真实验证门缺陷；其余 P1/P2 在更新后的研究文档中明确当前状态、前置条件及验收。

## Task 1：三路只读审计与现状矩阵

- [x] 协议代理核对 A6/A7/A13–A16，区分散文纠正、审计重建与实际出站响应。
- [x] 可用性代理核对 P0、续写、2xx 异常、空回复和错误外显。
- [x] 研究代理核对工程方法、黄金门、死代码与部署入口。
- [x] 主代理修订研究文档与执行规划：记录 SHA、真实接线、已完成项、实缺与下一轮依赖。

## Task 2：续写安全（可用性代理所有权）

Files: `settings/spec_stream_continuation.go`、`domains/streaming/executors/executor_chat.go` 的续写部分、`domains/streaming/stream.go` 续写条件及对应测试。

- [x] 先写默认关闭、通用兼容端不续、tool call 禁续判据，确认旧行为失败。
- [x] 默认关闭；开启时只允许可证实支持 prefill 的官方端点/模型，DeepSeek beta 使用 `prefix:true`，Moonshot 使用 `partial:true`。
- [x] 保留一次预算、请求取消和原始请求重放约束；mock 通过 transport 映射官方 URL。
- [x] 跑同层与真实读循环测试；移除安全条件变异必须变红。

## Task 3：工具调用终态（协议代理所有权）

Files: `internal/ir/response.go`、`internal/ir/stream.go`、Chat normalizer、相关协议桥与测试。

- [x] 先复现有具名 tool call 但空/stop 终态被客户端当普通文字结束。
- [x] 仅空/stop 改为 tool_calls/tool_use；保留 length/content_filter，按 choice 隔离，未具名调用不升级。
- [x] 流式和非流式、Chat/Anthropic 桥均用真实序列化结果断言；移除修正变异必须变红。

## Task 4：非 API 的 2xx 响应（主代理所有权）

Files: `upstream/non_api.go`、`upstream/client.go`、`upstream/non_api_test.go`。

- [x] `go test ./upstream -run 'TestDoRejectsNonAPIReplies|TestDoPreservesAPIReplyBytes' -count=1`：旧代码四类错误响应仍 200，确认红。
- [x] 共享客户端在返回成功前识别 HTML、空体、JSON 非法前缀，变成内部可恢复的 upstream 502 并立即交候选恢复；不在同一个错误 relay 上反复等待。
- [x] 成功字节原样回放；SSE、音频、XML、压缩体、204、HEAD 与既有 4xx 保持契约。
- [x] 真实执行器验证坏候选切下一候选；对修正接线做去掉即红验证。

## Task 5：验证门可用性（研究代理所有权）

Files: `tests/verify_cgo_convention_test.sh`。

- [x] 复现父进程 CGO_ENABLED/VERIFY_CGO_ENABLED 污染，默认与强制 cgo 两种入口均失败。
- [x] 隔离测试子 shell 环境，再应用单用例变量；保留默认 cgo 政策、全量目标与 CI 契约。
- [x] 用父环境污染矩阵确认通过；变异恢复污染须失败。

## Task 6：审查、发布与部署验证

- [x] 规格代理审查所有行为与文档证据；再由 code-reviewer / go-reviewer 审查精确最终代码 SHA。
- [x] root `verify.sh` 全门、完整 Go tests/vet/build、installer 独立模块、前端 typecheck/tests/build。macOS cgo 使用已实测可用的 SDK 26.5、GOTOOLCHAIN=go1.27.2；不靠过滤目标掩盖失败。
- [x] 所有失败逐项定位并记录，未通过的发布门阻止合并推送。
- [x] 保存 Task Stop Summary，核对 staged/unstaged/deleted/untracked，验证 summary。
- [x] 小步 commit，`merge --no-ff` 到 main，push；冲突后重验。
- [x] 部署 `~/kaixuan/llm-gateway-go`，核对 active-port/release/version 指纹与 readyz/healthz；以实际运行服务执行鉴权、协议、异常恢复验证和浏览器操作。
- [x] 回滚：`AIAN_DEPLOY_LIB="$HOME/workspace/ai-native-tools/deploy-lib" GOTOOLCHAIN=go1.27.2 /opt/homebrew/bin/bash scripts/deploy-local.sh rollback --root "$HOME/kaixuan/llm-gateway-go"`；rollback自动选择已验证且非当前的可用release；代码回滚用 merge commit 的 `git revert -m 1`。此项仅核回滚命令和契约，未进行切回演练。
- [x] 文档记录验证命令、结果、限制、部署版本及后续方向，最终源码fb0b481e7已push，本地2659健康与browser通过；文档/版本元数据收口后再核远端与本地状态。

## 会话记录限制

已尝试 SSOT 的 session.create 客户端；`~/.acc/state/token.json` 不存在，session.append/pin/archive 的 ACC 鉴权不可用。本轮用入仓审计文档保存证据，不能声称已归档到 Memora。项目无 `.acc-session-policy`，rule 50 opt-in 门不适用。
