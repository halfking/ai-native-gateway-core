# 2026-09-21 Due-Diligence Tracker — 9 Suspicious Canonical Rows

**Status: 9 行仍 active,等运营方确认。本文档给出每行的 provider 联系人、证据收集清单、决策动作。**
**关联:** [`2026-09-20-phase5-due-diligence.md`](./2026-09-20-phase5-due-diligence.md) — 上一轮的根因分析与初版清单。

## 0. 状态总览(2026-09-21 09:33 实测)

| # | canonical | family | pm_refs | req_7d | req_all | due_action | owner | status |
|---:|---|---|---:|---:|---:|---|---|---|
| 1 | `claude-fable-5` | anthropic-claude | 8 | **1234** | **2310** | confirm upstream | operator-apiclaude/OR/aggregator | PENDING |
| 2 | `claude-fable-5-1` | anthropic-claude | 4 | 25 | 33 | confirm upstream | 同 #1 共享方 | PENDING |
| 3 | `claude-fable-5-thinking` | anthropic-claude | 1 | 0 | 0 | confirm upstream | zhima(9271) | PENDING |
| 4 | `claude-fable-latest` | anthropic-claude | 1 | 0 | 0 | confirm upstream | OpenRouter(21) | PENDING |
| 5 | `claude-opus-4.1` | anthropic-claude | 2 | 0 | 0 | confirm upstream | OpenRouter(21) | PENDING |
| 6 | `claude-opus-4.7-fast` | anthropic-claude | 1 | 0 | 0 | confirm upstream | OpenRouter(21) | PENDING |
| 7 | `claude-opus-4.8-fast` | anthropic-claude | 1 | 0 | 0 | confirm upstream | OpenRouter(21) | PENDING |
| 8 | `claude-opus-5-fast` | anthropic-claude | 1 | 0 | 0 | confirm upstream | OpenRouter(21) | PENDING |
| 9 | `grok-4.3` | xai-grok | 2 | 0 | 0 | confirm upstream | OpenRouter(21)+Vapeur(36) | PENDING |

## 1. 行 #1 — claude-fable-5(高危,活体流量)

```
pm_id: 193846/1605150/1637477/2077566/2434785/2661037/2661038/3501276
provider_id: 587 apiclaude / 5990 maishouai / 33 Evol / 9271 zhima / 13092 suyun / 21 OpenRouter / 36 Vapeur
raw_model_name 样本:
  pm 193846 (apiclaude):       claude-fable-5
  pm 1605150 (maishouai):      claude-fable-5
  pm 1637477 (Evol):           claude-fable-5
  pm 2661037 (OpenRouter):     anthropic/claude-fable-5
  pm 2661038 (OpenRouter):     anthropic/claude-fable-5:batch
  pm 3501276 (Vapeur):         claude-fable-5
请求量:1234 req/7d(本地);2310 all-time(请求会被路由到这 7 个 provider)
```

**假设**(参见 2026-09-20-phase5-due-diligence.md §2.1 排序):
1. **私有 Anthropic preview / staging**:"Project Fable" 已出现在 Anthropic 内部 preview 上下文里(公开不发布)。可能性最大。
2. **上游 relay 改写**:某个 provider 的中转层把别的真实模型重写成 `claude-fable-5`,实际指向 `claude-sonnet-5` 或 `claude-opus-5`。
3. **故意伪装**:某个 provider 用 anthropic-shape 名字掩盖非 anthropic 的真模型。

**运营方需要做的事**:
- [ ] 联系 apiclaude(587)运维:这是 Anthropic 官方 preview 模型吗?是否有 API 文档/邀请链接?
- [ ] 联系 OpenRouter(21)运维:`anthropic/claude-fable-5` 在 OpenRouter 公开目录里吗?(大概率不在)
- [ ] 联系 maishouai(5990)、Evol(33)、Vapeur(36)、zhima(9271)、suyun(13092)运维:这是哪家上游的统一品牌名?统一改成 `claude-sonnet-5` 还是 `claude-opus-5`?
- [ ] 任一方回复「这是 X 模型的别名」→ 我方走 `provider_models.canonical_id` 重指(从 193851 → 真实 canonical_id),`models_canonical.status='disabled'`,`disabled_reason='vendor alias for <X>; operator confirmed YYYY-MM-DD'`。
- [ ] 任一方回复「这是我自己的独家模型」→ 保留 active,改 `family='anthropic-claude'` → 真实厂商名(需新增 family 值)。
- [ ] 任一方回复「不知道,需要查证」→ 留 PENDING,加 30 天 review 触达。

**证据收集**:任一 provider 给出 Anthropic 邀请链接/Preview 文档截图,即可关闭本行。

