# AUTO 路由实测专项：分类正确但选型退化为「热度兜底」

- 日期: 2026-09-28
- 范围: `model=auto` 端到端路由能力（分类层 + 选型层），对照 `docs/planning/AUTO_ROUTING_CLOSED_LOOP_V2_PLAN.md` P0/P1 已交付基线
- 证据级别: 本轮在本地 8782 实跑 240 例 E2E 套件 + 直连 DB 取标签词表 + 源码定位；P2 接线状态取自仓内现状，未重连远端库
- 结论: **分类层可信（240/240），选型层对 11 类任务中的 5 类完全失效**——`code` / `code_audit` / `creative` / `intent_classification` / `vision` 恒定坍缩到 48 小时热度兜底，同一批 124 例全部选中同一个模型

---

## 一、进度对齐（动手前）

| Phase | 状态 | 证据 |
|-------|------|------|
| P0 测试地基 + 前端最后一公里 | 已交付 | `80bb3675a`；`cmd/auto-testbench/` + `scripts/auto-testbench.sh` + 三份套件 240 例 |
| P1 调参提案闭环（回路 C） | 已交付 | `97d8870aa` |
| P2 TierSelector 接线 | **设计稿未实现** | `AUTO_ROUTING_V2_P2_TIER_SELECTOR_DESIGN.md` 抬头自述"本稿不含任何已写/已跑的接线代码"；`grep NewTierSelector` 仅 `autoroute/tier_selector.go:78` 定义处，无生产构造点 |
| P3 V3 影子分类 | 未开始 | 无 `AUTO_V3_SHADOW` flag 引用 |

**离线回归当前状态**：`scripts/auto-testbench.sh` 240 例 accuracy=1.0 / macro_f1=1.0 / GRRQ=100 / GATE PASS。
该基线与 `cmd/auto-testbench/testdata/baseline.json` 的阈值完全相等（min=1.0/1.0/100），**门禁已无区分力**：任何回归都会 FAIL，但任何"改进"也无法被度量——它只证明分类层与随仓套件一致，不证明选型层。

---

## 二、实测发现

### 2.1 分类层：240/240 全对

用 240 例套件打真实网关，从 `X-Gw-Auto-Decision` 头取回决策（即使上游 429，决策头仍已写出，故分类判定仍可回收）：

```
total rows: 240
rows with captured decision: 240
classification correct: 240/240 = 100.0%
```

分类器（含 `heuristic_v2` + IDE 强信号 + 视觉硬覆盖）表现良好，**不是本次问题所在**。

### 2.2 选型层：5 类任务 100% 坍缩

按期望任务类型统计 `fallback_used`：

| expected_task | fallback_used | 样本 |
|---|---|---|
| code | **49/49 (100%)** | 49 |
| creative | **31/31 (100%)** | 31 |
| intent_classification | **20/20 (100%)** | 20 |
| code_audit | **12/12 (100%)** | 12 |
| vision | **12/12 (100%)** | 12 |
| chat | 0/30 | 30 |
| reasoning | 0/29 | 29 |
| planning | 0/19 | 19 |
| function_call | 0/15 | 15 |
| agent | 0/13 | 13 |
| long_context | 0/10 | 10 |

候选数分布：`{1: 124, 3: 116}`——**恰好 124 例只剩 1 个候选**，与上表 5 类之和精确吻合。

选中模型分布：

```
minimax-m3          124   ← 全部是坍缩的那批
deepseek-v4-flash    44
glm-5.2              42
glm-5.1              30
```

即：**52% 的请求，其"按任务定位模型"的能力实际未生效**，退化为"选过去 48 小时最热的一个模型"。

### 2.3 二次放大：单候选池 → 429 无处可退

坍缩后的候选列表只有 1 个模型。实测中该模型（minimax-m3 / credential 21）上游被限流：

```
{"name":"code_ide_cursor_fixnil", "expected_task":"code", "got_task":"code",
 "decision":{"chosen_model":"minimax-m3","chosen_credential_id":21,
             "fallback_used":true,"candidates_top3":[{...,"composite_score":50,"price_score":50}]}}
```

网关日志：

