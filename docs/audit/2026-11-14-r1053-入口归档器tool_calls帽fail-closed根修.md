# r1053 — 入口归档器 tool_calls 64KB 帽 fail-closed 根修（gpt-6-astra「split the protected input」第四案）

> 2026-11-14 01:1x+08 goal 会话。TraceID 97781748 / request 656ad240，model=gpt-6-astra，
> provider_code=context_budget_exceeded，422 retryable=false。211 在役 3045-161e2950
> （r1052 修复 79631ce8 已确认是其在役祖先——本案不是回归，是**新的第四处同族 fail-closed**）。

## 一、取证

- **入口拒绝不进 request_wal**（r1052 同款）；`http_request` 仅一行：1,059,055B / 422 / 97ms / 297B 响应体。
- 预算门无专项日志 ⇒ 取证钥匙＝session-archive 归档件：
  `gw_staging_ee2683c3…/large-v1/requests/7029f49d…/raw.json` **1,059,055B 逐字节吻合**。
- 形状画像：371 消息 / 99 tools（110,666B）/ **0 个图片 part** / 最大消息 96,721B（idx 59）。
  同会话族另两个 ~1.02MB 请求均**低于** 1MiB 阈值（1,048,576）未触发压缩腿——本笔是第一笔
  跨阈请求；会话仍在增长 ⇒ 不修则每笔必败（砖化，r1052 同族）。
- **先红复现**：真身重放（归档件 + 仓内真实 Reduce，包内临时 env 门测试）
  ⇒ `ErrProtectedContext` 逐字复现（371 spans，used=110,760）。
- **插桩定位**：`metadata.go` `fileMetadataBounded` 的 **tool_calls 字段 69,901B >
  materializeItem(64KB) 帽** ⇒ `read(p, materializeItem)` ⇒ `ErrProtectedContext`
  ⇒ 整个 Reduce 失败（metadata 循环 i=59，spanLen=70,113）。

## 二、根因＝第四案（改写层制造入口自己都不会放的形状）

- 消息 #59 是 assistant 消息，`tool_calls` 字段 ≈69.9KB——ZCode Write 工具的
  arguments 携带整个文件内容，一条消息合法携带 >64KB 的 tool_calls。
- r1052 修的是**媒体**三处（fileMetadata 媒体块/消息级媒体字段、≤64KB 消息
  Uncountable、objectExcerpt 超大非字符串）；**tool_calls >64KB 是同族的第四处
  fail-closed**，与媒体无关——纯文本 1MiB body 照样命中。
- **关键事实（决定修法安全性）**：`fileMetadata` 的输出只喂 `describeMessage` 抽
  role + 工具链 id（calls/refs 依赖图），**从不进入最终 body**——受保护消息从
  RAW 原样读回（`retained`），摘要面（objectExcerpt）只读 content/text/parts/output
  键、永不读 tool_calls。⇒ 元数据面只需保 id，arguments 载荷省略零保真损失。
- **用户之问「压缩算法失效了吗」的答案**：没有失效。session 压缩腿工作正常
  （同日 glm-5.3-flash 444→251 消息 / 78.4% savings 实证）；失效的是入口
  >1MiB Reduce 收敛腿里的**元数据提取器**——它的内部 64KB 预算帽打到了合法
  生产形状上，把「一条消息太大」升级成「整个请求拒绝」。三次事故
  （fable-5-2 媒体 / opus-4-8 半窗帽 / gpt-6-astra tool_calls）是三个不同
  环节的同一类病：**fail-closed 边界缺容错降级**。

## 三、修复（显式省略，与 r1052 裁决同构）

`metadata.go`：
1. `tool_calls` 为数组 ⇒ 逐元素 `toolCallIDStub`：只保 `id`/`type`（≤1024B/键），
   arguments/function 载荷显式省略；元素数 >`maxMetadataCalls`(1024) 截断省略
   （不 fail）；id 缺失时打 `gw_tool_call_omitted` 标记（不无声消失）。
2. `tool_calls` 非数组（畸形 wire）⇒ ≤64KB 照旧整体读；超帽打桩省略继续。
3. 每元素元数据预算 128B（`*remaining` 扣费），256KB/消息预算不变（结构性
   内存守卫，登记观察项：900+ content 块 × 256B/块 + 大 tool_calls 理论仍可
   触顶——复合形状未实证，维持 fail-closed 语义）。

## 四、门与验证

- 钉测 4（`r1053_tool_calls_reduce_test.go`，合成数据）：历史中部巨型
  tool_calls（68KB 单 call）收敛 + 最新 user 幸存 + arguments 不泄漏｜尾部
  活动工具链精确保留 + id 闭合｜40 call 并行形态 id 全保｜>1024 元素超额省略
  不 fail 且保护尾原文不外溢。
- **变异**：还原旧 64KB 帽 fail-closed（编译通过）⇒ 钉测 4/4 红 → 还原绿。
  ★ 变异臂首跑栽在正则替换（编译红非断言红，§710④ 教训重演）⇒ 改精确
  锚点替换。★ 钉测首版断言写错（中段工具链期待闭包保活）——保护集语义
  ＝tail 之后 + 闭包传播，中段工具链本就属可裁历史；**先读 protectInfo 再
  写断言**。
- **真实归档件重放：422 → 200**。1,059,055B → 119,788B（缩减 88.7%），
  371 消息结构保留，`Reduced=true`。
- `internal/sessioncontext` 全包绿（含 r1052 媒体族回归）。

## 五、登记不修 / 观察项

1. **ContextBytes 恒 1MiB 固定**（main.go:8461 未配，`LargeIngressConfig` 默认
   `1<<20`）——模型无关的入口收敛目标。r1051 的 dispatch 级预算门（模型窗口
   感知）在其下游兜底，两层面分工成立；若未来某模型窗口 <~260k tokens 需重审。
2. **fileMetadata 256KB/消息元数据预算**：复合形状（900+ content 块 + 大
   tool_calls）理论可触顶——维持 fail-closed（内存守卫），登记观察。
3. ZCode 会话在役增长无上界：>1MiB 后每笔走 Reduce，受保护集（system+首尾
   human+活动工具链）单条 >~930KB 仍拒（设计行为）。

## 六、部署与验证

- **211 部署**：`2.5.9-1f6e44c8-dirty-20261114-211` build 3046，槽 a 接管（8781），readyz/公网版本身份 OK（1f6e44c8 三探）。
- **活体验证矩阵**（铸临时 key id=256，验后 revoked；密钥文件 shred）：
  - glm-5.3-flash × 事故同形状合成体（1,140,235B + 68KB tool_calls 单 call）：**200**，模型正常作答（修复前该形状 422 必败）。
  - gpt-6-astra × 同形状：**503 + alternatives**（路由层无候选的既有形态，candidate_diag 有实证：23 offers 全被 manual_disabled/quota/provider_disabled 闸滤掉）——**关键判据＝不再是 422 bounded context**，入口 Reduce 腿通过；候选闸处置归运营面（与本案无关）。
  - 部署后 15 分钟窗口 `bounded context` 日志 **0 条**。
- 砖化会话 gw_staging_ee2683c3 暂无新请求（ZCode 侧未重试）；修复在役后该会话下一笔请求即恢复（Reduce 对同形状已实证收敛）。
- 取证坑回填：铸 key 的 `docker exec` 又一次栽在缺 `-i`（stdin 不转发，§609 同族第四次）——**psql 必须走 `docker exec -i`**。
