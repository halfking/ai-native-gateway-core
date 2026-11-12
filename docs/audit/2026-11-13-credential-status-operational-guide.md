# 2026-11-13 凭据状态运营处置指南

> **背景**：第五轮审计从 211 真实日志挖出 ASR/TTS 端点 503（`/v1/audio/transcriptions` 35 次），根因是遗留 `credentials.status='disabled'` 列把健康凭据挡在路由外。
> 本文档给出 **6 个凭据的处置建议** 与 **F1 口径分叉的修复方案**。

## 一、问题定性

### 1.1 现象（211 真库实测 2026-11-12）
- **503 端点**：`/v1/audio/transcriptions` 35 次 503、`/v1/embeddings` 23 次 503（6h 窗）
- **诊断信号不可用**：日志只有 `[candidate_diag] db_empty {plan_count:0}` ⇒ **分不清**「模型没登记」与「被状态闸滤空」

### 1.2 根因（211 真库只读取证）
| 层 | 发现 |
|---|---|
| **模型注册** | `model_offers` 里**有** `whisper-large-v3-turbo`（`canonical_id=3346055`） |
| **凭据状态** | 挂的凭据 `99009` 的 `credentials.status='disabled'`（2026-11-05 01:58 写入），而 `lifecycle_status='active'` / `availability_state='ready'` |
| **上游可达性** | 从 211 实测 `http://192.168.31.13:8081/v1/models` → **可达**（404 属 whisper-cpp 无该端点的正常形态） |
| **对照实验** | 生产 SQL 口径（`c.status='active'` ∧ `c.lifecycle_status='active'`）⇒ **0 候选**；放掉遗留 `c.status` 闸 ⇒ **2 候选** |

### 1.3 规模与影响
- **211 真库实测**（2026-11-12）：
  - `credentials` 总数：**97 条**（`status='active'` 72 / `='disabled'` 13 / `='deleted'` 12）
  - `status='disabled'` ∧ `lifecycle_status='active'`：**12 条**
  - 其中**带 offer 的 6 个凭据**：
    | credential_id | provider | 模型族 | offer 数 | 备注 |
    |---|---|---|---|---|
    | 99009 | whisper-cpp | ASR（whisper-large-v3-turbo 等） | 2 | 上游可达（192.168.31.13:8081） |
    | 99010 | whisper-cpp | ASR（whisper-large-v3-turbo 等） | 2 | 同上 |
    | 99011 | macos-say | TTS（tts-1 等） | N | 本机 TTS |
    | （待补充） | glm 家族 | （待补充） | N | （待补充） |
    | （待补充） | minimax 家族 | （待补充） | N | （待补充） |
    | （待补充） | （待补充） | （待补充） | N | （待补充） |

  ⇒ **影响**：ASR/TTS 端点在 211 上**全部 503**（因为唯一可用的凭据被 `status='disabled'` 挡住）。

## 二、F1 口径分叉（已知、未修、活体影响当前=0）

### 2.1 分叉定义
| 位置 | 口径 | 文件 | 行号 |
|---|---|---|---|
| **路由候选 SQL（严格）** | `c.status = 'active'` | `provider/client.go` | 1215 |
| **探针/回填/健康检查（宽松）** | `c.status IN ('active', 'cooling', 'degraded')` | `bg/node_probe.go` | 3527 |
| 同上 | 同上 | `bg/capability_backfill.go` | 630 |
| 同上 | 同上 | `bg/modality_verification.go` | 720 |
| 同上 | 同上 | `db/db_omnifree.go` | 368 |
| 同上 | 同上 | `bg/routing_health_checks.go` | 509 |

### 2.2 影响分析
- **现状**：`cooling` / `degraded` 的凭据会被**路由单方面丢弃**，而同一次探针/回填又把它当「可用」来测 ⇒ **口径不一致**。
- **活体影响当前=0**：211 上 `status='cooling'` / `='degraded'` 的凭据数 = **0**（2026-11-12 实测）⇒ 是**潜伏分叉**，不是当下事故。
- **风险**：将来某凭据进入 `cooling` / `degraded` 状态后，会出现「探针说可用、路由拒绝」的不一致现象。