```
safety_net_defer_fired  attempt_err_code=rate_limit_exceeded
http_request  status=429  duration_ms=88  error.kind=rate_limited
```

选型坍缩把"候选多样性"一并丢掉了：首选模型被限流时没有第二、第三候选可切换，直接 429 返回客户端。
（240 例本轮全部 429，是本地 `kx` 上游拥塞的环境问题，与本次缺陷正交；但**坍缩确实放大了它**——选型正常时 116 例是有 3 候选可退的。）

---

## 三、根因：required-tag 词表与线上 `models_canonical.tags` 词表不同构

`autoroute/scoring.go:427` `requiredTagsForTask` 要求：

| task | required tags |
|---|---|
| code | `code`, `programming` |
| code_audit | `code`, `review`, `security` |
| creative | `creative`, `writing` |
| intent_classification | `classification` |
| vision | `vision`, `multimodal` |
| reasoning | `reasoning`, `math`, `logic` |
| agent | `agent`, `tool_use`, `function_call` |
| function_call | `function_call`, `tool_use` |
| planning | `reasoning`, `planning`, `analysis` |
| long_context | `long_context`, `128k`, `200k`, `512k`, `1m` |

线上实际词表（`select unnest(tags) from models_canonical`，950 个 canonical）只有：

```
cap:long-context(69)  cap:tool-use(31)  cap:function-call(31)  cap:reasoning(14)
cap:vision(19)  modality:multimodal(19)  modality:text(68)
family:* / version:*  (family:doubao / family:openai-gpt / family:deepseek ...)
```

**没有任何 code / programming / creative / writing / classification / review / security 能力标签。**
`models_canonical.strengths`（设计为"比 tags 更精准"的运营标注列）**950 行全空**，没有兜底信号。

于是：

1. `TaskMatchScore(code, tags)` 对**每一个**候选都返回 0（唯一能蹭到的是 `family:codex` / `family:codegemma` / `family:starcoder2` 这类家族名里的 "code" 子串，属巧合而非能力声明）。
2. `autoroute/recommend_v2.go:272` 的 `MatchScore < 30` 判定成立 → **整池候选被丢弃**，换成单个 48h 热度兜底候选。
3. 兜底候选被写死 `Composite:50, MatchScore:<原值>, PriceScore:50, IsFallback:true`——正是实测看到的 `composite=50, match=0, ncand=1`。

注：`normalizeTagSeparators`（2026-09-14 审计）已修过下划线/连字符不同构，但**修的是分隔符，不是词表本身**。词表缺失是另一层问题，此前未被识别。

---

## 四、修复

### 4.1 判据：区分「不匹配」与「无从匹配」

原实现把两种事实压成同一个 0：

- 这个模型**不适合**这个任务 → 0（合理）
- 模型库**根本没有**这类任务的词汇，**任何**候选都只能是 0 → 0（误导）

修复引入两个符号（`autoroute/scoring.go`）：

- `TaskMatchScoreUnknown = 0.5` —— "该维度无信息"，与 `chat` 早已在用的中性值一致（`return 0.5 // chat or unknown → don't gate`）。
- `TaskVocabularyRepresented(task, cands) bool` —— 池内是否**至少有一个**候选携带该任务所需的任一能力标签。

### 4.2 接线（`autoroute/recommend_v2.go`）

```go
vocabularyPresent := TaskVocabularyRepresented(task, available)
if len(available) > 0 && !vocabularyPresent {
    for i := range available {
        available[i].TaskMatchScore = TaskMatchScoreUnknown
    }
}
```

```go
// 48h 热度坍缩只在标签维度真有区分度时才成立
if len(scored) > 0 && scored[0].Breakdown.MatchScore < 30 && vocabularyPresent {
```

**fail-open 方向的选择**：词表缺失时把该维度置为中性 0.5，而不是给模型打低分。理由——
中性化保留了价格/通道质量/可靠性的真实排序能力，也保留了候选多样性（429 时仍可换模型）；
反向选择（按"该维度无效"整体跳过）会让 price/quality 权重被静默放大，改变已有 composite 语义，风险更大。

### 4.3 回归钉桩（`autoroute/task_vocabulary_regression_test.go`，5 个用例）

