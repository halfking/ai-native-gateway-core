# 原厂直通生产验证第 2 轮 —— ClientType 请求边界命中 + UA 线材实态订正（2026-10-29）

> 承接 ㊶（2026-10-28 原厂直通生产验证轮）与其后的 408150bd3（三巡：ClientType
> 生产接线补齐——`ExecParams.ClientType` 在 messages/responses/handler 三个
> 构造点由 `extractClientType(r)` 一次确定，header/UA 成为首选路径）。
> 本轮目标：**部署后用三客户端真实流量确认 ClientType 在请求边界被命中，并
> 确认非原生客户端行为零变化。**

## §0 结论一句话

目标达成，且验证过程挖出并修掉两个真缺陷：**① 真实 MiniMax Code 客户端的
UA 是裸复合词 `MiniMaxCode`，两张 UA 识别表全脱靶（档案记的是
`MiniMax-Code/{ver}`，线上从未出现）→ header/UA 首选路径对该客户端整体
是暗的**；**② 原生对首发全参被 MiniMax 400 拒绝后，reqprobe 剥参重试的
判型链不认识 MiniMax 的 400 方言 ⇒ 无重试、整流 503 —— 同请求非原生对
反而被首发前改写保 200，直通特性倒挂伤到它服务的客户端**。两修均带
正/负臂测试并已随 499a2146 → 5fbba4ef 两次蓝绿上线 211。

## §1 部署身份与核验口径

- 开工时 a 槽 = `2.5.8-5b753fc5-dirty-20261029-211`（build_seq 2897，00:32
  构建）——5b753fc5 含 408150bd3 接线（祖先链实证）。二进制探针：
  `grep -c "native pair bypasses transform" /app/llm-gateway` = 1、
  `nativepair` = 11 ⇒ 特征在场。
- 本轮两次蓝绿部署（均**干净 worktree 钉根** `LLM_GATEWAY_ROOT=/tmp/npv-deploy`，
  避免 tar 整工作树把并行会话的未提交 WIP 带上生产——DD_REPO_ROOT 树移植
  语义下「部署 = 工作树快照」，主 checkout 常驻他人 WIP 时必须钉独立
  worktree）：
  - 第 1 次：`499a2146`（UA 修复①），槽 b 接管，版本串无 `-dirty`；
  - 第 2 次：`5fbba4ef`（reqprobe 修复②），槽 b 重建接管。
- 部署后二进制探针：`minimaxcode` = 1、`native pair bypasses transform` = 1。

## §2 三客户端真实流量盘点（修复前，docker logs http_request / 12h 窗口）

| 客户端 | UA 线材实态 | 12h 请求 | UA 表命中？ |
|---|---|---|---|
| zcode | `ZCode/3.16.3 ai-sdk/provider-utils/4.0.39 runtime/node.js/24` | 46 | ✓（`zcode/` 子串） |
| minimax-code | **`MiniMaxCode`（裸复合词，无连字符无版本）** | 23 | **✗ 脱靶** |
| deepseek-code | 无自然流量 | 0 | — |

抽查 12 条真实 MiniMaxCode 请求的 firehose body：全部**无 metadata、无
thinking 族参数** ⇒ UA 脱靶时（b 表兜底的 `metadata.client_type` 与
`X-Code-Session-Id` 头皆无从取证）该客户端实际被判 unknown。当天影响面 = 0
（无厂商参数可保真），但首选路径不亮是事实。

## §3 发现① 与修复①：UA 线材订正（fdd11735f）

- 两张表同步补 `minimaxcode` 裸形态 case（`domains/streaming/client_fingerprint.go`
  的 `extractClientType` = nativepair 首选链；`telemetry/request_metadata.go`
  的 `ExtractAgentNameFromRequest` = 统计侧——同一事实两处消费，只修一处
  就是口径分叉）。连字符形态与裸形态互不可匹配（连字符断开子串），共存无冲突。
- `docs/client-formats/minimax-code.md` 补线材实测订正块：档案的
  `MiniMax-Code/{version}` 来自 2026-09-21 记录、未在线上复现，识别判据以
  两张代码表为准。
- 测试：两表各补裸形态/带版本/deepseek 双写法用例；nativepair / telemetry /
  streaming 三套件全绿。

## §4 修复前基线（B1-pre，firehose 实证）

