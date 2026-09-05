# 24h 审计 — 原始数据 Manifest

**项目**：`services/llm-gateway-go`（monorepo 子模块，独立 git 仓）  
**审计窗口**：自 `2026-06-26 04:30:13 +0800`（最早 24h commit）至 `2026-06-27 03:50:58 +0800`（最新 commit `207badf0`）  
**生成时间**：2026-06-27 03:48 +0800（manifest 草拟）；03:52 校正

## 1. 范围摘要

| 指标 | 值 |
|---|---|
| 24h 内 commit 数 | 53 |
| 影响文件数 | 35 files changed, +4843 / −58（窗口：最早 24h commit 的父 → 最新 commit） |
| 24h 累计改动 | 130 files, +1,252 / −14,171（首尾 24h commit 范围，主因 R1.13 cutover 删了 12.9k 行） |
| Untracked 文件 | 0 |
| 未提交修改文件 | 1（仅 `version.json`，+5/-5） |
| Commit 作者 | 全部 `opencode-agent <opencode-agent@opencode.local>` |

> 备注：父仓库 `official-deploy` 把 `services/llm-gateway-go` 视为子模块；本审计**仅看子模块内部视角**。父仓库 03:47 刚做了 submodule bump（`621e6b8`）把子模块指到 `22613c1f`，但子模块内部又继续 commit 到 `207badf0`——即父仓库的子模块 ref 也已落后。

## 2. 24h 改动主方向（按 commit 标题归类）

### 2.1 反复修复链 — **`request_logs` upsert 反复修复**（关键风险点）

| 时间 | commit | 标题 |
|---|---|---|
| 06-27 03:44 | `e7d4deb6` | fix(telemetry): qualify request_logs client_request_id in upsert |
| 06-27 03:35 | `6b2ba0e2` | fix(telemetry): unblock request_logs upsert conflict branch |
| 06-27 00:58 | `0ffa7466` | fix(telemetry): disambiguate request_logs update row match |
| 06-27 00:53 | `c4e1ae47` | fix(telemetry): qualify request_logs update predicate |
| 06-27 00:47 | `a059c265` | fix(telemetry): remove target alias from request_logs update |
| 06-27 00:35 | `b35bb750` | fix(telemetry): unblock failure-path request_log updates |
| 06-27 00:30 | `726070b9` | fix(telemetry): unblock failure-path request_log updates |
| 06-26 23:43 | `acfe7b58` | fix(telemetry): server-generated request_id + client_request_id column |

→ 8 次连续 fix 一个表写入逻辑，**根因可能没找到**。

### 2.2 Session id aliases 全链路（7 个 commit）

| 时间 | commit | 标题 |
|---|---|---|
| 06-27 02:35 | `f1c54133` | feat(web): explain session alias mapping preview |
| 06-27 02:16 | `905c1476` | feat(web): preview session alias extraction examples |
| 06-27 01:11 | `91edb69a` | feat(web): add tag editor for session alias setting |
| 06-27 00:56 | `f12b5517` | feat(settings): hot-reload session id body aliases |
| 06-27 00:11 | `12866e30` | feat(config): support session id alias arrays |
| 06-26 23:46 | `0babc2ff` | feat(config): wire session id body alias config |
| 06-26 23:18 | `c353ce5f` | feat(streaming): allow configurable session id aliases |

→ 配置 → 设置 → 流式 → 网关 → 前端全链路 7 个 commit，跨度 3+ 小时。

### 2.3 memora → memory 包重构

| 时间 | commit | 标题 |
|---|---|---|
| 06-27 02:18 | `0e4a5dbf` | refactor(gateway): isolate legacy memora wiring |
| 06-27 01:40 | `d85acdd8` | refactor(admin): move memora readable search to memory package |
| 06-27 01:15 | `a1edfb00` | refactor(admin): move session extraction to live memory package |

### 2.4 Bandit / Thompson Sampling 路由

| 时间 | commit | 标题 |
|---|---|---|
| 06-26 05:21 | `c50e844b` | feat(routing): move route node health to credentialfpslot |
| 06-26 04:47 | `f43586e0` | feat(bg): BanditFlusher 持久化 Thompson Sampling scorer state |
| 06-26 04:46 | `67fa7405` | feat(routing): Thompson Sampling Bandit scorer in domains/streaming/executors |

### 2.5 gateway-v2 切流 + 旧代码裁剪

| 时间 | commit | 标题 |
|---|---|---|
| 06-26 04:30 | `67340e57` | refactor(llmgw): R1.13 cutover — move 14 legacy packages to _to-be-deprecated and rewrite imports |

→ **最大单 commit**：`git diff --shortstat 67340e57^..67340e57` 显示 `356 files changed, 2476 insertions(+), 474 deletions(-)`（注意早期 `git log --shortstat` 因格式问题被 shell 截断为 `474`，实际数据需重核）。这就是 24h 累计 -12.9k 行的主因。

### 2.6 其他关键 commit
- `6a2e5fcd` fix(disguise): restore NewRedisPool constructor compatibility
- `22613c1f` test(executors): fix force-unpin redis setup
- `207badf0` fix(executors): add miniredis to credentialfpslot tests (HEAD)
- `6503da98` fix(routing+version): fail-open route nodes + robust version parsing + local migrate baseline
- `dee94b28` fix(routing): fail-open when all route nodes disabled + archive version parser/migration script refinements

