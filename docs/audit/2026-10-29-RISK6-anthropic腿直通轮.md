# ㊸ RISK-6 收口轮：Anthropic 协议腿（Claude Code↔Claude）接直通 + 逐站点盖章（2026-10-29）

> 承接 ㊲ 登记的 **RISK-6**：「Anthropic 协议腿（`executor_anthropic.go`）未接直通；
> Claude Code↔Claude 原厂对该腿仍走既有改写路径 ⇒ 独立一轮；机制与本轮相同
> （`NativePassthrough` 已在 IR 层就位）」。照 ㊶ 教训：**先枚举该腿的全部
> 序列化站点再补盖章，别按 paramguard 数**。

## §0 结论速览

| 项 | 读数 |
| --- | --- |
| nativepair 表 | +1 行：`"claude-code": {"claude"}`（第 4 条内置原生对） |
| Anthropic 腿接线 | 3 个生效点：S1 盖章 / S2 跳 sanitize / S3 跳 paramguard+learned |
| 站点盘点 | 请求侧 IR 序列化站点全仓 **5 处**全部盖章（chat 腿 3 + inline 1 + anthropic 腿 1） |
| 自证 | 新增 3 测试（Q4 paramguard / Q4 工具形 / Q3 盖章）+ nativepair 表 8 例 |
| 变异臂 | 3/3（摘章 / 恢复 paramguard / 恢复 sanitize ⇒ 各自测试红、其余绿 ⇒ 还原 md5 一致） |
| 验证 | build ✔；nativepair / paramguard / paramreg / executors 全绿（-count=1） |
| 未做 | 真机部署验证（照 ㊴~㊶ 先例，部署后按 zcode/claude-code 实流量复核） |

## §1 生效点盘点（按「谁做序列化」数，㊆=本轮）

| # | 站点 | 位置 | 原生对处置 |
| --- | --- | --- | --- |
| S1 | IR 序列化站点：`ParseOpenAI→SerializeAnthropic`（Q3 转换，本腿唯一） | `prepareAnthropicRequestBody` IR 分支 | **盖 `irReq.NativePassthrough`（本轮补——漏盖 = 厂商 Extensions 在 anthropic 目标被 paramreg 方言裁剪，65d24d0df 在 chat 腿修的同族缺陷）** |
| S2 | `SanitizeAnthropicTools`（IR 分支 + legacy 分支各一次） | 同上两分支 | `!nativePair` 门控（工具形重写族 = chat 腿 SimplifyTools 同款豁免；sanitize 重建工具会**丢弃顶层自定义键**） |
| S3 | `finalizeAnthropicRequestBody`：paramguard(`DialectAnthropic`) + `reqprobeApplyLearned` | 该函数唯一 choke point（IR/legacy 两分支共用） | `!nativePair` 门控（chat 腿同款：学习式预剥离会让一次瞬态 400 把原生参数静默剥离 24h） |
| S4 | `FixAnthropicMessages` / `ValidateAnthropicMessages` | 两分支 | **保留**（结构合规：tool 角色转换/连续合并 —— chat 腿 MergeConsecutiveMessages 同款保留） |
| S5 | 模型替换 / 压缩 / `repairOutboundToolHistory` | 两分支 + attempt 边界 | **保留**（结构性；repair 在 chat 腿本就不受 nativePair 门控） |
| S6 | 响应侧 `ParseAnthropicResponse→SerializeOpenAI` | 仅 Q3 响应方向 | 无请求盖章语义（Extensions 还原是请求方向概念） |

⇒ 全仓请求侧 IR 序列化站点 **5 处**（chat 腿 2966/3057/3122 + inline_validation 55 + anthropic 腿 585）——`grep 'NativePassthrough = '` 与 `grep 'SerializeXxx('` 逐一对拍，无第二处漏盖。

## §2 三条腿的落地形态

- **Q4（claude-code → claude-\*，anthropic 入 anthropic 出）**：无协议转换、无 IR 往返。
  直通 = 跳 S2/S3，body 塑形只剩结构合规 + 压缩 + 模型替换。识别链：
  `X-Gw-Client-Type` / UA(`claude-code/`) 归一 → `nativepair.Match("claude-code","claude-…")`；
  无 body marker（保守不直通 = RISK-7 既有语义，如实登记为识别盲区）。
