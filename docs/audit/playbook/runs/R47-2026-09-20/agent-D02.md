# D02 协议适配与 taskprofile 审计 hook 子代理报告（窗口：45412e919..HEAD）

## 一、发现（候选）
| # | 级别候选 | 发现 | 证据 | 建议处置 |
|---|---|---|---|---|
| 1 | P3 | F8① 同文件同类残留：LiveRequestStreamV2 筛选行标签硬编码"筛选"（修复点同文件漏网；需新增 key ×8 locale） | web/src/components/LiveRequestStreamV2.vue:307 | 新增 filterGroup key×8 + t() |
| 2 | P3 | 审计契约注释与 import 端点行为矛盾：ImportCorrectionsCSV 的一切错误（含 CSV 400/32MB 超限）都投 failure 后 400——四端点口径不一（方向"多审"非漏审；400-emit 分支无测试钉） | taskprofile/handler.go:56、:343-348 | 改契约注释承认或加预检；补钉桩 |
| 3 | P3 | F8⑨"sink 禁止记录 headers/body"契约双侧只有注释零测试钉 | taskprofile/handler.go:70-72、admin/taskprofile_audit_sink.go:13-17 | 扩展 sink 测试断言 |
| 4 | P3 | 其它 ModelPicker 消费方潜伏吞值分支（RoutingDashboardView 静默 return；ModelCatalogFilterBar 只取 v[0]）——现均单选无现网影响，切 multi 即复现 F8⑩ | web/src/views/RoutingDashboardView.vue:718-723、ModelCatalogFilterBar.vue:44-46 | 登记待办 |
| 5 | P3 | sink 测试未覆盖"AuthContext 非 nil 但字段空"分支；detail 断言只判非 nil | admin/taskprofile_audit_sink_test.go:41-110 | 补一例 |

## 二、核实为健康的面
- F5 接线逐行等价（actor/tenant 初值、typed-nil 守卫、空串回退、同 msg 同 key 序）；panic 隔离保留在 emitAudit 侧；taskprofile 不 import admin 保持；接线点唯一且被钉；三层钉桩真实执行行为路径（SetAuthContext→GetAuthContext→JSON slog 捕获）。
- F8① 消费侧完整（:327 title 恢复 t()，8 locale 齐）；F8⑩ 语义闭环（[value] 包裹→空串清空）；F8⑨ 实现侧成立（缺口仅测试钉）；apply-tier-config 投递矩阵正确；ModelPicker 组件级硬编码为 R46 已登记存量（约 15 处清单）。

## 三、未覆盖项
e2e_audit_hook_integration_test.go（需 testcontainers）；vitest/vue-tsc 复跑；浏览器非 zh-CN locale 实渲染。
