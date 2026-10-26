# 2026-10-16 r1033 —— 多模型 tool×脱敏还原 E2E + circuit 镜像准入语义对齐

> 会话目标（用户指令）：特色模型（minimax / z.ai / vapeur）经网关逐模型多轮（~5 轮）
> tool 请求，跟踪 log 修问题、部署再验证直至全绿；重点是参数收发与转换，特别是
> 脱敏介入后客户端必须拿到 tools 参数里的可用真值（IP/用户/密码）；注意检查修正
> 凭据状态；最终本地/245/154/211 四台部署确认。
> 提交：`4cac78979`（circuit 语义对齐）；本地 2666、245=2667、154=2668、211 同代码。

## 一、凭据状态核查（用户点名项）

- minimax 42（hzx-2）/ zhipu 62（zipu-max-account2）：active / closed / ready / ok，全
  目标模型（MiniMax-M3、glm-5.3）绑定 available=t，无需修。
- **vapeur 63：组织余额今日（10-16 04:04–08:14 之间）真实耗尽**——
  `Organization balance insufficient (budget_exceeded)`，grok 与 gemini 两族模型同报，
  近 10 天探测记录中余额错误首次出现即今日。`rate_limited/permanently_exhausted`
  状态是故障阶梯的**正确反应**（force_enable 后单次 429 即打回），不是陈旧状态。
  **这是 owner 充值事项，代码侧不可修**；emergency-repair force_enable 机制本身两轮
  验证有效（availability/quota 正确改写、cmb+nps+URSM 三点联动）。
- 868 迁移（随本轮合并进 main）已把 vapeur claude 系绑定播种为 `non_stream_only`
  （上游拒绝该组合流式）。harness 已补 `--no-stream` 非流式腿（minimax 验证通过：
  5/5 轮、4 次 SSH、零违例），vapeur 充值后用非流式腿测 claude、流式腿测 grok。

## 二、行为级合并冲突：circuit 镜像准入语义（本轮唯一代码修复）

同一早晨两个并行会话在相反基线上各落一个修复，合并后 admin 两测试红
（`TestLivePlanOrderFiltersCredentialCircuitAndCompactsRanks`、
`TestApplyCredentialCircuitGateAgreesWithDispatch`）：

- `29f02985b`（03:49，本机）：`Candidate.UnavailableReason` 撤除 circuit_state 镜像
  准入——DB 镜像是 per-process 断路器的异步有损副本，按它准入会在重启后搁浅健康
  凭据、饿死半开恢复探测（Vapeur 搁浅事故）。
- `035cea8b5`（04:11，远端）：resolve 必须与 dispatch 一致——按旧 dispatch 行为
  （IsAvailable 拦 circuit:open）给 resolve 加了 `applyCredentialCircuitGate` 硬翻转。

**裁决**（依据：dispatch 真实准入链实测）：保留 29f02985b 语义。理由：
1. 发送点的内存态断路器 `Circuit.Allow`（executor_dispatch.go，半开探测归属地）
   是熔断的唯一权威——安全性不受影响；
2. resolve 看不到内存态，只能镜像 DB 可见的准入层（lifecycle/availability/quota/
   balance）——若按镜像硬翻转，重启后 resolve 比 dispatch 更保守，分歧以相反
   方向复活；
3. routeguard ForbiddenColumns 早已禁止镜像进 SQL conjunction，同构逻辑不应在
   resolve 响应层破例。

**落地**：`applyCredentialCircuitGate` 保留镜像字段展示（节点抽屉熔断列）但不再
翻转 Routable；balance_zero 硬拦截保留（dispatch 仍拦已知非正余额）；livePlanOrder
的 `IsAvailable` 过滤不动（它调的就是 dispatch 同一函数）。三个测试按新 dispatch
真值改写并保留反分歧精神；变异验证 M1（镜像阻断加回→4 测试红）/ M2（balance 门
删除→BalanceGate 红）有牙，恢复后全绿。

**教训（判据纪律候选第 10 条）**：并行会话在同一故障域（同一台机的 grok-4.6/211
cred 63）先后落相反方向的修复时，合并会过文本冲突关但不过行为关——合并后必须
跑**双方**各自的判据；一方的 precondition 断言（"production must block X"）本质上
是在钉对方的行为，谁后改语义谁就要连带改对方的钉子。

