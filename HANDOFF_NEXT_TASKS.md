# Handoff: Provider 587 凭据解密修复后续任务

## 背景
2026-09-05 provider 587 凭据解密失败事故已修复并验证通过。修复提交 0145f46d0 已合并到 main 分支并推送。本地、154、245 三端验证全部通过。

完整审计报告见: `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/AUDIT_CREDENTIAL_DECRYPT_FIX_20260905.md`

## 剩余任务(按优先级)

### 任务 1: 清理旧 worktree (高优先级)
**问题**: 以下 4 个 worktree 共享 `~/kaixuan` 安装根，但脚本还没有 `.env.local` 导入逻辑，从它们发起部署会复现事故：
- `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go-deploy-blue-green`
- `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go-fix-v1-legacy`
- `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go-integration`
- `/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go-node-state-sync`

**行动选项**:
1. **删除 worktree**(如果不再需要):
   ```bash
   cd /Users/xutaohuang/workspace/official-deploy/services
   git worktree remove llm-gateway-go-deploy-blue-green
   git worktree remove llm-gateway-go-fix-v1-legacy
   git worktree remove llm-gateway-go-integration
   git worktree remove llm-gateway-go-node-state-sync
   ```
2. **升级脚本**(如果还在使用): 将 main 分支的 `scripts/deploy-local*.sh` 复制到各 worktree

**建议**: 先问用户这些 worktree 是否还需要，不需要就直接删除。

---

### 任务 2: 文档更新 (中优先级)
补充部署文档和故障排查指南：

1. **本地部署指南**:
   - 文件: `docs/deployment/local-deployment-guide.md`(可能需要新建)
   - 内容: `.env.local` 必备性、密钥固定原则、从 245 同步密钥的步骤
   
2. **故障排查指南**:
   - 文件: `docs/troubleshooting/credential-decrypt-failed.md`(可能需要新建)
   - 内容: 本次事故案例、诊断步骤(网关日志 keyring=false、admin API credentials 接口)、修复流程

---

### 任务 3: 154/245 例行巡检脚本 (低优先级)
创建定期解密冒烟脚本:

**文件**: `scripts/remote-credential-smoke.sh`

**内容**:
```bash
#!/usr/bin/env bash
# 远端凭据解密冒烟(154/245 例行巡检)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SMOKE_PY="$(mktemp).py"

cat > "$SMOKE_PY" <<'PY'
# [省略,审计报告中的 decrypt_smoke.py 内容]
PY

echo "=== 154 (47.97.111.154:8782) ==="
ssh -o BatchMode=yes -p 25022 root@47.97.111.154 \
  "ENV_FILE=/etc/llm-gateway-go/env BASE=http://127.0.0.1:8782 python3 -" < "$SMOKE_PY"

echo
echo "=== 245 (8.136.114.245:8781) ==="
ssh -o BatchMode=yes -p 25022 root@8.136.114.245 \
  "ENV_FILE=/opt/llm-gateway-go/.env BASE=http://127.0.0.1:8781 python3 -" < "$SMOKE_PY"

rm -f "$SMOKE_PY"
```

**用法**: 部署后或每周运行一次，验证解密状态。

---

### 任务 4: .env.local 模板完善 (低优先级)
当前 `.env.local.example` 已补充密钥说明，但可以进一步完善：
1. 增加"从 245 同步密钥"的具体命令示例
2. 增加"首次全新安装"与"同步现有 DB"两种场景的分支说明
3. 增加 `LLM_GATEWAY_DECRYPT_SMOKE_PROVIDER_ID` 的用途说明

---

## 执行建议

### 并行执行(推荐)
可以启动两个子代理并行处理：
- **Agent A**: 任务 1(worktree 清理) + 任务 3(巡检脚本)
- **Agent B**: 任务 2(文档更新) + 任务 4(.env.local 模板完善)

### 顺序执行
如果用户只关心任务 1(高优先级),可以单独处理，其他任务留给后续会话。

---

## 提示词模板

### Agent A 提示词:
```
任务：清理 llm-gateway-go 旧 worktree 并创建远端巡检脚本

背景：2026-09-05 凭据解密事故已修复(commit 0145f46d0)。以下 4 个 worktree 脚本还没有 .env.local 导入逻辑，可能复现事故：
- llm-gateway-go-deploy-blue-green
- llm-gateway-go-fix-v1-legacy  
- llm-gateway-go-integration
- llm-gateway-go-node-state-sync

步骤：
1. 列出这 4 个 worktree 的分支/commit/最后修改时间
2. 询问用户是否还需要(如果超过 7 天未修改且不在活跃分支，建议删除)
3. 对于要删除的，执行 `git worktree remove <name>`
4. 创建 `scripts/remote-credential-smoke.sh` 脚本(内容见 handoff 文档任务 3)
5. 测试脚本能否正常连接 154/245

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
```

### Agent B 提示词:
```
任务：补充 llm-gateway 部署文档和故障排查指南

背景：2026-09-05 provider 587 凭据解密事故已修复。需要将经验固化到文档。

步骤：
1. 检查 docs/deployment/ 是否存在 local-deployment-guide.md，不存在则新建
2. 补充内容：.env.local 必备性、密钥固定原则、从 245 同步密钥步骤
3. 检查 docs/troubleshooting/ 是否存在 credential-decrypt-failed.md，不存在则新建  
4. 补充本次事故案例：症状、诊断步骤(keyring=false 日志、admin API)、修复流程
5. 完善 .env.local.example：增加"从 245 同步密钥"命令示例、两种场景分支说明
6. 提交文档更新(单独 commit)

参考：/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/AUDIT_CREDENTIAL_DECRYPT_FIX_20260905.md

工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
```

---

## 当前工作区状态
- 分支: main
- 与 origin/main 一致
- 未跟踪文件: branch-cleanup-report.md, cleanup-branches.sh, cleanup-remote-branches.sh, git-best-practices.md
- 修复提交: 0145f46d0 (已推送)
- 审计报告: AUDIT_CREDENTIAL_DECRYPT_FIX_20260905.md (未提交)
