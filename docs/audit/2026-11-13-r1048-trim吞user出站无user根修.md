# r1048 · mechanical trim 吞掉全部 user 消息 ⇒ 出站零 user ⇒ 智谱 1214「messages 参数非法」根修

> 2026-11-13 goal 会话。owner 报障（ZCode 报错信息）：
> `request rejected: the request body failed the provider wire format validation
> Reason: messages 参数非法。请检查文档。 TraceID: hydrate-trace`

## 一、事故单笔定位（211 只读取证）

- 请求：`3aa12b3234cce584f121fe4d31fdd40e`，2026-11-13 09:09:25+08，ZCode/3.18.0 →
  `POST /v1/chat/completions`（host llm.itestu.cn），model `glm-5.3-flash`，stream，
  会话 `gw_23a0614a-215c-46ae-9b11-f9237f453a7b`（磁盘清理自动化长会话，turn 884–1030）。
- 上游：provider 32 `open.bigmodel.cn/api/coding/paas/v4/chat/completions`，400
  `{"error":{"code":"1214","message":"messages 参数非法。请检查文档。"}}`（139ms 快拒）。
- 网关侧链路：1214 命中 `errorsx.invalidRequestFormatRe`（`messages[[:space:]]*(参数非法|…)`）
  → `KindClientBug` → client_bug 终态不转移（`dispatch: attempt cap reached`）→
  `requestShapeRejection` 把上游原因渲染成 400 回给客户端（流式 200 信封里的错误帧）。
- **7 天窗口内仅此 1 笔**；同会话 turn 884–1029 全部成功（上游实测 prompt_tokens 88–92K）。
- 容器 01:08:29Z 重建（同版本 6a9335855 重启）与事故同分钟——**无关变量**（本缺陷与进程状态无关）。

## 二、证据链（四层，全部亲验）

1. **出站 body 本体**：`session_bodies_hot` 该会话 `kind='final_full'`（09:09:26 写入，
   38943 jsonb 压缩字节）＝实际出网消息数组：**863 条 system + 2 assistant + 4 tool，
   零 user**。863 条 system 里 693 条 `<total_tokens>N tokens left</total_tokens>`、
   87 条 TodoWrite 提醒、64 条「user hasn't heard from you」——ZCode hydration 协议每轮
   注入的 system 消息（turn 884 的 request_delta 开头即 `<total_tokens>` system 消息实证）。
2. **缓存输入**：07:55:41 写入的上一笔 final_full（853 system + 2 asst + 2 tool，同样零
   user）被 07:56–09:06 的各请求合并小 delta 后**成功**发往智谱——证明智谱接受
   system 为主的奇形 body，**唯独零 user 不行**。
3. **管线数字全对上**：客户端 17MB（含附件）→ IR 解析+附件卸载 970,373B/1179 msgs
   （=缓存 857 + 新 delta 322，指纹锚点 client[:857]==cache 命中）→ strip 去旧工具回合
   （149 锚点+结果）→ 872 → **trim 丢 3 条** → 869 → IR 868 → 出网 395,303B。
   丢失的 3 条＝该 body 仅有的 **3 条 user 消息**（delta 头部的 system-reminder 注入、
   「Earlier history reduced」压缩声明、「请继续」）。
4. **本地全真重放**（真实缓存+真实 delta 走仓内真实代码）：merged 969,557B（生产
   970,373，差 0.1%）→ strip 后 {system:853, asst:4, tool:3, **user:3**} → trim
   dropped=3、census user 3→0，**先红复现**。

## 三、根因（两个缺陷合流 + 一个上游语义）

- **D1（致命）**：`transformation.trimOldestPairs` 的 `dropExtent` Case 3 从头按对丢弃，
  头部是 `[user,user,user]` 时 n=2 起步、又把第 3 条 user 当「工具回合的 user 确认」吞并
  （`n+1 < len(rest)` 守卫只保证「后面还有消息」，不保证「user 不被清零」）——
  **一轮吃光全 body 仅有的 user**。修复前循环逐迭代实证：iter1 dropExtent=3 全是 user。
- **D2（放大器）**：trim 循环的预算 `current+estimate(system)` **只算消息、不算
  envelope（tools 等）**——863 条 system ≈67.8K est ≤ softLimit 76.8K 即退出，而真实
  body（含 225KB tools）est 114K 远超。即便预算正确也会先丢 user（D1 在前），但 D2 让
  trim 看起来「已经收敛」。对照：aggressive 路径 `compressMessagesWithTarget` 已有
  envelope 预留（`messageBudget := softLimit - estimatePromptTokens(emptyBody)`），
  default 路径漏了同款——**登记不修**（改预算=全局改 trim 激进度，风险面大于收益）。
