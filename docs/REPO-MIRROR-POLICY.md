# SI-LLM-Gateway 双仓库策略 (REPO-MIRROR-POLICY)

> 版本：v1.0 · 2026-06-25 · 作者：halfking

本文档说明 SI-LLM-Gateway 仓库同时维护 `codeup` (主) + `github` (公开镜像)
两个远端时，如何保证**敏感信息不泄露**到公开仓库。

---

## 1. 仓库布局

| Remote | URL | 角色 | 推送方式 | 扫描器 |
|--------|-----|------|----------|--------|
| `codeup` (origin) | `https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git` | 主仓库（日常开发） | `git push` | 无 |
| `github` | `git@github.com:halfking/SI-LLM-Gateway.git` | 公开镜像（阶段发布） | `git push github` | **严格模式** |

**默认 push 永远走 codeup**。github 推送必须显式调用 `git push github`，
此时 `.githooks/pre-push` 自动运行严格扫描，命中即 `exit 1` 阻断。

---

## 2. 敏感信息扫描器

### 2.1 工具集

| 文件 | 用途 |
|------|------|
| `scripts/scan-secrets.sh` | 主扫描器（POSIX ERE，零外部依赖） |
| `scripts/scan-secrets.config` | 49+ 扫描规则（分类/严重度/正则） |
| `scripts/scan-secrets.baseline` | 已知误报清单（test fixture 占位符等） |
| `scripts/scan-secrets.replacements` | `git filter-repo` 历史重写表 |
| `.githooks/pre-push` | Git hook：仅 github 推送时触发 |
| `scripts/sync-to-github.sh` | 一键推 github（带 dry-run 选项） |

### 2.2 严重度分级

| 严重度 | 含义 | strict 模式 | normal 模式 |
|--------|------|-------------|-------------|
| **BLOCK** | 真实凭据 / 私钥 / 内部 IP / 内部域名 | **阻断** | 阻断 |
| **WARN** | 拓扑暴露 / 凭据模式（可能是 test fixture） | 阻断 | 警告 |
| **INFO** | 提示性 | 阻断 | 警告 |

### 2.3 扫描规则分类

| 类别 | 示例 |
|------|------|
| **LLM API Key** | `sk-…` (OpenAI), `sk-ant-…` (Anthropic), `AIza…` (Google), `sk_live_…` (Stripe) |
| **GitHub Token** | `ghp_/gho_/ghu_/ghs_/ghr_/github_pat_` 前缀 |
| **AWS Key** | `AKIA[0-9A-Z]{16}` |
| **私钥** | `-----BEGIN ... PRIVATE KEY-----` |
| **DB 连接串** | `postgres://<USER>/<PASSWORD>@<HOST>` (带密码字段) |
| **SSH 密码** | `K8S_SSH_PASSWORD=...` 含字面量赋值 |
| **内部 IP** | RFC1918 段 (10.x / 172.16-31.x / 192.168.x) |
| **内部域名** | 公司内部 `<corp>.internal.example.com` 段 |
| **已知泄露** | 之前从仓库提取的真实凭据字面量（黑名单） |
| **敏感文件** | `.env*` / `*.pem` / `*.key` / `*.kubeconfig` |

### 2.4 使用方式

```bash
# 全工作树扫描 (默认 normal 模式)
./scripts/scan-secrets.sh

# 严格模式 (github 推送时)
./scripts/scan-secrets.sh --mode=strict

# 仅扫描 git tracked 文件 (推送前预演)
./scripts/scan-secrets.sh --mode=strict --tracked-only

# 带 baseline (白名单已知误报)
./scripts/scan-secrets.sh --mode=strict --baseline=scripts/scan-secrets.baseline

# 限定路径
./scripts/scan-secrets.sh --paths relay/admin/routing

# 机器可读 (JSON)
./scripts/scan-secrets.sh --mode=strict --format=json
```

### 2.5 退出码

| 退出码 | 含义 |
|--------|------|
| 0 | CLEAN (无命中) 或 仅 WARN (normal/permissive 模式) |
| 1 | BLOCK 命中 (strict 模式必阻) |
| 2 | 用法错误 |

---

## 3. 推送工作流

### 3.1 日常开发 (推 codeup)

```bash
git push    # 默认走 codeup，无附加检查
```

### 3.2 阶段发布 (推 github)

**方式 A：自动扫描（推荐）**

```bash
# 直接 push，pre-push hook 自动扫描
git push github
#   ↓
# 🔍 GitHub push detected — running sensitive-info scan (strict mode)
# ... scan output ...
# ❌ 命中 → exit 1 → 推送被阻断
# ✅ 通过 → push 成功
```

