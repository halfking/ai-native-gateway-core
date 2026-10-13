# r1029 —— ErrSessionExpired 误判 store fault：auto_summary 503 重试循环与误导性 FORBIDDEN 文案

> 2026-10-14 凌晨轮。提交 725372743（+AGENTS 合并解决 69463a51e9）。
> 触发：/goal 轮样本提示词两腿 E2E 全绿后的**日志例行扫描**发现
> `SESSION_FORBIDDEN`×4051 / `auto_summary: transient error`×2040。

## 一、取证链（全部实测）

1. `gateway-8782.log`：`auto_summary: transient error, retrying` 2040 次、
   `failed to generate summary` 4051 次，error 形如
   `status 503: {"error":{"code":"SESSION_FORBIDDEN","message":
   "session not owned by this API key",...}}`——503 状态 + FORBIDDEN 文案
   的矛盾组合本身就是线索。
2. 同请求 `safety_net_defer_fired attempt_err_code=session_unavailable`；
   前一行 `session lookup failed error=session expired`。
3. 代码链：`domains/streaming/handler.go` 的 store-fault 分支条件是
   `err != nil && err != ErrSessionNotFound`——**ErrSessionExpired 落进
   503**，且该分支写 `i18n.MsgSessionForbidden`（文案+code 全错）；
   responses/messages 两腿经 `normalizeAndRegisterClientSession` 同构
   （`!errors.Is(getErr, ErrSessionNotFound)` → errClientSessionUnavailable）。
4. 受害者：auto_summary/auto_title 内部回环（gs_/gt_ 分支会话 TTL 短），
   每条用户轮次触发一次必败回环；503 被 `isTransientAutoTitleErr` 判
   transient → 重试一次 → 放弃 → 下一条用户消息再来一遍。

## 二、根因（两个叠加缺陷）

1. **过期被归为 store fault**：过期是确定性终态（Redis TTL 到期），同
   body 重试必再败——语义上 ≈ NotFound（id 保留、会话在同一 id 下重新
   开始），而非「存储暂不可用，稍后重试」。
2. **503 响应体误用 FORBIDDEN 文案**：chat 腿 store-fault 分支写
   `i18n.MsgSessionForbidden`（"session not owned by this API key"）——
   所有权语义的文案把运维归因引向 API key 问题（本次排查首查方向即被
   带偏）。responses/messages 两腿写的是正确的 "session store
   unavailable"，chat 腿独错。

## 三、修复（725372743）

1. `session_routing.go` 新增 `sessionAbsentErr(err)`（NotFound || Expired）；
   `normalizeAndRegisterClientSession` 的 fault 判定排除 Expired、认领
   分支纳入 Expired；chat 腿内联块同改。
2. chat 腿 store-fault 503 响应体改用新 i18n 键 `SESSION_STORE_UNAVAILABLE`
   （messages.go + 8 个 locale 全配）；审计 code `session_unavailable`
   不变（观测口径连续）。
3. 效果：gs_/gt_ 分支会话过期 → 原样保留、摘要正常生成；用户 gw_ 会话
   过期 → 原子认领重新开始（同 key 保留、异 key 仍 403——认领裁决不变）。

## 四、判据（`domains/streaming/r1029_session_expired_test.go`）

- normalize 层四臂：分支 id 保留 / gw_ 原子认领 / 无认领器换新 id /
  **真 store fault 仍 unavailable**（回归守卫）；
- 三层协议全链（chat/messages/responses httptest）：过期会话不得
  503 session_unavailable、不得出现 store/FORBIDDEN 文案；
- store-fault 守卫：503 + `SESSION_STORE_UNAVAILABLE` 在场 + FORBIDDEN
  文案不得回潮。

变异验证：M1（Expired 撤出 absent 家族）→ 三层腿全红，错误信息正是
生产 503 形态；M2（503 文案回潮 FORBIDDEN）→ 文案守卫红。
⚠ M1 首次变异是**无操作**（perl 先改了行首导致第二段模式不匹配，落盘
内容与预期不符仍显示「变异已应用」）——判据纪律第 9 条再+1：变异后
必须 `sed` 读回实际文件内容，不能只看命令退出码。streaming 152s /
i18n / session 全绿。

## 五、登记不修

1. auto_summary 的 `maxRetries=1` + transient 判定保持不变：真 store
   fault 的一次重试是合理语义；修复后过期不再产生 503，循环自然消失。
2. `i18n.MsgSessionForbidden` 的 locale 词条形态未动（本轮只新增键）。

## 六、多会话实录

- 提交时并行会话正往本 checkout 写入 AGENTS.md 追加节与未跟踪审计文档；
  用 `git stash push -- AGENTS.md` → commit 我的文件 → pull → stash pop，
  pop 冲突后**两节都保留**（上游补充验证轮 + 并行会话「网关智能体裁决」），
  其未跟踪文档留给该会话自行提交。
- 本轮 E2E 期间两次撞上并行会话部署窗口（2624→2625），非流式探针
  connection refused 为部署窗口现象，窗口后复验 200。
