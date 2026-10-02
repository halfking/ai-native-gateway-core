# 74 号报告：R88-g `cc-gateway` / `9router` 跨项目借鉴（objective 里最后两个未读参照）

- 日期：2026-10-01
- 轮次：R88-g
- 变更面：**纯文档**，零代码改动
- 承接：objective「需要从 `~/workspace/ai` 目录下的多个项目源代码进行分析与借鉴」——
  OmniRoute / cc-switch / CLIProxyAPI 已在 62 号读过，**本轮补上最后两个从未读的**

---

## 0. 结论先说

| 参照项目 | 规模 | 对主项目的价值 |
|---|---|---|
| `cc-gateway` | 29 文件 / 11 TS / **0 Go** | **近乎为零**，仅 2 个小模式可借鉴 |
| `9router` | 924 文件 / 629 TS / **0 Go** | **有限但真实**，3 个模式中**只有 1 个真的适用** |

**方法**：主代理派 2 个 `verifier` 子代理并行深读（objective 要求用子代理控制上下文），
**产物逐条由主代理回原文件复核**（既定纪律：子代理定级不采信）。
**复核后最重要的发现是：子代理推荐的 3 个「最值得抄」里，1 个不适用、1 个我只验证了一半。**

---

## 1. `cc-gateway`：基本没有可迁移价值

**它是什么**：Claude Code 客户端与 `api.anthropic.com` 之间的**单用途反向代理**，
唯一职责是把多台设备的指纹重写成同一个"规范身份"。

### 1.1 主代理已亲自复核的承重事实

| 论断 | 复核方式 | 结果 |
|---|---|---|
| 单一上游 | 读 `src/config.ts:16-22` | ✅ `upstream: { url: string }` 单数 |
| `/_verify` 改写自证端点 | `grep -n "_verify" src/proxy.ts` | ✅ 命中 `:74`（路由）、`:182`（注释） |
| `is_claubbit` 拼写错误 | `grep -rn "is_claubbit\|is_claudebot" src/` | ✅ `rewriter.ts:263` 确为 `is_claubbit`，**且无 `is_claudebot` 正确变体** |
| 启动期 fail-fast 校验 | 读 `src/config.ts:52-70` | ✅ 三条校验，错误信息直接给出可执行修复命令 |

**子代理双路复核为「无」的能力**（多供应商路由 / 失败降级 / 熔断 / 限流 / 配额 / 指标统计）：
每项都用「文件名 glob + 内容 grep」两路查过，0 命中我采信——
理由是这类项目规模只有 29 个文件，子代理**基本通读**了全部非 vendor 文件，
它报「无」的成本远低于误报。

### 1.2 值得借鉴的 2 个（仅此两个）

1. **配置启动期 fail-fast 校验**（`config.ts:58-66`）——
   尤其 `config.ts:58` 检测 `device_id` 是否仍是全 0 占位符，并报错
   「Run: `npm run generate-identity`」**直接给出修复命令**。
   → 主项目若有同类占位符默认值，可加这一层。
2. **`/_verify` dry-run 自证端点**（`proxy.ts:74-85` + `184-226`）——
   用构造样本跑**真实改写函数**，返回 before/after 对照。
   → 「改写类功能的自证入口」思路可迁移到主项目的 `disguise`/`annotation` 链路。
   **注意：这是功能点借鉴而非工程模式，且该项目未给它写任何测试。**

### 1.3 明确劣于主项目（这一节不能省）

- **全量缓冲请求体**（`proxy.ts:107-112`），上行无流式；
- **改写失败静默放行**（`proxy.ts:116-121`）——catch 后用**未改写的原 body** 转发。
  对一个以"防泄漏"为唯一目的的项目，这是**失效降级**；
- **`eval` 拼接凭证**（`quick-setup.sh:37-43`）、`add-client.sh:26` 把 `$CLIENT_NAME`
  直接插进 Python 字符串再写 YAML（名字含引号即破坏配置）；
- **无 CI**、Docker 无 `USER`（root 运行）、`tsconfig.json` 的 `include:["src"]` 排除了
  `tests/` ⇒ **测试未经类型检查**。

---

## 2. `9router`：3 个推荐里只有 1 个真的适用

**它是什么**：Next.js 16 单体，一个进程同时是 Dashboard 和网关；存储是
`db.json` / `usage.json` 文件。**无权重 LB、无排队、无熔断器、无横向扩展。**

**「无熔断器」有书面佐证**（我亲自复核 `README.md:1290-1296`）：

> **[OmniRoute] — A full-featured TypeScript fork of 9Router. Adds 36+ providers,
> 4-tier auto-fallback, …, **circuit breaker**, semantic cache, …**

⇒ **circuit breaker 是 OmniRoute fork 新增的，不是 9router 自有能力。**
这与主项目已读过 OmniRoute 一致，两份材料互证。

### 2.1 三个候选模式 —— 逐个做**主项目适用性检验**

