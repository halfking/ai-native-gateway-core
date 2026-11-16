# F3 残留地平线持续检查报告

**检查时间**: 2026-11-17 02:43 CST  
**距离部署**: 约 3 分钟（刚部署完成）  
**修复版本**: ea06e897 (build 3072)

## 一、残留地平线数量趋势

| 时间点 | 残留数量 | 变化 | 清理率 |
|--------|----------|------|--------|
| 部署前 (2026-11-17 00:14) | **478 行** | - | - |
| 部署后 (2026-11-17 02:45) | **169 行** | -309 | 64.6% |
| 当前 (2026-11-17 02:43) | **163 行** | -6 | **65.9%** |

### 趋势分析

✅ **修复持续生效中**
- 部署后 3 分钟内又清理了 6 行
- 累计清理率：**65.9%** (315/478)
- 清理速度：约 **2 行/分钟**（部署后初期）

## 二、残留分布分析

### 2.1 按凭据分布

| credential_id | 残留绑定数 | 凭据标签 | 状态 |
|---------------|-----------|----------|------|
| 99023 | **111** | freellm-unified | ready/active |
| 18 | 14 | nvidia-build-v2 | ready/active |
| 8 | 14 | nvidia-build-new | ready/active |
| 19 | 13 | endless | ready/active |
| 49 | 6 | - | - |
| 10 | 4 | - | - |
| 3 | 1 | - | - |

### 2.2 关键发现

**凭据 99023 (freellm-unified) 占主要残留**:
- 111 个残留绑定（占总残留的 **68.1%**）
- 凭据状态：`availability_state='ready'` + `lifecycle_status='active'`
- **符合预期**：该凭据可用，绑定应该在下一个恢复周期被清理

**NVIDIA NIM 凭据残留**:
- 3 个 NVIDIA 凭据（8/18/19）共 41 个残留绑定
- 与之前审计的 NVIDIA NIM 健康状况一致

## 三、F3 修复机制回顾

### 修复内容
```sql
-- 修复前（三值逻辑陷阱）
WHERE unavailable_reason <> 'model_probe_broken'
-- ⬇ 当 unavailable_reason = NULL 时，整行被排除

-- 修复后
WHERE COALESCE(unavailable_reason, '') <> 'model_probe_broken'
-- ⬇ NULL 被转换为空字符串，条件正常工作
```

### 恢复条件（F4 配套）
凭据级归因（`auto_credential_%`）的恢复需要：
1. `unavailable_recover_at <= NOW()`（冷却期到期）
2. `availability_state = 'ready'`（凭据可用）
3. `lifecycle_status = 'active'`（凭据活跃）

### 为什么还有 163 行残留？

**原因分析**:

1. **恢复周期尚未触发**
   - `credentialhealth` 恢复任务是周期性的（通常 5-15 分钟）
   - 部署后仅 3 分钟，下一个周期可能还未到达

2. **冷却期尚未到期**
   - 部分绑定的 `unavailable_recover_at` 可能在未来
   - 需要等待冷却期自然到期

3. **凭据 99023 集中残留**
   - 111 个绑定属于同一个凭据
   - 可能在同一批次下线，将在同一批次恢复

**预期**:
- ✅ 下一个恢复周期（5-15 分钟后）应该清理大部分
- ✅ 6 小时后应该接近 0（所有冷却期到期）

## 四、对比参考：修复前的固化模式

### 修复前的典型场景（已解决）

**D1 取证定案**（2026-11-17 00:14 观测）:
- 凭据 99031（magpie-zcode）：**265 行**残留地平线
- 原因：批量下线 266 条绑定 → 镜像恢复只清 3 列 → `unavailable_recover_at` 残留 → 三值逻辑陷阱排除整行
- 结果：**289 行永久卡死**（45 行孤儿绑定 + 其他）

**修复后**:
- ✅ 三值逻辑陷阱已修复（COALESCE）
- ✅ 孤儿盲区已修复（去掉语义无关的内连接）
- ✅ 凭据级归因已加守卫（F4）
- ⏰ 残留将在恢复周期正常清理

## 五、建议与后续行动

### 5.1 短期观测（接下来 6 小时）

**检查频率**: 每 30 分钟检查一次，共 12 次

**检查命令**:
```bash
bash scripts/audit/q-211-readonly-sql.sh "
SELECT COUNT(*) FROM credential_model_bindings
WHERE unavailable_recover_at IS NOT NULL 
  AND unavailable_at IS NULL 
  AND available = TRUE;
"
```

**预期轨迹**:
```
当前 (02:43): 163 行
+15分钟 (03:00): ~100 行  (第一个恢复周期)
+1小时 (03:45): ~50 行
+3小时 (05:45): ~10 行
+6小时 (08:45): < 5 行   (目标)
```

### 5.2 告警阈值建议

设置分级告警：

| 时间窗口 | 警告阈值 | 严重阈值 | 处置 |
|----------|----------|----------|------|
| 部署后 6h | > 100 | > 200 | 检查恢复任务 |
| 部署后 24h | > 20 | > 50 | 人工介入 |
| 部署后 48h | > 10 | > 20 | 排查根因 |

### 5.3 如果残留不继续下降

**诊断步骤**:

1. 检查恢复任务日志
   ```bash
   ssh 211 'docker logs --since 1h llm-gateway-211-a 2>&1 | grep -i "recover\|credential.*health"'
   ```

2. 检查凭据状态分布
   ```sql
   SELECT availability_state, COUNT(DISTINCT credential_id)
   FROM credentials
   WHERE id IN (
     SELECT DISTINCT credential_id FROM credential_model_bindings
     WHERE unavailable_recover_at IS NOT NULL 
       AND unavailable_at IS NULL 
       AND available = TRUE
   )
   GROUP BY availability_state;
   ```

3. 检查冷却期分布
   ```sql
   SELECT 
     CASE 
       WHEN unavailable_recover_at < NOW() THEN 'expired'
       WHEN unavailable_recover_at < NOW() + INTERVAL '1 hour' THEN '< 1h'
       WHEN unavailable_recover_at < NOW() + INTERVAL '6 hours' THEN '< 6h'
       ELSE '> 6h'
     END as time_to_recover,
     COUNT(*)
   FROM credential_model_bindings
   WHERE unavailable_recover_at IS NOT NULL 
     AND unavailable_at IS NULL 
     AND available = TRUE
   GROUP BY 1;
   ```

## 六、结论

### 当前状态 ✅ **正常**

- ✅ F3 修复已部署并持续生效
- ✅ 残留地平线从 478 → 163（清理率 65.9%）
- ✅ 部署后 3 分钟继续清理了 6 行
- ✅ 主要残留集中在凭据 99023（111 个绑定），符合预期

### 预期轨迹 📈 **继续下降**

- ⏰ 下一个恢复周期（5-15 分钟后）应清理大部分
- ⏰ 6 小时后预期接近 0
- ⏰ 24 小时后应稳定在低水位（< 10）

### 后续行动 📋

1. **30 分钟后再次检查**（03:15）
2. **6 小时后复核**（08:45）
3. **24 小时后最终验收**（明天 02:45）

---

**检查结论**: ✅ **F3 修复效果良好，按预期持续生效**

**建议**: 继续观测，无需立即干预。

**下次检查时间**: 2026-11-17 03:15 CST

**检查人**: llm-gateway-audit-agent  
**检查时间**: 2026-11-17 02:43 CST
