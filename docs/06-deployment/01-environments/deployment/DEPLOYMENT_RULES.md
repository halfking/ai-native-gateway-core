# LLM Gateway 部署强制规则

> 最后更新：2026-07-16
> 基于：245/154 部署事故经验总结

---

## 🚨 部署红线（必须遵守）

### 红线 1：禁止手动部署

**❌ 禁止操作**：
```bash
# 禁止手动编译 + scp
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/gateway ./cmd/gateway
scp /tmp/gateway root@245:/opt/llm-gateway-go/gateway
systemctl restart llm-gateway-go

# 问题：
# 1. 未更新 version.json 和 build_seq
# 2. 未构建前端（web/ 目录丢失）
# 3. 未执行 DB 迁移
# 4. 未原子切换（破坏性替换）
# 5. 无回滚能力
```

**✅ 正确操作**：
```bash
# 245 预发布环境
bash scripts/deploy-245.sh

# 154 生产环境（必须先过 245 验证）
bash scripts/deploy-154.sh

# 选项：
bash scripts/deploy-245.sh --seq 1114        # 指定 build_seq
bash scripts/deploy-245.sh --no-frontend     # 仅后端（紧急回滚用）
```

---

### 红线 2：版本号单源真理（version.json）

**SSOT（Single Source of Truth）**：
```json
{
  "version": "2.4.6-a2cdc398-20260716-1115",
  "git_tag": "2.4.6",
  "git_sha": "a2cdc398",
  "build_seq": 1115,
  "build_date": "20260716",
  "module": "llm-gateway-go"
}
```

**自动同步的 4 个文件**（由 `bump-version.sh` 管理）：
1. `version.json` — 后端 SSOT
2. `VERSION` — 兼容旧 binary
3. `web/public/version.json` — 前端读取
4. `docs/db-changelog.md` — 部署日志

**❌ 禁止操作**：
- 手动修改 `version.json`
- 手动修改 `build_seq`
- 跳过 `bump-version.sh`

**✅ 正确操作**：
```bash
# 自动 +1（deploy-*.sh 自动调用）
bash scripts/bump-version.sh

# 手动指定（特殊场景）
bash scripts/bump-version.sh --seq 1120
```

---

### 红线 3：前端不是可选项

**❌ 错误认知**：
- "我只改了后端，不用部署前端"
- "前端可以单独更新"
- "手动 scp gateway 就行"

**✅ 正确认知**：
- 前后端是**一个 bundle**（`releases/<seq>-<sha>/`）
- `deploy-seamless.sh` 默认**同时构建前后端**
- 原子符号链接切换：`gateway + web/ + version.json` 三者同步

**前端构建流程**（自动）：
```bash
# deploy-seamless.sh 自动执行：
cd web && npm run build            # 生成 web/dist/
host_stage_release ... "web/dist"  # 打包到 bundle/web/
upload_release                     # rsync 到远程
ln -sfn releases/<seq> current     # 原子切换
```

**唯一例外**：`--no-frontend` 标志
- 仅用于**紧急回滚后端 bug**
- 复用上一个 bundle 的 `web/` 目录
- 生产环境禁止使用（除非 P0 事故）

---

### 红线 4：245 是 154 的必经门禁

**晋级流程**（严格顺序）：
```
本地开发
   ↓
245 预发布（llmgo.kxpms.cn）
   ↓
完整验证（L1-L4）
   ↓
154 生产（llm.kxpms.cn）
```

**245 验证清单**：
```bash
# 部署到 245
bash scripts/deploy-245.sh

# L1: HTTP 存活
curl https://llmgo.kxpms.cn/healthz

# L2: 依赖连通
curl https://llmgo.kxpms.cn/api/system/version

# L3: 功能链路
bash scripts/ops/verify-245-full.sh

# L4: 业务真实（手动验证）
# - 登录界面
# - 创建 provider
# - 发送测试请求
# - 查看监控面板
```

**❌ 禁止操作**：
- 跳过 245 直接部署 154
- 245 验证失败仍部署 154
- 245 和 154 版本不连续（build_seq 必须递增）

---

## 📋 标准部署流程

### 流程 A：常规部署（推荐）

```bash
# Step 1: 本地开发完成，测试通过
go test ./...
cd web && npm run build && cd ..

# Step 2: 提交代码
git add .
git commit -m "feat: xxx"
git push origin main

# Step 3: 部署到 245
bash scripts/deploy-245.sh
# 输出：✅ 245 部署完成 (总 61s, 切换 40s) — version=1114-77bbf2e9 seq=1114

# Step 4: 验证 245
bash scripts/ops/verify-245-full.sh
curl https://llmgo.kxpms.cn/api/system/version

# Step 5: 通过后部署到 154
bash scripts/deploy-154.sh
# 输出：✅ 154 部署完成 (总 113s, 切换 90s) — version=1115-a2cdc398 seq=1115

# Step 6: 验证 154
curl https://llm.kxpms.cn/api/system/version
curl https://llm.kxpms.cn/healthz
```

### 流程 B：回滚

```bash
# 查看可回滚版本
bash scripts/deploy-seamless.sh status 245
bash scripts/deploy-seamless.sh status 154

# 回滚到上一个 verified 版本
bash scripts/deploy-seamless.sh rollback 245
bash scripts/deploy-seamless.sh rollback 154

# 验证回滚成功
systemctl status llm-gateway-go
curl http://localhost:8781/api/system/version
```

---

## 🔍 常见问题

### Q1: 为什么 web/ 目录丢失了？

