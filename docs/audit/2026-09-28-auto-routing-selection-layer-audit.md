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

## 六、遗留与建议（不在本轮范围）

1. **词表补齐是更彻底的解法**。本修复让"词表缺失"不再有害，但 code/creative 类任务**仍然无法按能力选型**——它们只是退回到按价格/通道质量/可靠性选。真正的能力选型需要给模型库补 `cap:code` / `cap:creative` 等标签（或启用 `models_canonical.strengths` 运营标注，950 行全空）。
2. **基线门禁已饱和**。`baseline.json` 阈值 = 当前值 = 满分，`auto-testbench` 无法度量任何选型改进。建议在 GRRQ 之外补一个**选型层**指标（如"坍缩率"= `fallback_used` 占比），当前 124/240 = 51.7% 应作为待收敛基线。
3. **E2E 采集器在 429 时丢弃判定**。`cmd/autoroute-e2e-audit` 把 `pass` 置 null（`*bool`），导致 240 例全 429 时报告层无法呈现分类正确率——虽然 `decision.task_type` 已捕获、可事后回收。建议让上层从 error 行也汇总分类判定，否则一次上游抖动就会让整轮 E2E "看起来没结论"。
4. **P2 TierSelector 仍是 G1 的真正堵点**。本轮修复的是选型退化（P0 范畴），"人工 apply 的分层建议运行时消费为零"这一原始缺口未动，仍按设计稿待执行。

---

## 七、复现方式

```bash
# 离线（分类层门禁）
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
