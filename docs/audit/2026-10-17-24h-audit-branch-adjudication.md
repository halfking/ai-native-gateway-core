# 2026-10-17 24h 审计轮 · 未合并子分支逐条裁决

> 范围：`git branch -r` 中全部 7 条非 `main` 远端分支。
> 判据：**内容是否在 main 已被独立落地**，而不是「diff 是否为空」。
> 删分支前对每条做逐文件/逐特征取证，取证结论写在本文件内；
> 3 条非祖先分支的 SHA 留档（见 §四），删除后仍可按 SHA 取回。

## 〇 结论速览

| 分支 | 最后提交 | ahead | 裁决 | 依据 |
|---|---|---|---|---|
| `llm-gateway-go-5` | 2026-09-28 | 0 | **删** | main 真祖先，无独有提交 |
| `main-merge` | 2026-09-03 | 0 | **删** | main 真祖先，无独有提交 |
| `main-final-20260827` | 2026-08-27 | 0 | **删** | main 真祖先，无独有提交 |
| `main-merge-provenance-20260903` | 2026-09-04 | 0 | **删** | main 真祖先，无独有提交 |
| `fix/w5-ml-ci-runbook` | 2026-09-07 | 1 | **删** | 实质代码已在 main 独立落地（§一） |
| `2026-07-25-realtime-routing-self-heal` | 2026-09-11 | 1 | **删** | 24/24 文件在 main 有逐字节相同副本（§二） |
| `fix/w6-monitor-docs` | 2026-09-07 | 1 | **吸收后删** | 两处实质改动均被 main 显式取代，仅文档缺口有效（§三） |

**净结果：7 条全清，无一条需要 `git merge`。** 唯一有价值的是 w6 的一处
文档缺口，已按 main 当前代码事实重写落地（见 §三末）。

---

## 一、`fix/w5-ml-ci-runbook` —— 实质代码已被 main 独立落地

分支唯一提交 `3147f5ef4`（2026-09-07），7 文件 +327/-14：
CI 工作流、runbook、`ml-training/config.yaml`、`src/data_loader.py`、
`tests/test_data_loader.py`、两份 README。

**取证（逐特征核对 main HEAD `a4e2480ea`）：**

| 分支声称的变更 | main 现状 | 位置 |
|---|---|---|
| `annotation_require_known_label` 默认强制 True | 已在，且 `data_loader.py:461` 的 fallback 也是 `True`（不是分支的 `False`） | `ml-training/config.yaml:31`、`ml-training/src/data_loader.py:461` |
| `merge_annotations(..., require_known_label=True)` 默认值 | 已在，且带「拒绝 + 计数 + UserWarning」三件套 | `ml-training/src/data_loader.py:194,222-236` |
| 对应回归测试 | 已在，含「默认即强制」「显式 False 是唯一退出途径」两条 | `ml-training/tests/test_data_loader.py:97-138` |
| `ml-training-ci.yml` 工作流 | 已在，且比分支版演化更新 | `.github/workflows/ml-training-ci.yml` |
| pytest 依赖 | 已在 | `ml-training/requirements-dev.txt` |
| 灰度回滚 runbook | 已在，且从 187 行继续演化 | `docs/ml/routing-ml-canary-rollback-runbook.md` |

分支版与 main 版文件 md5 不一致（如 runbook `69478305…` vs `2a6a87b2…`），
**这不是「未合并」的证据，恰恰是「已被超越」的证据** —— 若照搬反而会
用 40 天前的版本回退掉后续演化。裁决：删。

> 量具提示：本条若只看 `git diff main...branch` 会得到 7 文件全非空，
> 误判为「未合并」。分支与 main 的 merge-base 是 2026-09-07，
> 中间隔了 4400+ 提交，`diff` 反映的是**双向漂移**而非「待合入内容」。

---

## 二、`2026-07-25-realtime-routing-self-heal` —— 24/24 文件已在 main

分支唯一提交 `2a7696c46`（2026-09-11），24 文件 +9536，全部为
`docs/superpowers/plans/*.md` 归档文档，**零代码**。

**取证：** 24 个文件按 basename 在 main 全文检索，命中率 24/24；
逐个 md5 比对：

