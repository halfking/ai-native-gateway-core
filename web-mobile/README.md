# web-mobile — LLM 网关移动运维前端（Hyper 类型）

> 设计契约 SSOT：NBJL 仓库 `docs/UI规范/17-网关移动前端Hyper应用.md`（本文件管构建与接线）。
> 形态：独立 Vue 3 SPA，同源挂载在网关 `/m/*`（`MobileStaticHandler`），单端口与桌面 `web/` 共存。
> 依赖：运行时仅 `vue` + `vue-router`；组件全部手写轻实现（不引入 Element Plus）。

## 与桌面端的关系：统一入口自动切换

- 桌面 `web/` 首帧脚本 `/entry-switch.js`：compact（<600px）访问 `/` → `location.replace('/m')`。
- 本应用首帧脚本 `/m/entry-switch.js`：large（≥1280px）访问 `/m` 根入口 → `location.replace('/')`。
- 判定走 window class（宽度），不猜 UA；`?ui=mobile|desktop` 与 `sessionStorage['llmgw_ui_mode']`
  覆盖优先；深链不弹跳。两个 SPA 同源同端口，用户只记一个地址。

## 命令

```bash
pnpm install
pnpm dev        # :5176，/api 代理到本地网关 :8782
pnpm test       # vitest（runtime + api spec）
pnpm typecheck  # vue-tsc --noEmit (strict)
pnpm build      # typecheck + css 门禁 + vite build → dist/
pnpm css:check  # 范围语法门禁（Safari 15，UI 规范 01 §3）
pnpm css:selftest
```

## 构建约束（门禁）

- `vite.config.ts`：`base: '/m/'`、`build.target`/`cssTarget` 含 `safari15`。
- `scripts/verify-css-media-syntax.mjs`：媒体查询范围语法命中必须为 0（自带 `--self-test`）。
- `vue-tsc` strict + `noUncheckedIndexedAccess` + `verbatimModuleSyntax`。

## 目录

```
src/
  api/          _core(cookie优先/401跳转) + auth/board/credentials/models/keys/alerts/usage/system
  runtime/      Hyper 运行时子集：epochs(R2) capabilities(R3) titleResolver navigationContext
                backDispatcher scrollHost pullToRefresh continuousList focusWorkspace
  composables/  useHyperPage / useHyperOverlay
  components/   shell(AppShell/MoreSheet) ui(Sheet/Confirm/StatusDot/Skeleton/Sparkline…)
                focus(FocusWorkspace) account(AccountSheet)
  views/        Login/Overview/Nodes/Models/Keys/Alerts/Usage
  i18n/         点路径词典 zh-CN + en-US
  styles/       theme.css(--app-*/--kx-* token) base.css(compact 布局基线)
```

## 部署接线

- Go 侧：`MOBILE_WEB_DIST` 指向本目录 dist；缺省探测 `<repo>/web-mobile/dist`。
- 路由：`/m-assets/*`（白名单扩展）、`/m/*`（SPA fallback）、`/m/api|/m/v1` 404 防遮蔽。
- `deploy-local.sh` / `deploy-252.sh` 会把 `web-mobile/dist` 打进 release bundle。
