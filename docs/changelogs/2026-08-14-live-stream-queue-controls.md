# Live Stream Queue Controls

## Summary

修复实时请求流“按处理队列”节点数据缺失和顶部控制栏在窄屏堆叠过高的问题。

## Changes

- 保留异步缓存节点 provider，移除后置无效 SQL provider 覆盖。
- 已接入但队列深度为零时显示“当前无排队请求，调度链路畅通”。
- 展示最近三条请求的网关、路由、节点和结果轨迹。
- 实时流顶部控制栏保持单行，在自身容器内支持鼠标和触控横向滚动。
- 增加空闲队列和移动端控制栏 CSS 契约回归测试。
- 全局顶部导航在移动端使用独立横向滚动；极窄屏隐藏非关键品牌文字和状态摘要，
  主题、语言与用户菜单保持可见。
- 版本标签直接使用 API 返回值，避免 `v` 前缀重复。
- 补齐实时流控制栏和免费资源池倒计时的 8 语言 i18n 键。

## Verification

- `pnpm exec vue-tsc --noEmit`
- `pnpm exec vitest run src/components/LiveRequestStreamV2.responsive.test.ts src/components/QueuePerspectivePanel.test.ts src/composables/liveStreamStore.test.ts`
- `pnpm build`
- `go test ./admin/... ./cmd/gateway/...`
- `go vet ./admin/... ./cmd/gateway/...`
- 245 build 1519：SSE 初始节点和 `node_update` 均为 33 个节点。
- Playwright：430px 控制栏 `clientWidth=372`、`scrollWidth=1222`、高度 49px，可滚动到末端连接/暂停/缓存控件。
- Browser-use：245 build 1522 的 430px 页面宽度为 430px；全局导航 `clientWidth=166`、
  `scrollWidth=610`、最大滚动距离 444px，主题切换后仍无页面级横向溢出。

## Rollback

```bash
bash scripts/deploy-seamless.sh rollback 245
```

代码回滚可直接 revert 本次提交。
