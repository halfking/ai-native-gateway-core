# R56 · D15 可观测性+UX · 48h 审计结论

> 时间：2026-09-23 · 执行：主代理+D15 子代理亲读复核

## 发现与处置
| 级别 | 项 | 处置 |
|---|---|---|
| P1 | B7 外网地域归类死分支：virtual_ip 全部为 10.x 假名 IP（identity.go virtualIPPrefix=10），classifyVirtualIP 对 10/8 原样返回——段表查找不可达且假名 IP 被当内网直显 | 登记 R57 首项（数据源级：落真实来源 dim/rollup 写 real ip；修好前 classifyVirtualIPPie 加 pseudo 标注） |
| P2 | 管理员重置密码不吊销目标用户旧 token（B4 只覆盖自改密） | ✅ reset 路径复用吊销 |
| P2 | B12 三键未登记 spec | ✅ GoalSpecs() 补三条 |
| P3 | B12 VERIFY 失败永久丢失（:281 注释与状态不符） | 登记挂账 |
| P3 | geoIPSourceStatus 零调用/哑赋值/陈旧注释 | ✅ 顺手清理（geoIPSourceStatus 补未接线标注） |

## 核实为健康
B4 三中间件全覆盖+JWT iat 贯通+5s TTL 跨实例滞后+fail-open 有意+前后端登出一致；A3 clamp 唯二入口+366d 全局帽不绕过；B12 状态机（提前通过/预算耗尽/fix-response 推进/单调原子/legacy 保留）；A5 managed header 并存+gw-continue 每请求授权；B5 warn<block 防御 clamp+CategoryRetry 纯展示+i18n 8/8；落库↔前端字段对齐（倍率双列名/瀑布字段/journey union/derived 段兜底）；看板饼图复用 DrilldownPieChart+menu-config 无漏注册+401 deep-link 保持。

## R57 追加（2026-09-23）
| 级别 | 项 | 处置 |
|---|---|---|
| P1 | 看板 IP 饼图数据源造假：i18n 标签写"客户端 IP"、数据是 identity 假名 10.x | ✅ 随 B7 数据源级修复：API 键 virtual_ips→client_ips，web 类型/绑定/live-merge 测试/8 语言 i18n 键同步；真源经 740 view 链投影（见 D06 追加） |
