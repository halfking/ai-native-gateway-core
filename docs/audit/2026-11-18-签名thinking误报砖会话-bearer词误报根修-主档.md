# 2026-11-18 签名 thinking 误报砖会话（bearer 词误报）根修 — 主档

- **owner 报障**：ZCode 报错「Provider rejected the model request / Turn execution failed provider=new-provider-2 model=claude-sonnet-5-5 reason=invalid_request status=400 retryable=false / Invalid request body」（TraceID `c7df6154-f1dd-4b4a-a36d-6fc568bb948c`，request `8c7f0963-2b75-4338-889f-552ca82c2f0d`）。
- **结论一句话**：400 由**网关自己**发出——输入脱敏中间件在**带签名的 thinking 块**里检出"敏感内容"后，因签名内容不可改写而**拒绝整个请求**；而那次"检出"是 **bearer 规则把普通英文单词 `token` 当成了凭据值**（模型在 thinking 里引用代码注释「line 38: `Bearer token` 检查」）。误报 × fail-closed ⇒ 7.66MB / 3049 条消息的会话**当场砖化**（客户端重发的还是同一份会话，不可自愈）。
- **修法（两层）**：① `bearer` 规则引擎级可信度守卫（`credibleBearerCandidate`：值须含数字，或 JWT 形态 ≥16 且 ≥2 个点；纯字母词一律豁免）；② 签名 thinking 命中时**不再拒绝**——只检测不改写、原样放行＋WARN 观测（类型/规则，不含用户原文），且**不铸占位符、不动 offset**（旧路径先铸造后拒绝，会污染会话字典）。

## 一、取证链（211 全实测）

1. **request_logs / 容器日志**：`http_request_start`（`POST /v1/messages`，`request_bytes=7662838`，UA=ZCode/3.18.2）→ 57s 后 `v2 pipeline: preflight error ... no healthy provider for model claude-sonnet-5-5`（落 v1）→ **`sanitize_middleware: sanitize failed — invalid sanitizable request: sensitive signed thinking`**（14:45:42.099）→ `http_request status=400 response_bytes=85`。**全程零 upstream 尝试** ⇒ 400 是网关自发的。
2. **响应体形状对拍**：85 字节 = `writeSanitizeFailure` 的 `/v1/messages` 形态 `{"type":"error","error":{"type":"invalid_request","message":"Invalid request body"}}`（`input_protocols.go`）。ZCode 的 `reason=invalid_request` 是它对任何 400 的归因渲染；`provider=new-provider-2` 是 ZCode 对网关端点的本地命名（与 2026-11-18 security 轮同一结论：**客户端报障原文 ≠ 系统真相**）。
3. **归档件解码重放**（取证钥匙＝session-archive）：`request_envelope` 无 body，但 large-ingress 把原文落在 `/app/data/session-archive/default/gw/gw_9d6e5d49-…/requests/eba04b74…/raw.json`（zstd 分帧压缩，`internal/largebody` codec；按 `ref.json.size=7662838` 精确定位）。解码后用**仓内检测器离线重放**（exported API，`cmd/zz-repro-r1058` 临时 harness，验后已删）。
4. **重放结果**：3049 条消息；**1072 个 thinking 块里 81 个带签名**（集中在会话尾部＝Anthropic 扩展思考的最终轮）；**唯一带命中的签名块 = msg[3047]**（最后一个 assistant 轮，829 字符），命中 2 处、值均为英文单词 **`token`**（len=5），上下文＝「`test_puller.py` 显示拉取器支持鉴权（line 21: `self.required_token`，line 38: **Bearer token** 检查）」。
5. **规则定位**：`secret.bearer`（YAML/DB 快照/fallback 三处同文）`(?i)\bBearer\s+([A-Za-z0-9._~+/=-]+)` group 1——值捕获组**不区分真凭据与跟在 Bearer 后面的普通英文词**。「Bearer token」是「Bearer 令牌」的普通英文说法，模型在思考里引用代码注释即中招。
6. **同会话面外证据**：其余 ~49 个**无签名** thinking 块的同类误报（the/a/just/for/in/on/flow/NOW/needed/header/passed/masked/tokens/Bearer/aud…211…）全部**静默改写**（无签名 ⇒ 可改写 ⇒ 不报错），证明这族误报量大、只是**带签名的那个**才把请求打死。

## 二、根因定性

- **引爆层（r1057 遗留契约）**：`sanitizeContentBlock` 对「检出敏感 + 带签名」的 thinking 返回 `errInvalidSanitizeInput` ⇒ 中间件 400。r1057（2026-11-17）把「有签名即拒」收窄成「检出且需改写才拒」，但**留下的一档仍然是拒绝**——检测器误报债（r1028 分节标题、r1046 数字 PII、本轮 bearer 词）会让它反复兑现，每次兑现＝一个生产会话砖化。
- **误报源（bearer 规则零可信度判据）**：仓内已有四族先例（`credibleSecretCandidate` / `credibleCardCandidate` / `credibleIDCardCandidate` / `isGitSCPRemoteForm`），bearer 是唯一没有整串守卫的"形状规则"。
- **次生缺陷（本轮发现并一并修掉）**：旧路径**先 `sanitizeText` 铸占位符、后判签名再拒绝**——占位符已进 `worker.mapping`（随后提交进会话字典），而外发 body 根本不含它们 ⇒ **字典被不存在的占位符污染**（r1043 投毒族的近亲）。新路径在铸造之前分流，只检测。

