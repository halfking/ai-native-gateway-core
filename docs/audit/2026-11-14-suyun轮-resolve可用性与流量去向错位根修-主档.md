# 2026-11-14 suyun 轮 —— resolve 可用性与流量去向错位根修（主档）

> goal 会话。owner 报障：`https://llm.itestu.cn/routing-v2?tab=resolve` 查
> `claude-opus-5-5`，suyun 相关节点显示不可用，但请求日志里不少流量打向
> suyun 节点。要求：211 日志检查 → 分析原因 → 修正 → 部署 211 验证。
>
> 修复提交 **b8e5c8071**（双仓已推）；211 冻结树部署验证（本档 §六）。

## 一、取证（211 真库 + 真日志，全部只读）

### 1.1 suyun 凭据 × opus-5-5 的实况（01:44+08 时刻）

| cred | label | availability_state | 绑定 available | 探针实况 | resolve 页 |
|---|---|---|---|---|---|
| 37 | 不渗水 | ready | — | — | 可用 |
| 38 | kiro | unreachable（network_error，u.syapi.cn 30s 超时） | f（probe_timeout@01:44:06） | opus-5-5 连败 12（timeout） | 不可用 ✔（真） |
| 39 | claude | auth_failed（recover 01:47:56） | t | **opus-5-5 探针 150 连胜、01:32:15 刚成功** | 不可用 ✘（虚报） |
| 71 | claude-nomix | auth_failed（recover 11-15 00:21，24h） | t | opus-5-5 探针 95 连胜（11-09） | 不可用 ✘（虚报） |
| 67-70 | grok 系 | ready | — | — | 可用 |

### 1.2 流量去向（session_turns_hot，热窗 7-8h）

- cred 38/opus-5-5：30 笔（11-13 19:50 → 11-14 00:38），**全部成功**。
- cred 39/opus-5-5：114 笔（00:36:51 → 01:53:36），**全部成功**。
- **决定性分界**：auth_failed 窗口（01:32:56→01:47:56）内**零流量**；
  恢复（01:48:37 ready，credential_recovery ticker 兑现 recover_at）后
  01:48 起流量立即回来。⇒ **dispatch 侧完全守约，没有「无视不可用发请求」**。

### 1.3 「不可用却有流量」的真相

两条腿叠加：

1. **时间错位**：用户在 01:33+ 查 resolve 页（suyun 全红），而请求日志里的
   流量发生在标记前（ready 期）。dispatch 在标记后立刻停发——数据可证。
2. **resolve 页虚报「鉴权失败」**：两个 new_api 家族 403 形态被误分类成
   KindAuth，把健康凭据整颗标死（§二）。用户看到「鉴权失败」的红标与
   「流量成功打过去」的事实并排，形成直接矛盾感。

## 二、根因（两个 new_api 403 形态漏分类）

### 2.1 F1：余额预扣失败 → KindAuth（cred 39）

生产体（cred 39 state_reason_detail，state_updated_at 01:32:56）：

```
[auth] upstream 403: {"error":{"message":"预扣费额度失败, 用户剩余额度: ¥1.474664,
需要预扣费额度: ¥2.497896 (request id: 20261113173256...)","type":"new_api_error",
"code":"insufficient_user_quota"}}
```

- 上游语义＝**余额不足以预扣这笔请求**（预扣费按请求大小估算；¥1.47 余额
  扛得住小请求、扛不住 ¥2.50 的大请求）——key 有效、探针 150 连胜。
- 分类链：`ClassifyErrorWithBody(403, body)` 的 `budgetExceededRe` 词表
  不命中（配额码清单只有 INSUFFICIENT_BALANCE/insufficient_balance；
  中文动词表「用尽/用完/耗尽/不足/超限/上限」**不含「失败」**）⇒ 落 403
  状态兜底 ⇒ `KindAuth` ⇒ writer.go 整凭据 `auth_failed` + 15 分钟。
- 72h 窗口同类误标 15+ 次（candidate_failure_logs error_kind='auth'：
  11-11×9、11-12×4、11-13×1，全部是该家族 403）。
