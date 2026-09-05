# 2026-07-24 — Dashboard 筛选弹窗与探测队列并入自检

## Summary

- 实时请求流筛选改为弹窗，避免窄下拉截断。
- 模型维度/筛选使用标准名（canonical），不用供应商 raw。
- 系统监测展示并入系统自检；Dashboard Tab 移除系统监测。

## Files

- `web/src/components/LiveStreamFilterDialog.vue`（新）
- `web/src/components/LiveRequestStreamV2.vue`
- `web/src/views/SelfCheckPanel.vue` / `DashboardView.vue` / `DashboardViewV2.vue`
- `admin/live_stream_redis_store.go` / `admin/probe_dashboard.go`
- `web/src/router.ts` / `web/src/config/appNav.ts`
