# F3 热区 requests/ 双写量化与 owner 决策材料（2026-10-01）

- 挂账来源：docs/audit/2026-09-30-hotzone-p3p4-critical-audit.md F3 + e2e drill §六.7（维持挂账）
- 本文目标：把「telemetry 信封 vs v2 turn 数组同树双写」的触发条件、字节构成、
  生产流量速率写成 owner 可拍板的决策材料（修复方向 vs 接受现状）
- 数据来源：245 预发布生产流量实测（2026-10-01 STORAGE_MODE=full 激活轮）+ 代码实勘
- 状态：**材料已出，决策待 owner**

---

## 一、双写结构（代码实勘，两写方共用同一 RequestMirror 实例）

装配：`cmd/gateway/storage_mode_init.go:287`（requestMirror）→ `storageRt.bodyMirrorFn()`
被注入**两个**消费方：

| 消费方 | 接线点 | 触发时机 | 粒度 | 租户目录 | 文件名 |
|---|---|---|---|---|---|
| telemetry | `telemetry.Client.mirrorRequestBodies`（client.go:1207，persistRequestLog 入口） | **任何 PG 往返之前**（S4 停写门 `requestLogsWriteEnabled()` 同门） | 整请求信封（RequestBody/ResponseBody/OutboundBody 原文） | `ApplicationCode \|\| TenantID`（如 `ide/`） | `{requestID}.{req\|resp\|out}.json.gz` |
| v2 会话 | `SessionWriterV2.SetBodyMirror`（main.go:2651，mirrorTurnBodies :143 / mirrorFinalFull :160） | turn+bodies tx **commit 之后**（+ mirror outbox 重放同路径幂等重写） | per-turn 增量（RequestDelta/ResponseDelta）+ final_full 出向快照 | 会话租户（如 `default/`） | turn 同 `{requestID}.{dir}`；final_full 仅 out，名 `final_full:{session}` |

两写方文件名同构但租户目录通常不同（drill O2/R-2 的租户分裂口径）。实际呈现按
租户口径是否重合分两种（2026-10-01 生产实证，见 §三.3）：

1. **租户分裂**（application_code ≠ 会话租户，如 ide/openpocket/applicant 应用流量
   + default 会话）：同 rid 两套文件在不同租户目录**并存**，磁盘双份。
2. **租户重合**（application_code == 会话租户，如 default）：**同路径同文件名**
   `{tenant}/{date}/{rid}.{dir}.json.gz`——telemetry 先写（PG 往返前）、v2 后写
   （commit 后）覆盖。文件内容恒为 v2 turn 粒度（后写者赢），telemetry 信封份
   是纯浪费的写 IO，磁盘不放大。out 方向例外：灰度关时 v2 per-turn out 同名
   覆盖；灰度开时 final_full:{session}.out 与 telemetry {rid}.out 并存。

两环境 final_full 灰度均未开启（两处 requests 树 0 个 final_full 文件），当前
out 重复形态为重合租户同名覆盖 + 分裂租户两套并存。

对账脚本（scripts/hotzone-request-mirror-reconcile.sh）已按「租户目录判源 +
双写方去重」消化；重合租户覆盖写在脚本语义下即「该 rid 只有 turn 粒度一份」。

## 二、重叠度分析（哪一半是纯冗余）

| 方向 | telemetry 侧内容 | v2 侧内容 | 重叠判定 |
|---|---|---|---|
| req | 整请求信封（含全 history + system） | 本 turn 增量消息（RequestDelta） | **语义包含**：delta ⊂ 信封；多轮会话下信封字节 ≈ delta × 轮次均值倍 |
| resp | ResponseBody（网关聚合响应） | ResponseDelta（本 turn 响应片段） | 单 turn 请求≈同一内容双写；流式多段时信封≥delta |
| out | OutboundBody（出向上游载荷） | final_full 出向快照（灰度开）或 per-turn outbound（灰度关） | **同一份出向内容两遍**（二选一机制，但两侧各写一遍恒成立） |

结论：req/resp 双写有「信封 vs 增量」的语义差（对账互证价值），**out 方向是零语义
差的纯重复**——两侧都是完整出向载荷原文。

## 三、生产流量量化（245 预生产 + 154 生产，2026-10-01 激活后实测）

### 3.1 速率与磁盘增长

| 环境 | 窗口 | 文件增速 | 字节增速 | mirror 计数增速 | 外推 |
|---|---|---|---|---|---|
| 245（预生产） | 11:09:35→11:12:51（196s） | 148→234（26 files/min） | 90,334→129,459 B（**≈17 MB/day**） | 63→167（32/min） | 7h 稳态 ≈5 MB |
| 154（生产） | 11:11:18→11:13:59（161s） | 62→210（**55 files/min**） | 74,675→529,089 B（**≈244 MB/day**） | 228（≈57/min，装配起算） | 7h 稳态 ≈71 MB |

