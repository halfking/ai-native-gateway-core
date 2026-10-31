# SI-LLM-Gateway 双仓库策略 (REPO-MIRROR-POLICY)

> 版本：v2.0 · 2026-10-27 · 作者：halfking（v2.0 契约决断见 §10；§1~§9 为 v1.x 存档，
> 与 §10 冲突处以 §10 为准）

本文档说明 SI-LLM-Gateway 仓库同时维护 `codeup` (主) + `github` (公开镜像)
两个远端时，如何保证**敏感信息不泄露**到公开仓库。

---

## 1. 仓库布局

| Remote | URL | 角色 | 推送方式 | 扫描器 |
|--------|-----|------|----------|--------|
| `codeup` (origin) | `__REPO_URL_1__.git` | 主仓库（日常开发） | `git push` | 无 |
| `github` | `git@github.com:halfking/ai-native-gateway-core.git` | 公开镜像（**保留原始历史**，v2.0） | **树移植**（§10） | **严格模式** |

> ★ v2.0 更正：镜像仓库已从 v1.x 的 `SI-LLM-Gateway.git` 变更为
> `ai-native-gateway-core`；其历史是 **2026-05~10 的原始历史**（未经过
> filter-repo 改写），与 codeup 互相独立（merge-base 停在 2026-10-07）。

**默认 push 永远走 codeup**。github 推送必须显式走 §10 的树移植流程；
推送前严格扫描（`scan-secrets.sh --mode=strict --tracked-only`，
**BLOCK 必须 0**）仍然成立——树移植新提交的 tree 就是 codeup 当前树，
工作树扫描即发布内容扫描。

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
| **内部域名** | 公司内部 `<corp>.__DOMAIN_12__` 段 |
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
# 🔍 GitHub push detected — running sensitive-info scan (normal mode)
# ... scan output ...
# ❌ BLOCK 级命中 → exit 1 → 推送被阻断（WARN 级告警放行；STRICT_SCANNER=1 开严格全阻断）
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
| 已知 DB 密码 | `__SECRET_2__` |
| 已知 SSH 密码 | `__SECRET_3__` |
| 已知第三方 API key | `__REDACTED_<VENDOR>_API_KEY__` |
| 已知公网 IP（含内网 NAT） | `__INTERNAL_PUBLIC_IP__` |
| K8s host IP (10.43.62.x 段) | `__INTERNAL_K8S_HOST__` |
| Docker host IP (10.43.61.x 段) | `__INTERNAL_DOCKER_HOST__` |
| K8s 节点 IP (172.31.x 段) | `__INTERNAL_K8S_HOST__` |
| 内部镜像仓库 (registry.<corp>.cn) | `registry.<corp>.__DOMAIN_12__` |
| 业务主域名 (<svc>.<corp>.cn) | `<svc>.__DOMAIN_12__` |
| 通用内部域 (含 `.cn` / `.com`) | `__DOMAIN_12__` |

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
| v1.1 | 2026-10-08 | 首次全量历史改写落地；镜像同步契约改为 sync-to-github.sh；镜像 refs 修剪为 main+tags |

---

## 9. 2026-10-08 起的同步契约：镜像历史已完成首次全量改写

2026-10-08 起，公开镜像历史经 `scripts/sync-to-github.sh`（`git filter-repo`
+ 公开/私有替换表）完成首次全量改写并 force-push：

- 镜像历史中已编目的敏感串（含 108dcd2da 引入的 admin 口令，出现于 5 个
  历史提交与 CHANGELOG/审计文档）全部替换为 `__REDACTED_*__` 形态；镜像
  main 的 commit SHA 与 codeup **从此分叉（设计如此）**。
- 日常 `git push github main` 从此被拒（non-fast-forward）。镜像同步一律走：
  ```bash
  bash scripts/sync-to-github.sh --dry-run   # 先演练
  bash scripts/sync-to-github.sh             # 确认后 force-push
  ```
  脚本从本地原始历史确定性重导出脱敏镜像，可重复执行；替换表不变时产物
  SHA 稳定。
- 镜像只发布 `refs/heads/main` + 发布形态 tags（`[vV]?整数点分段`，如
  `1.0`/`V2.2.9`/`v2.4.1`；`backup/*`、`rollback/*`、`archive/*`、
  `domain-refactor-phase-*` 等内部考古 tag 不发布——2026-10-08 新增
  Step 4.5 修剪）；全量 refs 同步需显式 `SYNC_ALL_BRANCHES=1`，避免并行
  会话的 WIP 分支名 spray 到公开仓库。
