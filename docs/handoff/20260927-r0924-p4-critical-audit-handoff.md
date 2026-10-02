# r0924 supplier-protocol-optimization P4 批判式审计 Handoff

**项目**: 供应商协议多路化（multi-protocol endpoint）+ ollama-native 真实接入
**轮次**: 2026-09-27（r0926 审计轮；承接 [20260924-r0924-supplier-protocol-optimization-audit.md](20260924-r0924-supplier-protocol-optimization-audit.md)）
**状态**: ✅ 本轮审计发现已修（commit `607ba7db7`）；P4 **不等于完成**，残留三项见 §四
**关联 commit**: `607ba7db7`（本轮）/ `422bb5fc4`（P4 合入 main）/ `3244c5af1`（P4 功能）/ `8b94131d9`（P0+P2+P3）

---

## 一、最重要的结论：接力 prompt 的前提已过期

上一份 handoff §三 的「下一轮 prompt」被本轮原样执行。该 prompt 写于 **P4 合入之前**，
执行于 **P4 合入之后**，因此它开篇的断言已经不成立：

> prompt 原文：「代码现状见 commit `8b94131d9`（已在 main），selector 库就绪但未接入运行时」

**事实**：`8b94131d9` 当时确实是最新，但 P4.2+P4.3 已由 `3244c5af1` 实现并经 `422bb5fc4`
合入 main。即 prompt 描述的「待接线」状态早已是历史快照。

**方法论（务必继承）**：
1. 接力 prompt 里的 commit 号、状态断言、行号引用**一律先核当前 HEAD**，再决定动作。
   本轮若照字面执行「新建 executor_ollama.go / 加 NativeEndpoints 字段」，会与已合入代码冲突。
2. 「代码存在」≠「功能完成」。本轮逐条验证硬性要求后，发现已合入的 P4 有 **1 条未受开关保护的行为回归**。

---

## 二、本轮实际做了什么（不是 P4 落码，是 P4 审计）

| ID | 严重度 | 问题 | 处置 |
|----|--------|------|------|
| **B1** | **严重** | `FF_OLLAMA_NATIVE=false` 时 ollama-native 主端点被硬判 `KindUnsupportedFeature`；P4 前是 `default → executeOpenAI`（Ollama 有 OpenAI 兼容端点）。**两个开关都关时即可达**，且把开关调回 false **无法恢复**旧行为 → §5.3 承诺的唯一回滚手段失效 | ✅ 抽出 `classifyDispatchRoute`，flag off 降级为 `routeOpenAI` |
| **B2** | 中 | selector 决策算完即丢：`MatchRule`/`EndpointID`/`VendorNative`/`Passthrough` 无出口，AC8 四个 trace 属性全缺，灰度不可观测、回滚无判据 | ✅ 新增 `endpoint_selector_decision` 结构化日志（仅 selector 开启时输出） |
| **B3** | 低 | 候选 SQL `ORDER BY pep.weight ASC` 与 selector 的 weight **DESC** 相反；注释还谎称该顺序「避免 post-scan shuffle」 | ✅ 改 DESC + 重写注释说明其仅供展示 |
| **B4** | 中（覆盖缺口） | `jsonb_build_object` 键 ↔ `EndpointLite` json tag 的对应关系**零覆盖**。改一个 tag → 既有测试全绿 → 端点静默解码为零值 → 全落 Stage 4，不报错不打日志 | ✅ 抽出 `decodeNativeEndpoints` + 新增 `candidate_native_endpoints_decode_test.go` |

**B1 的触发路径**（完整证据链）：
`"ollama"` 是 `provider/catalog/protocol_normalize.go` 的合法别名 → 归一为 `"ollama-native"`；
V800 回填 `sql/migrations/startup/800_provider_endpoint_protocols.sql` 原样复制 `p.protocol`；
故 `providers.protocol='ollama-native'` 是**真实可达**状态，非理论假设。

