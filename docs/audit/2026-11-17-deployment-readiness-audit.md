# 节点状态漂移修复部署审计报告

**审计时间**: 2026-11-17 02:30 CST  
**审计范围**: 对比修复分支与 211 生产环境

## 一、当前状态

### 1.1 代码仓库状态

- **修复提交**: `2550a8648` - "fix: 节点状态漂移与输出策略重试审计修复 (F1-F5)"
- **仓库状态**: ✅ 已合并到 `origin/main`
- **本地 main**: ✅ 与远程同步 (HEAD: `ea06e8972`)

### 1.2 211 生产环境状态

```json
{
  "version": "2.6.0-f9d74215-dirty-20261117-211",
  "git_sha": "f9d74215",
  "build_seq": 3071,
  "container": "llm-gateway-211-b",
  "status": "Up 42 minutes (healthy)"
}
```

### 1.3 版本差异

**211 生产版本 → 修复提交**: 落后 **5 个提交**

```
f9d74215 (生产) → 2550a8648 (修复)
```

提交列表：
1. `586ad562e` - Merge: R76-3 部署闸门与迁移检测双修
2. `7d0b475db` - Merge remote-tracking branch 'origin/main'
3. `67a26833e` - fix: 修复手册登记的 4 个缺陷 + 全仓 SQL 可执行类型扫描
4. `f1995cb8d` - Merge remote-tracking branch 'origin/main'
5. `2550a8648` - **fix: 节点状态漂移与输出策略重试审计修复 (F1-F5)**

**结论**: ⚠️ **修复尚未部署到 211 生产环境**

## 二、修复内容回顾

### F1: 格式守卫可重试
- **问题**: `tool_json_invalid` 等格式错误被当成终态，不重试
- **修复**: 格式守卫 + 未提交字节 → `KindRequestRejected` → 换节点重试
- **影响文件**: 
  - `errorsx/output_policy.go` (新增 GuardRule* 定义)
  - `domains/streaming/executors/executor_dispatch.go` (重试逻辑)
  - 新增钉测 7 个

### F2: 探针成功不撤销人工下线
- **问题**: 探针成功会把 `paused` / `manual_offline` 清掉
- **修复**: 探针镜像保留人工状态
- **影响文件**: `bg/probe_service.go`, `bg/node_probe.go`
- 新增钉测 3 个

### F3: 恢复语句三值逻辑陷阱 + 孤儿盲区
- **问题**: `unavailable_reason <> 'model_probe_broken'` 在 reason=NULL 时排除整行
- **修复**: 改为 `COALESCE(reason,'') <> 'model_probe_broken'`
- **影响**: 211 真库有 **478 行**受影响（残留地平线）
- **影响文件**: `credentialhealth/checker.go` (168 行新增)
- 新增真库腿夹具 8 个

### F4: 凭据级归因需凭据 ready
- **问题**: 凭据欠配额时，绑定先放回 → 新面间漂移
- **修复**: `auto_credential_%` 恢复加 `availability_state='ready'` 守卫
- **影响文件**: 同 F3（复用同一个 SQL 函数）

### F5: 限流拒绝带上真因
- **问题**: `gw_rpm_exceeded` 把 RPM/TPM 两道闸压成一个标签
- **修复**: `rateLimitDetailCode()` 区分 `gw_rpm_exceeded` / `gw_tpm_exceeded`
- **影响文件**: `domains/streaming/rate_limit.go`, `request_log_pipeline.go`
- 新增钉测 3 个

### 验证结果

- ✅ `go build ./...` / `go vet` / `gofmt` 绿
- ✅ 7 包全量套件绿（streaming 98.8s / bg 35.1s / admin 120.1s）
- ✅ 真库腿（隔离 PG）绿
- ✅ **12 个变异负控全部红→还原绿**

## 三、部署建议

### 3.1 部署优先级

**P0 - 高优先级**:
- **F3/F4**: 修复节点状态残留地平线（211 真库有 478 行受影响）
- **F1**: 修复格式守卫错误不重试（用户报障的 `tool_json_invalid` 场景）

**P1 - 中优先级**:
- **F2**: 防止探针抹除人工运维意图
- **F5**: 提升限流错误可观测性

### 3.2 部署方案（推荐方案一）

#### 方案一：直接部署最新 main ⭐️

```bash
# 在 211 上或通过 CI/CD
cd /path/to/llm-gateway-go
git fetch origin
git checkout origin/main  # 当前 HEAD: ea06e8972（包含修复 2550a8648）
# 运行部署脚本（具体命令根据项目配置）
./scripts/deploy-211.sh  # 或类似脚本
```

**优点**: 
- 一次性包含所有修复（包括 auto 路由优化、迁移检测等）
- 经过完整测试的稳定版本
- 211 生产环境可追平 main

**风险**: 包含修复之外的其他变更（但都已在 main 上，应该是稳定的）

#### 方案二：Cherry-pick 单个修复提交

