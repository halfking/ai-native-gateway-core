# 2026-09-30 方案 B/C 收口

方案来源：`docs/proposals/2026-09-30-stats-ui/plan.md`。

## 行为

- 租户统计的积分查询同时返回入/出 token、缓存读写和平均耗时。日均请求副文案是较前一日，不是上一等长窗口。
- 应用分布行点击后进入该租户密钥 tab，并按应用代码过滤。`?tab=keys&owner=` 会打开密钥 tab 并按账号过滤。
- 用户详情抽屉链到 `/request-logs?owner_user=&preset=d7`，以及 `/tenants/:code?tab=keys&owner=`。
- 请求日志 `owner_user` 只匹配 `request_logs.api_key_owner_user`。

## 验证

`go test ./admin -count=1 -run TestStatsUIBCContract` 与 `UsersView.test.ts`。没有部署 8782。