- ★ 同族姊妹形态「用户额度不足，余额0」**已经**命中 quota
  （「额度不足」动词在表内；bg/credential_probe_classification_211_test
  的 creds67-70 钉测钉成 quota_permanent）——同一个上游、同一个 code，
  措辞差一个词就分叉成两个 family。§700 单源纪律的经典反例。

### 2.2 F2：单模型分组权限拒绝 → KindAuth（cred 71）

生产体（cred 71 state_reason_detail，state_updated_at 00:21:34）：

```
[auth_error] upstream 403: {"error":{"code":"","message":"无权访问 claude不降智稳定版
分组 (request id: ...)","type":"new_api_error"}}
```

- 上游语义＝**这个 key 无权访问这个模型分组**（new_api 分组权限体系）——
  证据指向 (凭据， 分组)＝发起请求的那一对 (凭据，模型)。
- 同凭据 opus-5-5 探针 95 连胜、opus-4-7 95 连胜——兄弟模型完全健康。
- 误判成 KindAuth 后走探针退避梯（classifyProbeFailure auth 分支），
  反复标记把 availability_recover_at 推到 **24h**（11-15 00:21）⇒
  resolve 页对健康模型显示「鉴权失败」整整一天。

### 2.3 修复原则

- F1 归 **quota 族（KindQuotaPermanent）**——与既有 creds67-70 钉测同族
  同判（「同上游同 code 必同 family」），恢复链＝BalanceQuotaProbe 真请求
  探活（2min 起步、退避 ≤1h，成功即清 quota_state 并恢复 ready）。
- F2 归 **KindModelUnavailable（绑定级）**——同裸 404 处置：只冷却这一对，
  保留跨凭据故障转移；nodehealth isBindingScoped ⇒ 不进凭据级熔断。
- 词表**单源**：两个分类器共用 budgetExceededRe / bindingGroupAccessRe，
  探针侧经导出面 `errorsx.IsBindingGroupAccessDenied` 接同一份词表
  （§700：防「分类器改了、探针侧忘改」三处抄写漂移）。

## 三、修复内容（b8e5c8071）

| 文件 | 变更 |
|---|---|
| errorsx/classify.go | budgetExceededRe 补 `"insufficient_user_quota"` 码 + `insufficient_user_quota` 裸词 + `预扣费` 中文形态（刻意不含裸 insufficient_quota——429 上既有口径 KindRateLimit 被 characterization 钉测钉死）；新增 bindingGroupAccessRe（「无权访问」+「分组」成对）+ 导出面 IsBindingGroupAccessDenied；两分类器在 noActiveQuotaRe 与 budget 门之间插入同门分组分支（403 ⇒ KindModelUnavailable） |
| bg/credential_probe_v2.go | isPerModelChatFailure 的 403 臂接单源词表 ⇒ 分组 403 探针只翻绑定（BindingOnly），凭据保持 ready |
| errorsx/classify_dual_classifier_lockstep_test.go | 语料补两个真实生产体（防「语料缺 ⇒ 门绿」，第十一轮教训） |

## 四、门（全绿）

- **钉测** errorsx/r1054_suyun_quota_group_test.go：真实生产体逐字 × 两分类器
  × 402/403/429 三态 + 阴性对照（裸 insufficient_quota 保持 rate_limit、
  账号级「无权访问」无「分组」保持 auth、仅「分组」不命中）+ kind 作用域
  对照（quota_permanent=凭据级 fatal、model_unavailable=绑定级）。
- **探针钉测** bg/r1054_probe_quota_group_test.go：分组 403 ⇒ BindingOnly +
  ready；余额预扣 403 ⇒ suspended + permanently_exhausted；歧义 403 仍凭据级。
- **锁步语料**：两个真实体进 lockstepBodyCorpus。
- **变异 3/3 红 → 还原绿**：M1 摘 budgetExceededRe 新词表（errorsx+bg 双红）；
  M2 摘请求路径分组分支（errorsx 红）；M3 摘探针侧单源接线（bg 红）。
- **回归**：errorsx 0.9s / bg 34s / credential 16s / nodehealth /
  streaming（全子树）/ cmd/gateway 7s 全绿；deploy 引擎 test-gate（bg+errorsx）
  两跑全绿；build/vet 干净。
