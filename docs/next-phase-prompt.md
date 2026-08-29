# Session V2 Staging 验证 - 下一阶段执行提示词

你是一个自主执行代理，负责完成 Session V2 在 154 staging 环境的完整验证工作。前期工作已完成 70%（环境准备、工具编译、单会话回填测试），现在需要完成剩余的关键任务。

---

## 背景信息

**项目**: llm-gateway-go Session V2 优化  
**分支**: `feat/session-turns-v2`  
**仓库**: `/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4`  
**Staging 环境**: 154 (47.97.111.154:25022)  
**数据库**: 172.16.2.210:5432/llm_gateway

**已完成工作**:
- ✅ 环境准备与数据库连接
- ✅ 工具编译与性能优化（回填工具从 60s 超时优化到 278ms）
- ✅ 单会话 session_bodies 回填成功（会话 `gt_gw_76d27976-59e4-42a5-853b-299182b9b336`）
- ✅ Delta 推导逻辑验证
- ✅ 3 个代码 bug 修复（schema、字段名、类型转换）

**参考文档**:
- `docs/staging-validation-final-report.md` - 详细验证报告
- `docs/staging-environment.md` - 环境配置文档
- `docs/sessions-v2-validation-runbook.md` - 验证 runbook

---

## 核心目标

完成以下**三个并行任务**，达到 100% 验证完成度：

### 任务 1: 修复并验证 validate_sessions_v2 工具 🔴 高优先级

**问题**: 工具查询假设 bodies 在 `request_logs` 表，但 staging 环境的 bodies 在独立的 `request_logs_bodies` 表。

**子任务**:
1. 修改 `cmd/tools/validate_sessions_v2/loader.go` 的 `LoadV1Turns` 方法
   - 添加 `LEFT JOIN request_logs_bodies b ON b.request_id = r.request_id`
   - 或改用两步查询（类似 backfill_session_bodies 的优化）
2. 本地编译测试
3. 推送到 `feat/session-turns-v2` 分支
4. 在 154 上重新编译并测试
   - 对已回填的会话运行校验：`gt_gw_76d27976-59e4-42a5-853b-299182b9b336`
   - 验证 V1 (request_logs) 和 V2 (session_bodies) 数据一致性
5. 记录校验结果（JSON 输出或文本报告）

**成功标准**: 工具成功运行，输出一致性校验结果（PASS 或 FAIL + diff）

---

### 任务 2: 调查并解决 request_logs_bodies 覆盖率低的问题 🟠 高优先级

**问题**: 7 天内约 500 个会话，只有 ~50 个（10%）有 bodies 数据。

**子任务**:
1. **数据分析**:
   - 统计不同时间段的覆盖率（按天分组）
   - 检查是否与 `success` 字段相关（只保留失败请求？）
   - 检查是否与特定 tenant/model 相关
   
   ```sql
   -- 按成功/失败分组
   SELECT r.success, 
          COUNT(DISTINCT r.gw_session_id) AS total_sessions,
          COUNT(DISTINCT CASE WHEN b.request_id IS NOT NULL THEN r.gw_session_id END) AS with_bodies
   FROM request_logs r
   LEFT JOIN request_logs_bodies b ON b.request_id = r.request_id
   WHERE r.gw_session_id IS NOT NULL AND r.ts > NOW() - INTERVAL '7 days'
   GROUP BY r.success;
   
   -- 按天分组
   SELECT DATE(r.ts) AS day,
          COUNT(DISTINCT r.gw_session_id) AS total_sessions,
          COUNT(DISTINCT CASE WHEN b.request_id IS NOT NULL THEN r.gw_session_id END) AS with_bodies
   FROM request_logs r
   LEFT JOIN request_logs_bodies b ON b.request_id = r.request_id
   WHERE r.gw_session_id IS NOT NULL AND r.ts > NOW() - INTERVAL '7 days'
   GROUP BY DATE(r.ts)
   ORDER BY day DESC;
   ```

2. **代码审查**:
   - 检查网关代码中 `request_logs_bodies` 的写入逻辑
   - 搜索 `request_logs_bodies` 相关的 INSERT 语句
   - 查看是否有条件判断（只在特定情况下写入 bodies）
   
   ```bash
   grep -rn "request_logs_bodies" cmd/gateway/main.go | head -20
   ```

3. **结论与建议**:
   - 如果是策略性存储（如只保留失败请求），评估对 V2 的影响
   - 如果是 bug（双写未完全启用），提出修复方案
   - 制定 V2 上线后的 bodies 存储策略（100% 覆盖或按需存储）

**成功标准**: 生成分析报告，明确覆盖率低的原因和解决方案

---

### 任务 3: 批量回填并验证多个会话 🟡 中优先级

**目标**: 扩大验证范围，测试批量回填性能和数据正确性。

**子任务**:
1. 选择 10 个有完整 bodies 的测试会话
   ```sql
   SELECT r.gw_session_id, r.tenant_id, COUNT(*) AS turns,
          COUNT(b.request_id) AS with_bodies,
          MIN(r.ts) AS first_turn
   FROM request_logs r
   LEFT JOIN request_logs_bodies b ON b.request_id = r.request_id
   WHERE r.gw_session_id IS NOT NULL
     AND r.ts > NOW() - INTERVAL '7 days'
   GROUP BY r.gw_session_id, r.tenant_id
   HAVING COUNT(*) BETWEEN 2 AND 5 
     AND COUNT(b.request_id) = COUNT(*)  -- 所有轮次都有 bodies
   ORDER BY MIN(r.ts) DESC
   LIMIT 10;
   ```

