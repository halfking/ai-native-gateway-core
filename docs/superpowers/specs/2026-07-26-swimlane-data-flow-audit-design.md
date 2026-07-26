# 实时请求流泳道：数据流审计与修正设计

**日期**: 2026-07-26
**状态**: 待评审
**取代**: `2026-07-26-swimlane-optimization-design.md` 中的「显示方向」决定（见 §0）

## 0. 与前一份 spec 的冲突及裁决

`2026-07-26-swimlane-optimization-design.md`（Status: Approved，提交 `5782a23c`）要求
**newest on LEFT**（右→左填充），并已由提交 `fad4683e` 实现为后端 DESC 下发 +
前端取头部。

本轮用户指令明确要求相反方向：

> 数据从向右填充满整个泳道，请求也是从旧到新排列。新的记录要放在尾部，
> 超过宽度了就将最左的挤出去。

**裁决**：以本轮用户指令为准，方向改为 **旧→新（最新在右尾）**。前一份 spec 的
「Issue 3 / Phase 2 显示方向」章节作废，其余章节（内存优化、大小写筛选、
可见性优化）仍然有效。

作废理由需记录在案：方向本身是产品偏好，无技术优劣；但**两轮相反改动叠加**
已经在代码里留下互相矛盾的注释（`SwimLane.vue:492` CSS 注释仍是 ASC，
JS 是 DESC），这类漂移本身就是缺陷来源，必须一次性对齐。

## 1. 背景：跳变的真实根因

用户报告「泳道数据经常跳变，有翻页感」。审计后确认**主因在前端合并逻辑，
不在 Redis**。三个缺陷叠加：

### 缺陷 1：`mergeTilesById` 顺序与渲染取片方向相反（最严重）

`fad4683e` 把后端改为 DESC 下发、渲染改为取头部 `slice(0, max)`，但
`liveStreamStore.ts:696-704` 的 `mergeTilesById` 未同步修改，仍将新 tile
`push` 到数组**尾部**：

```
现有(DESC): [n, n-1, ..., n-19]
新请求 X  : incoming = [X, n, n-1, ...]
drop 阶段 : 保留全部现有 → [n, n-1, ..., n-19]（顺序不变）
append    : X 未命中 → push 到尾 → [n, n-1, ..., n-19, X]
渲染      : slice(0, max) 取头部 → X 被切掉
```

后果：**新请求在已存在的泳道里根本不显示**，直到该泳道被整体替换
（首次出现、或快照生效）才突然重排 —— 这就是「翻页感」。

`fad4683e` 的提交信息声称修复了「新请求被截断丢弃」，实际只修了一半。

### 缺陷 2：`snapshot_refresh` 恒等被跳过

`liveStreamStore.ts:382-392`：

```ts
if (incomingTs && maxSeenTs && incomingTs <= maxSeenTs) return
```

`maxSeenTs` 被**每个 delta 的 tile 时间戳**不断抬高（396-406 行），而快照的
`latest_request_ts` 来自同一份 Redis 数据，二者通常**相等** → `<=` 成立 →
永远跳过。周期性对账从不生效，本地状态与 Redis 持续漂移；一旦某次快照 ts
严格更大（例如标签页切走导致 delta 被丢弃后），才整体重灌 → 大跳变。

### 缺陷 3：页面隐藏处理方向是反的

- `liveStreamStore.ts:766-770`：隐藏时消息**照收后丢弃**。后端照算照推，
  未减压；本地状态静默腐烂。
- 恢复可见时调用的 `requestSnapshotRefresh()`（813-822 行）**只设标志位并
  打日志，未发起任何请求**，契约未兑现。

缺陷 3 是缺陷 1、2 的放大器：切走再切回后，本地已丢失若干 delta，
`maxSeenTs` 冻结在旧值，随后第一个 ts 更大的快照触发全量重灌。

### 已确认无需修改的部分

- Redis 维度队列**已经**是精简 tile（`rid/ts/st/ek/p`，67B），主队列存裸
  `request_id`，详情在独立 STRING（4h TTL）。用户建议的「队列只存 id +
  基础信息」已由 2026-07-26 的改动完成。
- 模型筛选的**比较逻辑已忽略大小写**（`LiveRequestStreamV2.vue:59-61, 344-350`）。

## 2. 设计原则：渲染结果不依赖消息历史

根治思路一句话：**任意时刻的渲染结果只由服务端状态决定，与消息到达顺序、
重复、迟到无关**。

这是可测试的强不变量（§7 用属性测试断言），也直接排除了所有"漂移累积 →
突然纠正"类跳变。

## 3. 前端数据层（`liveStreamStore.ts`）

### 3.1 tile 合并改为确定性排序

废除「就地追加」。收到 delta 时，对每条 changed lane 用 incoming 数组整体
替换，随后按 `(ts ASC, request_id ASC)` 排序并截断到上限。

Vue `TransitionGroup` 依据 `:key="tile.request_id"` 复用组件实例，
**对象引用换新不会重跑进入/离开动画**，因此整体替换不产生闪烁。
排序键含 `request_id` 作为 tie-break，保证同毫秒时间戳下顺序稳定。

