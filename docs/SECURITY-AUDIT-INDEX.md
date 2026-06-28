# Security Audit Index — llm-gateway-go

> 索引本仓库所有安全审计文档，按日期倒序。每份审计应保留独立文件，互不覆盖。

---

## 已发布

| 文档 ID | 日期 | 主题 | 范围 | 严重度体系 | 状态 |
|---------|------|------|------|------------|------|
| [`SECURITY-AUDIT-2026-06-28`](../SECURITY-AUDIT-2026-06-28.md) | 2026-06-28 | 网络/传输层 | HTTP/CORS/Headers/暴露面/Slowloris/XFF | P0/P1/P2 | **11 项原审计 + 2 项 WIP build break = 13 项；9 项已修复 + 2 项临时绕过 + 2 项未修复（v1.4）** |
| `DEEP-AUDIT-2026-05-26.md` | 2026-05-26 | 数据面 18 个源文件 | 熔断、审计、shutdown、body cap、panic 恢复 | P0/P1/P2/P3 | 已审计 |
| `docs/comprehensive-code-audit-2026-06-20.md` | 2026-06-20 | 全量代码审计 | 错误路径 request-log、限速 peek 等 | CRITICAL 标签 | 已审计 |
| `docs/REPO-MIRROR-POLICY.md` | 持续 | 仓库镜像策略 | secret 扫描 + RFC1918 + 内部域名 | BLOCK/WARN/INFO | 持续维护 |

## 计划中（待起）

| 主题 | 计划文件 | 关键风险点 |
|------|----------|------------|
| 加密 / JWT / Fernet | `SECURITY-AUDIT-2026-07-XX-crypto.md` | admin 密码默认 `"admin"`、JWT secret fallback `"default-jwt-secret-change-me"`、`alg` 校验不完整、Fernet 时间戳未校验导致 replay |
| SQL 注入 | `SECURITY-AUDIT-2026-07-XX-sqli.md` | 7 个 `fmt.Sprintf` SQL 候选点 + 17 处弱 LIKE/通配构造 |
| 依赖 CVE | `SECURITY-AUDIT-2026-07-XX-deps.md` | 96 个直接依赖需 govulncheck 扫描 |
| 容器/部署 | `SECURITY-AUDIT-2026-07-XX-container.md` | Dockerfile 基镜像未 digest 固定、无 HEALTHCHECK、docker-compose 明文 DB 凭据 |
| supply chain | `SECURITY-AUDIT-2026-07-XX-supply-chain.md` | 无 SBOM、GOPROXY 单点、无 image signing |

---

## 命名约定

- 顶层审计：`SECURITY-AUDIT-<YYYY-MM-DD>.md`（仅在范围广、跨多领域时使用）
- 分类审计：`docs/<topic>-audit-<YYYY-MM-DD>.md`
- 单点修复报告：`docs/audit/<YYYY-MM-DD>-<topic>.md`

---

## 审计提交 Checklist（创建新审计时遵循）

- [ ] 文件名遵循上述约定
- [ ] 元信息表（日期/范围/作者/状态）
- [ ] 执行摘要（风险等级分布表）
- [ ] 范围与非范围（明确写出"未覆盖"以避免误读）
- [ ] 每项发现固定结构：ID · 等级 · 位置 · 风险 · 测试命令 · 验证结果 · 修复方案 · 验证状态 · 跟踪链接
- [ ] 至少 1 个可一键复制的检测命令（§ 附录）
- [ ] 引用本索引 + 关联已发布审计