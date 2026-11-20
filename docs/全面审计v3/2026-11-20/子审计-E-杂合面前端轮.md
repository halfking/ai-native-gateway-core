# 子审计 E：board/时区/预设 + anomalies + 杂合面前端轮（2026-11-18 后 48h 窗口）· 主控自查版

> 子代理两度撞配额/provider 不可用，由主控以定向 grep + 主档对拍收口关键判定。已修复的 4 个 vitest 增量红文件（MaaSUsageView/token/mobileListUx/RoutingAudit）不在此重复，见 02-台账 §五。

## 一、结论总表

| # | 审计问题 | 判定 | 证据 |
|---|---|---|---|
| E1 | 探测口径开关（includeProbes）接线完整性 | **已实现** | 五点链完整：`DashboardView.vue:168`（provide）→ `useDashboardBoard.ts`（状态）→ `BoardFilterBar.vue:21,99-119`（双向 emit）→ `BoardPanel.vue`；`055b40336` 补的漏 add 已在位 |
| E2 | 英雄区 usage_facts 真源切换 | **已实现（口径切换有徽标披露）** | `808413ee3` 切 usage_facts + 含/不含探测开关；`5cc758e85` 徽标独立键「含探测流量」+ 15-board-probe-scope flow 行为流；对账真源与页面口径切换有独立可辨识标记 |
| E3 | system.timezone 贯穿度 | **部分（见 M1 隐患）** | Go 面 `settings/timezone.go:31,34,134` 键+默认+Location() 实时读；前端 `/api/admin/system-timezone` 下发；**SQL 面 992 `cst_*` 函数硬编码 'Asia/Shanghai'**（13 个 admin/*.go 消费）⇒ 运维改非上海时区后 SQL 日切与 Go 日切分叉——主控 M1 登记（P2，当前默认一致不爆） |
| E4 | KxDateRangePicker 统一度 | **基本统一（1 处注释残留）** | 全站 grep `el-date-picker/el-date-editor` 仅 `RequestLogsView.vue:350` **注释**残留（代码已走 KxDateRangePicker:37）；预设口径经 `useSpanDaysRange`+`kxDatePresets.ts`（partsInSystemTz 系统时区）单源 |
| E5 | anomalies 徽标口径 | **已实现（有 211 活体验证归档）** | 根因=徽标只数 reqprobe、页面主体读 response_format_anomalies（两源必然对不上）；修法=新 `format-anomalies/count` 端点 + 双源 allSettled 合计 + 两 tab 统计卡接全量计数；主档 `docs/audit/2026-11-20-格式异常监控轮-徽标口径修正-主档.md` + `40ae366c9` 211 证据归档 |
| E6 | 预设即点即生效 | **已实现** | `a4ce71d67` 提交+关闭面板；与 E4 单源 presets 组件绑定 ⇒ 新使用点自动继承 |
| E7 | 12h 自愈轮 D6（i18n 三语种补译） | **已实现** | `2ac5eee84` 含 web/src/locales/{zh-CN,zh-TW,ja-JP} 补真译文；全量 vitest 复跑 same-as-source/untranslated 棘轮零红（3503 用例绿）实证 |

## 二、缺陷清单

| 级别 | 编号 | 缺陷 | 归属 |
|---|---|---|---|
| P2 | E-1（=主控 M1） | SQL `cst_*` 硬编码 vs `system.timezone` 可配置——「贯穿显示/查询/日切」自述在 SQL 面不成立（固定上海日切）；当前默认一致故不爆 | 时区列车后续轮：SQL 动态时区（需 SET app.timezone 或函数族参数化，schema 级决策） |
| P3 | E-2 | RequestLogsView:350 注释残留 el-date-picker 提示（代码已换）——误导性注释 | 顺手清理（本轮不修） |
| P3 | E-3 | board 顶部布局三轮反复（`3c79b5537`→`af8b44118` revert→`0c3487e4c`/`f932bacc0`）——终态以 `f932bacc0` 一行合并为准，过程抖动无功能残留（revert 干净） | — |

## 三、登记建议

1. E-1 归时区列车：若 owner 需要「时区真的可配」，必须同时改造 992 函数族（或 SQL 面全部改读 settings 下发）；否则把 settings 键描述改为「仅前端显示用，SQL 日切固定上海」防误配。
2. E2/E5 两轮均有「口径切换必须有独立可辨识标记/徽标」的自觉，符合本仓「数字对不上要先说清口径」纪律——保留为正面先例。
