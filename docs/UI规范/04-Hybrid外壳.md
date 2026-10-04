# 04 — Hybrid 外壳（Android / iOS）与 PWA

> 状态标记：**[已落地]** 有实现且有实跑证据；**[未落地]** 只有契约。
> 壳工程：`llm-gateway-client`（独立 git 仓库）。业务前端：`llm-gateway-go/web`。

## 1. 三层模型

**[已落地]** 壳**只装引导页，不装业务**：

```
原生壳（Capacitor 8.5.2）
  职责：图标 / 启动屏 / 网络豁免 / 插件位 / 进程
     ↓ 每次冷启动
引导页  webDist/index.html   ← 壳内唯一打包物（9344 字节，零 Vue）
  服务器地址输入 + GET /healthz 探活 + location.replace
     ↓ 同源 SPA
业务前端  web/ 构建产物，由部署机 origin 提供
  Bearer + sessionStorage，与桌面浏览器同一套
```

**为什么壳内不打包 `web/dist`**：改一个 Vue 组件 = 发 Web，不发商店包。
只有改引导页 / `capacitor.config.ts` / 图标才需要 `npx cap sync android` 后重编原生。

**壳内只有引导页这一份文件**：`assets/public/index.html` + 两个 0 字节 cordova 占位。
实跑解包核验过，没有业务 dist 混进去。

## 2. 引导页

**[已落地]** `llm-gateway-client/webDist/index.html`（244 行 / 9344 字节）

| 约束 | 值 | 为什么 |
| --- | --- | --- |
| 探活端点 | `GET /healthz` | 本仓公共健康端点。**`/api/*` 未鉴权会 401**，不能拿它探活 |
| 超时 | 5s | 慢网下不能把用户卡在引导页 |
| 凭据 | **零** | 原生工程不得出现 JWT / API Key。核验：解包 grep 唯一命中是说明该策略的注释 |
| 安全区 | 固定 padding，**不用 `env()`** | 引导页在原生 WebView 加载前渲染，此时 `env(safe-area-inset-*)` 尚未注入 |

## 3. `allowNavigation` 白名单（关键决策）

**[已落地]** `capacitor.config.ts`

```ts
const APPROVED_ORIGINS = [
  'http://localhost:5173', 'http://127.0.0.1:5173',
  'http://localhost:8080', 'http://10.0.2.2:8080',
  'http://192.168.0.1:8080',
]
```

**为什么不用 `['*']`**：Capacitor 未配置 `server.url` / `allowNavigation` 时，
默认把外部 URL 交给**系统浏览器**（Android 走外部 Intent）。也就是说引导页
`location.replace` 到用户填的服务器后，业务页**可能根本不在本 App 的 WebView 里** ——
桥没注入、插件没注册、原生能力全部不可用。

放开 `*` 等于让**任意用户输入的地址**获得全量原生能力。宁可窄：

- 白名单外的地址 → 被系统浏览器打开 → **功能可用，但没有原生壳能力**。这是已知且可接受的降级。
- 前端侧**不假设**自己留在 WebView：`lib/shell/hyper/capabilities.ts` 的握手
  **fail-closed**，桥不通就整体降级为普通 Web，**不谎报任何原生能力**。

> 参考仓 `nbjl3` 在自己的规范里把这个缺口记为 A16 / P0 且**至今未解**。
> 本项目不重复这个错误，但**也没有假装解决** —— 见 §7 未落地项。

## 4. 系统栏与 inset：唯一所有者

**[已落地]** `capacitor.config.ts` 的 `plugins.SystemBars`

```ts
SystemBars: { style: 'DARK', insetsHandling: 'css' }
```

- `style: 'DARK'` = 深色底 + 浅色图标。壳层与业务 SPA 都是深色，
  用 `LIGHT` 会得到深色图标压深色底（不可读）。
