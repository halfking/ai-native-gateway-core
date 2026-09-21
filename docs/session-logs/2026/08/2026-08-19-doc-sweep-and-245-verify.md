# 2026-08-19 — 245 验证 + P3 doc sweep + P2 URSM v2 回归 + ZCode WIP 协调

> Session: 接力 `handoff-20260819-audit-and-concurrent-zcode-preserve.md`。
> 主题: 8/19 audit 维护 + 后续 WIP 协调。
> Owner: AI 维护（OpenCode session-init）。
> Rule refs: rule 04 (AI 协议), rule 11 (执行协议), rule 36 (归档), rule 39 (脱敏),
> rule 43 (UTF-8 + 精准修改), rule 47 (envs SSOT), rule 50 (审计门禁)。

## 1. 接手时状态（00:35 from handoff）

- HEAD = `b3036166d`（handoff 记录的 HEAD）
- working tree clean
- ZCode (PID 72414) 在后台 active iteration
- 245 server build_seq=1617 (handoff 记录)

**实际接手时偏差**:
- HEAD 已前进到 `e9aaf3f01`（ZCode 后续 4 commits: 50bf5ba0f, aadd4dd92,
  3e9f879b4, d32426244, ed5352c2a）
- working tree 已有 ZCode 7 行 storage note 注释（`domains/hooks/observability/telemetry/client.go`）
- stash 保留 ZCode 3rd iteration（`admin/routing.go` 空白行调整，已被 HEAD e9aaf3f01 吸收一部分）
- 245 server build_seq=1618（比 handoff 时又新 1 个号，git_sha=b3036166）

## 2. 关键决策（rule 37 原则 1 编码前思考）

**问题 1: 是否 commit ZCode 7 行 storage note？**

判断:
- 注释内容合理（呼应 rule 33 partition + columnar 规范，解释 UPDATE 为什么打 request_logs_hot 而非父表）
- ZCode 仍在 active iteration（CPU 91%），可能继续修改
- 风险: 若我先 commit，ZCode 后续 edit 会冲突

**决定: 不 commit ZCode WIP**。保留 working tree，等 ZCode 自己 commit。这是
handoff 原则"不要丢弃其他人修改的代码"的具体执行：WIP 归 WIP owner。

**问题 2: 245 deploy 是否需要做？**

handoff §4.2 要求 deploy b3036166 修复版本到 245。但接手时 245 已经在跑:
- build_seq=1618, git_sha=b3036166 — 这正是 handoff 期望的目标版本
- binary 含 `admin.applyURSMOverlay` 函数（验证修复已包含）
- healthz OK

**决定: 跳过新 deploy**。245 已在目标版本，额外 deploy 会 bump 到 1619 + git_sha 升级，
**破坏 ZCode 8cc0d7b25 的 245 probe-canary 状态**（canary 是在 1618 build 上跑的）。

**问题 3: 如何 push 我自己的 sweep 修复而不破坏 ZCode？**

发现: 我 commit 25f6affbd 时被 remote 拒绝（ZCode 又 push 了 2 个新 commits）。
连续 2 轮 stash + rebase + push + pop 才能 push 成功。

## 3. 执行步骤（rule 11 §3 git 三律 + rule 50）

### 3.1 状态采集

```bash
git status --porcelain   # 1 modified file (ZCode WIP)
git stash list           # 1 stash (ZCode 3rd iteration)
git log --author=zcode --oneline b3036166d..HEAD  # 4 ZCode commits
ps -p 72414 -o pid,etime,pcpu  # ZCode Helper Renderer 仍跑，CPU 90%+
```

### 3.2 P3 文件 sweep（rule 39 铁律 1）

**Sweep 1 - <env:HOST_184_IP> IP 引用**:
- `docs/06-deployment/01-environments/deployment/CONFIGURATION_GUIDE.md:19,20,130`
  → 全部替换为 154/252 占位符
- `cmd/compression-bench/README.md:212-220` → 252 (PG17) 端口转发 + 占位符
- `cmd/verify-model-fetch/main.go:4` 注释 → `71/184` → `154/252`

**不修改文件（rule 36 豁免）**:
- `docs/archive/**` (20+ 文件, rule 36 归档后禁改)
- `CHANGELOG.md` 历史段（rule 36 保留原状）
- `tests/deploy_cli_test.sh` + `tests/deploy_sops_test.sh` (向后兼容 alias 测试)
- `deploy/sql/DEPLOYMENT_PLAN.md`（v1.0 历史方案，整篇关于 184 + PG/Citus，
  应归档到 `docs/archive/2026-07/`，作为 follow-up 留给 owner）
- `.kiro/skills/deploy-184.RETIRED.md`（本身是 RETIRED 标记）

### 3.3 提交 + rebase 循环（rule 50 §5 兜底）

```
commit 25f6affbd (本地)  → push 被拒（origin 有 61ba5c5a7 + 8cc0d7b25）
stash push WIP           → rebase origin/main → push origin main → stash pop
                        → 又被拒（origin 有 2bcf485ae）
再次 stash + rebase + push + pop → success (f54ae6de8)
```

**最终状态**:
- HEAD = `f54ae6de8` = origin/main (0 ahead/behind)
- 本会话 commit: `f54ae6de8 docs(sweep): redact remaining 184 server refs from active docs + add changelog`
- ZCode 6 commits absorbed: 50bf5ba0f, aadd4dd92, 3e9f879b4, d32426244, ed5352c2a, e9aaf3f01, 61ba5c5a7, 8cc0d7b25, 2bcf485ae