- **刻画的假设修正**：首版钉测误设 KindQuotaBalance（与既有族内钉测分叉）
  ——被 bg 既有 211 钉测（creds67-70 quota_permanent）当场咬红后收敛为
  同族同判。教训：给既有 family 加成员前先 grep 家族内既有钉测。

## 五、部署（冻结树）

- 双仓推送 b8e5c8071（origin codeup + github 镜像）。
- 冻结树部署（§6-3 纪律）：`git archive b8e5c8071` → /tmp/r1054-frozen-tree
  → 冻结树内构建 web + web-mobile → `LLM_GATEWAY_ROOT=… DD_SKIP_WEB_BUILD=1
  DD_BUILD_SHA=b8e5c807 bash scripts/deploy-211.sh`。
- 前两跑 fail-closed（不伤服务）：① 缺 web-mobile/dist（白页事故口径门）；
  ② 冻结树无 .git ⇒ 引擎要求显式 DD_BUILD_SHA。第三次通过。
- 部署引擎 test-gate 顺手把 ./bg ./errorsx 在部署机侧又跑了一遍（全绿）。

## 六、部署验证与活体测试（211）

- **部署**（02:23+08 完成）：蓝绿 槽 a(659ecf70a@8781) → **槽 b@8783 接管**，
  版本 `2.5.9-b8e5c807-20261114-211`（build_seq 3049）；readyz OK（db+redis）、
  admin SPA 可达、nginx upstream 切 8783、公网版本身份 OK（b8e5c807）、
  公网入口 200。登录冒烟未执行（部署机无管理员口令，引擎 WARN 原样披露）。
  旧槽 a 保留可 docker start 回滚。
- **误分类遗留态修复**（force-recover 同语义 SQL，带守卫
  `availability_state='auth_failed' AND state_reason_code IN ('auth','auth_error')`）：
  cred 71（分组权限 403 误标，探针 95 连胜在案）与 cred 39（余额预扣 403
  误标，02:18:06 旧二进制在役期最后标记，探针 150 连胜+96 笔真实成功在案）
  → ready。修复后 resolve 视图：

  | cred | is_routable | reason |
  |---|---|---|
  | 39 claude | **t** | - |
  | 71 claude-nomix | **t** | - |
  | 38 kiro | f | node_probe_failed（真网络超时，保留真实状态） |

- **活体测试**：铸临时 key（id 260，HMAC=SHA256(secret, key)，application_id=25
  同既有形态）→ `POST /v1/chat/completions` model=claude-opus-5-5 × **5 笔
  全部 200**（真实回答「PONG」）；路由按权重分散（cred 31×9、59×1、99036×1），
  suyun 39/71 已回候选池。**临时 key 验后 revoked（259/260）**。
- **在役容器健康**：新容器 panic=0/fatal=0（唯二匹配为既有非致命 WARN：
  storage full 配置提示＝历次部署同形、model_iq non-fatal）；部署后 19 笔
  dispatch succeeded。
- **被动观察锚**（下次自然流量即可复核）：suyun 下次「预扣费额度失败」403
  在 candidate_failure_logs 的 error_kind 应为 `quota_permanent`（修复前为
  `auth`）；cred 71 探针若再遇分组 403 应 BindingOnly（凭据保持 ready）。

## 七、遗留与登记

- cred 71 的存量 auth_failed（recover 11-15 00:21）是误分类遗留态：部署后
  用 force-recover 同语义 SQL 纠正（探针 95 连胜在案）。
- cred 38（kiro）unreachable 是真网络问题（u.syapi.cn 30s 超时）——非本次
  修复对象，等上游恢复探针自愈。
- cred 39 的上游真实余额（¥1.47）由运营面充值解决；修复后状态语义变为
  「配额（探测恢复）」，resolve 页显示配额族徽章而非「鉴权失败」。
- 「时间错位」感知面：resolve 页当前只报节点即时状态，不显示「该节点最近
  N 分钟收过多少成功请求」——跨面板时间对齐属产品面，登记不修。
- 「预扣费」粒度模型（按请求大小预扣）网关侧不建模：余额不足期大请求仍会
  失败并转移，属上游真实约束。
