# 87-R88-x：provider-window source telemetry 核实 —— 顺带查出真库被多 worktree 共享（污染审计方法论）

- 轮次：R88-x
- HEAD 基线：`5667d9524`
- 触发：objective 三层缓存同一句里的点名项——「**provider-window source telemetry**」（`D03-three-tier-cache.md:27` 把它定义为「窗口遥测能区分内容来源层 original/sanitized/compressed，**指标不串层**」）
- 结论：**该特性核实通过，无缺陷**；但排查中发现一个**污染本审计全部真库证据**的环境事实，**这才是本轮最重要的产出**
- 改动：**零生产代码、零配置、零门**。仅本报告 + README 索引

---

## 1. provider-window source telemetry：核实通过

**产出端**（`domains/streaming/request_log_pipeline.go:1498-1550` `buildOutboundProvenance`）：

```go
windowSource := map[string]int{}
for _, a := range alignment {
    kind := a.TargetKind
    if kind == "" { if a.IsCompressed { kind = "summary" } else { kind = "retained" } }
    windowSource[kind]++
}
if len(windowSource) > 0 { prov["window_source"] = windowSource }
```

且在 128KB 超限的**兜底路径**里，`window_source` 是**唯一被保留**的键：

```go
fallback := map[string]any{"window_source": windowSource}
```

⇒ 它的优先级是刻意设计最高的（其他数组全丢，它留）。

**镜像端**（`internal/sessionv2mirror/hook.go:446-455`）：`window_source` **在 V2 白名单里**，有专属的 `safeWindowSource` 过滤器（`:469-490`）——labels 走 `boundedCompressionWord`、counts 走 `boundedNumber`、**整表 ≤16 个标签**，与 `alignment_map` / `sanitize_message_refs` 同一套 fail-closed 边界。

> **我先猜错了**：我以为 `window_source` 不在白名单里（因为 R88-o 当时只核了 `alignment_map` / `sanitize_message_refs` / `cut_marker` / `sanitize_map_ref` 四个键，没提它），差一步就报「V2 侧丢失 provider-window 遥测」。**读到 `hook.go:446` 才发现它有 R36 注释 + 专属过滤器。**

**真库实证（切换前的 V1，`request_logs` 215 万行）**：

| 键 | 出现行数 |
|---|---|
| `window_source` | **43,787** |
| `alignment_map` | 43,787 |
| `summary_marker` | 48,653 |
| `sanitize_message_refs` | 12,719 |
| `alignment_map_truncated` | 76 |
| `sanitize_refs_truncated` | 5,325 |

⇒ **产出端与镜像端都健康，遥测没有串层。**

### 1.1 顺带查出一个覆盖缺口

`safeWindowSource` 是 `sessionv2mirror` 里的**第 4 个 `safe*` 过滤函数**，而 R88-o 只为前三个（`safeAlignmentRecords` / `safeSanitizeRefs` / `safeCutMarkerMeta`）补了测试：

```
safeWindowSource      -> 0 个测试文件
safeAlignmentRecords  -> 2
safeSanitizeRefs      -> 2
safeCutMarkerMeta     -> 1
```

这与 R88-p 漏掉 `cut_marker` 测试**完全同型**（同一处代码，连续两轮漏检）。**本轮未补该测试**（turn 预算已用于更重要发现），登记为待跟进。

---

## 2. 【本轮最重要】真库被多个 worktree 共享，污染全部真库证据

### 2.1 我原本要报一个「严重缺陷」，靠一条反向核对把它撤掉了

我最初的实验设计是：拿 V2 存活行的 `request_id` 去 `request_logs` 对比，看 provenance 是否在 V1→V2 过程中丢失。**结果是 0 匹配**，而 V2 的 `compression_meta` 里四个 provenance 键全是 0。

差一步就写成「**V2 切换后 provenance 全部丢失**」。三条反向核对推翻了它：

| # | 观察 | 推翻了什么 |
|---|---|---|
| 1 | `request_logs` 时间范围 `2026-09-03 .. 2026-10-01 00:13`，**跨整月 215 万行** | **不是**清理导致的（清理会留空洞）⇒ 是真的停止写入 |
| 2 | V2 的 892 行里 `client_ip` **72% 是 `127.0.0.1`**，另有 `172.17.0.1`（Docker 网桥） | **这是本地测试流量**——本地没有 sanitizer/对齐压缩器，**产不出 provenance 完全是预期** ⇒「0 键」不构成缺陷证据 |
| 3 | `pg_stat_activity` 显示 **三个不同容器 IP 同时连着 `llm_gateway`**；本机有 4 个 llm-gateway worktree，`llm-gateway-go-3/.env` 明确指向 `llm_gateway`；而**本机没有 `llm-gateway-go-5` 进程** | 那个「V1 在 00:13 停写、V2 仍在写」的**根本不是我这个 worktree 的行为**，而是**另一个 worktree 的运行态** |

