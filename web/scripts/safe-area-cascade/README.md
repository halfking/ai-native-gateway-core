# safe-area cascade 取证 harness

**这不是门禁**，是**人工取证**用的页面。跑完把渲染出的文本读走即可。

它回答一个问题：`var(--safe-area-inset-*, env(*, 0px))` 这条 cascade
在真实 CSS 引擎里，能不能把壳注入的值送到 `--app-safe-*`，再送到消费点的布局声明。

## `prod.css` 是什么（别当成最新的）

它是 **2026-10-06 18:12 一次构建的产物快照**，SHA-256 前 24 位 `8d875147bcd9f297fbf14f8d`，
用于让 §4.6.34 的那组数字**可复核**，**不是当前最新产物**。
跑之前请按上面第 1 步覆盖它；构建后 `.hyper-app` 的 scope hash 会变，
harness 里的 `data-v-6ff24b80` 也必须同步核对。

## 为什么需要它

源码里「我写了 cascade」不等于「值真的走通了」。
`web-mobile/dist/assets/index-*.css` 是发布物，harness 量的就是它，
而不是源码 substitute，也不是 jsdom（jsdom 不解析 `var()` 级联）。

## 跑法

```bash
# 1) 用**真实产物**替换 prod.css（不要用 src/styles/theme.css 顶替）
cp web-mobile/dist/assets/index-*.css web/scripts/safe-area-cascade/prod.css

# 2) 起服务
python3 -m http.server 8900 --bind 127.0.0.1 --directory web/scripts/safe-area-cascade

# 3) 浏览器打开 http://127.0.0.1:8900/harness.html，只读渲染出的文本
```

## 判读

| 步骤 | 期望 |
|---|---|
| A 注入前 | 四个 `--app-safe-*` 全 `0px`，`.hyper-app` padding `0px / 0px` |
| C 注入后 | 四项与注入值**逐项相等** |
| D 消费点 | `.hyper-app` 的 `padding-left` = 注入的 left |
| E 移除注入 | 四项回到 `0px`（回落链没把值钉死） |

任一步 ❌ ⇒ cascade 或消费点断了，**别只看「我写了」就当通了**。

## 边界（重要）

- 这是 **Chromium 桌面**引擎，**不是 Android WebView**。
  它**不证明** Android 上 `env(safe-area-inset-*)` 的运行时值，
  也**不证明** Capacitor 的 `evaluateJavascript` 真机上一定落库。
- 页面里的 scope 属性 `data-v-6ff24b80` 来自产物；
  **重新构建后 hash 会变**，跑之前请核对产物里的 `.hyper-app[data-v-*]` 选择器并同步。

台账：`docs/UI规范/10-审计与实施路线.md` §4.6.34（实测结果） / §4.6.33（订正）。
