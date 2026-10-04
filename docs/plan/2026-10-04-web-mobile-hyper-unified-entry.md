# 网关移动前端 Hyper 应用 + 移动/PC 统一入口（2026-10-04）

> 分支：`feat/web-mobile-hyper`（worktree `lgw-web-mobile`，基线 `origin/main d852f5292`）
> 上游设计 SSOT：NBJL 仓库 `docs/UI规范/17-网关移动前端Hyper应用.md`（本文档与其互补：这边管工程实现与接线，那边管设计契约）
> 范围：`web-mobile/` 新 SPA + Go 静态接线 + 双端统一入口自动切换 + deploy-{local,252}.sh 集成

## 1. 需求完善（用户原始诉求 → 可验收条目）

| # | 原始诉求 | 完善后的可验收条目 |
| --- | --- | --- |
| R1 | 移动端与 PC 端统一访问入口 | 同源单端口（网关一个进程同时服务 `web/` 桌面 SPA 与 `web-mobile/` 移动 SPA）；用户只记一个地址 |
| R2 | 自动根据屏幕的类型进行切换 | 入口自动切换按 **window class**（01 §2 SSOT），不猜 UA：compact（<600px）访问 `/` → 自动进 `/m`；large（≥1280px）访问 `/m` 根入口 → 回 `/`；medium/expanded（600–1279）双端皆宜不强制弹跳；`?ui=desktop` / `?ui=mobile` 与 sessionStorage 覆盖优先于自动判定 |
| R3 | 一个端口下访问 | `/m/*`、`/m-assets/*` 由网关 `MobileStaticHandler` 直接服务（仿 maintain 双 SPA 先例），无第二个端口/进程 |
| R4 | 统一的 deploy-{local\|252}.sh 部署 | `deploy-local.sh`（蓝绿链）把 `web-mobile/dist` 打进 release bundle；新增根 `deploy-252.sh` 统一入口（复用 `scripts/deploy-252-gateway.sh`），同样携带 web-mobile dist |
| R5 | Hyper 类型移动前端 | 按 UI 规范 06/07/11–15 协议子集构建：TitleResolver、BackDispatcher、下拉刷新、连续加载、滚动恢复、专注工作区、capabilities v2、三层代次（R2） |
| R6 | 紧凑、无多重标题、有序排列 | 每页恰一个页面标题（顶栏），区块用分组标签不用标题；数据用 label/value 有序列表与卡片；触控目标 ≥48px；输入字号 16px |
| R7 | 重要表格可点击全页查看与编辑 | 宽表（节点每模型明细、用量分布、密钥清单）提供「全页查看」入口 → 专注工作区（Teleport 单实例、背景 inert、停靠表头、可横滚），密钥表在专注层内可禁用/启用 |
| R8 | 完善 UI 规范文档 | 在 NBJL `docs/UI规范/17` 增补实现状态矩阵与 R11（统一入口自动切换）裁决，README 2.2 同步 |

## 2. 方案

### 2.1 架构

```
浏览器 ──同源──▶ 网关单进程 (默认 :8782)
                 ├── /                    web/dist        桌面 SPA（现状不动）
                 │    └─ entry-switch.js：compact → replace('/m')
                 ├── /m/*                 web-mobile/dist 移动 SPA（新增）
                 │    └─ entry-switch.js：large 根入口 → replace('/')
                 ├── /m-assets/*          web-mobile/dist/assets（白名单扩展）
                 └── /api/*               既有 API（零改动，cookie 优先鉴权）
```

- **形态**：独立 Vue 3 SPA（`web-mobile/`），不装 Capacitor 壳（17 §9 留白）；不引入 Element Plus，组件全部手写轻实现；依赖仅 `vue` + `vue-router`（运行时），`vite`/`vitest`/`vue-tsc` 开发依赖。
- **挂载**：Go 侧 `cmd/gateway/mobile_static.go`（`MobileStaticHandler`，逐字沿用 maintain 的扩展白名单 + SPA fallback + `/m/api|/m/v1` 404 防遮蔽）；`MOBILE_WEB_DIST` 指向 dist，缺省探测 `web-mobile/dist`。
- **统一入口切换**：CSP 不允许 inline script（web/index.html 注释实证），切换逻辑放外部脚本 `/entry-switch.js`（两侧各一份，`replace()` 不留历史记录）；window class 判定与 01 §2 断点双镜像一致（<600 / ≥1280）。

### 2.2 页面清单（17 §2 落地）

底栏 5 席：总览 `/`、节点 `/nodes`、模型 `/models`、密钥 `/keys`、更多（抽屉：告警/用量/外观/语言/登出）。账户固定顶栏头像 → AccountSheet。