**测试有效性反向验证**（本轮方法论要点）：
- B1：把缺陷实现重新塞回 `classifyDispatchRoute` → 3 个 flag-off 用例变红并指名缺陷；
- B4：把 `BaseURL` 的 tag 改成 `baseurl_typo` → 2 个用例变红并指名 `base_url` 无匹配 tag。
两者复原后转绿。**一个不会失败的测试不能证明任何事。**

---

## 三、仍然残留（本轮明确不修，写在这里而非掩盖）

| 缺口 | 为什么现在修更危险 |
|------|-------------------|
| `wantFamily` 恒为 `""`（`ExecParams` 无 family 字段） | 直接接 `discovery.InferFamily` 会让 **Stage 2 首次可达**（family 命中但协议不同）= 在**没有 passthrough executor** 时打开跨协议改道。风险高于收益，须与 P5 同批设计评审 |
| `Decision.Passthrough` 无消费者 | 归 P5；现已作为日志字段暴露，可观测但不改变行为 |
| 无 Ollama native **入站** handler | **G1 因此不成立**。当前仅支持「OpenAI Chat 入站 → ollama-native 出站」单向 |
| 无真实 Ollama 端到端用例 | 33 个 ollama 用例全是 mock httptest；开灰度前必须补 |
| `doOllamaProbe` 未做 | ollama-native 仍走 chat 探针，健康度可能误判 |
| `execParams.EndpointBaseURL()` 未实现 | 规格点名但全仓无此访问器；实现用 `cand.BaseURL`，功能等价（selector 改写同一份 cand 副本） |
| `NativeEndpoints` 类型是 `[]endpointselect.EndpointLite` 而非 §3.4 的 `[]provider.CandidateEndpoint` | 复用 selector 自己的类型让 JSON tag 成为唯一 wire 契约，是改进；但**按 §3.4 去找 `CandidateEndpoint` 会找不到**，已记入 §12.9 |

---

## 四、测试与环境事实

**本轮命令与真实退出码**：

```bash
go build ./...                                    # 0
go vet ./domains/streaming/executors/ ./provider/ # 0
go test -count=1 -timeout 900s ./...              # 见下
```

⚠️ **`go test ./... | tail -N` 的退出码是 `tail` 的**，会掩盖失败。本轮一度据此误判「全绿」，
改为重定向到文件 + 显式 `echo $?` 后才拿到真实结果。

**两个先于本轮存在、且与本轮无关的失败**（已在干净 HEAD 上 stash 复跑确认）：

| 包 / 用例 | 症状 | 定性 |
|----------|------|------|
| `discovery` `TestDiscoverGateways` | `mDNS query failed: sendto: no route to host` | 本机无 IPv6 组播路由，**环境问题** |
| `upstream` `TestDo_DNSFailureReservedTLD` | 期望 `network` 实得 `transient` | 本机 resolver 对保留 TLD 返回了结果，**环境问题** |

两者所在包本轮**零改动**（`git status` 可证）。不要把这两条记到 P4 账上，也不要为了让它变绿去改代码。

**门禁状态**：`./verify.sh --web` 仍**退出 1**（迁移校验 12 mismatch / 2 stale / 613 unregistered）。
该漂移自 r0924 起未治理，与 P4 无关；台账记录 662→663→664 撞号改编历史，
**不得为过校验而恢复已废弃版本或覆盖已应用版本的 SHA**。

---

## 五、下一轮 prompt（可直接复用）