| 用例 | 钉住的事实 |
|---|---|
| `TestTaskVocabularyRepresented_MatchesLiveTaxonomy` | 按**线上真实标签**逐任务断言 present/absent |
| `TestTaskVocabularyRepresented_EmptyPoolAndUntaggedModels` | 空池/无标签池都返回 absent，调用方只需一个 bool |
| `TestTaskMatchScore_AbsentVocabularyIsNotZero` | 中性值必须非 0，且等于 0.5 |
| `TestRecommendV2_AbsentVocabularyKeepsScoredPool` | **主缺陷钉桩**：code 任务必须保住 ≥2 候选、不坍缩、match≠0 |
| `TestRecommendV2_DiscriminatingLowMatchStillFallsBack` | 词表**有**代表性时，`MatchScore` 落在 30 边界不坍缩（防止过度修正） |

已验证该测试对**修复前**逻辑确实失败（回滚改动后 3 条子断言全红），非空转。

---

## 五、验证结果

### 5.1 同一基线前后对比（同 base commit、同一套件、同一网关）

`autoroute` 包修复前后分别 `scripts/deploy-local.sh` 部署，再各跑一轮 240 例 E2E 套件：

| 指标 | 修复前 | 修复后 |
|---|---|---|
| 分类正确率 | 240/240 | 240/240（无回退） |
| **坍缩率 `fallback_used`** | **124/240 = 51.7%** | **0/240 = 0.0%** |
| 候选数分布 | `{1: 124, 3: 116}` | `{3: 240}` |
| 实际派发成功（非 429） | 0/240 | 7/240 |
| 工具判定 PASS | 0 | 7 |

分任务坍缩率（修复前 → 修复后）：

| expected_task | before | after |
|---|---|---|
| code | 49/49 | **0/49** |
| creative | 31/31 | **0/31** |
| intent_classification | 20/20 | **0/20** |
| code_audit | 12/12 | **0/12** |
| vision | 12/12 | **0/12** |
| chat / reasoning / planning / function_call / agent / long_context | 0 | 0（对照组不变） |

单点复现（部署后实测）：

```
code    : ncand 1→3, fallback true→false, match 0→50, chosen minimax-m3→glm-5.2
creative: ncand 1→3, fallback true→false, match 0→50, chosen minimax-m3→glm-5.1
chat    : ncand 3,  fallback false,      match 50   （对照组，未变）
reasoning: ncand 3, fallback false,      match 33.3 （对照组，未变）
```

**关于"实际派发成功 0→7"**：本轮本地 `kx` 上游整体拥塞，240 例绝大多数返回 429。
这与本次缺陷正交（对照组 116 例修复前同样 429），但坍缩确实放大了它——选型正常时
有 3 候选可退，坍缩时首选被限流即无处可退。修复后候选多样性恢复，7 例在同样的
拥塞环境下真正派发成功并被判 PASS，即为该恢复的直接证据。

### 5.2 测试与门禁

| 检查 | 结果 |
|---|---|
| `go build ./...` | 通过 |
| `go test ./autoroute/` | ok（6.5s） |
| `go test ./cmd/auto-testbench/` | ok |
| `go test ./domains/streaming/` | ok（88.1s） |
| `bash scripts/auto-testbench.sh` | 240 例 accuracy=1.0 / macro_f1=1.0 / GRRQ=100 / **GATE PASS**（分类层未回退） |
| `scripts/deploy-local.sh` | `VERIFY_PASS=1`，build_seq=2277，providers=587 creds=7 failed=0 |

---

## 六、遗留与建议

### 6.1 本轮已收口

**选型层度量缺失（原 §六 遗留 #2 + #3）已在本轮补齐**。`cmd/auto-testbench` 原先只有
分类层门禁，且合并 E2E 报告时有两个缺陷导致选型层完全不可见：

1. **`pass` 解码吞掉上游故障**。`autoroute-e2e-audit` 对上游报错（429/派发失败）的用例写
   `"pass": null`，而 `e2eRow.Pass` 是 `bool`——`null` 解出来是 `false`，于是**每一次上游
   抖动都被当成分类 FAIL**。本轮 240 例全 429 时报告层显示 `0/240 pass`，而分类层实际
   240/240 全对；读者会据此得出完全相反的结论。
