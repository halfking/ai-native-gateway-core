---
name: maestro-browser-verification
description: 用 Maestro 对 llm-gateway-go 的管理界面做逐页浏览器验证。适用场景：部署后验收、新页面/新入口的端到端验证、mock-only 栈全量回归、线上环境 smoke 验证。含选择器语义、登录态、mock 栈夹具的已踩坑清单。
---

# Maestro 浏览器验证（mock 栈 + 线上两形态）

来源：`docs/audit/2026-11-17-PI智能体审计与完善-主档.md` §八/§8.13；`maestro/README.md`（872 行实测积累）。

## 一、两种验证形态

| 形态 | 栈 | 适用 flow | 命令 |
|---|---|---|---|
| mock-only 全量 | `tests/mock-system-test`（本地 compose） | 01..09 全部 | `cd tests/mock-system-test && bash run-mock-only.sh up` 然后 `BASE_URL=http://127.0.0.1:18781 bash maestro/run-mock-only.sh` |
| 线上验收 | 目标环境公网入口 | 按环境裁剪（见「量具边界」） | `maestro/run-local.sh`（BASE_URL 指向目标） |

mock 栈的已知夹具坑（全部已修，若复现按此归因）：
- PG init 脚本必须**烘进镜像层**（bind mount noexec ⇒ entrypoint `access(X_OK)` 误判 ⇒ exit 126）；
- gateway 容器要挂 `web/dist`（镜像不含前端 ⇒ SPA 全 404）；
- 必须设 `LLM_GATEWAY_LICENSE_ACTIVATED=1`（否则全站软重定向到 /bootstrap 向导）；
- 栈归一化要清 seed 管理员的 `must_change_password`（否则其余管理 API 全被拒、页面被改密弹窗接管）；
- `resolve-ids.mjs` 取数要 `pickEnabled`（非 mock 供应商被 G2 门禁用 ⇒ 取首项会拿到禁用项）。

## 二、选择器语义（实测推翻过仓内二手记载）

- `text:` 是**全匹配正则**（命令 JSON 字段名就是 `textRegex`）：`text: ".*条 · 成功率.*"` 能命中，裸子串 `"成功率"` 不命中。
- `id:` 能选到 `data-testid`，但**选不到 `<button>`**——按钮优先用 `textRegex` 或 `data-testid` 属性选择器。
- `pressKey: Escape` 在 web 驱动**不支持**（"Keycode ESCAPE is not supported on web"）——浮层 Esc 出口走页面内 CDP 断言。

## 三、登录态

共享 `_shared/login.yaml`：UI 登录 + cookie 持久化。注意**被测系统的真实缺陷会伪装成测试失败**——历史上 3045 在役版的登录弹窗无条件带 `captcha_id` 却只在填码时带 `captcha_code` ⇒ 后端恒判 captcha 不完整 ⇒ 所有需登录 flow 集体红。归因顺序：先 `curl` 直连登录 API 看真实错误原文，再决定修测试还是修被测系统。

## 四、环境变量传递（mock 栈专用）

`maestro/run-mock-only.sh` 里 `BASE_URL` 等**必须 export**——只设 shell 变量时子进程 `resolve-ids.mjs` 回落默认值 `127.0.0.1:8782`（本地开发网关）⇒ 把**另一个网关**的 provider/key id 灌给本栈 ⇒ 详情页报「invalid id / not found」三连，报错文本与真因完全脱节。

## 五、量具边界（断言前先确认「断言对象在该环境存在」）

线上环境若有 Maintain 平台接管 `/` 与运维域（`curl -I https://…/` ⇒ 302 到 `/maintain/home`）：
- 断言「网关落地页/仪表盘/运维页默认文案」的 flow 必红——**不是回归**；
- 适用集裁剪为网关自有域页面（例：03..08 六个）；
- 判定靠**独立证据源**：302 location 头、失败截图里的品牌名、同环境其它 flow 全绿。

## 六、防假红清单

1. Chrome profile **每次全新**（旧 `index.html` 缓存指向旧 bundle ⇒ 误判「功能没上线」；页面内断言工具必须 `setCacheDisabled`）。
2. 视口覆写 **1440×1400**（默认 800×600 触发 compact 布局 ⇒ 顶栏变汉堡菜单 ⇒ 顶栏断言全红）。
3. **跑脚本期间不要编辑它**（bash 逐块重读脚本 ⇒ 运行中改文件 ⇒ 莫名语法错；`bash -n` 却正常）。
4. 断言文案改动后，flow 的期望值要同步（文案被 `<span>` 拆开时靠 `textRegex` 兜）。

## 七、新增页面的覆盖纪律

「路由存在」≠「功能可用」。新页面必须有：
1. 至少一条 flow 走到它（登录 → 入口可见 → 落到目标路由）；
2. 页面内数据断言（读数与 API 实际值对拍，不只断文案存在）；
3. README §1 路由清单与 §3 逐条核对表同步（少一条时两边自洽地绿 ⇒ 覆盖数与路由数对齐才是真覆盖）。