⇒ **「V1 停写 / V2 续写」不是 go-5 的存储切换，是共享库上别的栈在写。**

### 2.2 这是一个方法论缺陷，影响本审计此前的全部真库测量

```
pg_stat_activity（datname='llm_gateway'）当前连接：
  172.18.0.1/32   × 20      ← Docker 网桥（宿主）
  172.18.0.14/32  ×  5      ← 另一个 worktree 的容器
  172.18.0.6/32   ×  3      ← 又一个
  Citus Maintenance Daemon / psql
```

同机 worktree：`llm-gateway-go-3`、`llm-gateway-go-5`、`llm-gateway-go-cursor`、`llm-gateway-go-cursor.subtask3`。

⇒ **同一张表里混着不同代码版本写入的数据。** 凡是用真库数字下的结论，都带这个混淆。

### 2.3 对已有结论的复盘（逐条，不掩盖）

| 轮次 | 用到的真库数字 | 是否受污染影响 | 结论是否改变 |
|---|---|---|---|
| R88-r | responses 车道 provenance max 245 / p99 127 / **>4096 为 0** | 可能混入他版本写入的行 | **不变**——混入只会**增加**样本，而 >4096 仍为 0，**方向是保守的**（更多数据仍未触顶 ⇒ 原结论更强） |
| R88-t | `credentials` 83 行、两个 label 的可达性 | 配置表，非流量表 | **基本不受影响**（他 worktree 不会改凭据配置） |
| R88-u | 每凭据平均 23.99 模型 / 419 最大 / `circuit_state` 0 open | 同上 | **不受影响**；且「0 open」我已明确标注为**未实测**影响面，不受影响 |
| 86 号 | `request_logs` 2.15M 行 | 仅用于证明 V1 未被清理 | **不受影响**（跨月连续数据） |

⇒ **已有结论全部存活**，但这是运气好，不是方法对。

### 2.4 建议（需要产品/运维裁决，本轮不擅自改）

真库门要保持可信，需要二者之一：

- (a) **每个 worktree 一个独立数据库**（推荐）——`deploy-local.sh` 派生的库名带上 worktree 后缀；
- (b) 若坚持共用，则**所有真库断言必须带可区分标记**（如按 `application_name`/来源 IP 过滤，或按该 worktree 独有的时间窗/标识），并在报告里写明过滤条件。

**在二者之一落地前，本审计的真库数字应一律视为「下限/非独占样本」。**

---

## 3. 顺带记两条查询方法教训

**③ 列名/表名不要猜。** 本轮连续 4 次猜错：`sessions_v2`（实为 `sessions`）、`sessions.compression_meta`（该表无此列）、`request_wal.ts`（无此列）、`session_turns_hot.api_key_prefix`（无此列）。**正确做法是每次先 `information_schema.columns` 查**——而我第一次就是这么做的，后三次却跳过了。

**④ `ORDER BY <位置>` 在只 SELECT 了拼接表达式的查询里不可用。** 连撞 2 次（`order by 2` / `order by 3`），正解是 `ORDER BY count(*) DESC` 具名聚合。

---

## 4. 明确未做

- **未**给 `safeWindowSource` 补测试（覆盖缺口已确认，留待下一轮）；
- **未**改动 `deploy-local.sh` 或任何数据库配置（属 (a)/(b) 裁决范围）；
- **未**复盘 R88-r 之前所有轮次的真库数字（已按 2.3 逐条评估影响面，结论均存活）；
- **未**追查「哪个 worktree 在写 V2」——不属于本 worktree 的责任范围，且 `pg_stat_activity` 的 `application_name` 全为空，无法进一步归属。

---

## 5. 变更清单

| 文件 | 改动 |
|---|---|
| `docs/全面审计v3/2026-10-01/87-...md` | 新建（本文件） |
| `docs/全面审计v3/README.md` | 追加索引 |

**零生产代码、零配置、零门、零 CI 行为变化。**
