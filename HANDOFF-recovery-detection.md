# Handoff：上游恢复检测及时性优化实施

> 目标：修复「没有及时发现上游状态已经恢复，包括大量免费供应商」问题
> 基线：2026-11-09，llm-gateway-go main 分支
> 详细分析：`docs/audit/2026-11-09-上游恢复检测及时性问题分析与优化方案.md`

---

> ## ⚠️ 完成状态横幅（2026-11-11 审计轮补，先读这段再读本文件正文）
>
> **本文件是 2026-11-09 的规划稿。P0 已按一条与本规划不同的路径完成并部署生产，
> 正文中的伪代码/验收命令/根因描述已过时，不要照正文执行。** 实际情况：
>
> | 规划项 | 实际状态 | 证据 |
> |---|---|---|
> | P0-1（F-B balance_floor_guard） | ✅ 已实施已部署 | `bg/balance_floor_guard.go:1403-1413`（证据门控 re-arm）；211 48h 日志命中 **17** 次（grep `quota-suspended but vendor plan windows have capacity`） |
> | P0-2（F-A 探测模型回退梯） | ✅ 已实施已部署 | `bg/credential_probe_v2.go:1074-1110`；211 48h 日志命中 **2** 次（grep `fallback model recovered the probe`） |
> | P0-3（免费模型 featured） | ✅ 演化为更深根因修复并部署 | 实际根因＝featured 匹配键与 namespaced raw 名不一致（非本规划的「手工标 featured」）；代码＝`bg/model_tier.go` 统一展开（static 210）+ 迁移 956；era2 部署 build_seq 2995（`8028459d6`）。详见 `docs/audit/2026-11-10-上游恢复检测及时性-取证与P0实施.md` 与 AGENTS.md §653–§654 |
> | P0-4（部署） | ✅ 完成 | `--message` 参数**不被部署引擎支持**（§653 实证），正确用法＝裸 `bash scripts/deploy-211.sh` |
> | P1-1/P1-2/P1-3、P2-1/P2-2 | ⏸ 按规划门控未实施 | 前置条件＝「P0 验收成功」；P0 验收＝Stage 1 判定 |
>
> **当前阶段：观测窗口已满，Stage 1 判定就绪。**
> - era2 切点＝2026-11-10 13:42:30 +08；**Stage 1 关窗＝2026-11-11 13:42:30 +08；Stage 2＝2026-11-17 13:42:30 +08**
> - **关窗后判定不要手写 SQL**，用已建好的量具（12 轮体检后的最终形态）：
>   ```bash
>   # 关窗前必跑（期望 rc=0，32 场景）：
>   python3 scripts/audit/recovery-detection-p0-3-verdict.py --selftest
>   # 三时钟门（pre 键时钟无关，期望 rc=0，44 键）：
>   python3 scripts/audit/recovery-detection-p0-3-clockcheck.py
>   # 负控（判据不是恒真，期望 rc=0）：
>   python3 scripts/audit/recovery-detection-p0-3-negcontrol.py
>   # Stage 1 正式判定（关窗后）：
>   python3 scripts/audit/recovery-detection-p0-3-verdict.py --stage 1
>   ```
> - **正文第 294/298 行的验收命令已废**（grep 的是不存在的日志文案，48h 实测 0 命中——
>   而真实文案 17/2 命中），照抄会误判「修复未生效」。正确文案见上表。
> - 判据口径的 20+ 处修正与诚实边界见 `docs/audit/` 下 `recovery-detection` 族审计档
>   （M11–M20 判据族），AGENTS.md 顶部条目有逐轮索引。

---

## 问题定义

**核心症状**：
- minimax 恢复 ~24 小时未感知（11-07 13:49 → 11-08 15:39）
- kimi-k3：15 绑定 → 0 可路由
- 免费凭据可用率仅 31.8%（4002 绑定 → 1271 可路由）

**根因**：
1. 配额 API 有读数但无动作路径（restore 只认 balance_floor）
2. 探测模型与凭据支持模型不匹配
3. 免费凭据无流量 + 不在 featured ⇒ 探测频率低