**根本原因**：绕过了 `deploy-seamless.sh`，手动 scp 只上传了 `gateway` 二进制。

**正确流程**：
```bash
# deploy-seamless.sh 自动做的事：
1. npm run build（生成 web/dist/）
2. host_stage_release（打包 gateway + web/ + version.json）
3. upload_release（rsync 整个 bundle）
4. 原子切换符号链接
```

**临时修复**：
```bash
# 从上一个 bundle 复制 web/
ssh root@245 'cp -r /opt/llm-gateway-go/releases/<prev-seq>/web \
  /opt/llm-gateway-go/releases/<current-seq>/ && \
  systemctl restart llm-gateway-go'
```

**永久修复**：使用 `deploy-245.sh` 重新部署。

---

### Q2: 版本号全错了怎么办？

**表现**：
- `build_seq` 未递增
- `git_sha` 是旧的
- `version.json` 和 `VERSION` 不一致

**根本原因**：跳过了 `bump-version.sh`。

**修复**：
```bash
# 手动 bump 版本
bash scripts/bump-version.sh --seq <下一个序号>

# 提交版本文件
git add version.json VERSION web/public/version.json docs/db-changelog.md
git commit -m "chore: bump version to <seq>"
git push origin main

# 重新部署
bash scripts/deploy-245.sh
bash scripts/deploy-154.sh
```

---

### Q3: 245 和 154 的 build_seq 差距很大正常吗？

**正常情况**：
- 245: 1114
- 154: 1115
- 差距：1（每次 245 验证通过后立即晋级 154）

**异常情况**：
- 245: 1114
- 154: 1102
- 差距：12（说明 245 部署了 12 次但未晋级 154）

**原因**：
- 245 验证失败，多次修复重试
- 开发分支在 245 反复测试
- 忘记晋级 154

**处理**：属于正常现象，只要最终 154 > 245 即可。

---

## 📝 部署检查清单

### 部署前（Pre-deployment）

- [ ] 代码已提交并推送到 `origin/main`
- [ ] 本地测试通过（`go test ./...`）
- [ ] 前端构建无报错（`cd web && npm run build`）
- [ ] 无明文凭据泄露（`git grep -E 'password.*=.*[^<]'`）
- [ ] DB 迁移脚本已准备（如有）

### 部署中（During deployment）

- [ ] 使用 `scripts/deploy-245.sh` 或 `scripts/deploy-154.sh`
- [ ] 观察部署输出，确认无报错
- [ ] 确认 `build_seq` 递增
- [ ] 确认 `git_sha` 是最新 commit

### 部署后（Post-deployment）

- [ ] `/healthz` 返回 200
- [ ] `/api/system/version` 返回正确版本
- [ ] 前端可访问（https://llmgo.kxpms.cn/ 或 https://llm.kxpms.cn/）
- [ ] 登录功能正常
- [ ] 关键业务流程验证（创建 provider、发送请求）
- [ ] 监控面板无异常

---

## 🛠️ 工具速查

| 命令 | 用途 |
|------|------|
| `bash scripts/deploy-245.sh` | 部署到 245 预发布 |
| `bash scripts/deploy-154.sh` | 部署到 154 生产 |
| `bash scripts/deploy-seamless.sh status 245` | 查看 245 部署状态 |
| `bash scripts/deploy-seamless.sh rollback 245` | 回滚 245 到上一版本 |
| `bash scripts/bump-version.sh` | 手动 bump 版本 |
| `bash scripts/ops/verify-245-full.sh` | 245 完整验证 |

---

## 🎯 事故案例

### 案例 1：手动部署导致前端丢失（2026-07-16）

**现象**：
- 部署后访问 https://llmgo.kxpms.cn/ 返回 404
- `/api/system/version` 正常
- systemd 日志无报错

**根本原因**：
手动 `go build + scp gateway`，未上传 `web/` 目录。

**修复**：
```bash
# 临时：从上一个 bundle 复制 web/
ssh root@245 'cp -r /opt/llm-gateway-go/releases/1113-f3a3ec02/web \
  /opt/llm-gateway-go/releases/1114-77bbf2e9/ && \
  systemctl restart llm-gateway-go'

# 永久：重新正确部署
bash scripts/deploy-245.sh
```

**经验**：
- ❌ 永远不要手动部署
- ✅ 使用 `scripts/deploy-*.sh`
- ✅ 前端是 bundle 的一部分，不可省略

---

### 案例 2：版本号未递增（2026-07-16）

**现象**：
- 部署后 `build_seq` 仍是 1113
- `git_sha` 是旧 commit
- `version.json` 未更新

**根本原因**：
手动部署，未调用 `bump-version.sh`。

**修复**：
```bash
bash scripts/bump-version.sh
git add version.json VERSION web/public/version.json docs/db-changelog.md
git commit -m "chore(deploy): bump version to 1114"
git push origin main
bash scripts/deploy-245.sh
```

**经验**：
- ❌ 不要手动修改版本文件
- ✅ 部署脚本自动调用 `bump-version.sh`
- ✅ `version.json` 是单源真理

---

## 📚 相关文档

- `scripts/deploy-245.sh` — 245 部署脚本
- `scripts/deploy-154.sh` — 154 部署脚本
- `scripts/deploy-seamless.sh` — 原子符号链接无缝部署
- `scripts/deploy-lib/host.sh` — 部署核心函数库
- `scripts/bump-version.sh` — 版本管理脚本
- `scripts/ops/verify-245-full.sh` — 245 完整验证脚本

---

**最后更新**：2026-07-16
**维护者**：Infrastructure Team
**适用版本**：v2.4.6+
