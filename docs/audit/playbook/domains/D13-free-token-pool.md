# D13 — 免费 token 资源池（扫描/注册/聚合）

> 领域编号: D13 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：免费 token 资源的自动扫描（freediscovery）、注册、聚合使用——作为**标准供应商池**的接入与治理；免费凭据的健康反馈、自动禁用/复活；与 auto 路由（D11）的接入面。
**不管**：auto 消费侧决策（D11）；熔断画像（D08）；凭据详情错误展示（D08）。

## 2. 参考基线

设计文档：
- `docs/freediscovery-requirements.md` — 需求与系统架构（migration 084、API、界面）
- `docs/freediscovery-configuration.md`、`docs/freediscovery-testing-runbook.md`
- `docs/auto-model-optimization/06-free-llm-integration.md` — 免费 LLM 接入 auto
- 历史审计：`docs/audit/2026-09-1*-rN-freediscovery-*-audit.md` 系列、`docs/audit/2026-09-15-freediscovery-orbi-gap-analysis.md`

代码入口：
- `domains/freediscovery/`（scan_scheduler、template_manager 等）、`domains/freeresource/`、`discovery/`
- 免费档熔断/画像：breaker free profile（与 D08 交界）

## 3. 检查清单

1. **池子标准化**：免费凭据进入标准供应商池后与付费凭据走同一凭据生命周期（发现→注册→健康→熔断→复活/淘汰），无平行私有通道。
2. **健康反馈链路**：扫描成功/失败 UPDATE 走特权事务（RLS 旁路守卫，runPrivileged 基准）——非 default 租户健康反馈必须生效；"连续 N 次失败自动禁用"对全部租户可用。
3. **template_manager 更新语义**：动态 SET（只写涉及字段），GET 快照与 UPDATE 之间不被调度器写入覆盖；自动禁用不被旧快照复活（手动启用=健康三列清零的 README 语义与代码一致）。
4. **注册幂等**：同源重复扫描/注册不产生重复凭据或重复计费位。
5. **聚合使用**：免费额度聚合（多账号额度汇总）口径正确；额度耗尽 → 熔断/轮换而非持续打 429。
6. **auto 接入**：免费池凭据可被 auto 路由选中（免费档 profile 生效），免费容量优化路径（NVIDIA 404 分类等）不回退。

## 4. 历史回归点（轮末回注区）

- [R30] 健康反馈绕过 RLS（裸连接池写 provider_templates，非 default 租户永不生效）— 修复 501ef519d；runPrivileged 特权事务
- [R30] template_manager.Update 全字段回写丢更新（自动禁用被静默复活）— 修复 501ef519d；动态 SET + README 对齐
- [R30] NVIDIA Function 404 分类修复 + 免费档熔断冷却 + 噪音治理 — 8f00bde9c
- [运维] freediscovery Preset 状态诚实标记、手动启用语义以代码为准

## 5. 子代理派发提示词

```text
你是 D13（免费 token 资源池）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D13-free-token-pool.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：窗口内 freediscovery/freerescovery 改动的 RLS 特权写、模板更新语义、注册幂等。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```