- `insetsHandling: 'css'` = 只用 Web 侧 `env(safe-area-inset-*)` 避让，
  保持与 [01 §3](./01-原则与断点.md) 的「单次消费」一致：原生不再额外 padding。
- 另有 `native`（原生 padding）与 `disable`（全不管），本项目都不选。

**两条实测踩坑，禁止回退**：

1. **不要在 `MainActivity` 里手工调 `setDecorFitsSystemWindows`**。
   实测会让本插件注入 safe-area CSS 失败并抛
   `TypeError: Cannot read properties of null (reading 'style')` → **白屏**。
   两个所有者抢同一件事。
2. **引导页与业务 SPA 的 `<meta name="viewport">` 必须含 `viewport-fit=cover`**。
   否则本插件认为不需要 passthrough，改走 native padding，与 CSS inset 叠加。

`MainActivity.java` 因此**只**设 window / decorView 底色与日志，不碰 inset 与 edge-to-edge。
踩坑记录以注释形式留在该文件里。

## 5. 原生资源

**[已落地]**

| 文件 | 内容 |
| --- | --- |
| `android/app/src/main/res/values/colors.xml` | `shellSurface = #FF0F1115` |
| `android/app/src/main/res/values/styles.xml` | 保留 API 24–34 兜底 + 指向真实机制的注释 |

> **XML 注释里不能出现 `--`**（曾致 `mergeDebugResources` 失败）。

## 6. 实跑证据

**[已落地]** 以下不是推断，是实际跑出来的：

| 项 | 结果 |
| --- | --- |
| `./gradlew assembleDebug` | **BUILD SUCCESSFUL 连续 3 次**（32s / 23s / 11s） |
| APK | `app-debug.apk` **4,412,281 字节** |
| SDK | minSdk 24 / targetSdk 36 / compileSdk 36，AGP 8.13.0，Gradle 8.14.3 |
| 模拟器 | `Medium_Phone_API_36.1`（`emulator-5554`，`boot_completed=1`）安装 + 启动成功 |
| 启动态 | `MainActivity` 为 `topResumedActivity`，引导页截图已留存 |
| 零凭据 | 原生 java/xml 无 token 字面量；解包 grep 唯一命中是 `webDist/index.html:15` 的说明注释 |

## 7. 已知缺口（**不得据本篇宣称已解决**）

| # | 问题 | 状态 | 详见 |
| --- | --- | --- | --- |
| A-1 | **API 35+ 系统栏仍为白底**（图标已浅色 → 白底浅字） | ✅ **已修复并实测**（2026-10-04）。真因是窗口背景为 `@null` 导致露白，**不是 WebView 版本**；一行 `windowBackground` 修好 | 本篇 §7.2 |
| A-2 | 引导页 `location.replace` 到远端后**是否仍留在 WebView** | **已取证（见下）**：白名单内**确实留在 WebView**，但 `window.Capacitor` 为 `undefined`、插件全无 ⇒ **原生能力在导航后不可用，且降级无提示** | 本篇 §7.1 |
| A-3 | 公网上架前必须收敛为固定 https 域名 + 关闭 `cleartext` | **未做**。当前是内网 http 明文 | 本篇 §3 |
| A-4 | iOS 侧未构建（只有 Android 工程） | **未做** | — |
| A-5 | PWA（manifest / service worker / 离线） | **未做**。参考仓 PWA 形态本仓未立项 | — |

### 7.1 A-2 取证结论（2026-10-04 实测，**两个独立缺陷，都已定位到根因**）

**测台**：模拟器 `emulator-5554`（API 36）、Capacitor **8.5.2**。
判据用 **DevTools 协议实测 `window.Capacitor`**，不是看截图 ——
截图只能证明「页面在 WebView 里」，证明不了「桥活着」。

**一句话**：白名单内的 origin **现在**能留在本 App 的 WebView 里，
但**桥仍然不注入** ⇒ 原生能力不可用，且降级对用户完全无提示。
这**不是一个问题，是两个独立问题**，修好第一个第二个仍在。