> **这是本轮最关键的一步。** 子代理的产出是「9router 有什么」，
> 而 actionable 的结论必须是「**主项目缺什么**」——
> 后者必须回主项目查，查到已存在就是冗余建议（D13 教训）。

| # | 9router 模式 | 我复核的 9router 证据 | **主项目是否已具备** | 结论 |
|---|---|---|---|---|
| ① | **refresh token in-flight 去重** | `tokenRefresh.js:36-38` `refreshPromiseCache`；`:550-560` 命中即返回同一 Promise；注释明写防 `refresh_token_reused` → Auth0 family revoke | **没有 OAuth 刷新流**：`grep refresh_token` 全仓仅命中 `cmd/license-authority/refresh_handler.go`（**授权服务器自己的实例令牌**），且 `find -type d -name "oauth*"` **0 命中** | ❌ **不适用**。给不存在的流程提建议 = D13 那次「编码一个不存在的需求」的同款错误 |
| ② | **per-model 冷却锁** | `accountFallback.js:106` `MODEL_LOCK_PREFIX = "modelLock_"`；`:110` `MODEL_LOCK_ALL`；`getModelLockKey(model)` | **主项目无**：`grep "model_cooldown\|modelCooldown\|model_lock\|perModelCooldown" --include="*.go"` **0 命中**。`singleflight` 只用于配置重载（`main.go:874`）与 API Key reveal（`domains/quotafetcher/`），**不覆盖熔断/冷却** | ✅ **唯一真正适用的推荐** |
| ③ | **502/503/504 短暂等待后再 fallback** | `combo.js:158-165`：`cooldownMs > 0 && <= 5000 && status ∈ {502,503,504}` ⇒ `await sleep(cooldownMs)` 再落下一个模型，注释明写「给短暂过载一次恢复机会而不是立即跳过」 | `executor_dispatch.go` 内 `time.Sleep`/`backoff` **0 命中**；但**我未完整追完候选重试循环的控制流**（只读到 `:1075-1100` 的失败日志块） | ⚠️ **只验证了一半**。可作为**待核线索**登记，不作为结论 |

### 2.2 唯一站得住的推荐（②）

**9router 做法**：冷却锁的粒度是「账号 × 模型」——某模型 429 只锁该模型，
同账号其他模型不受影响（`modelLock_${model}`，未知模型时用 `modelLock___all` 兜底）。

**主项目现状**：`domains/credential/breaker.go` 的熔断是**凭据级**的。
即一个模型 429 会连带影响该凭据下**所有**模型的路由。

**代价（子代理已指出，我认可）**：`modelLock_*` 是连接记录上的**扁平动态字段**，
主项目若用关系表需要清理过期键——9router 自己是懒清理（`auth.js:257-262`）。
**这不是纯增益，是拿「键空间膨胀 + 清理责任」换「更细的熔断粒度」。**

⇒ 登记为**待裁决第 30 条**，**本轮不实施**（改熔断粒度会改变路由行为，
且 ②的收益依赖具体流量形态，我手上没有「单凭据多模型 429 互相拖累」的真实发生率证据）。

### 2.3 9router 明确不如主项目

- **无熔断器、无半开探测**（0 命中双重复核 + README 书面佐证）：只有「到期时间戳」一种表达；
- **无权重、无排队、无并发上限**，靠一个进程内 Promise 链全局串行化（`auth.js:9,25-27`）；
- **凭据选择与 DB 强耦合**：每次选账号 3 次异步 IO（`auth.js:55,100,133`）；
- **子代理发现的死代码**：`filterAvailableAccounts` / `applyErrorState` / `resetAccountState`
  三个导出**全仓仅定义处 + index 再导出，0 个生产调用方**——生产路径走 `auth.js` 内联过滤。
  （这一条与主项目审计中反复出现的「有定义无调用方」是同一族，**值得记**。）

---

## 3. 本轮的方法论收获

1. **「子代理产出」与「可执行结论」之间隔着一层主项目适用性检验。**
   本轮 3 个推荐里有 2 个过不了这层检验——**若不查就写进方案，就是 D13 事故的复刻。**
2. **「未验证一半」要如实写成线索，不能写成结论。** 模式 ③ 我只读了失败日志块，
   没追完重试循环控制流，就明确标注为待核，而不是顺着子代理的结论写。
3. **跨项目的负面结论比正面结论更依赖复核方法。** 本轮「9router 无熔断器」之所以敢采信，
   是因为它同时有 **0 命中双路复核**和 **README 的书面自陈**两条独立证据。

---

## 4. 本轮未做

- 未改任何代码、配置或 schema（纯文档）。
- 未追完主项目候选重试循环 ⇒ 模式 ③ 未定性。
- 未审计 `cc-gateway` 的 `docs/vpn-integration-analysis.md`（子代理判为**待办清单而非实现**，
  我未复核，若后续要评其代理订阅/节点切换设计再单开一轮）。
- 未审计 `9router` 的 `open-sse/mitm/`、`open-sse/rtk/`、`gitbook/`。
