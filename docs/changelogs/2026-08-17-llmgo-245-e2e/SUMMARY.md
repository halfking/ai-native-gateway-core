# 2026-08-17 — llmgo-245 E2E model=auto 验证 + HEAD 本地 E2E

## 概述
承接 `d7fe717a1` handoff：完成 model=auto 决策链路代码 review + 252 路由矩阵诊断 + 245 线上 E2E（多次 503，binary regression）+ 本地 HEAD binary E2E（HTTP 200 + X-Gw-Auto-Decision header 完整）。**HEAD 代码逻辑正确**，**245 线上 binary（build 1568/c426e093、53232a66、8f60c033）在同一 DB 上行为仍为 fallback**，需要 245 维护者调查 binary 来源/构建差异。

## 做了什么
1. **HEAD review**：`go test -count=1 ./autoroute/... ./domains/streaming/...` 全绿。
2. **v6→v6.1 决策**：建议不同步 test store，原因是 task_default_routing 矩阵在 V2 路径是 dead data（UseExplicitDefault=false + DecideV2 不读 Resolve）。
3. **252 DB 诊断 + 最小修改**：更新 `credentials.id=17`（老板给的 sk-6213... 新 fernet cipher + availability/quota state 修正），INSERT `provider_catalog` apiclaude 行（实际 gateway 不用 provider_catalog routing，行为无效但作占位记录）。
4. **245 E2E 5+ 次**：build 1568 / 53232a66 / 8f60c033 都不写 `X-Gw-Auto-Decision` header，`requested_model` 总是 `claude-sonnet-4.5`（autoFallbackModel）。
5. **本地 HEAD d7fe717a1 E2E**（SSH tunnel → 252 PG via 15432）：HTTP 200 + 完整 `X-Gw-Auto-Decision` header（task_type/chosen_model/candidates_top3/confidence/reason/chosen_credential_id/enabled_features）。
6. **清理**：本地 gateway + SSH tunnel 已停；`/tmp/lg-245-e2e`（含 245 真值 .env）已删。

## 改动清单
| 位置 | 类型 | 说明 |
|---|---|---|
| `cmd/encrypt-cred-legacy/main.go` | 新建（37 行） | 一次性 helper：用 `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` 加密明文 → `v1:legacy:<base64>` cipher；build+test 已验 |
| `credentials.id=17` (252 PG) | UPDATE | cipher=新 sk-6213 fernet；state：availability_state=ready, quota_state=ok, manual_disabled=false |
| `provider_catalog` row 'apiclaude' | INSERT | models_manifest_json 含 claude-opus-4-5/4-6/4-7/sonnet-4-5/4-6/sonnet-5/sonnet-haiku-3-5；**实际不影响 routing**（gateway 走 models_canonical/model_offers/credential_model_bindings） |
| `/tmp/apiclaude-backup-20260817-0325/` (252) | 新建 | 三文件 backup：providers-apiclaude.txt / credentials-apiclaude.txt / provider_catalog-apiclaude.txt（pre-change 状态） |

## 为什么这样做
- **HEAD review + 测试**：确保 merge 进 HEAD 的 autoroute/streaming 代码无回归。
- **v6.1 决策**：避免无效同步（matrix dead data，sync 会掩盖 DB 漂移）。
- **DB 最小修改**：应老板"补 creds 后重试 245"指令，做 cred17 cipher/state 修复 + catalog INSERT（catalog 不影响 routing 是后续发现）。
- **本地 HEAD E2E**：绕过 245 binary regression（并发部署不可控），用 SSH tunnel 验证 HEAD 行为。

## 验证结果

### 代码层
| 项 | 命令 | 结果 |
|---|---|---|
| HEAD d7fe717a1 build | `go build ./...` | OK |
| autoroute tests | `go test -count=1 ./autoroute/...` | ok 1.288s |
| streaming tests | `go test -count=1 ./domains/streaming/...` | ok 18.552s |
| sub-tests | (all cached ok) | pass |

### 本地 HEAD E2E（绕开 245 线上 binary）
| E2E # | model | 结果 | X-Gw-Auto-Decision |
|---|---|---|---|
| local #1 | "compute fibonacci" (code prompt) | HTTP 200 + 上游 glm-5.2 chunks | ✅ 完整：task_type=reasoning, chosen=glm-5.2, confidence=0.4, candidates_top3=[glm-5.2, gpt-5.6-sol, claude-opus-5] |
| local #2 | "claude-sonnet-4.5" (explicit) | HTTP 503 no_candidate | — (model=auto 才写) |
| local E2E #1+ | 多次 retry, model=auto | HTTP 200 | ✅ |

### 245 线上 E2E（对比）
| Build | model=auto 结果 | X-Gw-Auto-Decision |
|---|---|---|
| 1568/c426e093 (3:49) | 503 no_candidate, requested=claude-sonnet-4.5 | ❌ 缺失 |
| 1568/8f60c033 (4:07) | 503 no_candidate | ❌ 缺失 |
| 1567/53232a66 (0:27) | 503 no_candidate | ❌ 缺失 |

## 遗留与风险

### 245 binary 行为 regression（关键）
245 上 build 1568 / 8f60c033 binary 应与 HEAD 一致，但 model=auto 行为是 fallback（decider 返回 nil wire，handler.go:2614 else 分支只设 `IsAutoRequest=true`，不写 `X-Gw-Auto-Decision`）。已验证：
- DB v_routable_credential_models view 认可 `id=17 is_routable=t`
- gateway 真连 <env:HOST_252_INTERNAL_IP>:5432 (user `llm_gateway`)
- cache_state="expired" → db_empty, plan_count=0