---

## 实施路线（3 阶段）

### 【Phase 0】取证与验证（0.5 天）

#### 任务 0.1：验证 P0 修复是否已部署

**AGENTS.md 项6 提到两个修复（F-A/F-B），需确认是否已在 211 生效：**

```bash
# 1. 查 211 代码
cd ~/workspace/ai-native-tools/llm-gateway/llm-gateway-go
git log --since="2026-11-07" --oneline | grep -E "balance_floor_guard|credential_probe_v2|F-A|F-B"

# 2. 查 211 日志（验证 F-B balance_floor_guard 是否触发）
ssh 211
docker ps | grep llm-gateway  # 确认在役容器名
docker logs <容器名> --since 24h | grep "SetOnPlanCapacityAvailable" | wc -l

# 3. 查 211 日志（验证 F-A 探测模型回退梯是否触发）
docker logs <容器名> --since 24h | grep -E "fallback.*model|probe.*retry" | head -20
```

**判定标准**：
- F-B 已生效：`SetOnPlanCapacityAvailable` 日志 >0
- F-A 已生效：出现 fallback 日志
- **若两者皆无 ⇒ P0 修复未部署或未触发 ⇒ 优先实施 P0**

#### 任务 0.2：采集基线指标

**连接 211 真库（acc-pg17/llm_gateway）：**

```sql
-- 指标 1：当前 unavailable 凭据数
SELECT COUNT(*) as unavailable_count 
FROM credential_model_bindings 
WHERE available = FALSE;

-- 指标 2：免费凭据可用率（需先确认 billing_mode 列名）
-- 先查表结构
\d credentials

-- 若有 billing_mode 列
SELECT 
    COUNT(*) as total,
    COUNT(*) FILTER (WHERE cmb.available = TRUE) as available,
    COUNT(*) FILTER (WHERE cmb.available = TRUE) * 100.0 / COUNT(*) as available_pct
FROM credential_model_bindings cmb
JOIN credentials c ON cmb.credential_id = c.id
WHERE c.billing_mode IN ('free', 'trial')  -- 或其他免费标识
   OR c.provider LIKE '%free%';

-- 若无 billing_mode，先识别免费供应商
SELECT DISTINCT provider, notes 
FROM credentials 
WHERE provider LIKE '%free%' OR notes LIKE '%free%' OR notes LIKE '%trial%';

-- 指标 3：探测间隔分布
SELECT 
    percentile_cont(0.50) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (NOW() - last_probe_at))/60) as p50_minutes,
    percentile_cont(0.95) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (NOW() - last_probe_at))/60) as p95_minutes,
    percentile_cont(0.99) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (NOW() - last_probe_at))/60) as p99_minutes
FROM node_probe_state
WHERE last_probe_at IS NOT NULL;

-- 指标 4：当前探测覆盖率（24h 内被探测过的比例）
SELECT 
    COUNT(*) as total,
    COUNT(*) FILTER (WHERE last_probe_at > NOW() - INTERVAL '24 hours') as probed_24h,
    COUNT(*) FILTER (WHERE last_probe_at > NOW() - INTERVAL '24 hours') * 100.0 / COUNT(*) as coverage_pct
FROM node_probe_state;
```

**保存结果**：将上述 SQL 结果写入 `baseline-metrics-2026-11-09.txt`

#### 任务 0.3：识别免费凭据清单

```sql
-- 方案 A：从 omnifree 种子识别
SELECT DISTINCT credential_id, provider, raw_model
FROM credential_model_bindings cmb
WHERE raw_model IN (
    SELECT DISTINCT model_id 
    FROM omnifree_seeds  -- 若有此表
);

-- 方案 B：从 provider 名称启发式识别
SELECT id, provider, api_base, notes
FROM credentials
WHERE provider ILIKE '%free%' 
   OR provider ILIKE '%trial%'
   OR notes ILIKE '%免费%'
ORDER BY provider;

-- 保存清单
\copy (上述查询) TO '/tmp/free_credentials.csv' CSV HEADER;
```

---

### 【Phase 1】P0 止血实施（1-2 天）

