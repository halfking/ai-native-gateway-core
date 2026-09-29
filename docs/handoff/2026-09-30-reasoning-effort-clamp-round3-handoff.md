# Handoff — reasoning_effort 收窄链路第二轮批判复审（2026-09-30）

> 上一轮见 `docs/audit/2026-09-28-reasoning-effort-clamp-audit.md` §7。
> 本文件只覆盖本轮事实、改动、未决项。**上一轮自身有一条错误归因，
> 已在审计文档 §8 明确推翻，本文件不重复论证。**

## 1. 一句话状态

本轮**推翻了上一轮自己下的结论**（「方舟不接受 `reasoning_effort` →
Ark case 应回退」），并据此发现一个此前没人看见的真缺陷：
`glm-5` 能力表声明的 7 档里，`none`/`minimal` **被上游明确拒绝**，
而本轮修的 `cheapestEffort` 恰好会把关闭意图改写成这两个值。
Ark case 保留，`glm-5` 档位收窄为 5 档。

## 2. 关键事实（绑定 git_sha，换轮需重取）

| 项 | 值 | 取证方式 |
|---|---|---|
| HEAD（提交前） | `2637ea0e1` | `git rev-parse` |
| 对照复审基线 | 上一轮 handoff 的 18784/18785 旁挂容器**已随会话消失** | `docker ps` |
| 直连端点 | `https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions` | curl |
| 可用凭据 | provider 34（`volcano-tokenplan`） | `secret.DecryptAESGCM` |
| **失效**凭据 | provider 35 → `401 API key status is not active` | 直连基线 401 |
| 真机模型 | `glm-5-2-260617`（= 网关 `glm-5.2` 的出站 raw_model） | provider_models |
| 对照模型 | `kimi-k2-thinking-251104`（同端点 12/12 全 200） | 直连 |
| 智谱原生 `api/paas/v4` | 404（方舟凭据无权限） | 直连 |
| provider 34/35 `catalog_code` | `volcengine-coding`（**不在** `catalogToDialect`） | SQL |

采样纪律：间隔 **7s**；`none`/`low` 各 **n=3 复验**；其余档 n=1 全扫。

## 3. 被推翻的结论（留档，防复发）

**上一轮写的**：「方舟真机不接受 `reasoning_effort` 字段（400 + reqprobe
`stripped=reasoning_effort`），Ark case 应回退」。

**错在两点**：

1. **单样本归因**。上游 400 既可能是「字段不支持」也可能是「值非法」，
   响应上不可区分，只看错误消息才能分。而上一轮看的是 reqprobe 的
   `stripped=reasoning_effort` —— 那是**网关自己的探测结论**
   （它剥了参数仍失败，于是归因该参数），不是上游对字段存在性的表态。
2. **对照组缺失**。只跑了非法值（`disabled`）就下「字段不支持」的结论。

上游原话（直连取得）：

```
400 InvalidParameter
  "The parameter `reasoning_effort` specified in the request are not valid:
   value `disabled` is invalid."
```

字段被**识别并按值校验**。字段不存在会是 `unknown parameter` 一类错误。
**Ark case 保留，回退建议作废。**

## 4. 真机档位图谱（本轮核心证据）

`glm-5-2-260617` 七档全扫：

| effort | HTTP | reasoning_content | 语义 |
|---|---|---|---|
| `none` | 400 `none is not supported by this model` | — | **被拒**（n=3） |
| `minimal` | 400 `none is not supported by this model` | — | **被拒**（归一化成 none） |
| `low` | 200 | **0** | **关闭档**（n=3） |
| `medium` | 200 | 0 | 关闭 |
| `high` | 200 | 0 | 关闭 |
| `xhigh` | 200 | 759 | 真开启 |
| `max` | 200 | 937 | 真开启 |

对照组 `kimi-k2-thinking-251104`：`minimal`/`low`/`high`/`disabled`/`zzz_bogus`
**全部 200**（12/12）——方舟是「收下但按模型能力处理」，不是「拒绝字段」。

## 5. 本轮改动

