# 路由可用性测试计划

## 背景
验证禁用 provider 过滤 + quota 耗尽后自动切换的修复效果。
需要多模型、多状态、多凭据的组合测试。

## 测试环境
- 数据库：r112_postgres (已应用 mig 136+137)
- 网关：本地 8080 端口
- 客户端：curl / Postman / 自动化脚本

## 测试矩阵

### 维度 1: Provider 状态
- ✅ **enabled=true** — 正常可路由
- ✅ **enabled=false** — 应被过滤（is_routable=false）
- ✅ **manual_disabled=true** — 应被过滤

### 维度 2: Credential 状态
- ✅ **active** + quota_state=ok — 正常
- ✅ **active** + quota_state=exhausted — 应触发切换
- ✅ **active** + quota_state=periodic_exhausted — 应被过滤
- ⏸️ **cooling** — 应被过滤（or 降级路由）
- ⏸️ **disabled** — 应被过滤

### 维度 3: Model 可用性
- ✅ **pm.available=true** — 正常
- ✅ **pm.available=false** — 应被过滤
- ✅ **cmb.available=false** — 应被过滤（binding 级别禁用）

### 维度 4: Plan Type 兼容性
- ✅ **token** credential + per_token model — 兼容
- ✅ **token_plan** credential + token_plan model — 兼容
- ❌ **token** credential + token_plan model — 不兼容（视图过滤）
- ❌ **token_plan** credential + per_token model — 不兼容

## 测试用例

### TC1: 禁用 provider 过滤
**前置**:
```sql
UPDATE providers SET enabled=false WHERE id=33; -- evol
```
**请求**: `POST /v1/chat/completions` model=`claude-3-5-sonnet-20241022` provider_hint=evol
**预期**: 
- 不路由到 evol 的任何凭据
- 候选列表中 evol 凭据的 unavailable_reason='provider_disabled'
- 自动切换到其他 enabled provider

### TC2: Quota 耗尽后自动切换
**前置**:
```sql
UPDATE credentials SET quota_state='exhausted', quota_recover_at=NOW()+INTERVAL '1 hour' 
WHERE id IN (SELECT id FROM credentials WHERE provider_id=1 LIMIT 1);
```
**请求**: `POST /v1/chat/completions` model=`claude-3-5-sonnet-20241022`
**预期**:
- 第1个候选 quota_exceeded → 静默切换
- 第2个候选成功 → 返回200
- **客户端不感知第1个失败**
- request_logs.credential_id 是成功的那个

### TC3: 多轮重试（所有候选都 quota_exhausted）
**前置**:
```sql
UPDATE credentials SET quota_state='exhausted' WHERE provider_id=1;
```
**请求**: `POST /v1/chat/completions` model=`claude-3-5-sonnet-20241022`
**预期**:
- 主循环遍历所有候选（假设3个）
- sync_retry 3轮
- 最多 3×4=12 次尝试
- 最终返回 quota_exceeded 错误给客户端
- **不会死循环**

### TC4: Plan type 不兼容过滤
**前置**:
```sql
UPDATE credentials SET plan_type='token_plan' WHERE id=10;
-- 确保有 billing_mode='per_token' 的 model
```
**请求**: 使用 credential_id=10 请求 billing_mode='per_token' 的模型
**预期**:
- 视图过滤该 binding（unavailable_reason='plan_incompatible_...'）
- 路由到其他兼容凭据

### TC5: Model 不可用过滤
**前置**:
```sql
UPDATE provider_models SET available=false WHERE raw_model_name='gpt-4o-2024-05-13';
```
**请求**: `POST /v1/chat/completions` model=`gpt-4o-2024-05-13`
**预期**:
- 视图过滤（unavailable_reason='model_unavailable'）
- 如果有其他 provider 提供该模型 → 切换
- 如果没有 → no_candidates_from_router

## 自动化脚本