#### P0-1：balance_floor_guard 放宽（若未实施）

**检查点**：任务 0.1 确认 F-B 未生效。

**实施**：
```bash
# 1. 找到代码位置
cd ~/workspace/ai-native-tools/llm-gateway/llm-gateway-go
grep -rn "balance_floor" bg/ | grep -E "quota|plan"

# 2. 阅读 bg/balance_quota_probe.go（或类似文件）
# 3. 修改逻辑（参考 AGENTS 项6 F-B）：
#    用尽族挂起 + MaxUsedPercent < 100 ⇒ SetOnPlanCapacityAvailable
```

**代码修改示例**（伪代码，需根据实际代码调整）：
```go
// bg/balance_quota_probe.go (假设位置)
func (s *BalanceQuotaProber) processPlanUsage(cred *Credential, usage *PlanUsage) {
    // 原逻辑：只在 balance_floor 守卫下恢复
    // if usage.BalanceFloor && usage.Available {
    //     s.SetOnPlanCapacityAvailable(cred.ID)
    // }
    
    // 新逻辑：配额部分恢复也触发
    credState := s.stateManager.GetState(cred.ID)
    if credState.QuotaExhausted && usage.MaxUsedPercent < 100 {
        s.SetOnPlanCapacityAvailable(cred.ID)
        s.logger.Info("plan capacity partially recovered",
            "credential_id", cred.ID,
            "max_used_percent", usage.MaxUsedPercent)
    }
}
```

#### P0-2：credential_probe_v2 探测模型回退梯（若未实施）

**检查点**：任务 0.1 确认 F-A 未生效。

**实施**：
```bash
# 1. 找到 credential_probe_v2.go
grep -rn "credential_probe_v2" bg/

# 2. 找到配额族失败处理分支
# 3. 添加 fail-fast 探测模型回退逻辑
```

**代码修改示例**（伪代码）：
```go
// bg/credential_probe_v2.go
func (s *ProbeService) probeCredential(cred *Credential) error {
    // 原逻辑：探测固定模型（如 featured 首个）
    model := s.selectProbeModel(cred)
    result := s.directProbe(cred.ID, model)
    
    if isQuotaError(result.Error) {
        // 新逻辑：配额族失败 ⇒ 尝试备选模型
        fallbackModels := s.getFallbackModels(cred, limit=3)
        for _, fbModel := range fallbackModels {
            fbResult := s.directProbe(cred.ID, fbModel)
            if fbResult.Success {
                s.logger.Info("probe succeeded with fallback model",
                    "credential_id", cred.ID,
                    "original_model", model,
                    "fallback_model", fbModel)
                return nil
            }
        }
    }
    
    return result.Error
}

func (s *ProbeService) getFallbackModels(cred *Credential, limit int) []string {
    // 按 node_probe_state.last_direct_ok = TRUE 优先
    // 返回该凭据的聊天模型绑定（≤limit 个）
    ...
}
```

#### P0-3：免费模型标记为 featured

**实施**：
```sql
-- 连接 211 真库
-- 1. 查当前 featured 模型
SELECT canonical_id, featured FROM models_canonical WHERE featured = TRUE;

-- 2. 识别免费层常见模型（需根据任务 0.3 的清单调整）
SELECT canonical_id, provider, input_price, output_price 
FROM models_canonical 
WHERE canonical_id ILIKE '%mini%' 
   OR canonical_id ILIKE '%flash%'
   OR canonical_id ILIKE '%haiku%'
   OR canonical_id ILIKE '%free%'
ORDER BY canonical_id;

-- 3. 标记为 featured（示例，需根据实际情况调整）
UPDATE models_canonical SET featured = TRUE 
WHERE canonical_id IN (
    'gpt-4o-mini',
    'claude-haiku-3.5', 
    'gemini-flash-2.0',
    'glm-5.1-flash',
    'deepseek-v4.1-flash'
    -- 根据任务 0.3 的清单补充
);

-- 4. 验证
SELECT canonical_id FROM models_canonical WHERE featured = TRUE ORDER BY canonical_id;
```