| 文件 | 性质 | 说明 |
|---|---|---|
| `internal/reasoncap/reasoning_defaults.go` | **真修复** | `glm-5` 档位 `{max,xhigh,high,medium,low,minimal,none}` → `{max,xhigh,high,medium,low}`。`glm-4.5`/`glm-z1` **保留原 7 档**（无真机证据，不凭同族推断删档） |
| `internal/paramguard/glm_effort_upstream_contract_test.go` | **新增守门（3 条）** | ① 能力表不得含真机被拒档 ② `ClampEffort` 关闭意图不得落在被拒档（喂**真机测得**的 supported 集，不读生产代码，避免门与被测物同源自证）③ 端到端出站体 + clamp 报告 |
| `internal/paramguard/reason_effort_dialect_test.go` | **改期望（2）+ 新增（1）** | `GLMNarrowesDisableIntent` want `none`→`low`；`GLMKeepsSupportedEffortUntouched` 档位清单去掉 `none`；新增 `GLMDowngradesUpstreamRejectedEffort`（`none`/`minimal` 不得原样透传） |
| `docs/audit/2026-09-28-reasoning-effort-clamp-audit.md` | 文档 | 新增 §8（直连取证全过程 + 表 4 + 变异验证 + 未闭合边界）；§5 的「未来守势」表述加订正；表 3「Ark 暴露 reasoning_effort」从「无法探针」改为「已证实」 |

**危害链**（为什么这是真缺陷而不是洁癖）：
`cheapestEffort` 取能力表最低档 → 关闭类写法（`disabled`/`off`/`none`/`no`/
`false`）被改写成 `none` → 上游 400。修复前是「原样透传给上游被拒」
（网关没动手），修复后是「网关主动改写成一个自己能力表宣称合法、实则被拒
的值」（责任转移到网关），且 paramledger 记成合法 `clamp`，调用方看不出来。

## 6. 测试与变异验证

提交前 rebase 到 `origin/main 42295c562`，**基线已换，故回归重跑**（不沿用
rebase 前的结果）：

```
go build ./...                                                          → OK
go test -count=1 ./internal/{reasonnorm,paramguard,reasoncap,paramreg,ir}/...
                                                                    → 全绿
go vet ./internal/{reasoncap,paramguard,reasonnorm}/...                 → OK
gofmt -l <本轮改的 3 个 .go>                                              → 干净
```

SKIP 审计：`grep -c '^\s*--- SKIP'` = **0**（不靠包级 `ok` 掩盖子测试 skip）。

**三个既存 FAIL，与本轮无关**：rebase 后跑 `./internal/... ./domains/streaming/...`
时出现

| FAIL | 归属判定 |
|---|---|
| `internal/sqlreadguard/TestSQLReadGuardWhitelistCurrent` | 纯 `origin/main` 基线同样红 |
| `domains/streaming/TestNativeStreamRealOutputComplianceRedactsCrossFramePhoneBeforeWire` | 同上 |
| `domains/streaming/TestRestoredSensitiveFrameIsHeldUntilRealOutputComplianceApproval` | 同上 |

判定方法：`git checkout origin/main` 跑同一命令，同样红。**本轮不修也不背**
（修它们要动 `providerprofile` 与 output-compliance 接线，超出范围）。
注意这三个是并发会话带进来的，**下一轮若仍红，不要误判为本轮回归**。

| 变异 | 结果 |
|---|---|
| 把 `none`/`minimal` 加回 `glm-5` 能力表 | 新门 **FAIL** |
| 摘掉 `DialectGLM` 方言门（`none` 原样透传） | **FAIL** |
| 两处还原 | 全绿 |

`gofmt -l internal/reasoncap/caps.go` 会红，但 **HEAD 版本同样标红**，
是既存偏差，本轮未碰该文件（判据：`git diff --stat` 为空 +
`git show HEAD:<file> | gofmt -l` 同样红）。

## 7. 遗留风险（勿当成已解决）

1. **`catalogToDialect` 缺口未补**。provider 34/35 的 `catalog_code` 是
   `volcengine-coding`，不在表内 → 方言门仍不开。**维持 R45 立场**：
   登记不修 + 逐上游真机验证接受度。本轮已在方舟侧拿到接受度证据，
   但补登记会同时影响 thinking 渲染路径（R45 已实测 delta=0），
   超出本轮范围，需单独立项。