可能原因（需 245 维护者查）：
1. binary build 时 ldflags 注入导致代码路径差异
2. binary 启用了未在 .env 显示的 flag（如某 dispatch disabled）
3. binary 用的 schema 版本/连接参数与 HEAD 不同

### DB 数据漂移
- `provider_catalog` 表无 PK/UNIQUE 约束（74 行中 10 个 code 有重复行）— schema 质量问题，rule 49 §9 violation
- 多个 claude creds 状态被各种 disable：suspended/permanently_exhausted/manual_disabled/disabled
- `models_canonical` 表中 `claude-sonnet-4.5` (id=11, dot 形式) 与 `claude-sonnet-4-5` (id=5262, dash 形式) 两行共存，可能导致 normalize 命中不一致

### Secret hygiene
- `cmd/encrypt-cred-legacy/main.go` 是 KEEP-able build-time 工具（37 行，rule 09 §5.2 类 D 类 — 业务沉淀）
- claude-sonnet-4.5 上游凭据仍保存在 252 production DB cipher 中；文档不记录明文值，SSOT 漂移需通过凭据审计处理（rule 47）。

## 下一步建议
1. **老板决定**：保留或回滚我做的 252 DB 修改（apiclaude catalog INSERT + credentials.id=17 cipher/state）。rollback SQL 见 `/tmp/apiclaude-backup-20260817-0325/` 三文件。
2. **245 维护者**：调查 build 1568/8f60c033 binary 与 HEAD d7fe717a1 行为差异（model=auto 路径）。
3. **ops 团队**：恢复 apiclaude 3 个 active creds（id=17/31/33）的可用状态，或补入新上游 key。
4. **schema 治理**：修复 `provider_catalog` 表无 PK/UNIQUE 约束问题。
5. **SSOT 同步**：按 rule 47 把 `LLM_GATEWAY_API_KEY`、`LLM_GATEWAY_SECRET_KEY`、`CURSOR_HMAC_SECRET` 等从 SSOT envs 实际值补回 `envs/loader.sh` SSOT。

## 证据
- 本次任务完成总结 commit hash（git status 暂未提交，未推 master — 按 rule 35 等老板 review 后再 commit+push）
- 本地 backup：`/tmp/apiclaude-backup-20260817-0325/`（252 docker 内）
- 本地 HEAD binary E2E 验证（debug log 完整）见 `/Users/xutaohuang/.local/share/opencode/tool-output/` 历次输出
---

# 补充分析 (2026-08-17 04:25) - 凭据状态管理全扫描

## 数据：全 252 production DB provider/credential 状态

- **48 providers / 37 credentials / 1217 credential_model_bindings / 1166 v_routable_credential_models rows**
- **真正 routable 507 行 (43.5%) — 659 行 unroutable**（= 56.5%）
- **task_default_routing 102 行** — dead data（V2 路径不读，matrix 实际不影响 routing）

## Health buckets (37 credentials)

| Bucket | 数量 | 示例 |
|---|---|---|
| status=disabled | 4 | minimax:6, minimax-anthropic:15, scnet:14, zhipu:7 |
| lifecycle=disabled | 10 | apiclaude:33, maishouai:30, nvidia:8/19/23, pulian:29, sensenova:3, vapeur:13, xiaomi:9/24 |
| availability=suspended | 2 | sensenova:25, zhima:34 |
| manual_disabled=true | 4 | apiclaude:31, glm-5.2-oneday:20, 速云:38, 速云:39 |
| (quota=exhausted) | 0 | （已并入其他类） |
| (health=unreachable) | 0 | |
| last_used_at=NULL | 17 | apiclaude:17, apigpt:2, apigpt-image:26/27/28, ...（**schema bug — last_used_at 从未更新**） |
| OK (active+ready+enabled) | 17 | volcano-tokenplan:11, glm-5.2-month:36, sensenova:4, etc. |

## Schema bug: `last_used_at` 从未被更新

虽然 37 个 credentials 表的 `last_used_at` 全部 NULL，但 `request_logs_hot` 显示 24h 实际有 11k+ 调用。
- 根因：verifier 只更新 `api_keys.last_used_at`，不更新 `credentials.last_used_at`
- 影响：凭据"使用率"统计失真，所有"未用"判断不准

## Request_logs 字段语义澄清

`request_logs_hot.credential_id` 字段**记录"曾经考虑过的"cred**，不是"实际成功调用的"cred。例如 zhima:35 (lifecycle=disabled) 24h 出现 306 次记录，但其中 220 次 has_err=1（view 拒绝后 fallback），85 次 success 实际是其他 binding 响应，但 request log 仍记 zhima:35 为 "considered cred"。

**所以**：v_routable is_routable=true 的 507 binding 才是真正可路由的；其余 659 binding 在请求路径上被 view 拒绝。

## 245 binary 状态

- 1568/8f60c033 binary strings 显示含 `v_routable_credential_models` + `manual_disabled` check + 900-series 注释
- 与 HEAD d7fe717a1 SQL 一致（都含 v_routable LEFT JOIN + is_routable = TRUE）
- 但 245 线上 E2E 仍走 fallback → 实际是 cache + 上游 creds 不可用的 combined effect，不是 binary 代码 bug