- **Q3（openai 入 → anthropic-messages 出，如 minimax-code 模型经 anthropic 方言宿主）**：
  IR 往返 `ParseOpenAI→SerializeAnthropic`，S1 盖章使 Extensions（thinking/moderation/
  厂商私有键）全量原样还原（`restoreExtensions` 的 NativePassthrough 分支，IR 层既有机制）。
- **chat 腿（claude-code 经 openai-completions 宿主打 claude-\*）**：`nativePairFor` 与
  既有门控在 chat 腿本就生效，nativepair 表 +1 行后自动覆盖 —— 无需新代码（有表级测试钉住）。

## §3 自证（`native_pair_anthropic_test.go` + `nativepair_test.go`）

1. **`ParamguardSkipped`（S3，Q4）**：claude-code→claude-sonnet-5，`thinking.enabled`
   + `temperature:0.7` ⇒ 原生对 temperature 原样（paramguard `removeSamplingIfThinking`
   不碰首发）；对照臂 cursor→claude ⇒ temperature 被裁、thinking 保留（牙齿在）。
2. **`ToolShapeVerbatim`（S2，Q4）**：工具顶层 `vendor_tool_flag` ⇒ 原生对保真；
   对照臂被 sanitize 桩重建丢弃（桩复刻 `SanitizeAnthropicToolDefinitions` 的丢弃形；
   钩子是 main.go 注入的，本测试钉门控接线）。
3. **`IRStamp`（S1，Q3）**：minimax-code → anthropic 方言宿主，`moderation` ⇒
   原生对原样、对照臂被 paramreg 裁（KindDialectOnly 注册方言 [openai-chat,responses]）。
   ★ **靶字段按目标方言 `knownBy` 真值选**：初版用 `CatalogCode:"minimax"` 对照臂不裁
   —— `BaseProtocol(DialectMiniMax)` 继承 openai-chat 基协议 ⇒ moderation 被认为
   「认识」⇒ 放行。换成 anthropic 目录（DialectAnthropic 不继承 openai-chat）后两臂分离。
4. **nativepair 表**：claude-code × {claude-sonnet-5 / claude-opus-4.5 / Claude-Haiku-4 /
   anthropic/claude-sonnet-5} 命中；× {minimax / glm} 与 cursor/unknown × claude 不命中
   （跨厂不误判）。

## §4 变异臂（3/3，每臂「注入 ⇒ 红且隔离 ⇒ 还原 ⇒ md5 逐字一致」）

- **M1 摘章**（`NativePassthrough = false`）⇒ 仅 `IRStamp` 红。
- **M2 恢复 paramguard**（去 `!nativePair` 门）⇒ 仅 `ParamguardSkipped` 红。
- **M3 恢复 sanitize**（两分支都去门）⇒ 仅 `ToolShapeVerbatim` 红。
  ★ 首次 M3 无效：perl 只替换了第一处（IR 分支），Q4 测试走的 legacy 分支没被变异
  ⇒ 加 `g` 标记两处同变；★ 且第一次跑出 `(cached)` ⇒ **缓存结果不作数**，
  变异臂一律 `-count=1`。

## §5 语义边界（如实记）

- **诚实失败语义**：本腿 `anthropicSameCandidateStripRetry=false`（P0-B 契约：
  Q4 直传体中途改写破坏字节契约）⇒ 原生对首发参数被上游硬拒时**无剥参重试回退**
  —— 与 RISK-1 同语义：修法是给该模型配正确能力表，**不是恢复钳制**。
- **识别盲区**：Claude Code 无 body marker（metadata 块只认 zcode/minimax/deepseek
  三家标记）⇒ 无 header/UA 的形态识别不出 ⇒ 不走直通（保守方向 = 功能未生效
  而非误生效，RISK-7 既有语义）。
- **RISK-2 不动**：工具顶层非标准键在 Q3 IR 往返中的丢失是全客户端一致的既有行为
  （NormalizeOpenAIToolDefinitions 族），本轮只在 S2 门控了**第二遍 sanitize 重建**，
  不在原生对下特判 IR 的工具解析 —— 独立轮修。

## §6 读数

- `go build ./...` ✔
- `-count=1` 全绿：`internal/nativepair` / `internal/paramguard` / `internal/paramreg` /
  `domains/streaming/executors`（44.5s，含原生对家族全部既有测试）
- 变异臂 3/3；还原后 md5 逐字一致
- 共享 go-build 缓存竞态复发一次（`could not import runtime: no such file or directory`）
  ⇒ `go clean -cache` 恢复（R34 既有先例）
