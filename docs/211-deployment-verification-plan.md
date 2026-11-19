# 211 部署验证计划

## 当前状态
- 代码已合并到本地 main (commit: 05cd885f2)
- 部署脚本正在运行中 (PID: 23420)
- 测试门控发现的问题:
  1. TestPipefailRaceLedgerCoversCandidateSet - 新脚本未登记
  2. TestScriptFamilyCompileSitesUseDedicatedGoCache - 编译站点数量变化
  3. TestPGTestEnvRegistryCoversEveryGapName - 环境变量未登记
  4. TestDestructiveTargetGuardNoUnguardedLeg - 需要台账登记
  5. TestRequestSurfaceDocEvidenceStillHolds - 文档与代码漂移

## 验证步骤

### 1. 部署完成后的基础验证
```bash
# 健康检查
curl -s https://llm.itestu.cn/api/health | jq

# 版本验证
curl -s https://llm.itestu.cn/api/health | jq -r '.version, .commit'

# 快速验证脚本
bash scripts/quick-test-211.sh
```

### 2. 登录和认证测试
```bash
# 使用验证脚本
ADMIN_PASSWORD=<口令> bash scripts/verify-211-deployment.sh
```

### 3. Maestro UI 测试
```bash
# Flow 11: 简易模式总览
BASE_URL=https://llm.itestu.cn \
  ADMIN_USER=admin \
  ADMIN_PASSWORD=<口令> \
  bash maestro/run-simple-overview.sh

# Flow 12: Live Stage Tree  
BASE_URL=https://llm.itestu.cn \
  ADMIN_USER=admin \
  ADMIN_PASSWORD=<口令> \
  bash maestro/run-stage-tree.sh

# Flow 13: Board Operational (新增)
# 需要先解析动态 ID
```

### 4. 新功能验证
- PI Analysis Stats 端点: `/api/admin/pi-analysis/stats`
- PI Analysis Queue 端点: `/api/admin/pi-analysis/queue`
- 迁移 980 应用状态

## 需要修复的测试问题

### 1. Pipefail 台账登记
新增脚本需要登记:
- scripts/test-211-full.sh (3处)
- scripts/quick-test-211.sh (可能)
- scripts/audit/auto-monitor.sh (3处)

### 2. 编译站点登记更新
- 扫描值: 191 个
- 登记值: 181 个
- 差异: 10 个

### 3. 环境变量登记
Casdoor 相关变量需要登记到 pg_test_env_registry.tsv

### 4. 文档同步
request_surface 相关文档需要与代码同步

## 修复优先级
1. **高**: 如果部署成功,先完成功能验证
2. **中**: 修复 pipefail 台账(影响 CI)
3. **低**: 更新其他登记和文档

## 提交计划
1. 验证通过后,提交测试脚本
2. 修复测试问题后,再次提交
3. 推送到远程仓库