上限取 `LiveStreamLaneVisibleLimit`（20，与后端一致），避免前端缓存比后端
权威数据更长而产生"后端已 trim、前端还留着"的不一致。

### 3.2 快照对账守卫修正

`maxSeenTs` 不再作为快照守卫的比较基准。改为每个 scope 记录
`lastAppliedSnapshotTs`，仅当 incoming 快照**严格更旧**时跳过：

```ts
if (incomingTs && incomingTs < lastAppliedSnapshotTs) return
```

相等时应用（同一份 Redis 状态，应用后幂等，配合 §3.1 无视觉变化）。
`maxSeenTs` 保留用于其它用途，不再参与此判断。

### 3.3 可见性：保持连接，暂停渲染，可见时全量拉取

按用户选择保持 SSE 连接。

- 隐藏时：不写入状态（沿用早退），置 `missedWhileHidden = true`
- 恢复可见：若 `missedWhileHidden`，调用已有的
  `POST /api/admin/live-stream/trigger-snapshot` **真正拉一次全量**，
  应用后清标志并恢复增量

这兑现「重新展示时全部拉取，然后再更新」。

### 3.4 顺带修复

- `closeConnection` 移除的是 `recomputeMaxVisible`，而 `openConnection`
  注册的是闭包 `onResize` —— 监听器泄漏，每次重连累积一个。改为持有同一引用。
- 删除死代码 `handleLaneIdleCheck`（444-501 行，从未被调用）。

## 4. 渲染层：方向与组件封装

### 4.1 方向反转

后端继续 DESC 下发（不改后端排序，减少改动面）；前端在 §3.1 排序时统一成
ASC，渲染层直接顺序输出：

- `SwimLaneTrack` 取**尾部** `slice(-max)`（保留最新 max 条）
- 渲染 ASC：左旧右新
- 溢出时最左（最旧）被挤出

动画同步改向：新 tile 从**右**滑入（`translateX(20px)`），离开的旧 tile
向**左**滑出（`translateX(-20px)`），`swim-tile-move` 保留位移过渡。

### 4.2 挂载瞬间的跳变

`SwimLane.vue:116-124` 的 `maxVisibleTiles` 在 `trackWidth === 0`（测量完成前）
返回 `total`，即先全量渲染、测量后再截断 —— 挂载时可见一次跳变。改为测量
完成前返回保守估算值，避免首帧过量渲染。

### 4.3 组件职责收敛

`SwimLane.vue` 中已失效的 `visibleRequests` / `renderedRequests` /
`isTileHighlighted` / `isTileDimmed`（128-178 行）与 `SwimLaneTrack` 重复，
且 template 已改为把 `lane.requests` 直传 `SwimLaneTrack`。删除死代码，
职责固定为：

- `SwimLane.vue`：标签、统计、诊断入口、轨道宽度测量
- `SwimLaneTrack.vue`：**纯展示** —— 接收 tiles + 配置，负责取片、排序无关的
  渲染与动画
- `RequestTile.vue`：单个色块的样式与内容

`SwimLaneTrack` 保持无副作用、无数据获取，仅由 props 驱动，便于 §7 反复测试。

同时删除两个组件里重复的动画 CSS（`SwimLane.vue:507-544` 与
`SwimLaneTrack.vue:82-105` 内容重复，实际生效的是后者，因为 tiles 由它渲染）。

## 5. Redis 存储与可靠性（阶段 2）

### 5.1 渲染信息只存一份，不随队列复制

用户建议「详情进库」。但泳道渲染需要 16 个字段
（model/vendor/provider/status/error_kind/latency/cost/tokens/probe…），
若把这些塞进精简 tile，会被复制到**最多 10 个队列**：

| 方案 | 每请求 Redis 占用 |
|---|---|
| 现状：67B 精简 tile ×10 + 244B 完整 JSON ×2 | ~1158B |
| 加宽 tile 到 ~170B ×10，详情进库 | **~1700B（更差）** |
| **队列只放 request_id (36B) ×10 + 渲染 tile 170B ×1** | **~530B** |

采用第三种：

- 维度/状态队列 member 退回裸 `request_id`
- `llmgw:live:req:{id}` 只存**渲染 tile**（不含 bodies 等详情）
- 去掉 tenant 副本 —— `request_id` 全局唯一，租户隔离由「读哪个队列」决定，
  现在 global + tenant 各存一份完整 JSON 是纯冗余
- 详情（bodies、完整记录）只在 DB；点开 tile 已走 `GET /api/logs/{id}`

改动比听起来小：快照重建本来就是 `request_id` → 批量取详情的流程，
代码结构不变，只是 member 解析和 payload 变窄。

### 5.2 `Record()` 原子化

`live_stream_redis_store.go:280-437` 是 GET-then-pipeline 的**读改写**：
先读旧 payload 决定要从哪些旧队列摘除，再在 pipeline 里执行。两个针对同一
`request_id` 的并发更新（in_progress 与终态几乎同时到达）会**都读到旧值**，
各自计算摘除集合 → 重复成员或丢失更新。这是 Redis 侧真实的跳变来源。