## 三、修法

### Fix B：bearer 规则引擎级可信度守卫（detector.go）
- `bearerShapeSource` 常量＋`isBearerRegex()`（按 **pattern 文本**识别，与 `vendorKeyShapeSource` 同机制）⇒ YAML 装载器（`detector_config.go`）、托管快照装载器（`managed_rules.go`，`r.Regex.String()`）、builtin fallback（`operational_patterns.go`，直接引用常量）三处标记 `bearerShape`。**生产 DB 快照（`sanitize_rule_versions` id=2 的 `secret.bearer` / `secret.bearer.egress`）与常量逐字同文 ⇒ 对已发布快照即刻生效，零 DB 写入。**
- `credibleBearerCandidate(value)`：含数字 ⇒ 可信；否则须 ≥16 且 ≥2 个点（JWT 形态；`e.g.` 这类散文缩写被长度下限挡住）。纯字母值（token/authentication/authorization/internationalization）一律豁免。**已登记代价**：无数字、非 JWT、≥16 位纯字母令牌被漏过（随机 base64 16+ 位纯字母概率 ~3.6%，与 vendor 规则的"须含数字"同一取舍）；既有钉测形态 `Bearer abc.DEF-123`（带数字）继续检出，两代钉测同向。
- 过滤点在匹配循环内（`TypeCreditCard`/`TypeIDCard` 守卫之后），对所有 scope（input/output/egress）同口径——**顺带修掉输出侧同族误报**（r1042 实证过：thinking lane 命中 ⇒ `rewrite_refused` 杀整回合）。

### Fix A：签名 thinking「检测不改写＋放行＋WARN」（input_protocols.go）
- `sanitizeContentBlock` 在改写发生**之前**把带签名 thinking 分流到 `sanitizeSignedThinkingBlock`：`detectInputFragments`（新增 Sanitizer 辅助，与 `sanitizeInputStable` 共用同一检测入口）只检测；有命中 ⇒ **原样放行**（`raw, false, nil`）＋WARN（`action=observe, hits, types, rules`——不含用户原文，security 轮纪律）；无命中 ⇒ 原样放行（与 r1057 行为一致）。**不铸占位符、不动 offset/mapping。**
- **为什么放行是安全的**：① 正常链路里这段文本是**模型自己上一轮的思考回显**（上游已见过并签名过这些字节），原样送回不产生新暴露面；② Anthropic 协议服务端验签——客户端伪造「带签名的 thinking」塞私货会在上游被拒，本路径不构成脱敏旁路（对真正转发 Anthropic 协议的上游）；③ 改写在密码学上不可能，**拒绝只留下「砖会话」一种结局**。
- **残余风险（登记 L2）**：不验签的 Anthropic 协议上游会收到原文；观测靠 WARN 的 `hits/types/rules` 频率（enforce 化裁决归 owner，同 security 轮 L1 模式）。

## 四、验证

- **定向钉测**：`r1058_signed_thinking_bearer_test.go`（bearer 词误报表×三检测器形态〔fallback/configured/managed-同文快照〕、事故 thinking 块原形重放＝逐字节原样＋零占位符、**中间件整链**＝旧契约的 400「Invalid request body」形状不再出现且 body 逐字未动）；`r1057_signed_thinking_false_positive_test.go` 契约更新（签名+敏感＝放行不再拒绝；无签名/空签名照常脱敏）＋新增「放行不得铸造占位符/推进 offset」断言。
- **变异 2/2 红→还原绿**：M1 摘 bearer 守卫 ⇒ 5 组散文误报全红；M2 签名 thinking 退回拒绝 ⇒ 中间件测试红且**复现事故 400 响应体原文**。
- **回归**：`security/sanitize` 全包 126.3s 绿；`domains/streaming`＋`domains/outputcompliance`＋`cmd/gateway`（见 §六）；`go build ./...` 绿；`go vet` 绿；gofmt 经手文件零漂移（包内 4 个 HEAD 既有漂移文件未动）。
- **活体验证**：见 §五。

## 五、部署与活体验证（211）