配额压力线：默认 1GB/7h。154 当前稳态 71 MB（配额占用 7%）；流量 ×10（2.4 GB/day）
稳态 710 MB 仍在配额内，×15 触顶后 trimmer 按 LRU 逐出（纯缓存语义，无损）。

### 3.2 租户分布与分裂占比（154 t1'，4 分钟窗）

| 租户目录 | files | bytes | 判源 |
|---|---|---|---|
| default | 146 | 160,374 | 混合：v2 turn（重合覆盖后剩 turn 粒度）+ default 应用 telemetry |
| openpocket | 35 | 52,863 | 纯 telemetry 信封（分裂：会话租户 default 另有一份） |
| ide | 19 | 51,913 | 同上 |
| applicant | 10 | 259,843 | 同上（**单文件均值 26 KB**，大信封应用） |

**分裂租户（纯 telemetry 副本）占镜像字节 69%**（364 KB / 529 KB）——磁盘双写
放大几乎全部来自分裂租户；且 applicant 类大信封应用是放大主力（均值 26 KB/文件
vs 全树均值 2.5 KB）。

### 3.3 双写并存与覆盖写的样例实证（gunzip 级）

- 并存（154，rid 1d86f68f…）：`ide/…/{rid}.req.json.gz`（282 B，信封
  `{"max_tokens":512,"messages":[…]}`）与 `default/…/{rid}.req.json.gz`
  （248 B，turn 数组 `[{"role":"user",…}]`）**同 rid 两份并存**。
- 覆盖（245，rid cd95f875…，租户重合）：`default/…/{rid}.req.json.gz` 内容为
  turn 数组——v2 后写覆盖 telemetry 信封；对照 `system/…/{rid}.req.json.gz`
  （probe 流量，无 v2 会话）保持信封原样。

## 四、决策选项（owner 拍板用）

| 选项 | 内容 | 代价 | 收益 |
|---|---|---|---|
| A 接受现状 | 双写保留，靠 1GB 配额 + 7h retention + 30m trim 兜底 | 磁盘放大 69% 来自分裂租户（154 实测）；写 IO 恒 ~2x | 零改动；req/resp 双粒度对账互证保留 |
| B 砍分裂租户的 telemetry 副本 | application_code ≠ 会话租户时 telemetry 侧不落（或落浅指针） | telemetry 侧改动 + 对账脚本判源规则更新 + F4 口径复核 | 直接削掉 69% 磁盘放大；信封原文 PG bodies_hot 仍在，对账有源 |
| C 砍 out 方向重复 | v2 侧 final_full/per-turn out 不落文件（PG session_bodies 已有同一份） | 改 mirrorTurnBodies/mirrorFinalFull out 分支 + 测试 | 省一个方向；但在重合租户下该方向本是覆盖写（只省 IO 不省盘），收益小于 B |
| D 统一租户口径 | telemetry 租户改走会话租户，两套落同目录按 requestID 合并 | 触碰对账三规则与 O2 口径，改动面大 | 目录不再分裂，消费方单树浏览成立；顺带把「分裂双份」变「重合覆盖」，磁盘放大同步消失 |

建议：**B > D > C > A**。B 的量化收益最大（69%）、改动局部（telemetry 侧租户
派生点 + reconcile 判据）；D 是彻底解但触碰面大；C 单独做收益最小；A 在当前
71 MB/7h 稳态下磁盘安全（配额 7%），但放大随 applicant 类大信封应用流量线性
增长，建议 owner 在流量增长前拍板。

## 五、证据锚点

- 装配：cmd/gateway/storage_mode_init.go:287-295（requestMirror 唯一实例）
- telemetry 写方：domains/hooks/observability/telemetry/client.go:1140-1142（S4 同门）、1207-1233（三方向）
- v2 写方：cmd/gateway/main.go:2643-2653（SetBodyMirror 接线）、domains/session/v2/session_writer_v2.go:143-174（mirrorTurnBodies/mirrorFinalFull/mirrorableTurnPayload）、775-783（commit 后触发 + finalFullWritten 门）
- admin ingest 写方（F5 第三落库点，2026-10-01 起在 245/154 live）：装配日志
  "admin ingest request body mirror wired"，admin/telemetry.go 经同一 bodyMirrorFn
  三件套换算（MirrorablePayload 单一事实源）
- 对账消化：scripts/hotzone-request-mirror-reconcile.sh（租户判源 + 双写方去重 + 三态）
- 本文量化样例：§3.3（154 rid 1d86f68f 并存 / 245 rid cd95f875 覆盖，均 gunzip 级）
