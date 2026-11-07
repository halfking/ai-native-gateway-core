# Bank Security Profile — 任务看板

> 配套方案：`docs/audit/2026-11-06-bank-security-profile-optimization.md`  
> 更新日期：2026-11-06  
> 用法：可直接拆成 Codeup Issue；状态列自行维护（Todo / Doing / Done）

## P0（Phase 1 演示阻断项）

| ID | 标题 | 状态 | 依赖 | 验收要点 |
|----|------|------|------|----------|
| T-P0-01 | 新增 `ops_readonly` 角色与权限矩阵 | Todo | — | ops 不可读会话正文；指标可读 |
| T-P0-02 | 会话正文字段级加密 / 加密工作区 | Todo | T-P0-01 | DB 密文；授权可读；ops 不可解 |
| T-P0-03 | 审计 / 管理接口默认脱敏摘要 | Todo | T-P0-01 | 默认无完整 prompt/completion |
| T-P0-04 | 模型目录 `residency` 标签 | Todo | — | unknown 在 Bank profile 禁路由 |
| T-P0-05 | 境外调用 DLP + 审批 / 白名单闸 | Todo | T-P0-04 | 敏感样本出境被拦且有审计 |
| T-P0-06 | Armor / Guardian Bank profile enforce | Todo | Profile 骨架 | enforce 可阻断；observe 仅审计 |
| T-P0-07 | Bank Security Profile 配置开关与运维文档 | Todo | 并行 | 开关可回滚；回归清单通过 |

## P1（融联联调与规模化）

| ID | 标题 | 状态 | 依赖 | 验收要点 |
|----|------|------|------|----------|
| T-P1-01 | 密钥外置 HSM/KMS Provider | Todo | 融联规格 | backend 可切换；轮换演练 |
| T-P1-02 | 管理面与数据面密钥分离 | Todo | T-P1-01, T-P0-02 | 运维身份不可解业务密钥 |
| T-P1-03 | 数据分级引擎（敏感/重要/核心） | Todo | T-P0-05 | 分级驱动路由与 TTL |
| T-P1-04 | Agent 一等公民身份 | Todo | identity | 独立主体审计；可吊销 |
| T-P1-05 | MCP / 工具 ACL + 高危确认 | Todo | T-P1-04 | 未授权 403；高危待确认 |
| T-P1-06 | 模型准入目录与熔断 | Todo | T-P0-04 | 未准入不可路由；可熔断 |

## P2（过审与运营）

| ID | 标题 | 状态 | 依赖 | 验收要点 |
|----|------|------|------|----------|
| T-P2-01 | 审计导出 SIEM（CEF/Syslog/Kafka） | Todo | T-P0-03 | collector 收齐脱敏事件 |
| T-P2-02 | 留存策略对齐银行年限 | Todo | T-P1-03 | 敏感≥1年、核心≥3年 |
| T-P2-03 | 异常调用告警加强 | Todo | 可选 T-P2-01 | 故障注入可告警 |
| T-P2-04 | 过审材料包（架构/密钥/角色/Runbook） | Todo | Phase1–2 | 内部预审无结构性缺口 |

## 建议迭代顺序

1. T-P0-07 骨架 + T-P0-01 + T-P0-04  
2. T-P0-06 + T-P0-05  
3. T-P0-02 + T-P0-03 → Phase 1 演示  
4. T-P1-* 按融联窗口  
5. T-P2-* 并行收尾  