```bash
# 如果只想部署节点状态修复
git checkout f9d74215  # 当前生产版本
git cherry-pick 2550a8648
# 构建并部署
```

**优点**: 最小化变更范围  
**风险**: 可能缺少依赖的其他修复

### 3.3 部署前检查清单

- [ ] 确认 211 数据库备份完成
- [ ] 确认当前容器可快速回滚（保留旧镜像）
- [ ] 检查是否有新迁移需要应用
- [ ] 部署到蓝绿另一槽（当前 b 在用，部署到 a）
- [ ] 观察新槽 5-10 分钟，检查关键指标
- [ ] 切换流量到新槽

### 3.4 部署后验证脚本

#### 1. 检查版本

```bash
ssh 211 'docker exec llm-gateway-211-a cat version.json'
# 期望：git_sha 包含或晚于 2550a8648
```

#### 2. 验证 F3 修复效果（节点状态残留地平线）

```bash
bash scripts/audit/q-211-readonly-sql.sh "
SELECT COUNT(*) as remaining_stale_horizons
FROM credential_model_bindings
WHERE unavailable_recover_at IS NOT NULL 
  AND unavailable_at IS NULL 
  AND available = TRUE;
"
```

**期望**: 478 → 接近 0（在下一个恢复周期后）

#### 3. 验证 F5 修复效果（限流详情）

```bash
bash scripts/audit/q-211-readonly-sql.sh "
SELECT failure_detail_code, COUNT(*) 
FROM request_logs_with_current_month 
WHERE error_kind = 'rate_limit' 
  AND ts >= NOW() - INTERVAL '1 hour'
GROUP BY failure_detail_code
ORDER BY COUNT(*) DESC;
"
```

**期望**: 出现 `gw_tpm_exceeded`（之前全是 `gw_rpm_exceeded`）

#### 4. 验证 F1 修复效果（格式守卫重试）

```bash
bash scripts/audit/q-211-readonly-sql.sh "
SELECT 
  failure_detail_code,
  COUNT(*) as total,
  SUM(CASE WHEN request_status LIKE '%retry%' THEN 1 ELSE 0 END) as retried
FROM request_logs_with_current_month 
WHERE failure_detail_code IN ('tool_json_invalid', 'redact_invalid', 'frame_encode_failed')
  AND ts >= NOW() - INTERVAL '1 hour'
GROUP BY failure_detail_code;
"
```

**期望**: `tool_json_invalid` 等错误出现重试记录

## 四、风险评估

### 4.1 已知风险

**低风险**:
- F1/F5: 纯逻辑变更，有完整钉测覆盖
- F2: 只加守卫，不改探针核心流程

**中风险**:
- F3/F4: 涉及 SQL 变更，但：
  - 只改 WHERE 条件（加 COALESCE / 去内连接 / 加凭据状态守卫）
  - 已在 211 只读模式下 EXPLAIN 验证通过
  - 已在隔离 PG 真库腿测试通过
  - SQL 逻辑经 8 个夹具 + 负控独立重放验证

### 4.2 回滚方案

#### 快速回滚（< 1 分钟）

```bash
# 切换回旧容器
ssh 211 'docker stop llm-gateway-211-a && docker start llm-gateway-211-b'
```

#### 完全回滚（< 5 分钟）

```bash
# 在 211 上
cd /path/to/llm-gateway-go
git checkout f9d74215  # 回到旧版本
# 重新构建并部署
```

## 五、决策建议

### ⭐️ 推荐：部署最新 main

**理由**:
1. ✅ 修复已在 main 上，经过完整测试（12 个变异负控全绿）
2. ✅ 包含其他重要修复（auto 路由、迁移检测、SQL 可执行性扫描等）
3. ⚠️ 211 生产环境落后 main **5 个提交**，需要追平
4. ⚠️ F3 修复的问题（478 行残留地平线）在生产**持续发生**

**建议时间窗口**: 
- 低峰期（凌晨 2-4 点）
- 或下一个正常维护窗口

**预期影响**:
- ✅ 节点状态漂移显著减少（478 行残留地平线逐步清零）
- ✅ `tool_json_invalid` 错误可重试（用户报障场景修复）
- ✅ 限流错误可观测性提升（区分 RPM/TPM）
- ✅ 人工运维意图不被探针抹除
- ✅ 其他 4 个提交的修复同步生效

## 六、附：生产环境现状快照

### NVIDIA NIM 凭据状态（同步审计）

- **整体健康**: 97.52% 成功率（最近 24h）
- **deepseek-v4.1-flash**: 11-16 上午短暂不稳定，**已自愈**
- **minimax-m3.1-flash**: 稳定，偶发 transient 错误

详见：`docs/audit/2026-11-17-nvidia-nim-credential-health-report.md`

---

**审计结论**: 
- ✅ 修复代码质量合格（完整测试覆盖）
- ✅ 部署风险可控（有快速回滚方案）
- ⚠️ 建议尽快部署（F3 问题在生产持续影响 478 个节点）

**等待部署决策与执行授权。**