2. **选型层从未被读取**。`e2eRow` 压根没解 `decision` 字段，所以"整池坍缩到 48h 热度
   兜底"——本轮 124/240 例的正是这个缺陷——在合并报告里**不产生任何信号**。

修复：`Pass` 改 `*bool` 区分"判错"与"没判成"；`e2eRow` 增加 `decision` 解析；新增选型层
指标（坍缩率、单候选池数、坍缩分任务归因、选型口径下的分类正确率），并随 JSON 报告落盘。
另修一处本轮自己引入的缺陷：同一行会被记两次 `Failures`（`pass` 分支 + 决策分支），
现按"每行最多记一次"收敛。

用本轮真实数据前后对比验证（同一套件、同一网关）：

```
修复前：e2e layer: 0/240 pass, 240 upstream-error
        collapse 124/240 (rate=0.5167)  code 49/49 code_audit 12/12
        creative 31/31 intent_classification 20/20 vision 12/12
        classification (decided): 240/240 (1.0000)   ← 分类与选型彻底解耦可见

修复后：e2e layer: 7/240 pass, 233 upstream-error
        collapse 0/240 (rate=0.0000)，无坍缩归因
        classification (decided): 240/240 (1.0000)
```

即：现在这类缺陷会被 harness **自动报出来**，不再需要事后手工捞 JSONL。
新增回归测试 `cmd/auto-testbench/e2e_selection_test.go`（7 例），覆盖 null-pass 语义、
坍缩可见性、健康运行、无决策行排除、错误掩盖真 miss、JSON 落盘、空输入与 R64 用法错误契约。

**后续收口（R75 子代理发现 #7）**：上述两个缺陷修的是 console 与 JSON 出口，但
`reports/auto-testbench.md` 仍只有 pass/failures——人读的主产物看不到选型层，
「124/240 坍缩」与「健康运行」的 markdown 完全相同，盲点只是从机器可见挪到了人
不可见。E2E 节现已补齐：上游报错数（说明其非分类判定）、以**取到决策的用例**为
分母的选型层坍缩率与单候选池、decided 口径分类准确率、坍缩分任务归因表；渲染逻辑
抽为 `e2eMarkdownSection` 并加两条回归（坍缩 run 断言数值与归因表、健康 run 断言
真实 0 值且不吐空表）。教训入册：**新增报表指标时 console / JSON / md 三个出口必须一起改**。

### 6.2 仍待处理

1. **词表补齐是更彻底的解法**。本修复让"词表缺失"不再有害，但 code/creative 类任务
   **仍然无法按能力选型**——它们只是退回到按价格/通道质量/可靠性选。真正的能力选型需要
   补 `cap:code` / `cap:creative` 等标签。本轮已查清补齐的真实规模，**这决定了它不是代码
   活而是数据/运营活**：

   | source | 模型数 | 带 `cap:*` 标签 |
   |---|---|---|
   | discovery | 418 | 3 |
   | provider_refresh | 318 | 1 |
   | db | 96 | 76 |
   | seed / auto_discovered / migration-* | 118 | 3 |

   全库 **950 个 canonical 中仅 80 个**带任何能力标签，且集中在 `source='db'` 的人工策展
   数据上；`discovery` 自动发现的 418 个模型几乎全是裸 `family:*`。补齐意味着要给约 870 个
   模型标注能力——这是**需要模型知识的数据运营任务**，靠猜会让真实流量被路由到错误的模型，
   因此本轮不做。`models_canonical.strengths`（设计为"比 tags 更精准"的运营标注列）950 行
   全空，是补齐的现成落点。
   **R77 订正**：本条曾被误当作 code_audit 坍缩的根因。实测证明坍缩的真因是判定口径——
   `TaskVocabularyRepresented` 把 `family:codex` 这类**分类标签**当成 `cap:code` **能力词表
   在场**的证据，那是代码缺陷，已在 §6.3 用 `isCapabilityTag` 根修（坍缩 12/240 → 0/240）。
   本条仍然成立且仍要做，但性质变了：它是**选型质量**提升（让 code/creative 真正按能力选型），
   不再是「路由坍缩挡在前面」的阻塞项。
