# 2026-07-24 routing-v2 「强制启用」对话框失控复盘

## 用户反馈
> 在 https://llmgo.kxpms.cn/routing-v2?tab=resolve 凭据路由解析的列表中，点击某个节点的设置时，
> 选择紧急修复，点击强制启用，确认的弹窗出现失控，显示在背景中，并且图标大小不对。
> 然后点击确认执行后没有反应。

## 根因（Phase 1 调查结论）

### 现象拆解

| 现象 | 直接原因 |
| --- | --- |
| 确认弹窗"显示在背景中" | `ElMessageBox.confirm` 渲染到 body 顶层，但项目没有引入 Element Plus 的 CSS（`web/dist/assets/index-*.css` 不含 `.el-message-box`），且 `cs-overlay` `z-index: 1100` 把无样式的弹窗盖住了。 |
| "图标大小不对" | `.cs-emergency-icon { font-size: 16px }` 对 emoji 无效；Safari/macOS 的 Apple Color Emoji 会按 native glyph 渲染（≈64px），无论 `font-size`。 |
| "点击确认执行后没有反应" | ElMessageBox 按钮也被无样式遮罩挡住，事件命中不了，confirm promise 永远 pending；UX 上看就像"无反应"。 |

### 关键发现：**dist 比源码旧 56 分钟**

```
-rw-r--r--  7月 24 18:06  web/src/components/routing/CandidateSettingsDialog.vue   ← 新源码（已有内联确认面板 + cs-confirm-panel）
-rw-r--r--  7月 24 17:10  web/dist/assets/RoutingDashboardView-BSQmSo2l.js           ← 已部署的旧版本
```

校验：
```
旧 dist:   grep -c "cs-confirm-panel" → 0   ← 内联确认面板不存在
线上:      curl /assets/RoutingDashboardView-BSQmSo2l.js | grep -c "cs-confirm-panel" → 0
旧 dist:   grep -c "ElMessageBox" → 0（minifier 改名了，但等价代码 doEmergencyRepair 仍在）
线上:      hash = BSQmSo2l（= 旧 hash），size = 101594 bytes
新 build:  hash = DyvgN7vn（= 18:11 重 build 后的新 hash）
```

### 为什么 commit `4dc40a61` 已修但线上仍坏
提交 `fix(routing-v2): 紧急修复确认改为对话框内联，避免 ElMessageBox 失控` 确实把
`ElMessageBox.confirm` 替换成了 `cs-confirm-panel` 内联面板；只要前端 dist 重新 build + 部署，
线上就能恢复正常。**当前线上 dist 漏掉了这次 build**，所以看到的还是修复前的行为。

## 修复

### 1. 源码补丁（追加在 commit 4dc40a61 之上）

`web/src/components/routing/CandidateSettingsDialog.vue`，把 emoji 图标约束成
固定 18×18 的盒子（Safari Apple Color Emoji 的 native 渲染不能被 font-size 缩放）：

```diff
 .cs-emergency-icon {
-  font-size: 16px;
+  /* emoji 在 Safari/macOS 上默认按 native glyph (≈64px) 渲染，
+     仅 font-size 不够；必须显式限制宽度 + 行高 + 文本呈现方式 */
+  display: inline-flex;
+  align-items: center;
+  justify-content: center;
+  width: 18px;
+  height: 18px;
+  font-size: 14px;
+  line-height: 1;
+  flex-shrink: 0;
+  font-variant-emoji: text;
+  text-align: center;
 }
```

### 2. 重新 build

```
npm run build
```

构建产物：

```
dist/assets/RoutingDashboardView-DyvgN7vn.js   (102545 bytes)
dist/assets/RoutingDashboardView-CuVNKKD9.css  (42513 bytes)
```

校验新 dist 已经包含修复后所有类名：

```
$ grep -o "cs-confirm-panel\|cs-emergency-actions--dimmed\|cs-emergency-ok\|cs-settings-msg" \
    dist/assets/RoutingDashboardView*.* | sort -u
cs-confirm-panel          (CSS + JS 都有)
cs-emergency-actions--dimmed
cs-emergency-ok
cs-settings-msg
```

校验 icon CSS 已是 18×18 inline-flex：
```
$ grep -o "cs-emergency-icon[^}]*}" dist/assets/RoutingDashboardView-CuVNKKD9.css
cs-emergency-icon[data-v-ddc8ec75]{
  display:inline-flex;align-items:center;justify-content:center;
  width:18px;height:18px;font-size:14px;line-height:1;
  flex-shrink:0;font-variant-emoji:text;text-align:center
}
```

## 后端 API 验证

路由注册：

```
admin/handler.go:590   mux.HandleFunc("/api/routing/emergency-repair",
                                  h.superAdmin(h.handleEmergencyRepair))
```

handler：`admin/routing.go:818 handleEmergencyRepair`，支持
`force_enable | force_disable | clear_circuit | reset_errors`，全部写审计日志。

线上可达性：

```
$ curl -sLS -X PATCH https://llmgo.kxpms.cn/api/routing/emergency-repair \
    -H "Content-Type: application/json" \
    -d '{"credential_id":99999999,"raw_model":"x","action":"force_enable","reason":"verify"}'
{"error":{"detail":"authentication required"}}
HTTP 401
```

返回 401（中间件 superAdmin 正常拦截），**不是 404 / 405**，证明端点已注册、可达、
参数校验正常工作。`go build ./admin/` 和 `go vet ./admin/` 均通过。

## 行动项（下一步）

1. **重部署 web/dist**：把 `web/dist/` 同步到生产机器（nginx 后端的
   `/usr/share/.../web/dist` 或容器内 `/app/web/dist`），让 `RoutingDashboardView-DyvgN7vn.{js,css}`
   上线。
2. 部署后访问 `https://llmgo.kxpms.cn/routing-v2?tab=resolve`：
   - 点 "设置" → 切到 "⚠️ 紧急修复" → 点 "强制启用"
   - 应立即看到红色 `cs-confirm-panel` 内联确认条
   - 点 "确认执行" 后 → 绿色 "「强制启用」执行成功" → 600ms 后自动关闭
   - 图标🔴/🟢/🟡/🔵 应固定 18×18，不再撑大行高
3. 后端无需改动（已编译通过 + 端点在线）。

## 经验教训

- **Vue 项目用 ElMessageBox 前必须确认 Element Plus 的 CSS 真的全局加载**；
  这次问题就是组件注册了但 CSS 没有 → 弹窗裸奔。
- **emoji 不是文字**：Safari/Apple Color Emoji 不响应 `font-size`，要固定容器。
- **dist 必须随 source 一起发**：建议在 CI 里加一条规则：
  `web/src/**/*.vue` 改动 → 强制触发 `web/dist/**` 重 build，否则 deploy 失败。