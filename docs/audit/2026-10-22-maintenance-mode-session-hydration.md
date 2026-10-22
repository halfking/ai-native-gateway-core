# 审计：部署版 maintenance_mode 打在 /api/auth/me 会话水合上 —— 服务器侧缺陷登记与前端侧止血

- 日期：2026-10-22
- 发现批次：llm-gateway-client iPhone 17 Pro 模拟器 Maestro 批跑（15 flow）
- 证据链：llm-gateway-client 仓 `.maestro/evidence/2026-10-22-6s-real/SERVICE-RELIABILITY-REPORT.md` §3
  与 `.maestro/evidence/2026-10-22-ios-sim-17pro/`（多轮 console/截图）

## 现象

登出 → 立即重登路径上（冷启首连也偶发），WebView 整页渲染出：

```
GET https://llmgo.kxpms.cn/api/auth/me?...client=shell&v=2.5.8
{"code":"maintenance_mode","message":"网关维护中，请稍后重试。","request_id":"01JAZ8..."}
```

- 登录 POST 本身全部成功（admin audit：`authentication.login` 逐条可查）。
- 客户端表现：登录后总览永不出现，Maestro 批跑 12 个 flow 级联失败（每条失败的
  尾部断言都是「总览不出现」）。
- 复现窗口：2026-10-22 01:50–01:53、02:19 稳定复现；其它时段 curl 直打
  `/api/auth/me`（带 Bearer / 纯 cookie / 各种 UA×Accept 组合）恒 200，无法外部复现。

## 定性：该信封不在任何本地源码中

对以下范围全文检索 `maintenance_mode` / `网关维护中`，**零命中**：

- 本仓（llm-gateway-go 全部工作区，含 `cmd/gateway`、`maintain_proxy.go`）
- `ai-native-maintain`（维护服务本仓）
- 部署在 llmgo 的 web-mobile SPA 产物（`/m-assets/` 全量 chunk 与 index bundle）
- llm-gateway-client 壳（webDist 源码与 bundle、iOS/Android 原生层）

⇒ 信封由**部署版构建（server / 边缘组件）**产生，且部署版比本地 main 新
（`/version` = 2.5.8-53649ce2-20261021-**2812**；本地 VERSION 标记 2811 未发）。
佐证：llmgo `HEAD /` 恒 302 → `/maintain/home`（维护门户分流），`/version` 的
`runtime_role: "traffic-only"`。

## 服务器侧待修（本机无对应源码，登记给有部署构建的会话/同学）

1. **管理面维护闸门不应吞 `/api/auth/me`**：会话水合探测是登录流程的组成部件。
   维护窗口对 `/api/auth/me` 至少要区分「会话裁决」（仍正常 401/200）与
   「维护降级」（可重试语义 + Retry-After），不能直接下发 JSON 整页信封。
2. `?client=shell&v=<ver>` 握手导航的发起方（部署版壳/SPA 新增逻辑）在维护态下
   应渲染可重试 UI 而非裸 JSON。
3. 顺带核对三站 `runtime_role=traffic-only` 的语义：管理面与会话裁决是否被
   误归入 traffic 通道。

## 前端侧止血（本提交，web-mobile）

1. `api/client.ts`
   - `errorCode()` 识别顶层 `{code,message,request_id}` 信封（原只认 `error.code`）。
   - 新增 `isMaintenanceMode(err)`。
   - `errorMessage()` 提取顶层 `message` 文案。
   - **会话探针 `sessionReallyDead()`：维护态失败 ≠ 会话死亡** —— 与 `EpochError`
     同列不踢登录。维护窗口内不再把有效会话误杀回登录页（否则形成
     「登录页 ↔ 维护 JSON」死循环，即本次批跑 12 红的形态）。
2. `stores/auth.ts` `hydrate()`：**只把 401 当会话死亡**；断网(status 0)与 5xx
   （维护态 503）是瞬时故障，不再清空 userInfo/token/localStorage ——
   修复「服务恢复后本地用户态已被抹掉、主题/语言/节点配置连带丢失」的放大器。
3. `views/LoginView.vue`：登录命中维护态时展示服务端原话（如「网关维护中，请稍后
   重试。」）而非通用「登录失败」，用户不会被误导去改密码重试。
4. 回归门：`api/client.spec.ts` 新增两测（信封解析 + 探针维护态不踢），15/15 绿。

## 客户端仓（llm-gateway-client）侧已做的配套

- flow 自愈：`_fill-credentials.yaml` / `01-login-persistence.yaml` 对维护态卡死页
  做「杀进程重启走冷启水合」恢复（commit 1362db9）。

## 追记（2026-10-22 13:50）：部署进展与双站握手发现

- 生产 llm.kxpms.cn 已升 **2813 且 runtime_role: traffic-only → active**（部署侧已在行动）。
- **关键新发现：部署版登录是双站同发握手** —— 同一秒的 authentication.login 在
  llm.kxpms.cn 与 llmgo.kxpms.cn 的审计中同时出现（13:33:59 等）；llm.itestu.cn
  亦见过同刻登录。⇒ **llmgo（仍 2812/traffic-only）不随 2813 一起修的话，
  生产单站修好也无法恢复移动端登录**——握手会被仍坏的次站拖死
  （实测：SPA 卡「正在登录…」/ me 探针吃 maintenance_mode JSON）。
- 给部署侧的补充要求：**三站（至少 prod+llmgo）同步部署 2813+ 并确认
  runtime_role=active**，移动端登录链才通。部署完成后收割命令（已备好）：
  `maestro --device 43984EB8-… test --reinstall-driver <flow>`（iOS 侧
  02/03/04/99-probe-deeplink，模板与凭据注入均就绪，见 llm-gateway-client
  仓 adf3e87）。