UA `MiniMaxCode` + `thinking{type:enabled}`（body 无任何标记）→ 上游首发
（唯一一次）`thinking={"type":"adaptive"}`、200。即：修复前该形态走的是
**非原生路径**（首发前被 `paramreg/translate.go` 的 MiniMax enabled→adaptive
改写），与 UA 脱靶结论互证。

## §5 发现② 与修复②：reqprobe 判型链的 MiniMax 方言断链（52ace2e02）

修复①部署后首轮矩阵：**n3 臂（MiniMaxCode UA → minimax-m3）503**，而同
模型 curl 臂 / zcode 跨厂臂均 200。日志链（request_id e360e8a0590f34094c23…）：

```
upstream_http_attempt attempt=0 upstream_status=400
  → body: "invalid params, invalid thinking.type: \"enabled\"
           (allowed: adaptive, disabled) (2013)"
dispatch: attempt cap reached, giving up attempts=1
goal_retry_exhausted … [client_bug] → 503
```

**首发 `enabled` 原样到达 MiniMax = 直通机制命中的直接证据**（非原生对会被
首发前改写，不可能 400）。但设计文档写明的「上游拒绝由本请求内的 4xx 剥参
重试回退」没有接住：`reqprobe.Diagnose` 对该错误体判「不感兴趣」——
`namedParamPatterns` 七条正则全断链（`invalid thinking.type: "enabled"` 的
参数名在冒号前且带命名空间点段、值带双引号，七条的形状全不匹配）、
`paramRejectionHints` 十五条 hint 也全不命中（`invalid params` 与
`allowed:` 都不在表内）⇒ diagOK=false ⇒ 无剥参重试 ⇒ client_bug 整流 503。
**倒挂**：直通把厂商参数送到上游挨拒，兜底却不认识上游的拒绝方言，最终
原生客户端 503、非原生客户端 200。

修法（named 精准路径，按 `wireKeyAliases` 既有先例，三处）：
1. `namedParamPatterns` 补第 8 条：`invalid <param>: "<value>"`（参数名冒号
   前、可带命名空间点段、值带引号）；
2. `wireKeyAliases` 补 `thinking.type → thinking`（上游命名空间形态 → 我方
   chat 线体顶层键）；
3. `extractNamedParam` 成员判定改走 `resolveWireKeys` 别名扩展——直查顶层键
   会空手而归（strip 侧本就支持别名，只有 diagnose 侧断链）。

测试：正臂用**线上实捕错误体**（含 2013 错误码原文）断言
`param_rejected` / `thinking.type` / 剥离落点 `thinking` / messages 骨架
存活；负臂断言出站体无可剥参数时仍不判 param_rejected。reqprobe 全套件绿。

## §6 最终矩阵与逐臂取证（5fbba4ef，槽 b）

全部合成请求 body **不含任何 body 标记**（无 `metadata.*`、无 `zcode_*`
工具名、无 `X-Code-Session-Id` 头）⇒ ClientType 只能来自 header/UA——
这是「首选路径被命中」的唯一可证形态。取证用 body 中段 base64 指纹
（须覆盖 tag 区；首版切片 [16:80] 只盖到 model 键导致五臂串号——指纹必须
盖住判别字段）在 raw_data firehose 里定位 request_id。

| 臂 | 身份 | 模型 | 上游首发 body | 尝试数 | 结果 |
|---|---|---|---|---|---|
| z1 curl（非原生对照） | UA curl | minimax-m3 | thinking=**adaptive**（改写） | 1 | 200 |
| z2 zcode（跨厂对照） | UA ZCode | minimax-m3 | thinking=**adaptive**（改写） | 1 | 200 |
| **n3 minimax-code（机制臂）** | UA MiniMaxCode | minimax-m3 | thinking=**enabled**（保真）→ 第 2 次**已剥** | 2 | **200** |
| n1 zcode→glm（UA 路径） | UA ZCode | glm-5.3 | thinking+enable_thinking **全保真** | — | 200 |
| n2 zcode（**header 路径**） | X-Gw-Client-Type + UA curl | glm-5.3 | z-ai 候选**保真**厂商参数 | — | 503* |
| z3 curl→glm（对照） | UA curl | glm-5.3 | z-ai 候选**剥参**；glm 候选保真 | 15 | 503* |
| n4/z4 deepseek 双臂 | — | deepseek-flash | — | — | 503** |

