# Handoff — 245 总览页「按处理队列」旧版回退 + 首开无数据根修（R51 增量轮，2026-09-22）

> 承接：R50（/v1/messages count_tokens + usage 全 0 根修，commits 634edc2e6 / ddb6a09b0，245 release 2167）。
> 本轮：审计用户报告的两个总览页问题并根修。代码 commit `993a41a1b`（origin/main）。

## 一、结论 / 根因

### 问题 A：245 总览页是旧的，没有「按处理队列」显示

**根因（部署管线）**：`scripts/deploy-seamless.sh` 的 `--no-frontend` 只跳过
`npm run build`，但第 4 步 `host_stage_release` 仍**无条件**把检出
`web/dist` 打进 bundle。245 源码检出的 `web/dist` 是一个多月前的旧构建
（早于 V3.2「按处理队列」合入的 850b1ca15），因此连续多轮
`LLM_GATEWAY_PREBUILT_BINARY + --no-frontend` 部署（2162/2163/2167）把线上
前端**整体回退**：线上 bundle `index-ECZm52dw.js` 里 grep 不到任何
`按处理队列` 字符串（已实测为 0 命中）。

**修复**：`--no-frontend` 语义改为「二进制更新，web 沿用线上」——上传后新增
5.5 步 `carry_forward_web_remote`：解析远端 `current` 符号链接，把线上
current 发布的 `web/` 原样顶替新 release 的 staged web。安全性依据：
`SHA256SUMS` 只覆盖二进制/version.json/VERSION/configs，不覆盖 web，远端
替换不影响 bundle 校验。边界：无 current（首次部署）/ current 即新版本
（同 seq 重跑）/ current 无 web / 远端拷贝失败 → 降级保留 staged web 并显式
warn。前端构建路径（不带 --no-frontend）行为不变。

### 问题 B：首次打开总览页直接停在「按处理队列」不请求数据，切到「按供应商」再切回才有数据

两个结构性成因（修复合并在 QueuePerspectivePanel）：

1. **hasData 整体门控**：模型分组/请求轨迹分区嵌在
   `v-if=!hasData → 空态 / v-else → qp-layers` 的 v-else 里。`hasData`
   要求 queue envelope 已接线（`queue.wired`）；dispatch 未启用的环境上
   queue 恒为 null → 整屏只剩「队列数据未接入」一行，模型节点与轨迹全部
   不可见，且切 tab 不改变 queue 状态本身。
2. **首开竞态无重试**：`loadModelScope()`（featured + 近 3 天热门 → resolve
   模型范围）只在挂载时执行一次。首次挂载早于登录态/网络就绪时请求整体
   失败 → scope 为空；此前唯一恢复路径是切走再切回（v-if 切换触发卸载/重
   挂载 → 重新执行 onMounted），与用户描述完全吻合。

**修复**：
- 模板重构：空态只声明队列层；stats/队列深度收进 `<template v-if=hasData>`；
  模型分组分区（独立由 `hasReportedRawModels` 门控）与
  RequestProcessingTrail/NodeDetailDrawer 移出 hasData 门控独立渲染。
- `FIRST_SCOPE_RETRY_MS=3000`：挂载 3s 后对空 scope 自动补一次
  `loadModelScope`（scope 已有数据零开销跳过），定时器随 onUnmounted 清理。

## 二、改动文件与关键行为

| 文件 | 改动 |
|---|---|
| `scripts/deploy-seamless.sh` | 新增 `carry_forward_web_remote()` + 上传后 5.5 步（仅 `--no-frontend` 时执行）；`--no-frontend` warn 文案更新 |
| `web/src/components/QueuePerspectivePanel.vue` | 模板解除 hasData 整体门控；`FIRST_SCOPE_RETRY_MS` 首开空 scope 3s 重试 + 卸载清理 |
| `web/src/components/QueuePerspectivePanel.test.ts` | +2 钉桩：queue 未接线时模型分组+轨迹仍渲染（空态共存）；fake-timers 首开失败→3s 重试恢复 |