## 三、E2E 战役结果（harness 判据 A–E，D=真值逐字比对）

本地（2665→2666）：minimax 4/4 + batch2 28/32 + 重试 4/4（失败全为上游 429 限流
节奏与 think burn，重试一次全过）+ zhipu 4/4 + 非流式腿 1/1。含 12 个项目目录
（gateway-2/memora/nbjjl/crm-go/GEOFlow/network-switch/agent-companion/RedClaw/
acc-toolkit/marketing/myvpn/nbjl）。chat+responses × merge+audit 全组合，5 轮 tool，
thinking=medium。

245（2667，8781，经 ssh 隧道）：minimax 4/4 + zhipu 4/4 **全绿**。
154（2668，8781，同 key 共享 SECRET_KEY）：minimax 4/4 + zhipu 4/4 **全绿**。
211：见 §五。

**脱敏回路介入取证（前提在场证明，防"压根没启用"的假绿）**：raw-logs 解码战役
请求的 upstream post_conversion 帧：tool result（config dump）出站为
`{SENSITIVE:account:N}/{SENSITIVE:secret:N}/{SENSITIVE:email:N}` 占位符（23 处），
私网 IP（10.24.7.91/.92）按设计保留（内网豁免）；assistant 历史 tool_calls 里
密码/用户名是**占位符**（客户端回传的还原真值被网关再遮蔽）；客户端侧判据 D
零失配（收到的 host/username/password 与注入真值逐字相等）。即完整闭环：
**遮蔽(→模型) → 占位符还原(→客户端) → 再遮蔽(→模型历史)**。
观察登记（未动，非本轮判据范围）：OPS_PHONE 在 tool-result lane 未遮蔽（电话类
不在 account/secret/email 遮蔽集）。

## 四、部署与验证

| 台 | 版本 | 端口 | 验证 |
|---|---|---|---|
| 本地 | 2666-4cac7897 | 8782 | E2E sanity 5/5 轮零违例 |
| 245 | 2667-4cac7897 | 8781 | E2E 8/8 全绿 |
| 154 | 2668-4cac7897 | 8781 | E2E 8/8 全绿 |
| 211 | 同代码 docker | 8781 | 见 §五 |

铸 key：245 `/api/auth/token` + `/api/keys`（alias `e2e-r1033-20261016`，id 200），
154 共享 SECRET_KEY 直接复用。

## 五、211 验证（docker）

- 部署：`deploy-211-docker.sh --via 192.168.31.211`（无参模式在 macOS 上会被正确
  refuse，提示 --via）。迁移腿重放正常（already applied 序列 + revision sequence
  completed）；**Redis 库号复用 db5（不再漂移——cbea71f88 的库号复用修复首次实战
  验证生效）**；readyz ready 且 DB/Redis 真连（full 模式，非 lite 降级）。
- 版本戳 `4cac7897-dirty`：--via 同步树无 .git 的既有已知形态，非脏构建。
- 本机旧 key（id 204）在 211 已失效（invalid_key）——211 库快照比 r1030b 时代
  更新过，重新铸 key `e2e-r1033-211`（id 205）。注意 /v1/models 对无效 key 返回
  **空列表而非 401**，探活时别被 200+空数据误导。
- **E2E：minimax 4/4 + zhipu 4/4 全绿**（与 245/154 同判据同 harness）。

## 六、遗留登记

1. **vapeur 组织余额耗尽**——owner 充值后：本地/生产各跑 force_enable + 战役
   （claude 用 `--no-stream`）。余额错误 10-16 首现，与 r1026 时代的键 TTL/回环
   问题无关（那些已修）。
2. gemini-3.5-flash 直探「该供应商不支持 Responses API」（探针 transport 与上游
   能力错配，probe 侧既有登记项族）。
3. glm-5.1 探测 InvalidParameter、gpt-image-1「生成类模型不支持此接口」——绑定
   目录级拍板项（owner），与 r1027 时代登记同族。
4. OPS_PHONE 未入 tool-result 遮蔽集（见 §三）——是否纳入属策略拍板。