---

#### 缺陷 1：`allowNavigation` 的条目格式错了，整份白名单一直是**空转**的

`server.allowNavigation` 经 `HostMask.Simple.parse` 按 `.` 拆标签并**反转**，
而送进去比较的是 `url.getHost()` 这个**裸 host**。
于是 `http://127.0.0.1:5173` 拆出来是 `["0:5173","0","1","http://127"]`，
拿去和 `["0","1","127"]` 比，**第一段就挂** —— 带 scheme 或端口的条目**永远匹配不上**。

**实测对照**（同一 origin `http://127.0.0.1:5173`，**只改列表格式**，其余全同）：

| `allowNavigation` 写法 | 焦点 | `ACTION_VIEW` | 结果 |
| --- | --- | --- | --- |
| `http://127.0.0.1:5173`（**原写法**） | `com.android.chrome` | 2 | **交系统浏览器** |
| `127.0.0.1`（bare host） | `com.kaixuan.llmgw` | 0 | **留在 WebView** |

**已按实测结论修复**：列表改为 bare host，重编重装后两向复核通过 ——
白名单内 `127.0.0.1:5173` 留应用内（VIEW=0），非白名单 `192.168.31.34:5791` 交出（VIEW=2）。

**⚠️ 修复的代价（必须进发布判据）**：bare host 只能表达「这台主机的**任意端口**」，
**无法表达 host+port**。生产收敛为固定域名后，**同主机上任何端口都会拿到全量原生桥**。
本机开发无所谓，公网分发前必须重新评估。

#### 缺陷 2：桥的注入被限定在 `appUrl` 的 origin ⇒ **即使进来了也没有原生能力**

编译后的字节码（`Bridge.loadWebView`，已核对 AAR，与源码一致）：

```java
allowedOrigin = Uri.parse(appUrl).path(null).fragment(null).clearQuery().build()  // = "http://localhost"
WebViewCompat.addDocumentStartJavaScript(webView, script, singleton(allowedOrigin))
```

`addDocumentStartJavaScript` 的第三个参数是 **origin rules**，缺端口即只认该 scheme 的默认端口（http = 80）。
`appUrl` 是 WebView 本地 server 的 `http://localhost`，所以**只有该 origin 会被注入桥**。

**判别式不需要 root**：同 host、只差端口——

| 页面 origin | `window.Capacitor` | 已注册插件 |
| --- | --- | --- |
| `http://localhost`（引导页，应用自己的 origin） | **`object`** | `SystemBars` / `CapacitorCookies` / `WebView` / `CapacitorHttp` |
| `http://localhost:5173`（同 host，端口不同） | **`undefined`** | **`null`** |

**⇒ 缺陷 2 的根因是实测确认的，不是源码推断。**

**降级是静默的**：业务 SPA 正常渲染、无崩溃、compact 判定正确（视口 411px），
`--safe-area-inset-top` 为 `0px`（与 SystemBars 缺席一致）——
功能全可用，只是原生能力没了，界面上**没有任何提示**。
`capabilities.ts` 的 fail-closed 握手保证应用不会谎报能力（这点是对的），但引导页没有提示。

---

#### 两个测量陷阱（本轮踩过，勿重复）

1. **「探活失败」长得和「没被交出去」一模一样。**
   引导页要先 `fetch(origin + '/healthz')` 成功才会 `location.replace`。
   本机后端 `/healthz` 返回 200，但**不带 `Access-Control-Allow-Origin`** ⇒
   WebView 的跨域 fetch 被拦 ⇒ 探活失败 ⇒ **压根没导航**。
   我第一版把这种「什么都没发生」读成了「留在应用内」，据此得出了**错误**的
   「`allowNavigation` 有效、语义是 scheme+host+port 同条配对」——
   并已写进本文档第一版，**现更正**。
   ⇒ **每一次探针都要分别记三件事：探活是否成功、页面最终 URL、有没有 VIEW Intent。**
   少记一件，结论就可能整个反过来。
