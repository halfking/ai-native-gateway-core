# 04 — 多级缓存与转发

> 主题：会话状态缓存（压缩用）、语义/前缀缓存、多级 sticky 路由、转发/中继架构。本仓库的多级 sticky **多实例可用**（Redis+PG 落盘），远胜 omniroute 进程内 Map；债务是 V1/V2 双轨 + 四类 cache 无统一指标。

## 1. 现状对比

| 维度 | llm-gateway-go | omniroute |
|---|---|---|
| **会话状态缓存（压缩）** | 三级：L1 in-proc LRU(1024) → L2 Redis Hash(TTL 30min) → L3 PG(request_logs)（`session_cache.go:40-60`）；V2 元数据缓存（仅 metadata 无 body，`cache_v2.go:45-56`） | 无对应（压缩无跨请求状态缓存，靠 resultMemo） |
| **sticky 路由** | **三级真实多实例**：L1 session+model(1h) → L2 client+model(24h) → L3 client(7d)，内存 + Redis + PG 落盘（`routing/sticky.go:25-40,133-226,272-352`） | **进程内 Map**（MAX=200，TTL 15min，`sessionManager.ts:46`）—— 多实例不共享，sticky 失效 |
| **线上缓存 hook** | `domains/hooks/cache/hook.go`（PreRouting priority 50，hash tenant+model+TransformedRequest，命中短路）—— **exact-hash，非语义相似** | 无统一 hook；语义缓存在 chat 内联 |
| **旧缓存包（未接新 pipeline）** | `cache/semantic/`、`cache/prefix/`、`cache/delta/`、`cache/kv/` —— `hooks/cache/types.go:8` 明确“不依赖旧包”，是历史遗留 | 两级（mem LRU + SQLite），仅 `temp===0`，apiKeyId 明文前缀隔离（`semanticCache.ts:119,176,357`） |
| **前缀缓存分析** | 无独立分析器；cache_control 由 IR 透传 | `promptCache/prefixAnalyzer.ts:25`（分类前缀类型 + 置信度，token=length/4） |
| **转发/中继** | 路由 + 凭据健康 + URSM v2 + 重试/熔断（数据面 owner） | 账号 fallback + combo 多目标 + proxy egress（`accountFallback.ts`/`combo.ts`/`proxyAutoSelector.ts`） |
| **指标** | 各 cache 各自指标，无统一聚合 | `cache_metrics` 表（hits/misses/tokens_saved） |

## 2. omniroute 的优点（可吸收）

1. **语义缓存的严格可缓存门禁**（`semanticCache.ts:357,371`）：**仅显式 `temp===0`** 才缓存（省略 temp 不缓存，因 provider 默认可能非确定）。本仓库 `cache/hook.go` 的可缓存条件需核对——**吸收此严格门禁**可避免缓存非确定输出。
2. **apiKeyId 明文前缀隔离**（`:140`）：不把 apiKeyId 折进 digest，而作明文前缀，既隔离又保确定性（还规避了 CodeQL password-hash 误报）。**模式可吸收**（若本仓库语义缓存 key 设计有同类问题）。
3. **`cache_metrics` 统一表**（hits/misses/tokens_saved）：本仓库四类 cache 无统一指标。**吸收统一指标表**。
4. **prompt-cache 前缀分析器**（`prefixAnalyzer.ts:25`）：识别 system_only/system_and_tools/system_tools_history 前缀类型 + 置信度。本仓库缺前缀分析——**吸收**（支撑 02-C8 缓存感知压缩与 01-M4 cache-safe 注入）。
5. **通用 LRU 的 byte+count+ttl 三限制**（`cacheLayer.ts:27`）：本仓库会话状态 L1 仅 count 限制（`l1MaxSessions`），无 byte 限制——大 session state 可能撑爆内存。**吸收 byte 限制**。

## 3. 本仓库的优点（保留）

1. **三级 sticky 多实例可用**：L1/L2/L3 + Redis + PG 落盘，是 omniroute 完全没有的生产能力。保留。
2. **三级 sticky 的显式 level int API**（`StickyRedisStore.GetLevel/SetLevel`）：避免 omniroute 那样的“冒号数猜层级”脆弱启发式。保留。
3. **2026-06-25 修复了 sticky key 缺 model+session 导致跨会话/跨模型污染**的根因（`buildStickyKeys:496-532`）。保留。
4. **会话状态三级缓存（L1/L2/L3）**：omniroute 压缩无跨请求状态，每请求重算。保留。
5. **V2 元数据缓存只存 metadata 不存 body**（`cache_v2.go:42-44` 注释）：避免 L1 内存被 body 撑爆。保留。