### 脚本 1: 状态组合遍历
```bash
#!/bin/bash
# test_routing_matrix.sh
# 遍历 provider/credential/model 的所有状态组合
# 每个组合发送 10 个请求，记录成功率和切换次数

MODELS=("claude-3-5-sonnet-20241022" "gpt-4o" "gpt-4-turbo")
STATES=("normal" "provider_disabled" "quota_exhausted" "model_unavailable")

for model in "${MODELS[@]}"; do
  for state in "${STATES[@]}"; do
    echo "=== Testing: model=$model state=$state ==="
    setup_state "$state"
    for i in {1..10}; do
      curl -X POST http://localhost:8080/v1/chat/completions \
        -H "Authorization: Bearer test-key" \
        -H "Content-Type: application/json" \
        -d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"test\"}]}" \
        >> results_${model}_${state}.json
    done
    cleanup_state "$state"
  done
done
```

### 脚本 2: Quota 耗尽压测
```bash
#!/bin/bash
# test_quota_failover.sh
# 模拟第一个凭据 quota 耗尽，验证自动切换

# 1. 标记 cred 1 为 exhausted
psql -c "UPDATE credentials SET quota_state='exhausted' WHERE id=1"

# 2. 发送 100 个并发请求
seq 1 100 | xargs -P 10 -I {} curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -d '{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"test"}]}' \
  > results_quota_failover.txt 2>&1

# 3. 统计成功率和使用的 credential_id 分布
psql -c "SELECT credential_id, COUNT(*) FROM request_logs 
         WHERE created_at > NOW() - INTERVAL '1 minute' 
         GROUP BY credential_id ORDER BY COUNT(*) DESC"

# 4. 验证 cred 1 没有被使用
psql -c "SELECT COUNT(*) FROM request_logs 
         WHERE credential_id=1 AND created_at > NOW() - INTERVAL '1 minute'"
```

### 脚本 3: 死循环检测
```bash
#!/bin/bash
# test_no_infinite_loop.sh
# 验证所有候选都失败时不会死循环

# 1. 标记所有凭据为 exhausted
psql -c "UPDATE credentials SET quota_state='exhausted'"

# 2. 发送请求并记录耗时
START=$(date +%s)
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -d '{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"test"}]}' \
  > result.json 2>&1
END=$(date +%s)
DURATION=$((END - START))

# 3. 验证耗时 < 30s（maxSyncRetryRounds=3, 每轮5s间隔）
if [ $DURATION -gt 30 ]; then
  echo "❌ FAIL: Request took ${DURATION}s, possible infinite loop"
  exit 1
else
  echo "✅ PASS: Request failed in ${DURATION}s"
fi

# 4. 验证返回 quota_exceeded 错误
grep -q "quota" result.json && echo "✅ Error message correct" || echo "❌ Wrong error"
```

## 验收标准

### 必须通过（P0）
- ✅ TC1: 禁用 provider 不出现在候选中
- ✅ TC2: Quota 耗尽后静默切换（客户端不感知）
- ✅ TC3: 所有候选失败时不死循环（<30s 返回错误）
- ✅ TC4: Plan type 不兼容被正确过滤

### 建议通过（P1）
- ⏸️ TC5: Model 不可用被正确过滤
- ⏸️ 压测 1000 并发无死锁
- ⏸️ 混沌测试：随机杀凭据 + 随机 quota 耗尽

## 执行时间表

| 阶段 | 任务 | 预计时间 |
|------|------|----------|
| Phase 1 | TC1-TC4 手动验证 | 30min |
| Phase 2 | 自动化脚本开发 | 1h |
| Phase 3 | 全量测试矩阵执行 | 2h |
| Phase 4 | 结果分析 + bug 修复 | 2h |
| **总计** | | **5.5h** |

## 下一步

建议优先执行 **Phase 1 手动验证**（30分钟），快速确认核心修复生效。
自动化脚本和全量测试可作为 CI 流程集成。
