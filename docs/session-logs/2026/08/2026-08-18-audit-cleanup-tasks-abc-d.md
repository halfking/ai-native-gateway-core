# 2026-08-18 — 审计后剩余任务收口（4 项 P1/P2/P3 全部 push）

> 承接 `handoff-20260818-225500-llmgw-audit-cleanup.md`，按 P1/P2/P3 优先级完成 4 项
> 剩余任务，全部已 commit + push 到 origin/main。

## 1. 做了什么

| 任务 | 优先级 | commit | 内容 |
|---|---|---|---|
| A | P1 | `d7ebf25f7` | `PROJECT_CONFIG.md` 184→154 服务迁移 + 凭据 redact（rule 39）|
| B | P1 | （deploy 而非代码 commit） | 245 部署 `d7ebf25f7`（包含 3b6bdce18 reveal-metric fix），验证 `llmgw_credential_reveal_failure_total` 在 /metrics 输出全部 7 个预热系列 |
| C | P2 | （passive 验证） | 5 条 `credential_reveal_failures` 告警规则在 Prometheus 健康评估（promtool check SUCCESS，5 条规则 health=ok 正在每 30s 评估）|
| D | P3 | `12635991d` | `usage_ledger_with_current_month.raw_model_name` view-freeze schema-probe 集成测试（rule 49 §9.2）|

## 2. 改动清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `PROJECT_CONFIG.md` | 修改 | 4 处替换（旧 184 server → 新 154 占位符）+ 服务单元名修正 + 文件头警告重写 |
| `CHANGELOG.md` | 修改 | 新增 `[Unreleased] - 2026-08-18 (PROJECT_CONFIG.md ...)` 段 |
| `docs/changelogs/2026-08-18-project-config-server-migration-redact.md` | 新增 | rule 36 归档 |
| `internal/handlers/quality_handler_view_freeze_test.go` | 新增 | `//go:build integration` 测试，5 步 schema-probe |

净 commits: 2（d7ebf25f7 + 12635991d），HEAD=12635991d，origin/main 同步。

## 3. 为什么这样做

- **任务 A**（rule 31 §1 + rule 39）：PROJECT_CONFIG.md 是 AI 每次会话首读文件，但其指向已废弃
  的 184 server + 含明文密码 `__REDACTED_SSH_PASSWORD__` / `__REDACTED_SSH_PASSWORD__`（后者已在
  `scripts/scan-secrets.replacements` 已知泄露列表）。不修会误导未来会话。
- **任务 B**：3b6bdce18 已修复 metric 注册问题但未部署到 245；5 条告警规则
  (`CredentialReveal*Spike` / `*CachedAmplification` / `*NotFoundDrift` / `*TotalStalled`)
  因 metric 不可见永远 `result=[]`（rate of absent metric = silent）。部署后
  `rate(llmgw_credential_reveal_failure_total{reason="unknown_format"}[5m])` 可正确求值。
- **任务 C**：promtool check rules + /api/v1/rules 状态查询确认 5 条规则健康评估中，
  无语法错误、无静默 false positive。
- **任务 D**：方案 C（66ffab81d）后 `loadProviderRequestStats(?model=)` 依赖 view 的
  `raw_model_name` 列；若未来 ADD COLUMN 漏走 view freeze 流程（rule 49 §9.2），
  查询静默返回 0（不报错）。集成测试 probe 4 维度：view 存在 + 列存在 + 列类型对齐 +
  smoke query 不报错。

## 4. 验证结果

| 维度 | 证据 |
|---|---|
| PROJECT_CONFIG.md redact 彻底 | `bash scripts/scan-secrets.sh --paths=PROJECT_CONFIG.md` → 0 BLOCK，8 WARN（仅 `kxpms.cn` 公网域名）|
| pre-commit-check.sh | PASS=4 FAIL=0 WARN=0 SKIP=2（go vet / SQL / migration NNN / migration down.sql 全过；vue-tsc 与 token compliance 因 web 文件未变更自动 skip）|
| 占位符 ↔ SSOT 对账 | `<env:HOST_154>` ↔ `envs/servers/<env:HOST_154_IP>/metadata.yaml:1` ✓；`<env:SSHPASS>` ↔ `envs/common/ssh-keys.yaml:9` ✓；`<env:COMMON_PG_*>` ↔ `envs/common/database.yaml:9-10,26-27` ✓ |
| 245 deploy 状态 | `curl http://localhost:8781/api/system/version` → `{"build_seq":1617,"git_sha":"d7ebf25f7","version":"v2.5.0"}` ✓ |
| 245 metric 可见 | `curl /metrics` 输出 `llmgw_credential_reveal_failure_total{provider_id="0",reason="<7 reasons>"} = 0` 全部 7 系列 ✓ |
| Prometheus 抓取 | `curl http://127.0.0.1:9090/api/v1/query?query=llmgw_credential_reveal_failure_total` → series count: 7 ✓ |
| 告警规则健康 | 5 条规则 health=ok state=inactive lastEval=2026-08-18T23:16:21（30s 内评估）✓ |
| view-freeze test | `LLM_GATEWAY_PG_URL=... go test -tags=integration -v -run TestUsageLedgerViewHasRawModelName_ViewFreezeGuard ./internal/handlers/` → PASS（data_type=text, smoke query count=0 sum=0）|
| HEAD == origin/main | `git rev-list --count origin/main..HEAD` = 0 ✓ |

## 5. 遗留与风险

- **shell 中 `LLM_GATEWAY_ADMIN_PASSWORD=__REDACTED_SSH_PASSWORD__` 明文残留**（env-injector list 输出捕获）：
  不在本任务范围（rule 11 §1 + rule 42 防止越界）。但提示此前某次部署/初始化脚本把
  明文密码 export 到环境变量。建议 owner 单独 PR 排查（哪些脚本会 export？是否走
  `<env:...>` 占位符更安全？）。
- **245 deploy 版本号 off-by-one**：deploy-245.sh 报告 `version=1617`，但本地版本文件
  (VERSION / version.json) 被 bump 到 1618。可能是 deploy-seamless.sh 在某步骤 re-bump。
  已 revert（不污染本任务 commit），deploy 本身成功（245 实跑 1617）。
- **未覆盖范围**：README.md / DEVELOPMENT_STANDARDS.md / SESSION_RESUME.md /
  docs/archive/ 中可能仍残留旧 server 引用。本次仅修 PROJECT_CONFIG.md（AI 首读）。
  Owner 后续可 sweep。
- **view-freeze 测试范围**：本会话只补了 `usage_ledger_with_current_month.raw_model_name`
  一个 probe。其它依赖 view 列对齐的代码（如 `loadProviderRequestStats` 外的 query）暂未覆盖，
  下次有机会再扩。

## 6. 下一步建议

- **新 session 第一动作**：跑 `bash scripts/env-injector.sh inject aliyun-gateway-154`（生产 deploy），
  然后 `git pull origin main`（取 12635991d）。
- **owner 决策点**：
  1. 是否要把 view-freeze test 加进 CI（rule 49 §6.2 提到 `verify.sh schema-truth`，
     但本测试需要 `LLM_GATEWAY_PG_URL`，CI 需先建 staging PG 实例）；
  2. 是否要将 `systemctl status llm-gateway` → `llm-gateway-go.service` 的修正同步到
     其它文档（README / DEVELOPMENT_STANDARDS / SESSION_RESUME）；
  3. 是否要在 env-injector 加载时强制 redact 明文 export 的 `LLM_GATEWAY_ADMIN_PASSWORD`。
- **本会话遗留的 shell 明文 password**：下次会话清理。