- 改写覆盖面（2026-10-08 实测补齐）：blob 内容（含二进制——
  `--replace-text` 会整体跳过非 UTF-8 blob，由生成的 `--blob-callback`
  字节级兜底）、提交/标签消息（`--replace-message`）、树对象里的路径名
  （`--filename-callback`）、作者/提交者 ident（`--commit-callback`）。
  四类机制全部由同一对替换表程序化生成，缺一类就是一类泄漏（首轮
  dry-run 批量验证门实测揪出 415 处残留）。
- 已知残留：GitHub 对 force-push 后的不可达对象会保留一段时间（旧 SHA 的
  raw URL 在 GC 前可能仍可访问）；如需立即清除请联系 GitHub Support。
- 口令轮换建议不变：历史改写只解决"可发现性"，已泄露口令仍应轮换。

---

## 10. 2026-10-27 契约决断：树移植是唯一实推通道（v2.0）

**决断内容**：codeup → github 的实推通道**只有树移植**（commit-tree）；
`sync-to-github.sh`（filter-repo 全量改写）**退役为 `--dry-run` 专用诊断工具**，
两者互斥，不得同用。

### 10.1 为什么树移植赢

- 镜像侧 `ai-native-gateway-core` 的价值主张是**保留 2026-05~10 的原始历史**
  （13000+ 笔）；树移植的父提交 = 镜像原 tip ⇒ 原始历史一条不丢。
- filter-repo 实推 = force-push 重写历史 ⇒ **毁掉上述主张**，且两仓从此
  再次分叉（v1.1 §9 的状态），回滚成本不可逆。
- 树移植验收可证伪：`git rev-parse origin/main^{tree}` == `github/main^{tree}`
  且 `git diff --stat origin/main github/main` 为空；`git ls-remote` 对拍远端真值。
  （2026-10-27 已执行到 992d2f6ee，tree SHA 两仓一致。）

### 10.2 为什么 filter-repo 只许 --dry-run

- 工具本身保留：它是**全库改写完整性的唯一验证器**（blob/message/filename/
  commit 四类机制 + 镜像检出复扫），dry-run 5 步能证明「如果哪天要再全量
  改写，管线是通的」。
- 但**当前不满足全量改写的完整性前提**：`scan-secrets.private.replacements`
  里**退役 252 DB/SSH 口令缺失**——原文只存在于 sops 加密的 `.env.252.enc` /
  `.env.kaixuan-1.enc`，**本机与 252 主机均无 sops/age 私钥**（2026-10-27
  实测 `which sops` 双双为空）⇒ 不可恢复 ⇒ 表头已如实注明。补齐前提：
  **用户侧提供 sops/age 密钥**，从加密文件恢复口令补表后，才重新具备
  「确定性重导出脱敏镜像」的能力。
- 若哪天实推 filter-repo：先 `rm -rf` 掉镜像仓重建（不可与保留历史共存），
  并把本节 §10.1 的镜像价值主张作废——那是另一个决断，需显式重开。

### 10.3 推 github 前的三段门（沿用 AGENTS.md 既有纪律）

1. `scripts/scan-secrets.sh --mode=normal --paths=. --tracked-only
   --baseline=scripts/scan-secrets.baseline` —— **BLOCK 必须 0**；
   （★ 2026-10-27 起 scan-secrets.sh 带 bash≥4 运行时守卫：bash 3.2 下
   `declare -A` 曾静默失败并打印 findings 0 **假绿**，守卫后响亮 exit 64；
   sync-to-github.sh 用 `"$BASH"` 把父解释器传给子进程。）
2. 审计守卫七包 + `bash scripts/checks/guards-sync.sh`；
3. `tests/*.sh` 全量。

### 10.4 版本

| 版本 | 日期 | 变更 |
|------|------|------|
| v2.0 | 2026-10-27 | 契约决断：树移植为唯一实推通道；filter-repo 退役为 dry-run 诊断；镜像仓更名 ai-native-gateway-core；scan-secrets.sh 解释器守卫；sops 口令缺口登记 |
| v1.1 | 2026-10-08 | 首次全量历史改写落地；镜像同步契约改为 sync-to-github.sh（**已被 v2.0 取代**） |
| v1.0 | 2026-06-25 | 初版 |