**方式 B：一键脚本（带 dry-run）**

```bash
# 1) 预览：扫描 + filter-repo + dry-run
./scripts/sync-to-github.sh --dry-run

# 2) 实际推送（会询问确认）
./scripts/sync-to-github.sh
#   Type 'yes' to confirm: yes
```

**方式 C：紧急绕过（不推荐）**

```bash
# 跳过 pre-push hook（仅紧急情况，需人工 review 后果）
git push --no-verify github
```

### 3.3 首次推 github 的特殊流程

首次推 github 时，**工作树已脱敏**（本次 scrub 完成），
但 git history 仍包含敏感信息（之前 commit 进来的）。此时：

1. 在本地创建 mirror：`git clone --mirror --no-hardlinks . /tmp/llmgw-github-mirror.git`
2. 用 `git filter-repo` 重写历史：`git filter-repo --force --replace-text scripts/scan-secrets.replacements`
3. 验证 mirror 干净：`./scripts/scan-secrets.sh --mode=strict --paths=/tmp/llmgw-verify`
4. 推送：`git push --mirror git@github.com:halfking/SI-LLM-Gateway.git`

> 注：codeup 端的历史**不会**被重写。GitHub 是独立的历史。

---

## 4. 历史重写规则 (`scan-secrets.replacements`)

| 模式类别 | 替换为 |
|------|--------|
| 已知 DB 密码 | `__REDACTED_DB_PASSWORD__` |
| 已知 SSH 密码 | `__REDACTED_SSH_PASSWORD__` |
| 已知第三方 API key | `__REDACTED_<VENDOR>_API_KEY__` |
| 已知公网 IP（含内网 NAT） | `__INTERNAL_PUBLIC_IP__` |
| K8s host IP (10.43.62.x 段) | `__INTERNAL_K8S_HOST__` |
| Docker host IP (10.43.61.x 段) | `__INTERNAL_DOCKER_HOST__` |
| K8s 节点 IP (172.31.x 段) | `__INTERNAL_K8S_HOST__` |
| 内部镜像仓库 (registry.<corp>.cn) | `registry.<corp>.internal.example.com` |
| 业务主域名 (<svc>.<corp>.cn) | `<svc>.internal.example.com` |
| 通用内部域 (含 `.cn` / `.com`) | `internal.example.com` |

> 注：具体替换表见 `scripts/scan-secrets.replacements` (生产环境内网 IP / 域名)。

---

## 5. 白名单 (`scan-secrets.baseline`)

针对测试 fixture 中的合法占位符（合成占位符 API key、合成 RFC5737 IP 等），
使用 baseline 文件标注为已知误报。

格式：`file:line:category`（每行一条，`#` 开头为注释）

```bash
admin/auth_middleware_test.go:10:CRED_ASSIGN
admin/superadmin_test.go:170:INTERNAL_IP
...
```

**白名单 vs 真实泄露的判断标准**：
- ✅ 可白名单：测试 fixture 中用 RFC5737 TEST-NET-1 (`192.0.2.x`)，或显式 placeholder (如 `test-key`, `mock-...`)
- ❌ 不可白名单：生产凭据 (即使已脱敏为 `__REDACTED__`，原始字面量也不能入 baseline)

---

## 6. 应急流程

### 6.1 误报（扫描器报但确实是安全的）

```bash
# 1) 确认安全
./scripts/scan-secrets.sh --mode=strict --format=json  # 查看具体命中

# 2) 加入 baseline
echo "path/to/file.go:123:CRED_ASSIGN" >> scripts/scan-secrets.baseline

# 3) 重新 push
git push github
```

### 6.2 真实泄露（commit 后才发现）

```bash
# 1) 立即轮换泄露的凭据（在 codeup / 生产环境）

# 2) 从 git history 清除（使用 BFG 或 git filter-repo）
cd /tmp/llmgw-github-mirror.git
git filter-repo --force --replace-text scripts/scan-secrets.replacements
git push --force github

# 3) 考虑 GitHub 端的 secret scanning 通知
```

### 6.3 pre-push hook 失效

如果 `.githooks/pre-push` 没生效：
```bash
# 重新安装
./scripts/install-githooks.sh

# 验证
git config core.hooksPath  # 应输出 .githooks
```

---

## 7. 责任人与联系方式

- 仓库所有者：halfking (`git@github.com:halfking`)
- 安全问题：见 [`SECURITY.md`](SECURITY.md)
- 紧急联系：内部 IM

---

## 8. 更新日志

| 版本 | 日期 | 变更 |
|------|------|------|
| v1.0 | 2026-06-25 | 初版：双仓库策略 + 49 规则扫描器 + filter-repo 历史重写 |