## 2. 行 #2 — claude-fable-5-1

```
pm_id: 2905233/2946603/3027668/3501368
provider: Evol(33)/suyun(13092)/apiclaude(587)/Vapeur(36)
请求量:25 req/7d;33 all-time(本地)
```

**假设**:`fable-5` 的 `5-1` 次版本(类比 claude-sonnet-4-1)。
**运营方**:同 #1 的共享 provider,只需追加一句「5-1 也是真模型还是别名?」
**额外风险**:25 req/7d 不算零,disable 之前需要 review 这部分流量的去向。

## 3. 行 #3 — claude-fable-5-thinking

```
pm_id: 2077567(zhima/9271)
请求量:0/0
```

**假设**:`fable-5` 的扩展推理变体(类比 `claude-opus-X-thinking`)。
**运营方**:联系 zhima(9271)即可;0 流量 disable 零风险。

## 4. 行 #4 — claude-fable-latest

```
pm_id: 2661036(OpenRouter/21,raw=`~anthropic/claude-fable-latest`)
请求量:0/0
```

**假设**:`fable` 家族的指针行(类比 5 行 `*-latest` 的 surface 标签)。
**建议**:与 #1 同步处理 —— 如果 #1 被 disable,本行也 disable;如果 #1 保留,本行加 `surface='pointer'`。
**运营方**:同 #1 的 OpenRouter 联络。

## 5. 行 #5 — claude-opus-4.1

```
pm_id: 2661237/2661238(OpenRouter/21;raw=`anthropic/claude-opus-4.1` + `:batch`)
请求量:0/0
```

**Anthropic 公开目录**:`opus-4 → 4.5 → 4.6 → 4.7 → 4.8 → 5`,**无 4.1**(可能是 typo,可能是 OpenRouter 内部标签)。
**建议**:联系 OpenRouter 确认,大概率是 typo(`4.5` or `4.6`)。如果是 typo,我方 `provider_models.raw_model_name` 标准化映射到 `claude-opus-4-5` 或 `claude-opus-4-6`,并 `status='disabled'`,`disabled_reason='not in anthropic public catalog; OpenRouter typo confirmed YYYY-MM-DD'`。
**运营方**:OpenRouter(21)。

## 6. 行 #6-#8 — claude-opus-4.7-fast / claude-opus-4.8-fast / claude-opus-5-fast

```
pm_id: 2661056/2661049/2660984(均 OpenRouter/21)
请求量:0/0/0
```

**Anthropic 公开目录**:无 `-fast` 后缀(Anthropic 的速度等级是隐式的,不在 model id 里)。
**假设**:第三方 provider 的「快速推理」分级(类比 OpenAI 的 `gpt-4-turbo`、`gpt-4o-mini`)。
**建议**:联系 OpenRouter 确认 `-fast` 的实际定价和模型细节;如果它们指向真实的 `claude-opus-4-7` / `claude-opus-4-8` / `claude-opus-5` 加快推理通道,我方把 raw_model_name 标准化去掉 `-fast`,canonical_id 重指到真身,本行 disable。
**运营方**:OpenRouter(21)。

## 7. 行 #9 — grok-4.3

```
pm_id: 2661061(OpenRouter/21,raw=`x-ai/grok-4.3`)/3501307(Vapeur/36,raw=`grok-4.3`)
请求量:0/0
```