2. **门禁仍只覆盖分类层**（已于 R77 收口，见 §6.3）。坍缩率目前**只报告不卡门**：`baseline.json`
   是离线产物，而坍缩率必须有 E2E 决策头才能算，离线门禁拿不到。给它加门禁需要一个 E2E 基线文件
   （本轮实测值 0/240 可作起点），属于下一步独立决策。
3. **P2 TierSelector 仍是 G1 的堵点，但本轮启动评估结论是：现在接线零收益且爆炸半径大，
   它的前置不是「接线工作」而是「V3 分类器落地」**。详见 §6.4。

---

### 6.3 R77 实跑复核 + L-5 根修（2026-09-28 晚）：240 例 HEAD 实测，code_audit 坍缩归零

§6.1/§6.2 里写的「修复后 0/240 坍缩」当时是对的，但下面第一轮实测在**同一套 240 例上测出
5.00% 坍缩**，且全部集中在 `code_audit`——本节给出归因与根修，现状已回到 0/240。

**跑法**：本地 8782 是 build 2296 / SHA `0ae9b5e2`，落后 HEAD 18 个 commit，**不含** L-4 /
planning-artifact / M-7 三笔修复，直接拿它跑 E2E 等于旧码自证。故以 HEAD 源码起旁挂实例：
同一 DSN / 同一 Redis（`LLM_GATEWAY_BG_MODE=data-plane` 关写侧后台任务，避免与 8782 双跑
reaper），监听 `127.0.0.1:8783`。

**实例确实含修复的证明**（同 key、同 prompt、两端口对照）：

| prompt | 8782（旧 build） | 8783（HEAD） |
|---|---|---|
| `写一个Django中间件` | `task=chat conf=0.10` | `task=code conf=0.65` |

**同基线前后对比（同一套 240 例 = v1 60 + v2 40 + v3 140，决策头覆盖率均 100%）**：

| 指标 | L-5 修复前（HEAD `ab2b7b9d0`） | L-5 修复后 | 判定 |
|---|---|---|---|
| 分类正确率（decided） | 240/240 = 1.0000 | 240/240 = 1.0000 | 不变（分类层未受影响） |
| 坍缩率 `fallback_used` | **12/240 = 5.00%** | **0/240 = 0.00%** | **根修** |
| 单候选池 | 12/240 | 0/240 | **根修** |
| 坍缩分任务 | `code_audit` 12/12 | 无 | **根修** |
| 上游 429 | 216/240（不算分类判决） | 232/240 | 环境噪声，见下 |

**根因不是数据活，是判定口径串了（此前误判为「~870 模型待标注」的数据/运营活）**：

`requiredTagsForTask(code_audit)` 写的是**能力**词表 `[code, review, security]`，
但 `TaskVocabularyRepresented` 只做子串匹配去问「池里有没有这个词」。线上真实存在的这些
**分类标签**全部命中子串 `code`：

```sql
family:codegemma   family:codex   family:starcoder2
version:codestral-latest          version:gpt-4o-audio-preview
```

模型族名叫 `codex`、版本名叫 `codestral`，并不能证明库里存在 code/review/security 的
**能力分类法**。于是 `vocabularyPresent` 被判 true → `recommend_v2.go:144` 的「词表缺失中性化」
被跳过 → 打分留在真实值 → 而 top 候选 minimax-m3 不带任何 code 标签 → `MatchScore 0 < 30`
→ `recommend_v2.go:339` 的 48h 热度坍缩守卫命中 → 候选多样性 3 → 1（`composite=50,
quality=0, reliability=0, tier 空`）。这正是 §2.2 记录的「坍缩放大限流」的复现条件。

关键区分：**这是代码缺陷，不是数据缺口**。此前把它归到 §6.2 第 1 条「~870 个 canonical 待
标注」是错的——不管给多少模型补 `cap:*` 标签，只要库里存在一个叫 codex 的族，
review/security 这两个 required 词就永远无法被独立验证是否为真词表，判定会持续被族名挟持。