2. **logcat 计数会被上一轮残留污染。** 有一轮忘了 `logcat -c`，
   把上一条实验的 `ACTION_VIEW` 读成本轮的。**每轮先清。**

#### 现在的真实边界

| origin | 留在 App WebView | 有原生能力 |
| --- | --- | --- |
| `http://localhost:*` | ✅ | ❌（端口非 80） |
| `http://localhost`（引导页自身） | ✅ | ✅ |
| 白名单内非 localhost host（修复后） | ✅ | ❌ |
| 白名单外任何 origin | ❌ 交系统浏览器 | ❌ |

**⇒ 在缺陷 2 被解决之前，「原生能力」只有引导页自己用得上。**
可选路径（**属架构级决策，必须由产品拍板**）：

| 路径 | 代价 |
| --- | --- |
| ① 接受降级，引导页**显式提示**「你已失去原生能力」 | 最小。fail-closed 握手已就位，只差一句提示 |
| ② 改用 `server.url` 直连远端 | 桥随 `appUrl` 生效，缺陷 2 自然消失；但**与「壳内不打包 web/dist、改 Vue 只发 Web」的既定架构冲突** |
| ③ 换注入作用域（自建 `MainActivity` 调 `addDocumentStartJavaScript` 覆盖 `*`） | 绕过 origin 限制，但等于对**任意 origin**开桥，**放弃白名单**；且与 SystemBars 插件抢 WebView 初始化时机 |

**在此之前不应投入任何原生能力开发**（含 H4 手势，它依赖原生层）。


### A-1 已修复（2026-10-04 实测），**原先那条修复路径是多余的**

**[已修复]** 症状：API 35+ 状态栏**白底 + 浅色图标 = 白底白字**，完全不可读。

| 采样点 | 修复前 | 修复后 |
| --- | --- | --- |
| 状态栏中部 | `RGB(250,250,250)` 白 | **`RGB(15,17,21)`** |
| 页面底色（引导页） | `RGB(15,17,21)` | `RGB(15,17,21)` 不变 |
| 底部手势区（避开手柄） | `RGB(100,100,100)` 灰 | **`RGB(15,17,21)`** |
| 业务页状态栏 | 白 | **`RGB(15,17,21)`** |

**真因不是「WebView 版本太老」。** 模拟器 WebView 是 **134.0.6998.135**（低于 140），
而原先记的修复路径是「升级到 ≥140 再重测」——**那条路径不但没验证过，
而且根本不需要**：

1. `targetSdkVersion = 36` ⇒ Android 15+ **强制 edge-to-edge**，
   `android:statusBarColor` / `navigationBarColor` **被系统忽略**（styles.xml 里已注明）。
2. 状态栏区域露出的是**窗口背景**，而 `AppTheme.NoActionBar` 把 `android:background` 设成了 `@null`
   ⇒ 该区域是系统默认白。
3. `SystemBars.style: 'DARK'` 其实**生效了**（图标确实变浅色）——
   于是就得到最坏的组合：**浅色图标压在白底上**。

**修法**（`styles.xml` 一行，不碰 WebView、不碰 `setDecorFitsSystemWindows`）：

```xml
<item name="android:windowBackground">@color/shellSurface</item>
```

`shellSurface` = `#FF0F1115`，与 `webDist/index.html` 的页面底色本来就是同一个值。
重编重装后：引导页与业务页状态栏均为 `RGB(15,17,21)`，无白屏，底部手势区同步变深。

**⚠️ 遗留的观感问题（不是缺陷）**：业务 SPA 的登录页是**浅色**底，
而系统栏按 `style: 'DARK'` 恒为深色 ⇒ 浅色页顶部有一条深色带。
原先的注释假设「业务 SPA 都是深色」，该假设对登录页不成立。
**现在至少是可读的**（修复前是不可读），要不要按页切换栏色属后续观感优化。