**xAI 公开目录**:`grok-1 → 2 → 3 → 4 → 4.5 → 4.6`,**无 4.3**(类比 #5 的 -4.1,可能是中间版本号)。
**建议**:联系 OpenRouter + Vapeur 确认指向 `grok-4.x` 的具体哪个;大概率 disable。
**运营方**:OpenRouter(21)+ Vapeur(36)。

## 8. 联系人映射

| provider_id | code | display_name | base_url | 联系难度 | 备注 |
|---:|---|---|---|---|---|
| 21 | openrouter | OpenRouter | openrouter.ai | 中(英文邮件/工单) | 6 行归属,可批量问 |
| 36 | vapeur | Vapeur AI | api.vapeur.ai | 高(中文为主) | 3 行归属 |
| 587 | apiclaude | apiclaude | apiclaude.cc | 高(中文代理) | 2 行归属 |
| 9271 | zhima | 智码 | glmcoding.cn | 中(中文) | 2 行归属 |
| 33 | evol | EvolAI 聚合代理 | mg-new.evolai.cn | 中(中文) | 2 行归属 |
| 13092 | suyun | 速云U站 | u.syapi.cn | 中(中文) | 2 行归属 |
| 5990 | maishouai | maishouai | maishouai.top | 中(中文) | 1 行归属 |

**效率建议**:OpenRouter(21)6 行 → 一次英文工单问完。其余中文 provider → 由 ops 团队打包 5 个中文问题集中发送(避免每行单发)。

## 9. 时间窗

- **2026-09-21 ~ 09-28**:ops 团队发问,记录回复。
- **2026-09-29**:根据回复批量执行 disable 或保留;无回复的视情况 disable 或延期。
- **复跑 audit**:任何 disable 后,跑 `govern-junk-canonical -json` 确认 Suspects 仍 null,且 `request_logs` 在新 disabled 行上 0 新增。

## 10. 触发本 tracker 的根本代码问题

(留档给 R51 代码修复用)
- `provider/client.go` 的 auto_discovered / provider_refresh 路径未对接 Anthropic/xAI 的公开目录校验。9 行里有 7 行是 `source=provider_refresh`,意味着 provider 上游说什么我们就信什么。
- 建议 R51 引入白名单校验:对 `anthropic-claude` / `xai-grok` family,canonical_name 必须命中 Anthropic/xAI 公开文档里的白名单才允许 seed。未知模型走 quarantine 状态(`status='quarantine'`),需 ops 审核后才提升 active。
- 本轮不抓 9 行的硬根因(避免打断 ops 审核流程),但 R51 必修。
## 11. 2026-09-21 下午公开目录检证结果(证据刷新,行状态不动)

同日下午对 9 行做公开目录 web 检证 + 双库现值复查。**结论:5 行确认为真实模型,1 行真实但上游已退役,3 行为聚合渠道 SKU。** 与 09:33 时"大概率是别名/伪装"的假设相比大幅翻转;fable 家族的"高危"定性撤销。行 status 一律不动,仍由 ops 按 §9 流程执行;本节只更新决策依据。

### 11.1 检证结论

| # | canonical | 检证结果 | 证据 | 建议动作(供 ops 采纳) |
|---:|---|---|---|---|
| 1-4 | `claude-fable-5` / `-5-1` / `-5-thinking` / `-latest` | **真实模型** | Anthropic 2026-06-09 正式发布 Claude Fable 5("Mythos-class, safe for general use"),platform.claude.com 有文档,CNBC/Wikipedia 独立佐证 | 全部保留 active;撤销"高危"标记;无需 provider 触达 |
| 5 | `claude-opus-4.1` | **真实但已退役** | Anthropic Model Deprecations:2026-06-05 通知、2026-08-05 退役,API 请求现返回错误 | disable 2 条 OpenRouter pm 行 + canonical 标 disabled(reason=upstream retired 2026-08-05);我方 0 流量,零风险 |
| 6-8 | `claude-opus-4.7-fast` / `-4.8-fast` / `-5-fast` | **聚合渠道 SKU**(非伪造) | OpenRouter 2026-07-24 上架 opus-5-fast;4.7-fast 见于 Cursor/Emergent 渠道;均不在 Anthropic 官方文档 | 维持 PENDING 但降级为低优先(各 1 pm、0 流量);向 OpenRouter 确认 SKU 归属即可 |
| 9 | `grok-4.3` | **真实模型** | docs.x.ai 定价页在列;OCI `xai.grok-4.3`、Bedrock 企业版有售;VentureBeat 报道 | 保留 active;无需触达 |

### 11.2 双库现值复查(2026-09-21 午后,本地 .34)

| # | canonical | pm | req_7d | vs 09:33 |
|---:|---|---:|---:|---|
| 1 | claude-fable-5 | 8 | 1231 | ≈平(1234) |
| 2 | claude-fable-5-1 | 4 | 29 | ≈平(25) |
| 3 | claude-fable-5-thinking | 1 | 0 | 平 |
| 4 | claude-fable-latest | 1 | 0 | 平 |
| 5-9 | 其余 5 行 | 不变 | 0 | 平 |

252(shared)侧 9 行均不在 -cn 批次内,状态独立;本轮 252 预检确认其不受 `-cn` rename 延期影响(参见 `2026-09-21-unmapped-cn-round2.md` §3)。

### 11.3 检证方法备注

- 检证时间为 2026-09-21,来源含 Anthropic/xAI 官方文档、platform.claude.com、CNBC、VentureBeat、OCI/Bedrock 目录页;第三方聚合目录(big-agi、Referee 等)仅用于 -fast SKU 的存在性佐证。
- **教训入档**:09:33 版 tracker 把 fable 判为"relay 改写或伪装"的概率排序,是"不在训练目录=可疑"的推断;对 2026-06 后发布的新模型,先查官方发布记录再定性,避免误伤高流量真实模型。