### 3.4 P2 URSM v2 集成回归（read-only 验证）

```bash
# 1. go vet 全仓库
go vet ./...                                              # PASS

# 2. applyURSMOverlay 相关 short 测试
go test -short -count=1 ./admin/ \
  -run "TestApplyURSMOverlay" -v                         # 4 tests PASS

# 3. URSM v2 全栈 short 测试（16 子包）
go test -short -count=1 -timeout=120s \
  ./admin/ ./provider/ ./bg/ ./domains/ursm/...           # 全部 PASS

# 4. 245 healthz 验证
curl http://<env:HOST_245_IP>:8781/healthz                    # OK
curl http://<env:HOST_245_IP>:8781/api/system/version         # 2.5.0-b3036166-20260818-1618

# 5. 245 binary 含 applyURSMOverlay
strings /opt/llm-gateway-go/current/gateway | grep applyURSMOverlay
# → github.com/kaixuan/llm-gateway-go/admin.applyURSMOverlay ✓

# 6. 245 reveal-metric（handoff §4.2 关键验证）
curl -H "Authorization: Bearer <env:LLM_GATEWAY_ADMIN_API_KEY>" \
  http://<env:HOST_245_IP>:8781/metrics | grep llmgw_credential_reveal
# → llmgw_credential_reveal_failure_total{provider_id="0",reason="..."} 0
#   7 个 reason 全部 pre-warmed (cached/decrypt_error/not_configured/not_found/other/rotation/unknown_format)
```

### 3.5 245 deploy 状态判断（结果: 已完成）

handoff §4.2 目标: "deploy b3036166d 修复版本到 245 + curl /metrics 验证 reveal-metric"。

**实际状态**:
- 245 跑的是 b3036166+1618（handoff 期望的 git_sha）✓
- 245 binary 含 applyURSMOverlay（handoff 期望的 syntax fix）✓
- 245 reveal-metric 工作正常 ✓

**结论: handoff §4.2 目标已达成**。不需要做新 deploy（额外 deploy 会破坏 ZCode 8cc0d7b25 的 canary 状态）。

## 4. P3 follow-up 移交 owner

1. **`deploy/sql/DEPLOYMENT_PLAN.md` 归档**: 整篇 v1.0 关于 184 + PG/Citus 部署。
   应 mv 到 `docs/archive/2026-07/specs/deployment-plan-v1-184-pg-citus.md` 并加
   deprecation banner。本会话未做（避免又一轮 ZCode rebase 循环）。
2. **`envs/` 仓库 __REDACTED_SSH_PASSWORD__ 默认密码**: env-injector list 输出明文暴露。
   跨项目 SSOT 设计如此（rule 47 §3 双写明文 + 加密），但屏幕/日志泄露风险
   由 owner 决策 rotation 周期。本会话**不擅自动**（rule 10 §2.3 human_only）。

## 5. 验证（rule 09 FACT + rule 11 §10）

- ✅ Factuality: 所有 sweep 修改与 codeup.aliyun.com 远程 main 一致
- ✅ Alignment: 完整覆盖 handoff §4.1-§4.5 任务
- ✅ Consistency: code style 匹配项目既有 CHANGELOG/changelog 格式
- ✅ 段落级验证:
  - `go vet ./...` PASS（rule 11 §14）
  - `go test -short` PASS（rule 17 三件套之单测）
  - `git diff` 确认 sweep 只改 184 引用（rule 37 原则 3）
  - `file` 命令确认 3 文件 UTF-8 编码（rule 43 §2.1）
  - pre-commit checks: 4 PASS / 0 FAIL / 0 WARN / 2 SKIP（web-only）

## 6. 遗留与风险

- **ZCode 仍在 CPU 59% active iteration**: working tree 7 行 storage note 是 ZCode 未提交工作。
  下次会话开工时必须先 `git status` 验证，可能又有新 WIP 出现。
- **stash@{0} 仍在**: ZCode 3rd iteration（admin/routing.go 空白行调整）还没 pop/apply。
  如果 ZCode 后续 iteration 重新基于这个 stash 内容，会 conflict。
- **245 在 b3036166 + 1618 build**: 不是最新 HEAD（f54ae6de8）。如果生产 bug 出现在
  ZCode 4 个新 commits（50bf5ba0f 起），245 不会复现。154 server 状态未验证。
- **DEPLOYMENT_PLAN.md 仍未归档**: 见 §4 follow-up #1。

## 7. 下一步建议

- **owner review session log**: 检查 sweep commit f54ae6de8 + 本 session log
- **DEPLOYMENT_PLAN.md 归档**: 在 ZCode 安静时（CPU < 10%）执行
- **ZCode 监控**: 下次会话开工先 git status + git stash list
- **154 server 同步**: ZCode 4 commits 没 deploy 到 154，需要单独 verify 路径

## 8. commit / 验证人

- Author: AI 维护（OpenCode session-init 接力）
- Reviewer: 待 owner review
- 关键 commits: `f54ae6de8 docs(sweep): redact remaining 184 server refs from active docs + add changelog`
- Rule refs: 04 / 09 / 11 / 17 / 36 / 37 / 39 / 43 / 47 / 50