**修法**（`autoroute/scoring.go`）：新增 `isCapabilityTag`，能力词表的「在场」判定只认
`cap:` 命名空间（以及无命名空间的裸标签，`Candidate.Tags` 历来支持 `["reasoning","code"]`
这种写法）；`family:` / `version:` / `modality:` 等命名空间是「这是哪个模型」而非「它能做什么」
的分类元数据，不再能冒充能力证据。**刻意不动 `TaskMatchScore`**：它做排序不做在场判定，
其子串宽松（`code` 匹配 `code_completion`）是有文档、有测试依赖的既有行为。

**验证**：
- 3 条新回归（`autoroute/task_taxonomy_tag_regression_test.go`）：分类标签不得让能力词表
  在场；真正的 `cap:*` 必须仍被判在场（反向护栏，防止把 R73 的「词表缺失中性化」与
  「在场但 winner<30 仍须坍缩」两条契约一起打死）；端到端 code_audit 池不得坍缩且 MatchScore
  必须中性化。修复前 3 条全红。
- 变异检验：摘掉 `isCapabilityTag` 过滤（还原旧的全标签子串匹配）→ 3 条全红；恢复后全绿。
- 真机复跑：同一 12 例 code_audit，`cand=1 / fallback=True / scores=[50]` →
  `cand=3 / fallback=False / scores=[79.2, 76.2, 76.2]`，分类 12/12 正确。
- 门禁：`max_collapse_rate` 保持 **0.05**（余量 12/240，第 13 例才红）。变异检验：修复后
  数据 → PASS(0)；修复前 12/240 @0.05 → PASS(0)（恰好边界）；@0.04 → FAIL(1)。

**口径订正**：首轮归档曾按 249 例报数（13/249 = 5.22%），那是错的——v3 套件有 9 行 `#` 注释
被误当用例并入，且最后 9 例被重复计入两次。套件头注写明总量是 60+40+140 = **240 例**，去重
后唯一 case 数正好 240。正确值是 12/240 = **5.00%**。两处归档文件均已订正为 240 行
（`-r77.jsonl` 为修复前、`-r77-l5fixed.jsonl` 为修复后）。

**关于上游 429**：232/240 是上游 429（`token.sensenova.cn` credential 25 独占 96/126 次上游
尝试），**与选型层正交**——带决策头的行即使 HTTP 429 也能拿到 task_type 与候选池，分类判决
不依赖上游成功。降速（每 10 例休 20–25s）实测无效，说明是 provider 侧持续限流而非打得太快。
（首轮跑 50/60 报错时曾误判「错误行没有决策头」，实为查错了 JSON 键：`decision` 是嵌套字段，
错误行同样有。）另有一个**独立的集中度风险**，本轮未修：96/126 次上游尝试打到单一 provider，
该 provider 一限流就同时打穿所有任务类型的退路。

### 6.4 P2 TierSelector 接线启动评估（R77，R66 点名的优先入口）

R66 把「auto TierSelector 接线启动评估」列为优先入口。本节是那份评估的结论，**不接线**，
只回答「在什么条件下接线才有意义」。钉桩见 `autoroute/tier_selector_wiring_prereq_test.go`
（4 条特征化护栏，变异检验确认有牙）。

**核心事实：全仓存在两套互不相交的 TaskType 词表。**

| | 词表 | 状态 |
|---|---|---|
| **V2** | `chat` `reasoning` `code` `agent` `creative` `long_context` `vision` `function_call` `code_audit` `intent_classification` `planning` | **生产分类器实际发出**（`NewHeuristicClassifierWithTuning`，`cmd/gateway/main.go:5036`）；240 例 E2E 实测发出的正是这 11 类 |
| **V3** | `architecture` `audit` `debugging` `coding` `refactoring` `testing` `devops` `documentation` `summary` `dependency` | `AllTaskTypesV3`（`task_types_v3.go:143`），**恰等于 `task_type_tier_config` 的 10 条种子行** |

而 `NewV3Classifier` 与 `NewTierSelector` **都零生产构造点**——`NewV3Classifier` 自身注释
还写着「V3 分类器保留供实验对照, 下轮清理候选」（`classifier_v3.go:40`）。