> **任务**：r0924 supplier-protocol-optimization **P1 admin UI endpoint 管理**（只读不调度）。
>
> **先核前提（必做第一步）**：`git log --oneline -20`、`git log --merges --oneline | head`、
> `docs/供应商协议优化-实施规划.md` §12.2 与 §12.9。P4 已合入（`422bb5fc4`）并经 r0926 审计修正（`607ba7db7`）。
> **不要**假设 §3.1 的文件清单是现状——它是目标态。
>
> **硬性要求**：
> 1. `admin/admin_endpoint.go`：6 个 CRUD（list/get/create/update/delete/set-primary）+ 单 provider 批量按 catalog 推荐补全。
> 2. `web/src/views/provider-detail/SettingsTab.vue` 新增 endpoint 列表 card；协议下拉含 `ollama-native`；
>    `vendor_native` 下拉取 `discovery.vendorCanonicalFamilies ∪ canonicalFamilyIDs`（**不要**新造枚举）。
> 3. 契约测试：`SELECT DISTINCT vendor_native` 必须落在上述 family 词表内（§3.3 承诺过但未实现）。
> 4. **不要**让 UI 改动影响 dispatch 行为——P1 阶段 dispatch 仍由 FF 控制。
>
> **不要做**：
> - 不动 `provider_endpoint_protocols.sql` DDL
> - 不做 P5 passthrough
> - **不接 `wantFamily`**（见 §三：会让 Stage 2 跨协议改道首次可达，须与 P5 同批评审）
> - 不引入新依赖（本仓 8 个 worktree 并行，依赖面变更会放大冲突）
> - 不动 `discovery` / `upstream` 两个环境性失败用例来「刷绿」
>
> **交付物**：1 个 commit（`feat(r0924): P1 admin endpoint CRUD + SettingsTab card`）+
> 更新 §12.2 状态表 + 最终交付报告（commit hash / 测试真实退出码 / 灰度回滚手段）。

---

## 六、并行环境操作要点（本轮踩到）

- 本仓是**多 worktree** 仓库（本轮 `git worktree list` 见 8 个）。`main` 被
  `/private/tmp/llm-gw-audit2` 占用，**在本 checkout 无法 `git checkout main`**。
- 工作目录的分支会被**并行会话切换**。本轮开工时是 `main`，中途被切到
  `feat/session-detail-body-status`。**开工与提交前各确认一次 `git rev-parse --abbrev-ref HEAD`**。
- 正确姿势：`git stash push -u` → `git checkout -b <branch> main` → `git stash pop`。
  切换前先 `git merge-base --is-ancestor <当前 tip> main` 确认不会丢提交。
- 提交前核对 `git status --porcelain` 只有自己的文件；别把别人的 WIP 一起 commit。

---

## 七、关键文件指向

| 关注点 | 路径 |
|--------|------|
| FF 开关（本轮修正语义） | `settings/p4_feature_flags.go` |
| **路由判定 + 回滚契约** | `domains/streaming/executors/executor_dispatch.go` → `classifyDispatchRoute` |
| 决策可观测日志 | 同上 → `endpoint_selector_decision` |
| 决策库（4 stage） | `internal/endpointselect/selector.go` |
| **JSONB 解码 + 漂移告警** | `provider/client.go` → `decodeNativeEndpoints`；`provider/candidate_native_endpoints_decode_test.go` |
| SQL 文本形状测试 | `provider/candidate_query_sql_shape_test.go` |
| Ollama executor（已落码） | `domains/streaming/executors/executor_ollama.go` |
| 审计结论全文 | [docs/供应商协议优化-实施规划.md §12.9](../供应商协议优化-实施规划.md) |

---

## 八、不要做的事（hardening）

1. **不要**把「P4 已合入 / 开关已接线」当成「供应商协议优化完成」向运营汇报。G1 不成立（无入站 handler），
   G4 只在 `ollama.*` Extensions 命名空间下成立，passthrough 无消费者。
2. **不要**在没读 `endpoint_selector_decision` 日志的情况下开 `FF_OLLAMA_NATIVE` 灰度——现在可观测了，用它。
3. **不要**把 `discovery` / `upstream` 那两个环境性失败算作 P4 回归。
4. **不要**直接删 `selector.go` 的 `_ = MatchStage3` 占位常量（observability label 已固化）。
5. **不要**照抄上一轮 handoff 的 commit 号与状态断言而不核 HEAD（§一就是教训本身）。
