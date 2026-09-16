# D15 — 可观测性与前端一致性

> 领域编号: D15 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：流程可观测性（日志/指标/追踪覆盖与统一性）；数据展示统一性（web 端复用同组控件）；菜单组织易用性；交互操作规范一致性（确认弹窗/表格/表单/暗色模式）。
**不管**：统计口径正确性（D10）；灰藏页的路由注册本身（D16 闭环），但"该不该灰藏"归本域。

## 2. 参考基线

设计文档：
- `docs/metrics-catalog.md` — 指标目录（命名/标签规范）
- `docs/03-design/02-feature-design/design/DASHBOARD_V2_TECHNICAL_DESIGN.md` 等三份
- web 端 ui 套件（DataTable/AppModal/confirmDialog——以 web/src 实际布局为准）

代码入口：
- `web/`（Vue 管理台）、`web/public/menu-config.json`（生成物，冲突取远程）
- `telemetry/`、`metrics/`、`monitoring/`、`internal/observability/`、`internal/logging/`

## 3. 检查清单

1. **新增必有观测**：窗口内新增流程/worker/端点有结构化日志与指标（复用 metrics-catalog 命名），不引入私有 printf 风格日志通道。
2. **控件复用**：新增页面/区块复用 ui 套件（DataTable/AppModal/confirmDialog），不手写 table/window.confirm（FreeDiscoveryView 教训）；列表/详情/筛选三形态风格一致。
3. **暗色模式**：新增页面暗色下无白底/白字（用户协议页教训 7c9e3b042）。
4. **菜单组织**：新增路由入 menu-config（或明确登记灰藏+理由）；无"无菜单无页内链接"的孤儿页（R30 遗留 #2 清单为基线，逐项核对是否仍孤儿）。
5. **交互一致**：破坏性操作有确认；加载态/空态/错误态三态齐备；分页与时间窗选择控件与既有页面一致。
6. **生成物纪律**：menu-config.json 变更走生成流程；冲突取远程不手并。

## 4. 历史回归点（轮末回注区）

- [R30 遗留#2] 七个孤儿路由（/routing-decisions、/admin/approvals、/admin/output-compliance、/admin/usage、/correlations、/quality-correlations、/routing/overrides）——开放债，逐轮核对
- [R30 遗留#3] FreeDiscoveryView 手写 table/window.confirm 未复用套件；freediscovery 页未展示 auto_disabled_at 等健康字段——开放债
- [09-16] 用户协议页暗色白底 — 修复 7c9e3b042

## 5. 子代理派发提示词

```text
你是 D15（可观测性与前端一致性）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D15-observability-ux.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：窗口内 web/ 与观测面的 diff；孤儿路由与手写控件存量债是否恶化。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```