2. 编写批量回填脚本
   ```bash
   #!/usr/bin/env bash
   # batch_backfill.sh
   
   DSN='postgres://...'
   SESSIONS=(
     "gt_gw_76d27976-59e4-42a5-853b-299182b9b336"
     "gt_gw_02029a6c-223b-4a58-a282-0fdcdfbcc6db"
     # ... 其他会话
   )
   
   for session in "${SESSIONS[@]}"; do
     echo "Backfilling $session..."
     time /tmp/backfill_session_bodies --dsn="$DSN" --tenant="default" --session="$session" --dry-run=false
   done
   ```

3. 在 154 上执行批量回填，记录：
   - 每个会话的回填耗时
   - 成功/失败状态
   - 写入的 session_bodies 行数

4. 对所有回填的会话运行 `validate_sessions_v2`（依赖任务 1 完成）

5. 统计验证结果：
   - 一致性通过率
   - 发现的数据差异（如果有）

**成功标准**: 10 个会话全部回填成功，一致性校验通过率 > 95%

---

## 执行策略

### 并行执行
- **任务 1** 和 **任务 2** 可并行（互不依赖）
- **任务 3** 依赖任务 1 的校验工具修复

### 任务分配建议
你可以创建 2 个子代理：
- **Agent A**: 负责任务 1（修复 validate_sessions_v2）
- **Agent B**: 负责任务 2（调查 bodies 覆盖率）
- **主代理**: 等待 Agent A 完成后，执行任务 3（批量回填）

### 时间预估
- 任务 1: 2-3 小时
- 任务 2: 1-2 小时（主要是数据分析）
- 任务 3: 1 小时（批量回填 + 验证）

**总计**: 4-6 小时

---

## 关键约束

1. **SSH 连接稳定性**: 
   - 154 环境的 SSH 可能超时，使用 `timeout` 命令限制长查询
   - 大型操作拆分为小批次

2. **数据库查询性能**:
   - 避免全表扫描（已知 `request_logs` 超时问题）
   - 使用索引列（gw_session_id, ts）
   - 复杂查询先加 `LIMIT 10` 测试

3. **代码变更流程**:
   - 本地修改 → 编译测试 → 提交到分支 → 在 154 拉取并重新编译
   - 每次推送后标注 commit message

4. **不要删除已有数据**:
   - 所有操作都是增量（INSERT），不 DELETE/TRUNCATE
   - 如果需要重新回填，先检查是否已存在（ON CONFLICT DO NOTHING）

---

## 输出要求

完成所有任务后，生成最终报告（Markdown 格式），包含：

### 1. 任务 1 结果
- 修改的文件和 commit hash
- 在 154 上的测试输出（validate_sessions_v2 运行结果）
- 一致性校验结论

### 2. 任务 2 结果
- Bodies 覆盖率统计表（按天、按 success 状态等维度）
- 代码审查发现（写入逻辑说明）
- 根因分析与建议

### 3. 任务 3 结果
- 批量回填统计表（会话 ID、轮数、耗时、状态）
- 一致性校验汇总（通过/失败会话数）
- 性能基准（平均每会话耗时）

### 4. 整体结论
- 验证完成度（目标 100%）
- 发现的新问题（如果有）
- 生产部署建议（Go/No-Go 决策依据）

---

## 环境访问

### SSH
```bash
ssh -p 25022 root@47.97.111.154
```

### 数据库
```bash
export PGPASSWORD='4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg'
psql -h 172.16.2.210 -U llm_gateway -d llm_gateway
```

### 代码仓库
```bash
cd /Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4
git status  # 当前在 main 分支
git checkout feat/session-turns-v2  # 切换到验证分支
```

### 154 上的代码
```bash
cd /root/llm-gateway-go-session-v2
git pull origin feat/session-turns-v2
```

---

## 参考资料

- **前期报告**: `docs/staging-validation-final-report.md`
- **环境配置**: `docs/staging-environment.md`
- **Runbook**: `docs/sessions-v2-validation-runbook.md`
- **Schema 文档**: `docs/session-v2-schema.md`

---

## 开始执行

请按以下步骤自主执行：

1. **阅读参考文档**，理解前期工作和当前状态
2. **制定详细计划**，决定任务执行顺序和是否使用子代理
3. **逐任务执行**，遇到问题时自主调试（参考已知问题和解决方案）
4. **持续更新进度**，将关键输出保存到文件
5. **生成最终报告**，汇总所有验证结果

**期望输出**: 一份完整的验证报告（Markdown），证明 Session V2 在 staging 环境可以正常工作，为生产部署提供决策依据。

---

**执行权限**: 你可以修改代码、编译、测试、推送到分支、在 154 上执行命令、查询数据库。
**时间限制**: 无硬性限制，但建议在 6 小时内完成。
**自主程度**: 完全自主，无需每步确认，遇到阻塞自主解决或调整方案。