**所以把 TierSelector 接进生产热路径，今天拿不到任何按任务类型的真实配置。** 两条路都试算过：

- `enableV3=false`：`SelectTier` 第一行就返回 `tier-b`（`tier_selector.go:97-105`），
  对**所有**请求一视同仁。这是一道「一刀切硬过滤」，会把 tier-a / tier-c 候选全滤掉；
  离线套件实测 tier 分布 `tier-a=85 / tier-b=105 / tier-c=50`，爆炸半径远超「小步灰度」。
- `enableV3=true`：分类器仍发 V2 类型 → 配置查询全部 miss → 落 `getDefaultTier` →
  `TaskTypeTierMapping`（`task_types_v3.go:344`）无任何 V2 键 → 仍默认 `tier-b`。

两条路都是**零收益 + 大爆炸半径**。

**正确顺序**：V3 分类器先落地（P3，影子观察 ≥7 天是硬前置，规划 §4.6 plan:222-224 明文
「替换本身是独立裁决」）→ 之后 tier 配置词表才与运行时词表对齐 → TierSelector 才有真实
配置可消费。**TierSelector 不是一项独立的接线工作，它是 V3 分类器灰度的下游消费者。**
这与设计稿 `AUTO_ROUTING_V2_P2_TIER_SELECTOR_DESIGN.md` 的「影子 → 灰度 → 生效」不冲突，
但把它的**启动前置**从「排期」改成了「V3 分类器是否已在生产」。

**顺带核实的既有基础（R43 的 ensure 确实在工作，非空壳）**：真库
`task_type_tier_config` 存在且是 V370 形态（BIGINT `tenant_id`、
`min_confidence numeric(3,2) DEFAULT 0.70`、`UNIQUE (task_type, COALESCE(tenant_id,0))`），
10 条种子齐全。设计稿点名的 `cmd/gateway/main.go` 行号整体漂移 6–22 行
（`NewDecider` 5035→5041、`SetRoleLLMRouter` 5244→5250），语义未变。
两个 flag `AutoTierSelectorEnabled` / `AutoTierSelectorShadowOnly` 仍**未在 `config/` 定义**，
与设计稿 §89「待新增」一致。

**本轮不做**：不接线、不新增 flag、不改种子词表（V2↔V3 词表映射是**产品/路由策略裁决**，
不是代码能替 owner 定的）。owner 需要的决策已从「要不要接线」变成
「V3 分类器什么时候进生产」。

**§6.2 第 1 条的订正**：能力词表补齐（`cap:code` / `cap:creative` 等）**仍然是**让 code /
code_audit / creative 真正按能力选型的唯一解法——L-5 只保证「没有能力词表时别假装有」，
不会凭空造出选型能力。但它的紧迫性已从「数据活挡着路由」降为「选型质量提升」：修复后
code_audit 走的是价格 / 通道质量 / 可靠性三个真实维度，与 §6.1 中性化的设计意图一致。


---

## 七、复现方式

```bash
bash scripts/auto-testbench.sh

# 端到端（选型层）
export AUTO_AUDIT_API_KEY=<inference key>   # 非 admin key
go run ./cmd/autoroute-e2e-audit -gateway http://127.0.0.1:8782 \
  -suite autoroute/testdata/auto_matching_suite_v3.jsonl -out /tmp/e2e.jsonl

# 单点复现坍缩
curl -sD- -X POST http://127.0.0.1:8782/v1/chat/completions \
  -H "Authorization: Bearer $AUTO_AUDIT_API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"auto","messages":[{"role":"user","content":"写一个 Python 快速排序"}],"max_tokens":8}' \
  | grep -i x-gw-auto-decision
# 修复前：candidates_top3 只有 1 项、match=0、fallback_used=true
```

## 八、标签词表取证

```sql
-- 线上实际能力标签
select unnest(tags) t, count(*) from models_canonical group by 1 order by 2 desc;

-- 运营标注列（本轮全空）
select count(*) filter (where strengths is not null and cardinality(strengths)>0)
from models_canonical;   -- → 0 / 950
```