#### P0-4：编译、测试、部署

```bash
# 1. 编译（若有代码改动）
cd ~/workspace/ai-native-tools/llm-gateway/llm-gateway-go
go build ./cmd/gateway
go vet ./bg/...

# 2. 测试（若有代码改动）
go test ./bg/... -run Probe

# 3. 提交
git add .
git commit -m "fix(probe): 恢复检测及时性 P0 修复

- balance_floor_guard 放宽：配额部分恢复也触发
- credential_probe_v2 fail-fast：配额族失败尝试备选模型
- 标记免费模型为 featured：提升探测优先级

ref: docs/audit/2026-11-09-上游恢复检测及时性问题分析与优化方案.md
"

# 4. 推送
git push origin main
git push github main

# 5. 部署 211（★ 2026-11-11 修正：--message 不被部署引擎支持——引擎报 unknown argument，
#    §653 实证；且脚本在仓内 scripts/ 而非 deploy 目录）
bash scripts/deploy-211.sh

# 6. 验证部署
curl https://llm.itestu.cn/api/system/version
# 查看 build_seq 和 git_sha，确认已部署最新版本
```

---

### 【Phase 2】P0 验收（24 小时观测）

#### 任务 2.1：复采基线指标

**等待 24 小时后，重新执行任务 0.2 的 SQL，对比结果：**

```bash
# 保存为 post-p0-metrics-2026-11-10.txt
# 计算变化：
# - unavailable_count: 应下降 20-30%
# - 免费凭据可用率: 应上升至 >40%
# - P95 探测间隔: 应下降至 <60min
# - 探测覆盖率: 应上升至 >70%
```

#### 任务 2.2：验证日志行为（★ 2026-11-11 修正：文案对齐真实实现）

```bash
# 1. 验证 balance_floor_guard 证据门控 re-arm 触发
# ★ 真实文案来自 bg/balance_floor_guard.go:1406（规划稿原文案在生产代码中不存在，48h 实测 0 命中）
ssh 211
docker logs llm-gateway-211-<槽> --since 48h 2>&1 | grep -c "quota-suspended but vendor plan windows have capacity"
# 2026-11-11 实测：17（已生效）

# 2. 验证探测模型回退梯（F-A）
# ★ 真实文案来自 bg/credential_probe_v2.go:1101
docker logs llm-gateway-211-<槽> --since 48h 2>&1 | grep -c "fallback model recovered the probe"
# 2026-11-11 实测：2（已生效；另有失败侧文案 "fallback probe model also failed"）

# 3. 验证 featured 统一展开生效（P0-3 实际形态，替代本文件原「手工标 featured」方案）
docker logs llm-gateway-211-<槽> 2>&1 | grep "featured set refreshed" | tail -1
# 预期：static=210（era1=92，修复前=45）⇒ 统一展开在役

# 4. 免费面可用率（原基线 66.4%，见 §653 取证档；正式判据走 M20，不用这个数）
docker exec acc-pg17 psql -U acc_super -d llm_gateway -tA -c "
SELECT COUNT(*) FILTER (WHERE available) * 100.0 / COUNT(*)
FROM v_routable_credential_models;"
```

#### 任务 2.3：判定 P0 成败（★ 已由 Stage 1/2 判定流程取代）

**本节的 4 条简单标准已被 `recovery-detection-p0-3-verdict.py --stage 1` 的完整判据族
取代（M14/M14x/M15x/M16–M20 + 诚实边界），不要用本节判定成败。**
生产证据（2026-11-11 实测）：F-B 17 次 / F-A 2 次命中 ⇒ 两修复均已生效；
正式成败裁决见 Stage 1 判定输出。

---

### 【Phase 3】P1 增强（可选，2-3 周）

**若 P0 验收成功且用户要求继续优化，执行以下任务：**

#### P1-1：影子探测实现

**目标**：每小时从 unavailable 池抽样 10 个凭据，验证是否实际已恢复。

**设计要点**：
- 不与主探测竞争资源（独立 goroutine）
- 按 `unavailable_at` 升序（最早被标记的优先）
- 成功 ⇒ 立即 restore + 记录指标 `shadow_probe_hit_rate`

