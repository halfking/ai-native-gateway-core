# 移动网关功能审计与修复（2026-10-10）

## 范围与执行计划

审计跨 `llm-gateway-client` 原生壳和 `llm-gateway-go` 的移动 SPA/API，覆盖图标、节点切换与配对、NetBird、系统即时队列、路由/模型选择、租户/用户/Key 操作及统计、费用对账、请求与多轮会话、语言切换。

执行顺序：固定两仓基线并建立隔离工作树 → 跟踪五类链路和权限 → 修复 → 添加能复现问题的回归 → 编译/依赖检查 → 复核 diff → 提交并推送审计分支。原工作树中的并行工作和设备验收产物不纳入提交。

审计基线为 gateway `f60e9eb3e`、client `0b08afc`；包含审计期间 main 上新增的原生注销测试和其他并行提交。

## 发现与修复

| 等级 | 问题 | 修复与证据 |
|---|---|---|
| P1 | 原生注销通道无法从受隔离远端页面调用；全局清理还会破坏其他网关会话和 iOS 节点/指纹存储 | 移除两平台的全局清理插件；使用同源服务端注销、本地退出标记、认证代次和取消机制；新登录等待注销结束。离线注销重启、迟到水合、原生隔离回归通过 |
| P1 | 普通用户 JWT 可进入 Key 管理 API；空租户上下文可能退化成全量列表 | Key 根路由和资源路由要求管理角色；管理角色守卫拒绝空 tenant_admin scope；保留默认租户的显式限制；角色拒绝和数据库不可用回归通过 |
| P1 | 用户下创建 Key 不携带租户；后端应用始终落在 default | 请求包含目标用户 tenant_id；按目标租户查找/创建 application，处理并发唯一键冲突；本地 PostgreSQL 同码跨租户应用和移动 payload 回归通过 |
| P1 | NetBird 先改 peer 组后校验策略；同节点并发可重复创建策略 | 预检 peer、来源/目标组、策略标记及完整窄规则；专用单节点组；先策略后成员；数据库 session advisory lock；幂等复用已有策略，保留 posture checks；并发/失败无写入/数据库锁回归通过 |
| P1 | NetBird GET 的 sources/destinations 是组对象；旧实现按字符串解析 | 接受 GET 对象和 ID 两种形状，写入仍发 ID；拒绝附加端口范围/资源规则、重复策略、重定向及超限/尾随 JSON；按官方 Groups/Policies API核对 |
| P2 | 用量查询失败/扫描跳行被返回为零或空成功；多次查询读到不同快照 | 用户统计用只读 Repeatable Read 事务；任一失败/截断返回 500；汇总按 user/key/fact 租户同时归属；跨租户事实排除和故障注入回归通过 |
| P2 | 错误率比例直接加 %；费用累加日表遗漏窗口首日；未知延迟变成 0 | 百分比乘 100；KPI 提供完整滚动窗口 cost_usd；日表包括首个部分日；无有效延迟时 P95=null；旧节点缺失费用字段显示未知；窗口边界、比例与费用回归通过 |
| P2 | 请求列表无法跳转；切用户后迟到响应/Key 明文可进入新面板 | 统计提供 request_id，链接统一请求详情；读取取消与代次保护，路由切换/抽屉关闭清理临时明文；迟到响应回归通过 |
| P2 | 对账月份用设备时区；旧月份数据可能显示在新月份；零账单被认为未导入 | UTC 月份；取消旧查询和清空旧数据；无账单显示未知；已导入零账单仍计为账单，正用量触发差异告警；API形状和金额校验；对应回归通过 |
| P2 | 账户节点入口依赖超级管理员成功生成 QR；旧 QR 可保留到过期后 | 节点切换入口独立显示；QR关闭/失败/过期清理；显示供管理员独立传递的 SHA-256 指纹；入口/过期/迟到响应回归通过 |
| P2 | 首页卸载后首屏请求结束仍创建轮询；模型选择默认文案固定在初始语言 | 卸载后禁止启动队列轮询，刷新同时更新队列；模型标题/触发文案随语言响应；生命周期和语言切换回归通过 |
| P2 | 租户详情旧数据可在切换目标时仍用于操作；新增样式引用不存在 token | 清空旧详情、取消旧加载、锁定操作目标和加载期间动作；改用已有 surface token；确认后变更与刷新回归、样式门通过 |
| P2 | 开发依赖 glob 10.4.5 存在 CLI 命令注入漏洞 | npm/pnpm 同步覆盖至 10.5.0 并更新两套锁文件；官方 registry 的全依赖审计为 0 |

## 五轴审查

- 数据流：QR签名/独立信任指纹 → 本地目录 → 精确 native origin；身份 → 租户 → user/key/fact 聚合；费用对账 → UTC月份；请求 → session/turn 详情。业务标识不翻译，金额/日期按语言格式化。
- 业务闭环：节点入口不依赖 QR 配置；状态操作确认、成功刷新、错误可见；用户最近请求可进入统一详情；原有会话/turn/request 页面及租户/Key 入口沿用现有权限。
- 状态机：新增代次、取消和清理覆盖加载、切换用户/租户/月、关闭抽屉、注销、扫码过期、卸载；不将未知读数显示成成功或零。
- 并发：统计同一数据库快照；NetBird在共享数据库锁内完成读取/校验/写入；重复扫码无额外策略更新；应用创建处理唯一键竞争。
- 兼容：保留旧节点响应（新增字段可选）；8语言原生目录与完整中/英移动 SPA分工不变；iOS 15 CSS构建门、触控目标与 i18n 键覆盖门；图标原生资产及同步链未退化。

## 验证记录

- 移动 SPA：全量 Vitest **191 文件 / 7169 测试通过**；typecheck、production build、CSS/触控/i18n 门通过。Node 26 测试使用 `NODE_OPTIONS=--no-experimental-webstorage`，让 jsdom 提供浏览器 Storage。
- 原生壳：**18 回归通过**；typecheck、Capacitor sync、Android assembleDebug、iOS Simulator Debug（不签名）构建通过。
- Go：`CGO_ENABLED=0 go build ./...`、相关包 vet、目标管理/NetBird/对账测试通过；本地临时 PostgreSQL 17 实跑统计窗口、跨租户、故障注入、应用归属、数据库锁回归。
- 两仓 `npm audit --registry=https://registry.npmjs.org` 全依赖为 0；未修改用户 registry 配置。
- 代码图工具因既有 better-sqlite3 与当前 Node ABI 不匹配无法生成；以直接调用链追踪、diff审查与上述回归补充。未安装或修改全局工具。
- 未运行生产数据库测试或修改线上 NetBird；未部署新包、未进行真机公网/私网验收；Go 的故意红 `TestV1BodiesReadersAreAssessed` 保持原样。未安装缺失的 govulncheck，未声称完成 Go 全依赖漏洞扫描。

官方核对：[NetBird Groups](https://docs.netbird.io/api/resources/groups)、[NetBird Policies](https://docs.netbird.io/api/resources/policies)。漏洞依据：[glob GHSA-5j98-mcp5-4vw2](https://github.com/advisories/GHSA-5j98-mcp5-4vw2)。