- **23 个与 main 副本逐字节相同**（main 已被 `docs-archive` 流程重定位到
  `docs/archive/...`）。
- **1 个「不同」经查是同名不同文档**：分支把该 SPEC 放在 `plans/`，
  而 main 的 `docs/archive/2026-07/process/2026-07-25-…` 存的是
  **handoff**（另文档）。改与 main 的
  `docs/archive/process/superpowers/specs/2026-07-25-…` 比对，
  md5 `527721af…` **完全一致** —— 即 SPEC 本身也已在 main，
  只是分支的副本停留在归档前的旧路径。

裁决：内容零丢失，删。

---

## 三、`fix/w6-monitor-docs` —— 两处实质改动被 main 显式取代，吸收文档缺口

分支唯一提交 `601bd36d1`（2026-09-07），6 文件 +613/-94。两处实质改动：

### 3.1 F-7「热图接入 Prometheus 打点」→ 被 main 的 slog 方案取代

分支在 `admin/credential_monitor_heatmap.go` 引入 `promauto` 五个指标
（`llmgw_credential_heatmap_*`）。main HEAD 该文件 **无任何 `promauto`**
（`grep -c promauto` = 0），但已有 `recordHeatmapQueryMetrics()`
（`admin/credential_monitor_heatmap.go:244-260`），带显式取舍说明：

> 「接入既有 slog 结构化日志体系，**后续可迁移至 OpenTelemetry metrics**」

即 main 不是「漏了可观测性」，而是**主动选了 slog→OTel 的路线**，
并已覆盖调用量 / 成功失败 / 延迟 / 数据点数 / 慢查询阈值(5s)。
照搬分支会引入一套与已声明路线相反的 Prometheus 依赖点。**不并。**

### 3.2 F-7「1min 进程内缓存 + singleflight 让 cache_hit 真实化」→ 被 main 显式回退

main `admin/credential_monitor_heatmap.go:235-236`：

```go
// cache_hit 现由缓存层控制；无缓存层时恒为 false（实时查询）
resp.Meta.CacheHit = false
```

这是**刻意**保留的诚实语义（宁可恒 false，不谎报命中）。
且 main 近期在重端点上统一走 **stale-while-revalidate**
（见 `1233e7931`、`ff83f3c12`、`a03b60bde` 一族提交）。把 40 天前的
端点内缓存并回来，会与该方向冲突并让 `cache_hit` 重新变成
「进程内命中」而非「缓存层命中」。**不并。**

### 3.3 F-13 文档对齐 —— 唯一有效部分，已按当前代码事实重写落地

分支把 `exclude_self_test`、epoch 分桶等写进部署指南。核对发现这是
**真实缺口**：main 代码里两个行为都在

- `exclude_self_test`：`admin/credential_monitor_heatmap.go:80,88,106-110`
- epoch 对齐分桶：`admin/credential_monitor_heatmap.go:265,457,667`

但 `docs/deployment/credential-monitor-heatmap-deployment-guide.md`
对两者的命中数均为 **0**（`grep -ci`）。分支那份是 40 天前的**整篇
改写**（525 行 vs main 492 行），直接覆盖会回退后续演化；
故按 main 当前代码重写为增量小节（见该文档新增 §6.2/§6.3）。

裁决：缺口吸收后删分支。

---

## 四、留档 SHA（删除后可按 SHA 取回）

```
fix/w5-ml-ci-runbook                  3147f5ef4f12827227ee8c9924b0c4e94aee59c8
2026-07-25-realtime-routing-self-heal 2a7696c467dbb858cfc8ab1d89adb7137ff8229b
fix/w6-monitor-docs                   601bd36d1ecc66b6b1b3c8883ec8eb91cd57a7d9
```

对象未必仍在远端（取决于服务端 GC 窗口），但 commit id 已在本文件留档。

## 五、本轮新增的可复用量具纪律

**「分支 diff 非空」≠「分支有未落地内容」。** `git diff main...branch`
在 merge-base 距今很远时反映的是双向漂移。正确判据是
「**逐特征/逐文件在 main 找等价物**」——本轮 3 条非祖先分支里有
2 条靠这一判据才看清是「已被超越」而非「待合并」。
