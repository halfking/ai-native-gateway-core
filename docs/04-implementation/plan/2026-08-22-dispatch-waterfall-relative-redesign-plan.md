# 队列瀑布图相对时间重设计（2026-08-22）

**状态**：Deployed 154（build **1666** / git `f687be4e`）  
**验收 URL**：https://llm.kxpms.cn/dispatch/waterfall  
**范围**：前端 `web/src` 调度瀑布页。不改 API / DB / ring。  
**决策**：问卷选定 Chrome 网络面板式相对瀑布；绝对时钟 Gantt 本轮不做。

## 154 部署验收（2026-08-22）

| 项 | 结果 |
|---|---|
| `version.json` | `2.5.0-f687be4e-20260822-1666` build_seq **1666** |
| lazy chunk | `assets/DispatchWaterfallView-DuZXTxgZ.js` |
| `renderItem` | **无**（旧 ECharts Gantt 已移除） |
| 新指纹 | `样本中位构成` · `qwt-legend` · `minmax(140px, 200px)` 网格 |
| 浏览器实测 | 登录后 50 样本；两行图例（排队/执行）；点行开右侧抽屉 + T0–T9 阶段表 |

## 核心表达

页面回答：这条（这批）请求的时间花在 T0–T9 哪一段。横轴从每条请求的 T0 对齐到本页 `max(total_ms)`。

## 9 段间隙（路由不作并列条）

`routing_ms`（T2–T5）覆盖模型队列，只出现在详情数字。

到达 / 总队列 / 入模 / 模型队列 / 选凭据 / 凭据队列 / 获取 / 上游TTFB / 流式。0ms 不画。

## UI

- CSS 网格，不用 ECharts  
- 图例分两行（排队 / 执行），对齐瀑布列宽；列头 `0 — max`  
- 瀑布轨 25/50/75% 参考线；去掉独立 queue/ttfb 列（次要行展示）  
- 空态不挂图例  
- 顶部：样本各阶段中位耗时占比  
- 一行一请求，点行打开右侧抽屉（放大条 + T0–T9 阶段表；routing 仅数字）  

## 验证

vitest；本地打开组件/预览看图例与条带同列；现网无样本时只验空态。