| 页面 | 数据源 | 形态 |
| --- | --- | --- |
| 登录 `/login` | POST /api/auth/token | 全屏表单 16px |
| 总览 `/` | /healthz + /api/system/version + board?days=7&include_operational=1 | 状态条+汇总卡+sparkline+任务 chips，下拉刷新 |
| 节点 `/nodes` | /api/credentials/monitor-summary | 卡片列表，点开 Sheet；每模型宽表 → 专注模式 |
| 模型 `/models` | /api/routing/available-models | 家族分组连续加载 + 搜索 250ms debounce |
| 密钥 `/keys` | /api/keys 全套 | 卡片列表 + 创建 Sheet + 禁用/启用/揭示（确认框，不缓存） |
| 告警 `/alerts` | /api/candidate-failures/alerts | 时间线卡片 |
| 用量 `/usage` | /api/usage/summary + by-model | 汇总卡 + 模型分布（→ 专注宽表），Tab 停靠 |

### 2.3 Hyper 运行时子集

`src/runtime/`：epochs（R2 三层代次）、capabilities（R3 v2 schema，Web 基线）、titleResolver（06 §2 优先级链）、navigationContext（06 §3，sessionStorage 去敏）、backDispatcher（06 §5 单一仲裁 + 覆盖层 history 标记）、scrollHost（06 §6 快照/恢复）、pullToRefresh（07 §2 状态机 64/12/96）、continuousList（07 §3 sentinel 240px）、focusWorkspace（07 §8–9 单实例 + inert + 滚动锁）。每模块配同名 `.spec.ts`。

### 2.4 工程门禁

- `vite.config.ts`：`cssTarget: ['safari15']` + `build.target` 含 safari15（01 §3 iOS 15 硬约束）；`scripts/verify-css-media-syntax.mjs` 范围语法计数 = 0。
- 构建：`vue-tsc --noEmit`（strict + noUncheckedIndexedAccess + verbatimModuleSyntax）→ `vite build`。
- 测试：vitest 运行时 + 组件 spec。

### 2.5 部署

- `scripts/deploy-local.sh`：frontend 构建段并行加 `web-mobile`（`--no-frontend` 同语义复用既有 dist），`dl_stage_release` 追加 `web-mobile/dist`，容器 env 注入 `MOBILE_WEB_DIST`。
- 根 `deploy-252.sh`：薄封装（对齐根 `deploy-local.sh` 先例），内部复用 `scripts/deploy-252-gateway.sh`；后者构建段追加 web-mobile 并随 bundle 上传、systemd env 加 `MOBILE_WEB_DIST=/opt/llm-gateway-go/web-mobile-dist`。

### 2.6 验收（本地）—— 实测记录（2026-10-04）

1. `pnpm build` + `vue-tsc` + vitest 全绿（36 用例）；CSS 范围语法门 0 + `--self-test` 正/负对照通过。
2. Go 单测：`go test ./cmd/gateway/ -run MobileStatic` 5 用例全绿（含真实文件**内容**断言）。
3. `bash deploy-local.sh` 全量部署（release 2.5.8.2445/2446，VERIFY_PASS=1）后浏览器实测（playwright chromium，11/11 通过）：
   - 390px 视口访问 `/` → entry-switch → `/m` → 应用启动 → 未登录落 `/m/login`（完整链路）；
   - 1280px 访问 `/m` 根入口 → 回 `/` → 桌面 SPA 接管（落其内页）；`?ui=mobile` 强制停留；
   - `/m` 服务 SPA；`/m/api/*`、`/m/v1/*` 404 防遮蔽；`/m-assets/` 非白名单 404；资产 MIME 正确；
   - **带认证真实数据**：登录表单 → 总览渲染真实指标（48.6K 请求 / 129.7M tokens / $3.72 等）；
     密钥页 186 张真实卡片；「全页查看」专注宽表 186 行 + 背景 inert + 停靠表头 + Esc 退出恢复；
     节点页在共享 PG 超载（monitor-summary 15s 超时 500，**环境既有问题**）下正确进入失败保旧 + 手动重试态。

**首部署实测抓出并修复的两个真缺陷**（均有回归锚）：
1. `ServeSPA` 沿用 maintain 的 `Join(distDir, upath)` 写法未剥 `/m` 挂载前缀——真实文件
   （`/m/assets/*`、`/m/entry-switch.js`）永远 stat 不到，全部回落 index.html，MIME text/html
   使 SPA 启动失败；而「只断言 200」的测试假绿。修为剥前缀 + `http.ServeFile`，测试补
   内容与 MIME 断言（maintain 不受影响：其资产走 `/maintain-assets/` 前缀路由）。
2. 专注层关闭双路径不同效：Esc/系统返回走 BackDispatcher→runtime.close()，不经视图组件，
   `focusHandle` ref 滞留 → 覆盖层滞留且挡交互。修为 FocusWorkspace 订阅 runtime 关闭事件
   同步 emit('exit')。

## 3. 明确不做（本轮）

- Capacitor 原生壳、SSE 实时流、8 语言（17 §9 留白原样）。
- 桌面 `web/` 的任何 UI 改动（仅新增 `/entry-switch.js` 一行引用，桌面零回归红线）。
- 移动端不接 LLM 数据面、不做路由调试/对账/审计等桌面作业（17 §1 定位）。