**实施步骤**：
1. 新建 `bg/shadow_probe.go`
2. 实现 `ShadowProber.Run()`（每小时触发）
3. 接线到 `main.go`
4. 增加指标：`shadow_probe_total` / `shadow_probe_hit` / `shadow_probe_miss`

#### P1-2：渐进恢复实现

**目标**：配额降到 80% 以下时就部分恢复（而非等到 0%）。

**设计要点**：
- 修改 `balance_quota_probe.go`
- 增加 `cmb.quota_status` 列（可选：'full' / 'partial' / 'exhausted'）
- 路由评分中 `quota_status='partial'` 的凭据降权但仍可用

#### P1-3：免费凭据专用探测池

**目标**：免费凭据单独分配探测预算（30min 间隔 vs 付费 1h）。

**前置条件**：
- 凭据表需有 `tier` 列或从 `billing_mode` 推断
- 免费供应商清单维护（从 omnifree 种子同步）

---

## 验收清单（P0 必须全过）

- [ ] **任务 0.1**：确认 F-A/F-B 实施状态
- [ ] **任务 0.2**：采集基线指标（4 个 SQL）
- [ ] **任务 0.3**：识别免费凭据清单（CSV 导出）
- [ ] **P0-1**：balance_floor_guard 放宽（若需要）
- [ ] **P0-2**：credential_probe_v2 回退梯（若需要）
- [ ] **P0-3**：免费模型标记 featured（SQL 已执行）
- [ ] **P0-4**：编译、测试、部署 211
- [ ] **任务 2.1**：24h 后复采指标，对比基线
- [ ] **任务 2.2**：验证日志行为（3 个 grep）
- [ ] **任务 2.3**：判定 P0 成功（4 个标准至少 3 个满足）
- [ ] **归档**：写审计档 + 更新 AGENTS.md + 更新 LESSONS.md

---

## 紧急联系与回退

**若 P0 部署后出现异常**：

1. **回滚命令**（★ 2026-11-11 修正：部署引擎**没有** `--rollback` 参数——参数面实测只有
   `--help`/`--force` 族；蓝绿回滚＝手工起旧槽。历轮实际操作形态：）
   ```bash
   ssh 211
   docker ps -a --format '{{.Names}} {{.Status}}' | grep llm-gateway  # 先核对在役/旧槽名
   docker start llm-gateway-211-<旧槽>       # 旧槽容器在切换后保留（Exited 137）可回滚
   # 再把 nginx upstream 切回旧槽端口并 reload（在役槽 a=8781 / b=8783）
   ```

2. **异常判定标准**：
   - 错误率 >1%（vs 基线 <0.1%）
   - P95 延迟 >5s（vs 基线 <2s）
   - 探测调用量 >2×（vs 基线）

3. **应急措施**：
   - ★ 注意：P0-3 的实际形态是**代码统一展开**（`bg/model_tier.go`）+ 迁移 956 数据标记，
     「关闭 featured 标记」只回退数据面不回退代码面——真正的开关是 `static=210` 的展开逻辑，
     应急应整槽回滚到 era1 镜像，而非只改 SQL。
   - 或整体回滚代码

---

## 参考文档

1. **详细分析**：`docs/audit/2026-11-09-上游恢复检测及时性问题分析与优化方案.md`
2. **实际实施与判据族**（2026-11-10 起的 12+ 轮，优先读这些）：
   - `docs/audit/2026-11-10-上游恢复检测及时性-取证与P0实施.md`（§653：根因与 P0-3 实施取证）
   - AGENTS.md 顶部 §653–§663 条目（判据口径 20+ 处修正的逐轮索引）
   - `scripts/audit/recovery-detection-p0-3-{metrics.sql,verdict.py,clockcheck.py,negcontrol.py}`（量具本体）
2. **AGENTS.md**：2026-11-09 运营审计轮 - 项6
3. **§640**：2026-11-07 凭据探测时间点规划轮
4. **§642**：2026-11-07 Token 桶双层路由优化方案
