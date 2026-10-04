# llm-gateway web-mobile（Hyper 移动前端）

> 设计契约：`nbjl-3/docs/UI规范/17-网关移动前端Hyper应用.md`（Hyper 协议在网关场景的应用 + 跨文档裁决 R1–R10）
> 本 README 是**工程侧 SSOT**：构建、挂载、目录、门禁。

LLM 网关的移动运维前端（Hyper 类型）：总览 / 节点健康 / 模型目录 / API 密钥 / 告警 / 用量，手机快查定位。移动端优先（compact 主战场），medium+ 自动获得宽栅格，不写第二套桌面代码。

## 技术栈

- Vue 3.5（`<script setup lang="ts">`）+ vue-router + Pinia，**无 UI 组件库**（手写轻组件，token CSS）
- Vite 8；`target`/`cssTarget` 钉 `safari15`（iOS 15 WebView 媒体查询范围语法整层丢弃，见 UI规范 01 §3）
- Vitest（jsdom）核心运行时单测；`vue-tsc` 严格类型门禁
- 视觉 token 复刻 nbjl-3 `theme.css`（`--app-*` / `--kx-*`，浅 `#1e4fd6` / 暗 `#5b8cff`）

## 目录

```
src/
  hyper/            Hyper Web Runtime（08 §1 第 2 层）
    navigation/       TitleResolver / NavigationStore / BackDispatcher
    scroll/           ScrollHost / PullToRefreshMachine / ContinuousListController / DockCoordinator
    focus/            FocusMachine / scrollLock（引用计数配对）
    capabilities.ts   capabilities v2 协商（Web 基线）
    runtime.ts        装配：覆盖层注册表 + history 标记（06 §5.1）+ 路由适配 + 恢复消费
    useHyperPage / useHyperOverlay   接入 API（17 §4-R6 契约）
  components/
    shell/            HyperApp / AppTopbar / AppBottomNav / AppDrawer / AppAccountSheet
    common/           AppSheet / AppConfirm / HyperList / PullRefreshContainer / FocusLayer / …
  views/            Login / Home / Nodes / Models / Keys / Alerts / Usage / NotFound
  api/              client（cookie-first + Bearer fallback + sessionEpoch 代次）+ 域模块
  stores/           auth（token 只进内存）/ theme
  i18n/             zh-CN + en-US 点路径词典
  composables/      useWindowClass（四档 SSOT 镜像）/ usePullToRefreshGesture
```

## 开发

```bash
cd web-mobile
pnpm install
pnpm dev            # http://localhost:5790/m/（代理 /api → GATEWAY_API_TARGET，默认 127.0.0.1:8782）
```

## 构建 / 测试 / 门禁

```bash
pnpm test           # vitest（导航/滚动/仲裁/代次/断点镜像/客户端）
pnpm typecheck      # vue-tsc -b（strict + noUncheckedIndexedAccess + verbatimModuleSyntax）
pnpm build          # verify-css-media-syntax（范围语法=0）→ vue-tsc → vite build
```

## 挂载与统一入口（生产）

仿 maintain 双 SPA 先例（`cmd/gateway/maintain_static.go`）：

- Go 侧 `MobileStaticHandler`（`cmd/gateway/mobile_static.go`）：
  - `/m/*` → dist 文件，缺省 SPA fallback 到 index.html；`/m/api/*`、`/m/v1/*` 404（防 SPA 遮蔽）
  - `/m-assets/*` → `dist/assets/*`（扩展名白名单复用 streaming.IsAllowedStaticExt）
  - 统一入口：挂载存在时，GET/HEAD 裸入口（`/` 与 `/index.html`）移动端 UA → 302 `/m/`（`Cache-Control: no-store`）。深链与 API 永不切换；`?desktop` 参数存在即留 PC（与 `web/public/entry-switch.js` 客户端腿同判——iPadOS 桌面级 UA 由该腿补）；`MOBILE_WEB_ENTRY_REDIRECT=false|0|off` 停用分流但保留 /m 挂载
- 配置：`MOBILE_WEB_DIST` env 指向 dist；缺省按 `web-mobile/dist` → `web-mobile` 双候选探测进程 cwd（repo 检出与 release bundle 平铺两种布局都命中）。dist 缺失（空目录）= /m 与入口分流整体不注册，零行为变化
- 资产 base `/m-assets/`、路由 base `/m/`——开发与生产同路径

### deploy 链路（2026-10-04 起）

- `deploy-local.sh`：`build_frontend` 并列构建 web-mobile（失败即中止部署）；bundle 携带 `web-mobile/`；docker 运行镜像 `COPY web-mobile`（docker 模式 cwd=/opt/llm-gateway-go 探测命中）
- `deploy-245.sh` / `deploy-154.sh`（deploy-seamless.sh）：同链路构建与 staging；`--no-frontend` 时 web-mobile 随 web 一起沿用线上 current；远端根软链 `web-mobile -> current/web-mobile` 在切换/回滚路径维护（两节点 unit WorkingDirectory=/opt/llm-gateway-go）
- lockfile 守门：`scripts/check-frontend-lockfiles.sh` 以 **pnpm-only** 契约覆盖本目录（pnpm-lock.yaml 必须跟踪且与 package.json 声明区间一致）

## 鉴权

`POST /api/auth/token` → HttpOnly `llmgw_session` cookie + body `access_token`（内存持有，不落 localStorage；对齐 web/ 2026-08-26 P1-7 语义）。localStorage 只存非敏感 `llmgw_mobile_user`。401 → replace `/login?redirect=`（06 §4）。代次校验（17 §4-R2）：`sessionEpoch` 变更后旧响应丢弃（EpochError）。

## 红线

- 禁止在 media query 里用 Level 4 范围语法；新增断点只写 `min-width`/`max-width` 传统语法
- 禁止新造 px 断点常量——JS 判档只走 `useWindowClass`；与 `theme.css` `--app-bp-*` 双镜像同步改
- 新增触控控件 ≥48 CSS px（17 §4-R1）；输入字号 16px（防 iOS 缩放）
- 密钥明文（reveal/create 结果）不缓存不预取；导航持久化经 `sanitizeFullPath` 去敏