## 4. 缺口 / 债务

| 编号 | 债务 | 证据 | 影响 |
|---|---|---|---|
| D1 | **V1/V2 会话状态缓存双轨，V2 生产关闭** | `shouldUseV2 return false`（同 03-A1） | V2 cache 实现齐全但 dead code |
| D2 | **四类 cache 无统一指标** | semantic/prefix/delta/kv 各自 | 无法回答“缓存整体命中率/节省 token” |
| D3 | **L1 仅 count 限制，无 byte 限制** | `session_cache.go` l1MaxSessions | 大 session state 撑爆内存风险 |
| D4 | **两个 sticky 实现** | `routing/sticky.go`（多级，真）vs `session/sticky_router.go`（单字段，旧） | 混淆、误用 |
| D5 | **sticky 概念散布 4 包** | `routing/`、`session/`、`ursm/v2/cache/`、`executors/` | 难整体推理 |
| D6 | **线上缓存 hook 可缓存门禁待收紧** | `domains/hooks/cache/hook.go`（exact-hash），temp 门禁待核 | 可能缓存非确定输出 |
| D7 | **无 prompt-cache 前缀分析** | 无 | 无法做缓存感知压缩/注入 |
| D8 | **旧缓存包（`cache/semantic\|prefix\|delta\|kv`）未接新 pipeline 且未退役** | `hooks/cache/types.go:8` 明确不依赖 | 死代码、误用风险 |

## 5. 优化建议（带优先级）

### D2（P1）统一 cache 指标 `NEW-DESIGN`（吸收 omniroute `cache_metrics`）
新建 `cache_metrics` 表（或复用现有 metrics 基建）：按 `(tenant, cache_layer, hit|miss, tokens_saved, ts)` 记录。四类 cache（semantic/prefix/session-state/sticky）统一上报。admin dashboard 聚合。
- 验收：能查到每层缓存命中率与 token 节省。

### D7（P1）prompt-cache 前缀分析器 `NEW-DESIGN`（吸收 omniroute）
新建 `domains/promptcache/`（或 `internal/ir/prefixanalyzer.go`）：输入 IR Messages，输出前缀类型 + 置信度 + 稳定前缀 hash。服务于 02-C8（缓存感知压缩）、01-M4（cache-safe 注入）。
- token 估算用 02-C4 的统一函数（不用 omniroute 的 length/4）。

### D3（P2）L1 加 byte 限制 `NEW-DESIGN`（吸收 omniroute）
`SessionCache.l1` 增加 maxBytes；evict 同时按 count 和 byte 触发。settings_kv 热加载 `cache.session_l1_max_bytes`。

### D6（P1）收紧线上缓存 hook 的可缓存门禁 + 评估旧包退役 `TARGET-BOUNDARY` + `NEW-DESIGN`
- 线上缓存是 `domains/hooks/cache/hook.go`（exact-hash），**不是** `cache/semantic/`（后者是历史遗留、未接新 pipeline，`hooks/cache/types.go:8` 明确）。核对 `hooks/cache` 的可缓存条件：若未严格 `temperature===0`，吸收 omniroute 严格门禁（省略 temp 不缓存）+ bypass header（`X-No-Cache`）。apiKeyId/api-key 隔离用明文前缀模式。
- **新增 D8**：评估旧包 `cache/semantic|prefix|delta|kv` 是否可随 V1 一起退役（先确认无生产引用）。

### D4/D5（P2）收敛 sticky 实现 `TARGET-BOUNDARY`
删除/弃用 `session/sticky_router.go`（单字段旧版）；统一到 `routing/sticky.go`。把 `ursm/v2/cache/sticky.go`、`executors/sticky.go` 明确为 sticky 的后端/消费方，文档化职责（见 07 审计的包依赖图建议）。

### D1（P0→P1）随 03-A1 放开 V2 cache
V2 cache 放开与 V2 读放开同节奏（03-A1）。V2 cache 只存 metadata，内存更省，是 V1 的升级目标。

## 6. 不做（明确非目标）

- **不引入 omniroute 的进程内 sticky Map**：本仓库多实例 Redis+PG 方案更优。
- **不引入 omniroute 的 combo/accountFallback 转发**：本仓库数据面转发（路由+凭据健康+URSM）已是 owner，不复制第二套转发。
- **不做 sqlite-vec/Qdrant 向量后端**：本仓库用 PG 单向量列。

## 7. 与其他主题的依赖

- D1 随 03-A1。
- D7 是 02-C8、01-M4 的前置。
- D2（统一指标）被 06 路线图作为各阶段验收的基础设施。