注意：**`--no-frontend` 现在永远不会把新前端带上线**（沿线上 current）。
要让前端变更上线必须走带前端构建的部署（245 检出 node v20 + npm 10.8 +
node_modules 齐备，`npm run build` 可用）。

## 三、测试命令与结果

- `bash -n scripts/deploy-seamless.sh` → SYNTAX_OK
- `npx vitest run src/components/QueuePerspectivePanel.test.ts` → **31/31
  passed**（29 存量 + 2 新增）
- `npm run typecheck`（vue-tsc）→ 改动文件 0 错误；输出仅有的 8 个
  `annotation.ts` locale 重复键错误为 main 预存（已用 `git stash` 对照实证）
- `npm run build` → 41.14s 成功，新主 bundle `index-D8_v6AnE.js`
- 245 端到端（见下）部署记录与验证

## 四、245 部署与验证记录（实况）

- 检出 `git pull` → 993a41a1b；podman 静态重建二进制 `llm-gateway-go-r51`
  （BUILD_OK，63,963,664B）。
- **2168 两次尝试均失败**（healthz OK → readyz 候选 503，
  `{"database":null,"redis":connected}`，600s/1200s 窗口均超时；旧实例保持
  服务未受影响）。事后定位这不是瞬时库负载：并行李轮 commit
  `beda1cd89`（fix(db): session_summaries 回填段 5s 快失败——30s rolconfig
  烧穿启动预算的部署 blocker 根修）正是该 readyz 卡死的根修。
- **实际收口来自并行会话的 2170-beda1cd8 部署**（已含本轮全部修复：
  `git merge-base --is-ancestor 993a41a1b beda1cd89` 实证）。线上验证：
  - active=8782，`/api/system/version` → build_seq 2170 / git_sha beda1cd8
  - `slots/8782/web/index.html` → 新构建 `index-DSu0mxtq.js`（哈希异于此前
    所有历史 bundle，证明是 beda1cd89 源码的全新前端构建）
  - 线上 bundle grep 命中「按处理队列」→ **问题 A 线上已消除**
- 245 检出同步至 `d56b3aeac`（最新 main，含修复版 deploy-seamless.sh，
  `carry_forward_web_remote` 就位）；并行会话遗留的 version.json 部署戳
  已 stash 保留（`deploy-stamp-2170-beda1cd8`，信息另存于 releases/2170）。
- carry-forward 端到端回归（破坏检出 dist + `--no-frontend` 重跑断言沿用
  线上 web）**未执行**，见遗留风险 1。

## 五、遗留风险

1. **carry-forward 只到脚本级验证**：`carry_forward_web_remote` 经 bash -n
   与代码审读，未做端到端回归（破坏检出 dist + --no-frontend 部署断言沿用
   线上 web）。降级分支（无 current 首次部署）同样未端到端执行。
2. **问题 B 的线上确认停留在"源码 ancestry + 全新构建哈希"级**：单测钉桩
   了两条修复路径（31/31 绿），但浏览器级 e2e（清 localStorage 首开、延迟
   登录竞态）未做；若线上仍复现，下一轮抓 /api 401 时序。
3. releases/2167 目录保留了 22:59 手工覆盖的混排 web 快照（新旧资产并存，
   功能可用）；回滚到 2167 会回到混排目录。active 已轮换到 8782/2170，
   slots/8781 不再服务。
4. annotation.ts 八语言 TS1117 重复键为 main 预存，建议下一轮顺手清。
5. 245 检出 dist 仍是旧构建——修复版脚本下 --no-frontend 不再staged它，
   但每次 bundle 会多带一份陈旧 web 上传（无害，浪费带宽）。

## 六、下一轮提示词（建议）

> R52 入口：1) 浏览器实测 245 总览页「按处理队列」首开（清 localStorage
> + 慢网/延迟登录模拟），确认无"切 tab 才有数据"；2) 清理 annotation.ts
> 八语言 TS1117 重复键；3) 复查 releases/2167 混排 web 是否需要清理归档；
> 4) R50 遗留：grok-4.6 no_candidate（凭据侧）与 154 部署 R50+R51 仍未执行。