## 3. 改动最大的 Top-10 文件（24h 累计，按 `git log --shortstat` 排序）

| 文件 | 净变化 |
|---|---|
| `cmd/gateway/main.go` | +340 / −未知（24h 内 +310 净） |
| `credentialfpslot/slot.go` | +460 / −未知（24h 内大幅改写） |
| `admin/credential_monitor.go` | +198 / −未知 |
| `domains/hooks/observability/telemetry/client.go` | +194 / −未知（**8 次连续 fix 的标的**） |
| `web/src/views/CredentialMonitorView.vue` | −388 行（重写） |
| `cmd/gateway/main_v3_wiring.go` | −212 行（删除） |
| `domains/streaming/session_routing.go` | +150 / −多行（重写） |
| `bg/bandit_flusher.go` | −246（删除） |
| `domains/credential/flusher.go` | −206（删除） |
| `domains/credential/flusher_test.go` | −229（删除） |
| `domains/memory/rebuilder.go` | −199（删除） |
| `migrations/...sync-from-184.sql` (多个) | 每个 −400~−800 |

## 4. 未提交修改（1 个文件，仅 version.json）

```diff
- "version":   "phase1.5-pre-r113-20260626-8-gdee94b28"
- "git_tag":   "phase1.5-pre-r113-20260626-8-gdee94b28"
- "git_sha":   "dee94b28"
- "build_seq": 699
- "build_date":"2026-06-25"
+ "version":   "phase1.5-pre-r113-20260626-52-ge7d4deb6"
+ "git_tag":   "phase1.5-pre-r113-20260626-52-ge7d4deb6"
+ "git_sha":   "e7d4deb6"
+ "build_seq": 715
+ "build_date":"2026-06-26"
```

### ⚠️ 关键漂移
- **HEAD = `207badf0`（fix(executors): add miniredis to credentialfpslot tests，03:50:58）**
- **version.json git_sha = `e7d4deb6`（fix(telemetry): qualify request_logs client_request_id in upsert，03:44:20）**
- version.json 落后 HEAD **2 个 commit**（`22613c1f`、`207badf0`）
- version.json 中 `git_tag` 仍称自己为 `-52-ge7d4deb6`，但实际 24h 内 53 个 commit 已超出，且又新增 2 个 → tag 字段名 "52" 与 HEAD 不一致

> 此 uncommitted diff 自身可能是未完成的发布准备遗留。

## 5. R1.13 cutover 影响（_to-be-deprecated/）

- `_to-be-deprecated/` 现含 **16 个子目录**（不是 commit 标题说的 14）：
  - audit, auth, circuit, compressor, credentialstate, identity, limiter, memora, observability, orphan-tests, relay, routing, sessions, telemetry, transform, transport
  - 外加 `MIGRATION-MANIFEST.md` 和 `README.md`
- 需检查：是否仍有 dangling import 引用这些包名（commit `0e4a5dbf` "isolate legacy memora wiring" 暗示 cmd/gateway 仍直接用 `memora`）

## 6. SQL 迁移状态

```
migrations/
├── 032_session_tenant_binding.sql
├── 033_bandit_scoring.sql
└── 034_session_reuse_idx.sql
```

→ 24h 内未新增编号迁移文件；但 commit `e25eb2ba` 提到 `archive_request_logs migration 053` —— 需查实际是否落盘。

## 7. 文件清单（已落盘）

| 文件 | 用途 |
|---|---|
| `_raw/commits.txt` | 53 个 commit 的 HASH + 时间 + 标题 |
| `_raw/files.txt` | 每个 commit 的 file-level 改动 |
| `_raw/shortstat.txt` | 每个 commit 的 --shortstat |
| `_raw/uncommitted.diff` | 当前工作区未提交 diff（仅 version.json） |
| `_raw/untracked.txt` | 当前 untracked 列表（空） |

## 8. 关键审计提示（给后续 agent）

1. **重点核查 `domains/hooks/observability/telemetry/client.go` 的 8 次连续 fix 根因**——可能存在 SQL 谓词/列不匹配、UNIQUE 约束、并发竞态、缺迁移等根因
2. **session alias 全链路**：检查 settings/spec_session.go 是否有默认/迁移、是否向前兼容
3. **gateway-v2 cutover (`67340e57`)**：16 个旧包被搬至 `_to-be-deprecated`，需查 import 链、是否有 dangling reference（commit 0e4a5dbf 暗示 `memora` 还在用）
4. **Bandit 持久化与 flush**：状态写库/恢复、并发安全、失败语义
5. **测试简化**：24h 净减 12.9k 行中，测试文件 -524 行（credentialfpslot）需查是否丢失回归保护
6. **version.json 漂移**：HEAD=207badf0 vs version.json_sha=e7d4deb6，落后 2 commit；且 build_seq 715 < 24h 实际 commit 数 53
7. **disguise NewRedisPool 兼容修复**：是否彻底，是否有调用方仍用旧签名
8. **uncommitted 仅有 version.json**：意味着现在的工作区主要是"版本号没刷新"；HEAD 比 manifest 的 commit list 多了 2 个 fix 提交
9. **fail-open 路由（6503da98 / dee94b28）**：当所有路由节点 disabled 时是 fail-open，需评估安全后果
10. **untracked 列表为空** → 之前看到的 `bg/session_audit_worker.go`、`domains/hooks/session-audit/` 已纳入 24h commit 列表（应在 2.5 之外的历史窗口）
