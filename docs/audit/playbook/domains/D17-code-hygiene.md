# D17 — 代码卫生与冗余治理

> 领域编号: D17 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1 ｜ 恒查域（每轮必审，轻量）

## 1. 领域边界

**管**：冗余流程与代码的标注/隔离/迁移准备——死代码、零调用接缝、重复实现、误导性注释、遗留 TODO 的诚实化；为后续清理建立"待清清单"。
**不管**：任何功能正确性（各域）；清理动作本身在本域只做"标注或移文件"，删除性重构须单独立项。

## 2. 参考基线

- 历史模式：R30 P3 批（restorePulledCurrency 死代码移除、误导注释更正）、R31（CloseProbe/ProbeCheck 死接缝删除）
- `docs/03-design/01-architecture/domains/DOMAIN_REGISTRY.md` — 领域注册表（归属判断）

## 3. 检查清单

1. **零调用接缝**：窗口内改动暴露出的导出函数/端点/表列若无生产调用方——标注 `// DEAD-CODE(<轮次>): <理由>` 或直接删除（低风险时）。
2. **重复实现**：窗口内新代码与既有工具函数重复（复制粘贴型）——改为复用或在两处标注指向。
3. **注释诚实**：窗口内 diff 中被行为变更波及的旧注释/日志文案同步更正（"exponential 不实"教训）；文档与代码矛盾以代码为准并回改文档。
4. **待清清单**：不适合本轮清理的，登记到轮文档 §遗留（带 file:line），格式与 R30 一致；下轮从这里捞。
5. **文件归位**：明显放错位置的代码（如领域逻辑滞留在 transport 层）移到正确文件并在 CHANGELOG 提及，不做大开大合重构。

## 4. 历史回归点（轮末回注区）

- [R30] restorePulledCurrency 死代码移除 + 注释如实化；709 down 注释错字；freediscovery README 与代码矛盾回改 —— 模式基准
- [R31] Manager.ProbeCheck/CloseProbe 零调用死接缝删除（防 billing-blind 复活）—— 模式基准
- [R37] 测试自身可成为跨测试毒源：TestOutcomeBackfill_RespectsWriteBound 占坑 goroutine 不释放槽位→32 槽永久泄漏→全包后续反馈写静默 shed（dropped 恒定+4、新测试超时假失败）——占资源类测试必须对称释放；恒真测试（TestDefaultDispatchFollowUpHitsLiveServer 手工造请求自证）与命名误导（stub 冒充 production dispatcher）已改写；gofmt 存量 272 文件待按包机械批单独 commit

## 5. 子代理派发提示词

```text
你是 D17（代码卫生与冗余治理）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D17-code-hygiene.md 全文。
第二步：以窗口改动面为主扫描死代码/重复实现/误导注释/零调用接缝，按域文档 §3 清单核对。审计窗口：<窗口>。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line；建议只到"标注/迁移"粒度。
```