2. **`TestArk_EffortIsTierNotKillSwitch` 钉的能力表在真机上不可达**。
   它锁的 `doubao-pro-thinking` 不在 provider 34/35 的模型清单里
   （实际是 `doubao-1-5-thinking-*` / `doubao-seed-1-6-thinking-*` /
   `kimi-k2-thinking-*`）。该测试目前只证明「网关内部一致」，不证明
   「与上游一致」。**这是它的真实覆盖边界，不要当成方舟契约已被守住。**
3. **GLM 家族真机覆盖只有一条路径**。只验了 `glm-5-2-260617` @ 方舟 coding
   端点；智谱原生 `api/paas/v4` 404 无法取证。`glm-4.5` / `glm-z1` /
   `glm-5.3` 的档位声明**均未经真机验证**。
4. **`low` 是「关闭档」这一结论仅 n=3**。样本小；若方舟后续改档位语义，
   结论会漂。守门测试锁的是当前证据，不是永久事实。
5. **凭据 35 已失效**（401）。需要方舟真机证据时只能用 provider 34。
6. **本轮未做部署验证**。改动只过了单测 + 变异，未重建镜像、未起旁挂实例、
   未跑端到端真机确认「网关出站的 `low` 真的被上游接受」。这是**明确的
   未闭合项**，不是「已验证有效」。

## 8. 下一轮提示词

- **先 fetch 再核**：`git fetch origin main && git log --oneline HEAD..origin/main --stat`。
  上一轮的教训是文档里的 baseline 会过期。
- **若要闭合风险 6（部署验证）**：重建镜像 + 起旁挂实例（同 DSN/同 Redis、
  `LLM_GATEWAY_BG_MODE=data-plane`、备用端口，**不要重启 8782**），
  然后看**真实出站体**（不是响应）——`reasoning_effort` 是否为 `low`，
  以及上游是否 200。注意 `version.json` 不会随增量镜像更新，
  `/healthz` 显示的是 base 镜像的 sha，要用 `go version -m <binary>`
  或 `git_sha` 标签确认实际版本。
- **若要做 Ark 真实探针**：先解决模型名可达性——`doubao-1-5-thinking-pro-250415`
  经网关请求会被规范化成 `doubao-1-5-thinking-pro` 从而 `no_candidate`
  （`modelname` 会剥日期后缀，`reasoncap` 包注释已警告这点）。
  **直连是更省事的路径**：用 provider 34 凭据打
  `ark.cn-beijing.volces.com/api/coding/v3/chat/completions`。
- **若要补 `catalogToDialect`**：按 R45 立场先登记「逐上游真机验证接受度」
  门，再用本轮 §8 的方法（合法值 + 非法值双对照、间隔 7s、
  关键档 n≥3 复验）逐上游取证。**不要盲登。**
- **若要动 `disableIntentEfforts` 词表或 `cheapestEffort`**：先读
  `internal/paramguard/glm_effort_upstream_contract_test.go` 顶部的档位图谱。
  那条门刻意**不读生产代码**（自己持一份真机测得的 supported 集），
  就是为了避免「门与被测物同源」的自证——改能力表时它才真的能红。
- **取证纪律（本轮血泪）**：判「上游不支持某字段」必须
  ①读上游错误消息原文 ②跑合法值对照组 ③关键结论 n≥3 复验。
  只看 HTTP 状态码 + 网关自己的 reqprobe 日志得出字段级结论，
  已被本轮证伪过一次。

## 9. 命令清单（可重放）

```bash
# 全量回归
go test -count=1 ./internal/... ./domains/streaming/...

# 守门与变异验证
go test ./internal/paramguard/ -run 'TestReasoncap_GLM5|TestClampEffort_DisableIntentNever|TestApplyReported_GLM' -v

# 直连方舟（凭据需本机解密，勿入库/勿入日志）
# provider 34 = volcano-tokenplan；provider 35 已 401 失效
URL=https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions
MODEL=glm-5-2-260617
# 间隔 7s 逐档：none minimal low medium high xhigh max

# 结构事实
docker exec llm-gateway-pg psql -X -q -U llm_gateway -d llm_gateway -c \
  "SELECT p.id,p.code,p.catalog_code,p.display_name,count(c.id) FILTER (WHERE c.status='active')
   FROM providers p LEFT JOIN credentials c ON c.provider_id=p.id
   WHERE p.catalog_code ILIKE ANY(ARRAY['%ark%','%volc%','%doubao%'])
   GROUP BY p.id,p.code,p.catalog_code,p.display_name;"
```