### 2.3 建议修复方案
#### 方案 A：收窄到「只 active 可用」（推荐）
```go
// 把 5 处「宽松口径」改成与路由一致
- status IN ('active', 'cooling', 'degraded')
+ status = 'active'
```
- **理由**：`cooling` / `degraded` 本就是「暂时不可用」的语义 ⇒ 路由不选它们是对的。
- **代价**：探针/回填会跳过 `cooling` / `degraded` 凭据 ⇒ 若运营依赖「探针覆盖所有非 deleted 凭据」的假设，需调整监控口径。

#### 方案 B：放宽到「cooling/degraded 也可路由」
```go
// 把路由口径改成宽松
- c.status = 'active'
+ c.status IN ('active', 'cooling', 'degraded')
```
- **理由**：`cooling` / `degraded` 只是「降级」，不是「完全不可用」。
- **代价**：改动会让 `cooling` / `degraded` 的凭据**立刻重新进入路由** ⇒ **活体流量分配变更** ⇒ 需评估：
  - `cooling` 的语义是什么？（短期限流？探测期？）
  - `degraded` 的语义是什么？（部分功能可用？性能下降？）
  - 它们是否应该参与路由？

#### 推荐
- **先做方案 A**（收窄到「只 active」），把口径统一到 SSOT。
- **然后**评估 `cooling` / `degraded` 的业务语义，若确认「应该参与路由」⇒ 再统一放宽（方案 B）。

## 三、6 个凭据的处置方案

### 3.1 前置检查（必须先做）
#### ① 确认「disabled 非人工置位」
```sql
-- 在 211 真库上执行
SELECT id, provider_id, status, lifecycle_status, availability_state, 
       health_status, health_checked_at, updated_at,
       auto_disabled_reason, manual_disabled
  FROM credentials 
 WHERE COALESCE(status, 'active') = 'disabled'
 ORDER BY id;
```

**判定**：
- 若 `manual_disabled = TRUE` ⇒ **人工置位** ⇒ **不擅自改**（先找运营确认原因）。
- 若 `auto_disabled_reason IS NOT NULL` ⇒ 自动降级 ⇒ 可能是上游故障触发 ⇒ 核对上游是否已恢复。
- 若 `manual_disabled = FALSE` 且 `auto_disabled_reason IS NULL` ⇒ **遗留 v1 健康列** ⇒ 可以翻转。

#### ② 核对上游可达性
```bash
# 在 211 上执行（每个凭据的 base_url）
curl -v http://192.168.31.13:8081/v1/models  # whisper-cpp 两个凭据
curl -v <glm_base_url>/v1/models
curl -v <minimax_base_url>/v1/models
# macos-say 是本机服务，检查进程：ps aux | grep 'say'
```

**判定**：
- 可达（200 / 404 但有响应头）⇒ 上游活着 ⇒ 可以翻转。
- 不可达（连接超时 / 拒绝连接）⇒ 上游挂了 ⇒ **翻转也没用**（503 变成上游超时）。

### 3.2 处置 SQL（仅在前置检查通过后执行）
```sql
-- ★★ 在 211 真库上执行（在事务内，先 SELECT 核对、再 UPDATE、最后 COMMIT）
BEGIN;

-- 核对：这 6 个凭据应该全部满足「非人工、无 auto_disabled_reason、lifecycle=active」
SELECT id, provider_id, status, lifecycle_status, manual_disabled, auto_disabled_reason
  FROM credentials
 WHERE id IN (99009, 99010, 99011, <其余 3 个 id>);
-- ★ 若任何一行 manual_disabled=TRUE ⇒ ROLLBACK，找运营确认

-- 翻转
UPDATE credentials 
   SET status = 'active', 
       updated_at = NOW()
 WHERE id IN (99009, 99010, 99011, <其余 3 个 id>)
   AND COALESCE(status, 'active') = 'disabled'
   AND COALESCE(lifecycle_status, 'active') = 'active'
   AND COALESCE(manual_disabled, FALSE) = FALSE;

-- 验证：受影响行数应该 = 6
-- ★ 若 < 6 ⇒ 有凭据不满足条件 ⇒ ROLLBACK，逐条排查
SELECT id, status FROM credentials WHERE id IN (99009, 99010, 99011, <其余 3 个 id>);

COMMIT;
```

