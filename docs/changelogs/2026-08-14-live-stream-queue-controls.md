# Live Stream Queue Controls

## Summary

修复实时请求流“按处理队列”节点数据缺失和顶部控制栏在窄屏堆叠过高的问题。

## Changes

- 保留异步缓存节点 provider，移除后置无效 SQL provider 覆盖。
- 已接入但队列深度为零时显示“当前无排队请求，调度链路畅通”。
- 展示最近三条请求的网关、路由、节点和结果轨迹。
- 实时流顶部控制栏保持单行，在自身容器内支持鼠标和触控横向滚动。
- 增加空闲队列和移动端控制栏 CSS 契约回归测试。

## Verification

- `pnpm exec vue-tsc --noEmit`
- `pnpm exec vitest run src/components/LiveRequestStreamV2.responsive.test.ts src/components/QueuePerspectivePanel.test.ts src/composables/liveStreamStore.test.ts`
- `pnpm build`
- `go test ./admin/... ./cmd/gateway/...`
- `go vet ./admin/... ./cmd/gateway/...`
- 245 build 1519：SSE 初始节点和 `node_update` 均为 33 个节点。
- Playwright：430px 控制栏 `clientWidth=372`、`scrollWidth=1222`、高度 49px，可滚动到末端连接/暂停/缓存控件。

## Rollback

```bash
bash scripts/deploy-seamless.sh rollback 245
```

代码回滚可直接 revert 本次提交。