\* glm-5.3 候选簇当时在抖动（窗口内 empty_response ×32 / server_error ×14；
z3 败 15 候选、n1 成、n2 败 24 候选交错——失败与客户端身份无关：同为原生臂
的 n1 成功夹在两次 503 之间）。**z3 vs n2 恰好构成 glm 腿的干净差分**：同一
批 `z-ai/glm-5.3-flash` 候选，curl 被剥参、zcode（header 单信号）保真——
ClientType 在请求边界决定了直通与否，503 是上游可用性问题、与直通正交。
\*\* deepseek-flash 当前 0 候选（双臂一致；与 12h 前尚有自然流量相比属上游
下线，非本轮改动所致）——如实记「未观察到」。

n3 全链路 = 设计文档的逐字兑现：**首发全参 → 上游 400 → reqprobe 诊断
（新方言）→ 剥 thinking → 免费重试 → 200**。

### 学习态漂移（设计内，如实记）

n3 剥参成功后 reqprobe 拒绝即学习生效：p1（curl，学习后）首发 body 的
thinking 从「adaptive 改写」变为「整键剥除」。两者皆 200；这是 reqprobe
24h 滑窗学习机制的既有设计（P0-B），不是 nativepair 回归——但**「零变化」
读数必须在预学习态取**（本轮 z1/z2 先于 n3 执行即为此外序纪律）。

## §7 判定汇总

1. **ClientType 在请求边界被命中（header/UA 首选路径）**：成立。UA 路径
   （n1/n3）与 header 路径（n2，UA 为 curl 仍命中）各有决定性臂；全部请求
   无 body 标记 ⇒ 兜底层不可能代答。真实流量侧：zcode 46 条自然请求全部
   UA 命中；MiniMaxCode 修复后进入首选路径（修复前 23 条全脱靶）。
2. **非原生客户端行为零变化**：成立。z1/z2 与部署前基线（B1-pre 及 23 条
   自然流量形态）逐字段一致：首发前 adaptive 改写、单次尝试、200。z3 的
   503 归因上游抖动（身份无关），n2 同身份同因。deepseek 双臂一致
   （no_candidate，路由层零变化）。唯一漂移 = 学习态 p1（设计内，见 §6）。

## §8 挂账与触发器盘点

- **RISK-6（Anthropic 协议腿直通）**：本轮进行中已被并行轮收口
  （5c8d655c2，Claude Code↔Claude 对，`clientModelPrefixes` 第 4 行 +
  逐站点盖章）——扩容触发器「新厂商工具 → nativepair 加一行 + 补测」被
  该轮如实执行。映射表现 4 对，未过 ~10 对迁移阈值，§3 留接口不动。
- **RISK-2（工具顶层非标准键盲区）**：仍挂账，独立一轮。
- **RISK-9（弱名 body 标记重复解析 0.608ms/3 候选）**：维持不修决策。
- **deepseek-code 腿**：上游 0 候选期间无法实测（上轮 RISK-1 同族「未观察
  到」口径延续）；判型修复（§5）对 deepseek 400 方言同样生效与否，待上游
  恢复后由自然流量或复测臂验证。

## §9 通用式（详见 LESSONS §537）

1. 档案里的客户端形态 ≠ 线上的客户端形态——识别表判据要定期用生产 UA
   分布对账（档案是写入时的快照，客户端会变）。
2. 「兜底链救了功能」≠「首选路径活着」——首选路径验证必须做「只有首选
   信号」的差分臂（无 body 标记 + 单一 header/UA 信号）。
3. 直通类特性的机制证据常常是**上游的 400**（非原生对根本不会把参数送出
   去）——别把上游报错一律当失败。
4. 兜底机制的最后一公里在**错误方言词表**里——新接一家上游 = 把它的 4xx
   文案样本喂一遍判型链，否则兜底断链整流 503，且倒挂伤到特性服务的客户端。
5. 上游点名参数可能是命名空间形态 ⇒ 点名→线体键必须走别名链，且判型侧
   与剥离侧必须同源（同一份别名表）。
6. 「拒绝即学习」是设计内的行为漂移源——零变化验收必须在预学习态取数，
   外序纪律（对照臂先行）要写进验证脚本。
