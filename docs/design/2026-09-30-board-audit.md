# 看板与统一日历 · 批判审计（2026-09-30）

对照 `docs/design/2026-09-30-board-and-daterangepicker-mockup.html` 的 A–E 节，核对已推送实现。下面只写核对过的事实。

## 结论

布局骨架（运维 chip、4 张英雄卡、8 张次要卡、供应商区、筛选条、模型表 + 趋势、5 张分布卡、`KxDateRangePicker`）和 D 节天数页迁移是在的。上一轮 changelog 把「成本卡已按用量排序」写成完成，但还有三处和效果图或真实数据不一致，本轮改了代码并补了测试。

## 已核对、本轮未改

- 裸 `input type=date|datetime-local|month` 与页面里的 `el-date-picker` 由 `dateInputUnification.test.ts` 守卫；`PENDING_MIGRATION` 为空。D 节 7 个天数页要求出现 `KxDateRangePicker`。
- 粒度手动档、余额聚合接口、真 xlsx，效果图 E 节写明不做。当前粒度是只读「自动 + 桶宽」，导出仍是 CSV。
- 看板默认范围仍是「今天」，不是效果图触发器上画的「近 7 天」。那是原 `BoardTimeRange` 默认值，本轮没有改默认，避免把所有人的窗口悄悄拉长。

## 本轮修正

1. **模型分布多了一列费用。** 效果图 A 节列是模型 / 请求 / Token / 占比，E 节写「不虚构费用列」。实现却渲染 `pies.models.cost_usd`，并标成 Top 8。该字段在供应商饼图上已经被证明经常是 0，模型列同样不能当窗口费用。已去掉费用列，改为 Top 6。占比在指标缺失时不再算出 `NaN%`。
2. **成本卡在用量没回来时回退到饼图。** `d8695f4a2` 只在 `usageRows.length > 0` 时按用量成本排序，否则仍按 `pie.cost_usd` 取前 8。用量请求失败或尚未返回时，页面会先铺 `$0` 的 `__other__`。效果图画的是一行 5 张。现改为只按用量 `total_cost_usd` 取前 5；没有用量行就空着，并显示错误。积分仍按代码或名称回查饼图。
3. **RPM/TPM 分母把今天还没到的小时算进去。** `today` / `7d` / `30d` 的终点是 `now`。自定义范围（昨天以外的「本月」「上月」以及任意含今天的区间）的终点是结束日次日 00:00 UTC。含今天时，分母多出剩余小时，速率被压低。`boardRangeElapsedMinutes` 把终点封到 `now`。已结束的历史窗口仍用完整跨度。第一分钟内分母最低按 1 分钟，避免除零。

## 8782 实测（热更新，不是镜像重建）

构建后拷进 `llm-gateway-local-8782` 的 `/opt/llm-gateway-go/web/`（容器中途重启过一次，最后在跑的是 `index-DBog8mOc.js`）。登录打开 `/dashboard?tab=board`（默认今天）：

- 模型表标题是 Top 6，表头是模型 / 请求 / Token / 占比，没有费用列，数据行 6。
- 成本卡 5 张，按窗口成本：suyun $3.38、apigpt $0.19、Vapeur $0.0061、apiclaude $0.0033、火山方舟 $0.0001。没有 `__other__`。
- 次要卡 8 张。余额在这台库上有值（例如 suyun $8000），不是一律「—」。
- 评分徽章 0 个。`GET /api/admin/report-rollup/summary?start=2026-09-30&end=2026-09-30&view=provider` 返回 200，但 `snapshot_dates` 为空、没有 `providers`。同一接口在 2026-09-23 到 2026-09-30 有 7 个快照、18 家供应商。默认「今天」看不到评分，是因为今日快照还没落，不是前端把字段读错。没有用昨天的分数冒充今天。

容器重建后这份静态文件会丢。

## 遗留

- 积分不在用量行上，只在饼图 key 等于 `provider_code` 或 `provider_name` 时能对上。这次 5 张卡对上了（21 / 7 / 199 / 27 / 249），换一批 key 对不上时积分会是 0，页面不会标明「未匹配」。
- 评分同样只按 `provider_name` 精确匹配，而且依赖已落盘的 report snapshot。今天的窗口经常没有快照。
- 英雄卡总费用和供应商窗口成本不是同一个数。实测默认今天：英雄卡 `$0.0033`，suyun 一张卡就是 `$3.38`。英雄卡读 `request_stats_minute.cost_usd` 的合计；供应商卡和用量表读用量接口的 `cost_usd`（`admin/usage.go` `usageByProvider`）。效果图 E 节禁止改这两套后端口径，所以本轮没有把英雄卡改成用量合计，也没有反过来。页面上这两处会继续对不上，直到分钟表的 cost 写全。
- 请求体/响应体的「峰值」紧贴在单位后面（`KBPeak`）。次要卡 `small` 加了左边距。Vue 会吃掉插值前的空格。
- `BoardProviderSection.vue` 仍超过 300 行。本轮只把排序抽到 `providerCards.ts`，没有拆模板。
- 工作区里还有未提交的会话目录和安装器改动，不属于本审计，没有并进这次提交。