### 3.3 翻转后验证
#### ① 候选数应从 0 变正
```sql
-- whisper-large-v3-turbo 的候选数（用第五轮的对照 SQL）
SELECT COUNT(*) AS candidate_count
  FROM model_offers mo
  JOIN credentials c ON mo.credential_id = c.id
  JOIN providers p ON c.provider_id = p.id
 WHERE mo.canonical_id = 3346055  -- whisper-large-v3-turbo
   AND c.status = 'active'
   AND c.lifecycle_status = 'active'
   AND p.enabled = TRUE;
-- 期望结果：candidate_count = 2（cred 99009 + 99010）
```

#### ② 实测端点应从 503 变 200
```bash
# 在 211 上执行（需要有效 token）
curl -X POST http://192.168.31.211:12112/v1/audio/transcriptions \
  -H "Authorization: Bearer $TOKEN" \
  -F "file=@test.mp3" \
  -F "model=whisper-large-v3-turbo"
# 期望：200 + {text: "..."}
```

#### ③ 观察日志（翻转后 5 分钟内）
```bash
# 在 211 容器内
docker logs llm-gateway-211-b --tail=100 | grep -E "candidate_diag|whisper"
# 期望：db_empty 消失，出现 credential_id=99009/99010 的路由日志
```

### 3.4 回滚方案（若翻转后出问题）
```sql
-- 立即回退
UPDATE credentials 
   SET status = 'disabled', 
       updated_at = NOW()
 WHERE id IN (99009, 99010, 99011, <其余 3 个 id>);
```

## 四、完整处置流程图

```
┌─────────────────────────┐
│ 前置检查 ① 确认非人工  │
└───────┬─────────────────┘
        ↓
   manual_disabled?
        ├─ TRUE ──→ [找运营确认] ──→ [停止]
        └─ FALSE
            ↓
┌─────────────────────────┐
│ 前置检查 ② 核对上游    │
└───────┬─────────────────┘
        ↓
    上游可达?
        ├─ 否 ──→ [上游故障，翻转无用] ──→ [停止 or 先修上游]
        └─ 是
            ↓
┌─────────────────────────┐
│ 在 211 真库执行 UPDATE │
│ （事务内：核对→改→验证）│
└───────┬─────────────────┘
        ↓
   受影响行数 = 6?
        ├─ 否 ──→ [ROLLBACK + 逐条排查]
        └─ 是 (COMMIT)
            ↓
┌─────────────────────────┐
│ 翻转后验证              │
│ ① 候选数 0→2           │
│ ② 端点 503→200         │
│ ③ 日志 db_empty 消失   │
└───────┬─────────────────┘
        ↓
   验证通过?
        ├─ 否 ──→ [立即回滚 + 根因排查]
        └─ 是
            ↓
    [处置完成，监控 24h]
```

## 五、F1 口径分叉的修复流程

### 5.1 代码修改（推荐方案 A）
```diff
# bg/node_probe.go:3527
- WHERE c.status IN ('active', 'cooling', 'degraded')
+ WHERE c.status = 'active'

# bg/capability_backfill.go:630
- WHERE c.status IN ('active', 'cooling', 'degraded')
+ WHERE c.status = 'active'

# bg/modality_verification.go:720
- WHERE c.status IN ('active', 'cooling', 'degraded')
+ WHERE c.status = 'active'

# db/db_omnifree.go:368
- WHERE c.status IN ('active', 'cooling', 'degraded')
+ WHERE c.status = 'active'

# bg/routing_health_checks.go:509
- WHERE c.status IN ('active', 'cooling', 'degraded')
+ WHERE c.status = 'active'
```

