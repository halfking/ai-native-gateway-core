# Security Policy — SI-LLM-Gateway

## 支持的版本

下表列出了 SI-LLM-Gateway 各个版本的安全更新支持情况：

| 版本 | 支持状态 |
|------|----------|
| `main` 分支最新 | ✅ 活跃支持 |
| 最近 3 个 minor release | ✅ 接受安全修复 |
| 更早版本 | ❌ 不再支持 |

---

## 漏洞报告

我们重视安全问题。**请勿**在 GitHub Issues 中公开披露安全漏洞。

### 私密报告渠道

📧 **邮箱**：security@internal.example.com（推荐中文/英文均可）

请在邮件中包含：

1. **漏洞描述**：简要说明问题
2. **复现步骤**：详细的复现命令 / 请求 / 响应
3. **影响范围**：哪些模块/版本受影响
4. **严重度自评**（参考 [CVSS 3.1](https://www.first.org/cvss/calculator/3.1)）
5. **披露计划**（如有）：你打算何时公开

### 响应承诺

| 阶段 | 时间 |
|------|------|
| 初步确认 | 收到报告后 3 个工作日内 |
| 影响评估 | 5 个工作日内 |
| 修复 / 缓解措施 | 视严重度：<br>🔴 严重 (RCE/Auth bypass) — 24-72h<br>🟡 中等 — 14 天<br>🟢 轻微 — 下个 release |
| 公开致谢 | 修复发布后（如果你同意） |

---

## 公开致谢政策

- 修复发布后，我们会在 [`SECURITY_ACKNOWLEDGMENTS.md`](SECURITY_ACKNOWLEDGMENTS.md) 中致谢报告者（除非你选择匿名）
- 严重漏洞可酌情给予 bounty / swag（视公司政策）

---

## 已知的安全注意事项

### 已主动脱敏的领域

参见 [`docs/REPO-MIRROR-POLICY.md`](docs/REPO-MIRROR-POLICY.md) 了解
本仓库如何处理 codeup (内部) ⇄ github (公开) 双仓库同步时的敏感信息保护。

### 法务白名单

[`docs/legal/disguise-compliance.md`](docs/legal/disguise-compliance.md) —
`disguise/` 模块的请求伪装 (UA / TLS ClientHello rotation) 功能的法律合规说明。
该功能**默认关闭**，需要法务审批后才可启用。

### 不安全的功能默认关闭

- **disguise/ 请求伪装** — 默认 `LLM_GATEWAY_DISGUISE_DISABLE=true`
- **凭据自动重试** — 仅在凭证白名单内生效
- **A2A 跨 Agent 调用** — 当前未上线（Q1 2027）

---

## 部署时的安全检查清单

部署到生产前请确认：

- [ ] 所有 `.env` / `secrets.json` / `*.pem` 已在 `.gitignore` 内
- [ ] 数据库密码已用 Fernet 加密（不要明文存在 DB）
- [ ] `LLM_GATEWAY_ADMIN_TOKEN` 已设置为强随机值
- [ ] 启用 HTTPS（k3s ingress 用 cert-manager）
- [ ] 启用 audit pipeline (不要禁用 DLQ)
- [ ] 凭据轮换 cron 已配置
- [ ] SIEM 推送已配置（v3.2 之后）
- [ ] 已复跑 [`SECURITY-AUDIT-2026-06-28.md`](SECURITY-AUDIT-2026-06-28.md) §7 附录的 11 条检测命令，未命中新增问题（特别是 NET-001/NET-005/NET-007/NET-008 这 4 项易回归）

---

## 第三方依赖

定期使用以下工具扫描依赖漏洞：

```bash
go list -m -u -mod=mod all  # 检查 Go 依赖更新
go list -m -json -u all | jq '.Path + " " + .Version'  # 详细
```

严重 CVE 我们会在 minor release 中修复。

---

## 已发布的安全审计

按发布时间倒序，所有审计文档请见 [`docs/SECURITY-AUDIT-INDEX.md`](docs/SECURITY-AUDIT-INDEX.md) 索引。

最新：

- **2026-06-28 — [网络/传输层审计](SECURITY-AUDIT-2026-06-28.md)**：11 项原审计发现 + 2 项 WIP 构建断裂（NET-012/013）= 13 项。**v1.4 已修复 9/13**（NET-001/002/003/004/005/007/008/010/011），2/13 临时绕过（NET-012/013 待完整 PR），2/13 未修复（NET-006 v1 端 + NET-009 TLS）。建议每季度 + 每次发布前复跑文末 §7 一键检测脚本。

---

## 联系方式

- 安全邮箱：security@internal.example.com
- 项目仓库：https://github.com/halfking/SI-LLM-Gateway
- 主仓库：https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go
