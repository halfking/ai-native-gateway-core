# 2026-08-17 仓库结构治理（稳妥版）

## 背景

用户要求"理解所有子目录及系统架构，检查并优化，重新规划文件及存储结构，让系统易于理解并有序"。
探查结论：主服务为单一二进制 `cmd/gateway`，核心在 `domains/`（56 限界上下文）；但仓库根目录约 90 个
顶层条目，混有 6 个死包、约 250MB 本地二进制、约 15 个一次性脚本、多组重复目录与 2 处硬编码密钥泄漏。
上一轮文档归档（rule 36）有 1463 个路径暂存未提交。经用户确认采用**稳妥版**：不动 Go 包路径。

## 方案

六阶段：①提交基线→②安全清理→③删死包→④untrack 二进制+本地产物清理→⑤根目录去杂（归档/分桶/去重/untrack）
→⑥布局文档化（REPO_LAYOUT 权威地图 + ADR-0002 Go 分层迁移路线）。Go 大迁移因 domains↔internal 等
双向依赖（97/17 处）刻意推迟到 B1 CQRS 拆环之后，路线已写入 ADR-0002。

## 实现

分支 `chore/repo-restructure-2026-08`，6 个 commit：
1. `472e7b893` 基线（1463 路径 docs 重组 + 根脚本入 `_to_be_deleted/`；首提交漏掉删除侧，已 amend 补全）
2. `178ca741f` 删除含密钥调试脚本（debug.go、test_volcengine_*.sh、.tmp_check154.py、check_154_routing.sh）
3. `3af93cea6` 删除 6 个零引用死包（tracing/logging/alerting/safety/circuit/integration）
4. `b0e6b751f` untrack 二进制（61MB+8MB）、删 build_seq、make clean 增强、.gitignore 修正
5. `20351d4f1` 根目录去杂：7+3 个目录消失（audit-*/knowledge/memory-bank/reports/scratch/k8s/migrations/observability 归位）、8 个脚本分桶、测试目录收敛、4 处悬空引用修复
6. （本 commit）REPO_LAYOUT.md + ADR-0002 + INDEX/README 更新

## 测试验收

- 每 commit 走 pre-commit 钩子（go vet/SQL 检查）全过
- `go build ./...` 全绿（死包删除后）
- `tests/` 目录 `go vet` 通过（regression 测试移动后相对路径 `../../sql` 已修正）
- 悬空引用 grep 复核：4 处已修复（check-partition-health.sh、verify-partition-alignment.sh、ursm_v2_to_legacy.sh、doctor.sh）
- 工作区最终 clean

## 经验

- 首次 `git commit` 只提交了暂存侧的新增路径，旧路径删除仍在工作区（amend 前后用 `git status` 核对才发
  现）——大规模重组提交后必须验证"预期消失的路径确实从 HEAD 消失"。
- `tests/` 根已有 `package main`，外部测试包不能直接混入，须建子目录并修相对路径。
- 稳妥版边界有效：所有 Go 移动推迟到 B1 后，本轮零 import 行改动（除删除）。

## 关联资源

- `docs/architecture/REPO_LAYOUT.md`（布局权威地图+入位规则）
- `docs/adr/ADR-0002-target-go-package-layout.md`（Go 分层迁移路线）
- 需线下轮换密钥：debug.go 签名/加密 key（base64 `AwoRGB8m...`）、volcengine ark key
  `ark-a0e01643-...-dbc14`、（建议）8.136.114.154 root SSH 通道审计
- 后续项：git 历史重写清除 70MB 二进制与密钥、`deploy/sql/objects` 收敛、安装面三处合并