### 5.2 测试验证
```bash
# 单包测试（5 个文件所在包）
go test ./bg/ -run TestNodeProbe
go test ./bg/ -run TestCapabilityBackfill
go test ./bg/ -run TestModalityVerification
go test ./db/ -run TestOmnifree
go test ./bg/ -run TestRoutingHealthChecks

# 全量测试
go test ./...
```

### 5.3 部署后验证
- 观察探针/回填日志：不应再出现 `status='cooling'` / `='degraded'` 的凭据。
- 若将来某凭据进入 `cooling` / `degraded` ⇒ 路由与探针都跳过它 ⇒ **口径一致**。

## 六、时间线与责任方

| # | 任务 | 责任方 | 预计耗时 | 输出 |
|---|---|---|---|---|
| 1 | **前置检查 ①**：在 211 真库查询 6 个凭据的 `manual_disabled` / `auto_disabled_reason` | **DBA or 本会话** | 5 min | SQL 输出截图 |
| 2 | **前置检查 ②**：在 211 上 curl 上游 base_url，核对可达性 | **运维 or 本会话** | 10 min | 6 个 curl 响应 |
| 3 | **处置决策**：基于前置检查结果，确认「是否翻转」 | **运营 + 技术负责人** | 书面确认 | 决策记录（含理由） |
| 4 | **执行翻转**：在 211 真库事务内执行 UPDATE | **DBA or 本会话** | 5 min | 事务日志 + 受影响行数 |
| 5 | **翻转后验证**：候选数 / 端点冒烟 / 日志观察 | **本会话** | 10 min | 验证报告 |
| 6 | **24h 监控**：观察 503 是否复现、上游是否稳定 | **运维** | 24 h | 监控截图 |
| 7 | **F1 修复**：代码改 5 处 + 测试 + 部署 | **本会话** | 1 h | PR + 部署记录 |

## 七、风险与应急

### 7.1 风险
| 风险 | 概率 | 影响 | 缓解措施 |
|---|---|---|---|
| 翻转后上游仍 503（上游其实挂了） | 低 | 高 | 前置检查 ② 先核对上游可达性 |
| 翻转后出现新错误（未知依赖 `status='disabled'`） | 低 | 中 | 事务内先 SELECT 核对、有异常立即回滚 |
| F1 修复后探针覆盖率下降（运营依赖宽松口径） | 中 | 低 | 修复前确认「探针监控不依赖 cooling/degraded」 |

### 7.2 应急预案
- **若翻转后端点仍 503**：
  1. 立即回滚（见 §3.4）
  2. 逐层排查：上游可达性 / 候选 SQL / 日志 `candidate_diag`
  3. 若上游挂了 ⇒ 先修上游、再重试翻转
- **若翻转后出现新错误**：
  1. 立即回滚
  2. 读错误日志，定位「哪个模块依赖 `status='disabled'`」
  3. 修复依赖后重试

## 八、诚实边界

1. **6 个凭据的完整列表尚未补齐**（表格只填了 3 个，剩余 3 个待从 211 真库取）⇒ 执行前必须先查出完整 ID 列表。
2. **`cooling` / `degraded` 的业务语义未明确**（是短期限流？探测期？降级可用？）⇒ F1 修复方案需与运营确认语义。
3. **本文档未包含 245/154 的处置**（它们共用 252 库，凭据状态与 211 可能不同）⇒ 若 245/154 也有 ASR/TTS 503，需单独核查。
4. **翻转后的 24h 监控由运维执行**，本文档未给出监控脚本 ⇒ 需运维提供「503 数 / 候选数 / 上游健康度」的监控面板。

---

**文档版本**：v1.0  
**作者**：本会话（Mavis）  
**日期**：2026-11-13  
**状态**：待执行（需先完成前置检查）
