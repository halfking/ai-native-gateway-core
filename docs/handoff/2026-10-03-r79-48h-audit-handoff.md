# Handoff — 48h 修订审计 · 三十九轮（R89-DE / 195 号）

日期：2026-10-03
基线：`origin/main` = `1cc3c1b3d`（本轮根修提交，已推送，0/0）
承接：`docs/handoff/2026-09-30-r78-48h-audit-handoff.md`（三十八轮）与
`docs/handoff/2026-09-30-verify-gate-closure-handoff.md`（verify 收口轮）
完整取证见 `docs/全面审计v3/2026-10-03/195-R89DE-*.md`。

## 0. 一句话状态

合并了并发会话的 **818 个提交**（`go build ./...` exit 0，零功能回归），
审了 194 号之后 335 个提交里的 4 个热点提交 + 1 条自查线，
**抓到 1 个真缺陷并根修**（输出合规 lane 漏了两类载体），
**证伪 13 条**（含我自己差点发出的 3 条），登记 3 条待裁决。

## 1. 交接基线（别再踩）

- `git fetch origin main` 后本地曾落后 **818 个提交**，且**收尾时又多了 2 个**。
  ⇒ **本轮两次都是先 fetch / 合并、再提交推送**，没有盲推。
- 并发会话在 `admin/session_analytics_handler.go` 独立实现了合规查询列对齐
  （`7a2240962`），与本轮改动**零文件重叠**，快进合并无冲突。

## 2. 本轮改动（1 处生产代码 + 1 个钉测 + 3 份文档）

| 文件 | 改动 |
|---|---|
| `domains/hooks/outputcompliance/protocol_text.go` | 非流 `addOutputItem` 与流式 `output_item.added` 两处 switch 各加 `case "tool_search_call", "mcp_approval_request"` → `addToolInput(item,"arguments", lane+".arguments")` |
| `domains/hooks/outputcompliance/edge_tool_carrier_lane_test.go` | **新增**钉测（整体 body + added 帧 + done 帧） |
| `domains/hooks/outputcompliance/stream_compliance.go` | import 顺序 gofmt（**HEAD 既有脏，非本轮引入**） |
| `docs/全面审计v3/2026-10-03/195-*.md` | 审计正文 |
| `docs/全面审计v3/00-审计覆盖台账.md` / `README.md` | 索引 + playbook §87/§88 |

## 3. 复验命令（可重放）

```bash
git fetch origin main && git rev-list --left-right --count HEAD...origin/main
go build ./...                                                        # exit 0
gofmt -l domains/hooks/outputcompliance/                              # 净
go vet ./domains/hooks/outputcompliance/                              # exit 0
go test ./domains/hooks/outputcompliance/ -run EdgeToolCarrier -count=1   # ok
go test ./domains/hooks/outputcompliance/ ./security/sanitize/ -count=1   # ok / ok

# 缺口复现（修复前 0 命中）
git grep -n "tool_search_call\|mcp_approval_request" -- \
  "domains/hooks/outputcompliance/*.go" ":!*_test.go"
```

## 4. 下一轮顺位（**第一条已挂三轮**）

1. **`SanitizedMessageRefs` ↔ `AlignmentMap` 在压缩后的一一对应** ——
   objective 点名的「三层 provenance 完整 identity/occurrence 映射」正落在这里。
   子代理 A 明确未查完（只验了占位符编号唯一性，没验压缩后的 index 重映射）；
   主代理也只看了 `threetier/align.go` 的快照基线那一半。
   入口：`session_compressor.go` 里生成 `AlignmentMap` / `MsgHashes` 的函数
   + `domains/hooks/compression/threetier/align.go` 全文。
2. **待裁决 81 取数**：`cachedBodyPassesGuard` 失配率（真库）。有量级再谈修法。
3. **待裁决 79 产品裁决**：messages / responses / gemini 三个 `ServeHTTP` 面要不要补预算闸。
4. **待裁决 80**：抓一次上游 Responses SSE 的 `output_item.*` 帧序列。
5. **提交态测试**：`3483152cb` 的测试是在 HEAD 而非提交态跑的（只读约束无法 checkout），
   要严格结论需在独立 worktree 里 checkout 复跑。

## 5. 本轮方法教训（已落 playbook §87/§88）

- **§87 枚举收口判别准则**：**不对称才是缺口，对称缺席是设计。**
  `e0625c6a5` 写「双侧收口」，实际是**四份**枚举（入向 sanitize / 出向 restore /
  输出合规非流 / 输出合规流）—— 提交方与评审方都**合理地**漏掉了第三份。
  ⇒ **措辞决定了评审覆盖面**；收口类提交必须在正文贴对账表。
  ⇒ 收口前先问一句「这个载体在协议上会不会出现在这一侧」：
  `function_call_output` / `custom_tool_call_output` 在输出 lane 缺席是**对的**
  （Responses 协议里只出现在请求侧），补上去只白增检查面。
- **§88 负控必须先在未修复代码上跑。** 判据取**可观测后果**
  （敏感值被改写 / `ModifiedBody` 非空 / `issues>0`），**不取「结构存在」** ——
  后者加一个空 case 就能过。先改代码再补测试，就无法区分
  「新判据抓到了真缺陷」与「新判据迎合了新代码」。

## 6. 风险与诚实边界

- **本轮根修未做部署级验证**（未重建镜像、未起真进程、未跑真库 e2e）。
  改动是纯枚举扩容、风险面窄，但这是**未验证**而不是**已验证**。
- 本机 **无 Redis / 无 PG / 无 Docker** ⇒ `domains/dispatch` 的 Redis 集成钉测、
  待裁决 81 的量级、子代理 C 的两条推演**均未实跑**。
- `go test ./...` 全量**未跑**（Windows + 缺外部服务，套件必红，与本轮零关联）。
- **origin/main 由并行会话高频推送**（本轮两次 fetch 分别落后 818 / 2 个提交）。
  提交前必须重新 `git fetch` 核对，push 被拒先看远端新增提交与自己的改动是否重叠。
