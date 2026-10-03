# Handoff — 48h 修订审计 · 四十轮（R89-DF / 196 号）

日期：2026-10-03（承接同日 `2026-10-03-r79-48h-audit-handoff.md` / 195 号）
基线：196 号提交后为 `origin/main` HEAD（**先 fetch 再核**，见下）
完整取证见 `docs/全面审计v3/2026-10-03/196-R89DF-*.md`（195 号见同目录 `195-R89DE-*.md`）

## 0. 一句话状态

**闭合了挂三轮的 objective 点名项**：三层 provenance 的 identity/occurrence
映射**没有闭合**——两套索引空间错位（实测 offset −2，且偏移量无公式可依赖），
**哈希也不是替代路**（`msgHash` 32-hex/键序重排 vs `MessageFingerprint` 64-hex/键序原样，
同一条消息永不相等）。
**本轮生产代码零改动**（只补注释），新增 4 条钉测把两条事实钉成可执行判据，
登记 **待裁决 82（P2）**。

## 1. 交接基线（别再踩）

- **origin/main 由并行会话高频推送**：195 号那轮本地曾落后 **818 个提交**，
  收尾时又多 2 个；196 号开工与收尾都是 0/0。⇒ **每轮开工先 `git fetch` + `git rev-list --left-right --count HEAD...origin/main`**，提交前再 fetch 一次。
- 195 号收尾时并入的 `7a2240962`（`admin/session_analytics_handler.go` 合规查询列对齐）
  与本轮改动**零文件重叠**。

## 2. 本轮改动（0 处生产行为 + 1 个钉测 + 3 份文档）

| 文件 | 改动 |
|---|---|
| `domains/hooks/compression/types.go` | **仅注释**：`AlignmentInfo` 补坐标系说明（原中文文档块内追加，未切断 doc block） |
| `domains/hooks/compression/sanitize_info.go` | **仅注释**：`SanitizedMessageRef` 补坐标系说明 + 指向钉测 |
| `domains/hooks/compression/provenance_seam_pin_test.go` | **新增** 4 条：索引空间错位（空 delta 夹具）、assembled 顺序（非空 delta 夹具）、哈希空间不通、ref 索引空间恒等 |
| `docs/全面审计v3/2026-10-03/196-*.md` | 审计正文 |
| `docs/全面审计v3/00-审计覆盖台账.md` / `README.md` | 索引 + playbook §89/§90 |

## 3. 复验命令（可重放）

```bash
go test ./domains/hooks/compression/ -run ProvenanceSeam -count=1   # ok（4 条）
gofmt -l domains/hooks/compression/types.go \
       domains/hooks/compression/sanitize_info.go \
       domains/hooks/compression/provenance_seam_pin_test.go         # 净
go vet ./domains/hooks/compression/                                  # exit 0
go test ./domains/hooks/compression/ -count=1                        # ok
go build ./...                                                       # exit 0

# 坐标系取证
git grep -n "SanitizedIndex\|OriginalIndex" -- "*.go" ":!*_test.go"
git grep -n "SanitizedIndex" -- "docs/"     # 零命中 ⇒ 全仓从未讨论过这个坐标系
```

## 4. 下一轮顺位

1. **待裁决 82 的裁决前提是先问「谁会是第一个消费方」** ——
   若观测面要按消息粒度回溯 sanitize↔compression，必须先统一坐标系；
   若只做条数级观测，**当前状态已够用、不必付这笔账**。
   **先有需求，再有修法。** 两条候选修法（统一指纹 / 按出向空间产出 refs）
   都是格式或语义变更，不要由审计轮单方面决定。
2. **待裁决 81**（195 号）：`cachedBodyPassesGuard` 失配率，真库取数。
3. **待裁决 79**（195 号）：messages / responses / gemini 三面补不补预算闸（产品裁决）。
4. **待裁决 80**（195 号）：抓一次上游 Responses SSE 的 `output_item.*` 帧序列。
5. **提交态测试**（195 号）：`3483152cb` 需在独立 worktree checkout 后复跑。
6. **P3 顺带**：`domains/hooks/compression/` 有 3 个 **HEAD 既有** gofmt 脏文件
   （`alignment_reverse_test.go` / `responses_alignment.go` / `threetier_hook_test.go`）——
   本轮刻意未批量改（并发会话活跃时只制造合并摩擦），可择机一并清掉。

## 5. 本轮方法教训（已落 playbook §89/§90）

- **§89 一个字段的坐标系是它契约的一部分。** 三个都叫「消息下标」的字段
  （`RawIndex` / `SanitizedIndex` / `OriginalIndex`）分属**两个不同的数组**。
  写「消息级映射」必须回答三问：索引哪个数组？那个数组谁拼的（缓存优先还是增量优先）？
  **偏移量能否由存下来的数据重建？** 本例第三问答案是「不能」
  ⇒ **「存下来了」不等于「以后能 join」**。
- **§90 夹具要让被测性质真正参与计算。** 变异 M2（合并顺序对调）**第一次没让判据转红**，
  因为夹具的 `deltaTail` 为空，被测的「顺序」在空集合上**不可观测**。
  ⇒ 写「顺序/拼接/优先级」类判据先问「我这个量非零吗」，再用变异反查。
  **判据全绿 + 变异不红 = 判据没在测那个性质。**

## 6. 风险与诚实边界

- **本轮零生产行为改动，但同样未做部署级验证**（未重建镜像 / 未起真进程 / 未跑真库 e2e）。
  结论来自代码 + 可执行判据（钉测 + 双向变异），**未**经部署面验证。
- 本机 **无 Redis / 无 PG / 无 Docker**。
- `go test ./...` 全量**未跑**（Windows + 缺外部服务，套件必红，与本轮零关联）。
- **origin/main 由并行会话高频推送**（本会话两次 fetch 分别落后 818 / 2 / 0 个提交）。
  提交前必须重新 `git fetch` 核对；push 被拒先看远端新增提交与自己的改动是否重叠，
  **用 `git merge origin/main`，绝不用 `git checkout <branch> -- <paths>`**。