- **上游语义（实证）**：智谱 coding 端点对**零 user 消息**的 body 回 1214
  「messages 参数非法」。活体对照（211 临时 key id=250）：经 /v1/messages 发一条
  assistant-only body → **逐字复现 owner 报错**（网关渲染层原样输出）。同形状带 1 条
  user 的 869 条消息 body → 200；869 条/862 连 system/630KB 大 body → 全部 200。
  网关自己在 chat 入口就有 `HasUserMessage` 强制（`no_user_message` 400）——
  **入口有守卫、出站无守卫**：压缩层悄悄制造了入口自己都不放行的 body。

## 四、修复（transformation 层，两原语统一守卫）

`domains/transformation/ctx_compress.go`：

- 新增 `ensureUserSurvived(original, trimmed)`：trim 结果若零 user 而原 body 有 user，
  则把**最新一条非 tool-result 载体**的 user 消息回插到首条非 system 消息之前
  （历史上它比幸存者都旧，顺序单调；Anthropic 面首消息本来就必须 user，正好对齐）；
  user 本就是 tool_result 载体（Anthropic 形态）时不回插（回插=孤儿 tool_result）；
  WARN 留痕 `ctx_compress: trim would drop the last user message; restored newest user turn`。
- 接线两处：`trimOldestPairs` 重组返回前 + `dropOldestMessageUnit`（aggressive 续丢循环
  原语）。两个原语覆盖全部 4 个调用点（default openai / aggressive openai / anthropic /
  续丢循环）。
- 行为面：正常会话（user 多、在尾部）永不触发守卫；仅「trim 会把 user 清零」的病态形状
  触发，多保 1 条 user（几百字节级）换取上游不拒收。

## 五、门与验证

- 钉测 6 条（`r1048_trim_last_user_test.go`，合成数据——生产 body 含私有会话内容不入库）：
  事故形状端到端（900 system+3 user+rounds+tools pad，断言 trim 后 ≥1 user 且 system 仍在
  头）/ 原语级最新 user 回插 / Anthropic tool_result 载体跳过 / dropOldestMessageUnit 守卫 /
  无 user 原 body no-op / user 幸存 no-op。
- 变异：摘除两处 `ensureUserSurvived` 接线 → 6 测红（MUTATION×2 处）→ 还原绿。
- 包面：transformation + hooks/compression + streaming/executors(23.8s) + streaming(97.3s)
  全绿；build/vet/gofmt 干净。
- 真数据回归（本地、未入库）：/tmp 捕获件（finalfull_0755+delta1030）走真实管线，
  修复前 user 3→0 红、修复后 user=1 绿。

## 六、211 部署验证（部署后回填）

- 部署：冻结树蓝绿，新槽接管后 readyz 200。
- 活体验证：
  1. **事故形状重放**（真实捕获件 1179 msgs + tools，fresh session，chat 端点）→
     修复前形状必 400 1214，修复后期望 200；
  2. 原因性对照（/v1/messages assistant-only）维持 400 ——上游语义，非回归；
  3. WARN 行在部署后日志可 grep（`restored newest user turn`）。
- 临时 key id=250 验后 revoked。

## 七、遗留裁决面（登记不修）

- **D2 default 路径 trim 预算不含 envelope**（见 §三）：修=改全局 trim 激进度，归 owner。
- ZCode hydration 协议累积 853 条 system 注入（≈400KB/turn 常驻）：上游接受（实证
  630KB/869 条全 200），属客户端设计；若未来要省 token 可在网关做 system 提醒合并，
  独立项裁决。
- 压缩兜底模型当日无候选（minimax-text-01/gemini-2.5-flash 配额/禁用闸，日志
  `compaction: no candidate`）⇒ LLM 摘要层缺席、mechanical trim 独扛——运营面处置。
- `requestShapeRejection` 的分类与渲染**本就正确**（确实是不合法 body，只是网关自己
  造的）；修复在制造侧不在渲染侧。
- 会话 `gw_23a0614a` 已死（失败后无新请求）；其 v2 缓存里仍是零 user 的 final_full——
  该会话若复活，首次请求合并后 trim 守卫会兜住（出站必带 user）。

## 八、编号与推送

- 本轮编号 **r1048**；LESSONS **§718**；主档＝本文件。
- 双仓推送（codeup + github）：修复笔 + 本档笔。
