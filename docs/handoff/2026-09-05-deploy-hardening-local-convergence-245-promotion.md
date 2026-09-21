# Session Handoff: 部署链路加固收口 + 本地环境收敛 + 245 预发晋升（2026-09-05）

> 会话：主代理（orchestrator），2026-09-05 14:5x–16:2x (+08)
> 分支 main；会话结束时 HEAD 见 §4。全文不含任何凭据值。

## 1. 任务概要（Mission Summary）

四目标（G1–G4）：
1. **G1** 部署链路同源风险清零：245/154/seamless/local-lib 完成陈旧二进制事故同类漏洞审计并修复。
2. **G2** 回归防线固化：CGO=0 构建 + 「构建失败不得静默」进自动化测试。
3. **G3** 本地环境收敛到 origin/main 精确构建并留证。
4. **G4** 特性晋升 245 预发（154 生产只产出放行清单，不自动晋升）。

四个目标**全部完成**，无 BLOCKED。

## 2. 当前方案（Approach / Plan）

- Phase A 三子代理并行（A1 脚本审计修复 / A2 CGO=0 回归测试 / A3 文档收口），
  子代理只改文件不碰 git，主代理串行审查提交。
- Phase B 本地蓝绿重部署 + 部署后身份核验合同（vcs.revision 三方比对）+ 端到端三连。
- Phase C 245 晋升（env-injector 凭据 → dry-run → apply → 9 步验证）+ 154 放行清单文档。

## 3. 任务进度（Progress）

| 提交 | 内容 | 阶段 |
|---|---|---|
| `72e56dbb6` | fix(deploy): seamless/upload_release 远端残留清理+活跃 release 拒覆盖；do_deploy 吞错拆分+go build 三重防线；host.sh stage/verify/switch 加固；local-lib dl_release_name/dl_stage_release 赋值语境防护 | A1 |
| `9edd5ffef` | test(deploy): tests/deploy_nocgo_build_test.sh（正向 CGO=0 构建 + 反向 cgo-only 回归 + go list -deps 静态绊线，8 断言） | A2 |
| `3f99000f8` | docs: 事故复盘 §5 三条勾选 + skill Post-promotion identity check + nocgo 测试入验证合同 | A3 |
| `1ab23e09c` | merge origin/main（并行会话 dc3c43015，零冲突） | A 收口 |
| `a5330422c` | docs: local-8782 收敛 2.5.0.1943 全绿（§4/§5） | B |
| `f6ea47da6` | fix(db): migration 658 视图 DROP+CREATE 可重放化（修 245 迁移失败） | C1 |

推送均在 `pull --ff-only`/审阅对方提交后 merge 完成，无 force push、无 rebase。

## 4. 当前状态（Current State）

- **origin/main**：`f6ea47da6`（本会话最后推送）。
- **local 8782**：`2.5.0.1943`（vcs=`c087914eb`）在跑；f6ea47da6 相对其只有 658 迁移与
  文档差异，下次本地部署即收敛（无强制原因时无需立即重部）。
- **245 预发**：`2.5.0.1945`（sha=`f6ea47da`）在跑，`current → releases/1945-f6ea47da`，
  全 9 步验证通过。
- **154 生产**：未动，等人工按放行清单执行。
- **共享 PG**：migration 658 已应用（by 245 部署）；632 已是可重放版。
- 工作区遗留版本产物（VERSION/version.json/web/public/*）为部署脚本生成，永不提交。

## 5. 下一步（Next Steps）

1. **154 放行**（人工）：按
   `docs/06-deployment/01-environments/154-production-release-checklist-20260905.md`
   逐项勾选执行；候选 `2.5.0.1945`，建议 ≥24h 预发观察期。
2. 本地/245 下一次部署即自然收敛到最新 main；每次晋升后执行 skill 新增的
   Post-promotion identity check。
3. 遗留小项：`docs/06-deployment/01-environments/INDEX.md` 的 staging 行仍引用
   `staging-validation-progress-20260829.md`（存在）；多 clone 纪律仍为流程约定未工具化。
4. 并行会话遗留（非本会话范围）：`TestMigration632_RoutingAnalyticsMaterializedView`
   在 HEAD 上失败（632 重写后测试预期未同步）——已实证为并行会话引入的既有失败。

## 6. 关键事实（Key Facts）

- **事故根因**（docs/audit/2026-09-05-stale-binary-deploy.md）：storage/sqlite(CGO) 进
  cmd/gateway 依赖图 + bash `local x=$(fn)`/`var=$(fn)` 赋值语境吞 set -e → 陈旧二进制
  打新版本号。修复风格：先删旧产物 → 显式退出码 → 非空校验（bc6e696b3 同款）。
- **部署身份核验合同**（已入 skill）：`grep -a 'vcs.revision=' <bundle>/gateway` ==
  bundle version.json git_sha == 构建 HEAD；`run/gateway.build` mtime=部署时间。
- **本地验证**：详情端点参数是 `request_id` 字符串（非热表数字 id）；写侧 `request_logs_hot`
  热表时间列是 `ts`；快照端点对不存在会话返回 2 字段 200（现存最大形态 14 字段）。
- **本地 admin JWT**：容器 `LLM_GATEWAY_SECRET_KEY` 为空值时，空密钥 HMAC 签发的 JWT
  可通过 VerifyToken（SignToken 才拒绝空密钥）——文档 §2 验证路径的原理。
- **env-injector**：Bash 工具是 zsh，须 `bash -c 'source …/env-injector.sh inject <id> && …'`
  同 shell 串联（直接 zsh source 会因 BASH_SOURCE 未绑定失败）。
- **迁移可重放纪律**：改视图/物化视图必须 DROP+CREATE，不能 CREATE OR REPLACE 改列名
  （658 与 632 同类教训）；迁移按版本号记账、无内容 checksum，改未应用迁移内容安全。
- **一次性 PG 验证容器**：`docker run --rm -p 127.0.0.1:55432:5432 kx-citus-pg17:offline-arm64`
  可隔离复现迁移问题，不触碰共享 llm-gateway-pg。

## 7. 阻塞 / 风险（Blockers / Risks）

- 无 BLOCKED。风险：
  - 并行会话同仓高频提交，push 前必须 pull；同工作目录下 HEAD 可能随时移动，
    部署前重新确认 HEAD 与 version.json。
  - 154/245 共享 PG：154 晋升的迁移步骤应为 0 pending，若非 0 先核对来源。
  - nbjl3 项目也有 `deploy-local.sh`（pgrep 会误报），以锁文件
    `/tmp/kx-llm-gateway-build.lock` 与进程 cwd 判别。

## 8. 建议加载的 skills（Suggested Skills）

`llm-gateway-deploy`（验证合同已更新）、`llm-gateway-test`、`env-injector`、`handoff`。

## 9. 引用（References）

- 事故复盘：`docs/audit/2026-09-05-stale-binary-deploy.md`（§5 已勾选 3/4）
- 本地环境状态：`docs/06-deployment/01-environments/local-8782-env-state-20260905.md`（§4 已更新）
- 154 放行清单：`docs/06-deployment/01-environments/154-production-release-checklist-20260905.md`
- 特性验证：`docs/FEATURE-REQ-session-snapshot-and-request-detail.md` §4
- 部署 skill（身份核验合同）：`.agents/skills/llm-gateway-deploy/SKILL.md`
- 部署日志（本会话，宿主机临时区，含完整 9 步证据）：`/tmp/c1-deploy-245-v2.log`、
  `/tmp/b1-deploy3.log`