改为 Lua 脚本，把「读旧值 → 从旧队列摘除 → 写新值 → 加入新队列 → trim」
做成一次原子调用。时间戳单调性检查（318-323 行）一并移入脚本内，
消除 check-then-act 竞态。

### 5.3 旧数据迁移

队列 member 格式变更（精简 tile JSON → 裸 request_id）采用**兼容读取，
自然过期**：读取时按首字符判断（`{` 开头当 tile 解，否则当 request_id）。
队列 TTL 24h，一天后旧格式自然消失，无需停机或清库。

## 6. 模型筛选标准化

`LiveRequestStreamV2.vue:386-400` 的 `availableModels` 按**原始大小写**去重
（392 行注释「Keep original case for display」），因此 `GPT-4o` 与 `gpt-4o`
会产生两个选项。

改为按小写 key 去重，保留一个规范展示名（优先后端 canonical 名）。
tile 入泳道时的 model 也统一走同一个规范化函数，与后端
`normalizeModelKey`（trim + 折叠空白 + 小写）语义保持一致。

比较逻辑已忽略大小写，保持不变。

## 7. 测试策略（先数据，后显示）

用户要求「大量数据测试与验证，先从数据上验证，再从显示上验证」。分三层：

### 7.1 Go 单测（数据可靠性）

- 并发 `Record()` 同一 `request_id`（in_progress 与终态竞争）→ 断言队列
  无重复成员、无丢失
- in_progress → success 转换 → 断言旧维度不残留
- 乱序 / 迟到更新 → 断言时间戳单调性不被破坏
- 队列 trim 边界（第 21 条进入时第 1 条被移除）
- 维度值为空 / unknown / other 时不建队列

### 7.2 Vitest 数据层（不变量 —— 核心）

对 `liveStreamStore` 做**属性测试**，这是验证「无跳变」的关键：

- 生成上千条随机 envelope 序列（乱序、重复、迟到、快照与 delta 交错）
- 断言 1：最终渲染顺序 == 按 `(ts, request_id)` 排序的服务端权威状态
- 断言 2：**相同服务端状态、不同到达顺序 → 相同渲染结果**（无历史依赖）
- 断言 3：新请求到达后必定出现在可见窗口的**右端**（回归缺陷 1）
- 断言 4：快照与 delta 携带同一 ts 时，快照被应用且渲染无变化（回归缺陷 2）
- 断言 5：隐藏期间丢弃的更新，在恢复可见后经全量拉取被补齐（回归缺陷 3）

### 7.3 Vitest 组件层（显示）

`SwimLaneTrack` 为纯展示组件，可直接喂数据断言 DOM：

- 方向：DOM 中第一个 tile 是最旧、最后一个是最新
- 溢出：超过 `maxVisible` 时被挤出的是最旧的
- key 稳定性：同一 `request_id` 在多次更新后复用同一 DOM 节点
- 动画 class：新增触发 enter、移出触发 leave

`package.json` 目前无 `test` 脚本（仅 `i18n:check:strict` 间接用 vitest），
补 `"test": "vitest run"`。

## 8. 清理

审计中发现的确定性缺陷，一并修掉：

- `admin/swim_lane_init.go:83-100`：SQL 选取 `request_at, model,
  original_model_family, credential_name, credential_code, status`，
  而 `request_logs_hot` 实际列名为 `ts, client_model, …, request_status`
  —— 该端点**必定 500**
- `admin/live_stream_sse.go:584` `needsFullRefresh`：定义后从未调用（死代码）
- `admin/live_stream_sse.go.bak2`：67KB 残留备份文件
- `SnapshotRefreshInterval` 双默认值（`main.go:1431` 30m 与
  `live_stream_sse.go:243` 2h）—— 统一并注明生效值
- `live_stream_redis_store.go:20,23` 文档漂移：注释称主队列存 JSON（实为裸
  request_id）、TTL 2h（实为 24h）

## 9. 分阶段上线

**阶段 1（前端为主，跳变主因）**：§3、§4、§6、§7.2、§7.3、§8 前端部分
→ 合入 main → 部署 245 验证跳变是否消除

**阶段 2（Redis 重构）**：§5、§7.1、§8 后端部分
→ 在干净基线上做，出问题容易定位

分阶段的理由：跳变主因在前端，阶段 1 即可验证效果；若与阶段 2 同时上线，
245 上一旦仍有异常，难以区分是前端合并还是 Redis 格式引起。

## 10. 验收标准

- 泳道方向：左旧右新，新记录出现在右端，溢出时最左被挤出
- 新请求到达后**立即可见**（回归缺陷 1）
- 连续观察 30 分钟以上无整体重排（覆盖原 30 分钟快照周期）
- 切走标签页 10 分钟再切回：先全量刷新到最新，随后平滑增量
- 模型筛选选项无大小写重复项，筛选忽略大小写
- §7 三层测试全绿