- **提交与推送**：`c19c80db5`（sanitize 双根修＋r1058 钉测＋主档）→ 并行车先后推 `c80a5ba31`/`568a99433`，我方合并 `7b5006ae3` 后推送 ✅；origin/main 尖端 `568a99433` 同时含本修复与并行车 PI 功能（feat(pi-analysis) 错误详情四区展示）。
- **部署（两列并行车的协作实录）**：本方冻结树两次启动 `deploy-211.sh`（bash5＋`DD_TEST_GO_BIN`，`git worktree add --detach`）均被**本机部署锁**正确拦下（先：并行车在发其本地树 `56fa787f`@3106；后：并行车在发 `568a99433`@3107）⇒ 最终 211 由并行车部署 **`2.6.1-568a9943-20261118-211`（build_seq 3107，槽 b @8783 接管）**＝origin/main 尖端＝**含本修复**。公网 `llm.itestu.cn/healthz` 同身份 `568a9943` ready ✓。本方两次闸门（sanitize/admin/cmd-gateway/installer 四包＋核心包清单）全绿；部署树＝main 尖端，无回退面。
- **活体验证（211 本机，密钥不出目标机）**：临时 key 铸造→approve→**重放事故归档 body（7,722,638 字节，仅 max_tokens 128000→2048 收口控费，messages/thinking/签名逐字保留）**→删 key。
  - **重放①：HTTP 200，75.9s**，SSE `message_start`（model=claude-sonnet-5-5，input 1786 / cache_read 30232）→ `thinking_delta` 真实流式作答（「好的，让我…」）。
  - **重放②：HTTP 200**（cache_read 30140，上游缓存续接）。
  - **断言**：`Invalid request body` **absent**（事故响应体形状不再出现）。
  - **日志侧（5 分钟窗，两笔重放）**：`sanitize_middleware: sanitize failed`＝**0**；`sensitive signed thinking`＝**0**；`signed thinking passed through verbatim` WARN＝**0**（bearer 守卫把 "token" 豁免 ⇒ 这份 body 连 Fix A 的放行路径都没走到）；`upstream_call_starting`/`upstream_http_attempt` ×2 且 **`upstream_status:200`**（事故时零 upstream 尝试）。
  - **对照锚**：事故同 body 修复前＝400／85 字节／57s／零 upstream（§一）。
  - **清理双重确认**：临时 key 删除后 GET=**404** ＋ 旧 key 调用=**401**；211 与本地的重放 body/脚本/冻结树已删。
- **WARN（既有缺口）**：登录冒烟未执行（无 DD_ADMIN_PASSWORD，与本链既有缺口相同）。

## 六、遗留登记

- **L1（P2）password 规则裸空白分隔符的散文误报**：`secret.password` 的 KEY`\s+`值 分隔形态把相邻英文词当赋值（本会话无签名 thinking 里 ~49 处：the/a/just/for/in/on/flow/NOW/needed/header/passed/masked…）。未签名块被静默改写＝上下文质量债（模型读到 {SENSITIVE:secret:N} 散布在思考里）；修它＝收窄分隔符语义，属规则语义裁决（影响 input+egress 两面真实检出面），单独立项。
- **L2（P3）签名 thinking 放行路径的残余暴露面**：不验签的 Anthropic 协议上游会收到检出的原文；WARN 观测已就位（`action=observe`），是否对特定 type 升级为 enforce（如真凭据形态）归 owner。
- **L3（P3）restore 侧 thinking lane 与签名回显的先天冲突**：还原器把 thinking 增量里的占位符还原成真值 ⇒ 客户端持有的 thinking 文本 ≠ 模型签名的字节 ⇒ 下一轮回传必验签失败（上游 400，非本网关拒绝）。本轮 verbatim 放行让这种请求「原样到达上游」并由上游裁决；根修（thinking lane 不还原/回传时逆向重代换）另立项。
- **L4（P3）v2 pipeline 的 provider_discovery 看不见健康供应商，v1 看得见**：事故时与重放时**同一形态**（`v2 pipeline: preflight error ... no healthy provider for model claude-sonnet-5-5` → 落 v1 → v1 正常 dispatch 200）。v2 候选可见性与 v1 不同源，本请求因此多耗 ~57s（7.66MB 的 v2 侧处理）；不影响正确性（v1 兜底成功），单独立项排查。
- **登记不修**：无签名 thinking 的既有改写行为**保持不变**（真检出仍脱敏——本修复只动"带签名"分支与 bearer 误报面）。

## 七、通用式

1. **「拒绝」是放大器**：检测器的每一次误报，都会被 fail-closed 决策放大成「整个会话砖化」；误报债未清零之前，fail-closed 的代价是按「误报率 × 会话大小」计的。
2. **签名内容＝密码学上不可改写 ⇒ 处置只剩「放行或杀请求」，而「杀请求」在会话回显场景＝杀会话**；对不可改写内容，observe＋放行是唯一不砖化的选项，验签方（Anthropic）本身就是防伪造闸门。
3. **形状规则必带整串守卫**（bear/card/idcard/vendor-key 四族已齐）：值捕获组匹配「一个词」的规则，问一句「这个值像不像真凭据」。
4. **先铸造后拒绝＝字典污染**：拒绝路径必须发生在任何 bookkeeping 之前。
5. **客户端报错原文 ≠ 系统真相**（第三次实证）：`reason=invalid_request` 是 ZCode 对任何 400 的渲染；取证钥匙＝网关自己的 sanitize WARN ＋ 85 字节响应体形状。
6. **归档件是入口拒绝的唯一 body 证据**：入口被拒的请求不进 WAL；session-archive 的 raw.json（zstd 分帧）＋ `ref.json.size` 精确匹配是取证通路。
