# llm-gateway-go 多级 Sticky 路由部署验证报告

**部署时间**: 2026-06-25 21:23  
**部署环境**: 184 k3s (__DOMAIN_8__)  
**版本**: ee72c966  
**部署方式**: `./scripts/deploy-llm-gateway-go-184.sh --only app`  

---

## 部署状态

### ✅ 部署成功

**部署日志**:
```
deployment "llm-gateway-go-deployment" successfully rolled out
pod/llm-gateway-go-deployment-6d7c5fddd7-qz949 condition met
```

**健康检查**:
```bash
$ curl -s https://__DOMAIN_8__/healthz | jq .status
"ok"

$ curl -s https://__DOMAIN_8__/healthz | jq .version
"V2.2.0-77-gee72c966-ee72c966-2026-06-25-690"
```

✅ 服务正常运行  
✅ 版本正确（ee72c966 = 多级sticky实现）  

---

## 审计通过项

| 审计项 | 状态 | 详情 |
|--------|------|------|
| 租户隔离 | ✅ PASS | sticky_sessions 为系统表，key自带租户信息 |
| SQL注入 | ✅ PASS | 使用参数化查询 |
| 向后兼容 | ✅ PASS | 所有原有方法保留 |
| SSOT合规 | ✅ PASS | 部署脚本符合规范 |
| 单元测试 | ✅ PASS | 7/7 通过 |
| 编译检查 | ✅ PASS | 无错误 |
| Pre-commit | ✅ PASS | go vet + SQL检查通过 |

---

## 功能验证

### 自动化测试脚本

已创建: `scripts/test-multi-level-sticky.sh`

**测试场景**:
1. ✅ 选择 claude-opus-4-8 → 应该使用 claude credential
2. ✅ 同一会话切换到 minimax → 应该使用 minimax credential  
3. ✅ 切回 claude → 应该复用之前的 claude credential（L1命中）

**运行方式**:
```bash
export LLMGW_API_KEY="your-api-key"
./scripts/test-multi-level-sticky.sh
```

### 手动验证步骤

#### 1. 验证多级key存在

```bash
# SSH到184，查询 sticky_sessions 表
ssh root@__INTERNAL_K8S_HOST__
docker exec llm-gateway-pg psql -U kxuser -d llm_gateway -c "
  SELECT 
    sticky_key,
    credential_id,
    CASE 
      WHEN sticky_key LIKE '%:%:%:%:%:%' THEN 'L1 (session+model)'
      WHEN sticky_key LIKE '%:%:%:%:%' THEN 'L2 (client+model)'
      ELSE 'L3 (client)'
    END as level,
    expires_at
  FROM sticky_sessions
  ORDER BY set_at DESC
  LIMIT 20;
"
```

**预期结果**: 看到三种格式的key（L1/L2/L3）

#### 2. 验证日志中的sticky命中

```bash
# 查看日志
kubectl -n pms-test logs deploy/llm-gateway-go-deployment -f | grep sticky

# 预期看到:
# sticky L1 hit (session+model)
# sticky L2 hit (client+model)
# sticky L3 hit (client)
# sticky multi-level recorded
```

#### 3. 验证原问题是否修复

使用你之前遇到问题的场景：
- 手动选择 `claude-opus-4-8`
- 检查 `request_logs` 表中的 `routing_decision_summary`
- **预期**: 应该显示使用了 claude 相关的 credential，而不是 minimax

---

## 性能监控

### 需要监控的指标

1. **Sticky 命中率分布**:
   ```bash
   # 查看日志统计
   kubectl -n pms-test logs deploy/llm-gateway-go-deployment --since=1h | \
     grep "sticky" | \
     grep -E "L1|L2|L3" | \
     awk '{print $NF}' | \
     sort | uniq -c
   ```
   
   **预期分布**:
   - L1 命中: 40-60%（同会话同模型）
   - L2 命中: 20-30%（跨会话同模型）
   - L3 命中: 10-20%（兜底）
   - Miss: 10-20%（新用户/过期）

2. **sticky_sessions 表增长**:
   ```sql
   SELECT COUNT(*) as total_keys,
          COUNT(CASE WHEN sticky_key LIKE '%:%:%:%:%:%' THEN 1 END) as l1_keys,
          COUNT(CASE WHEN sticky_key LIKE '%:%:%:%:%' THEN 1 END) as l2_keys,
          COUNT(CASE WHEN sticky_key NOT LIKE '%:%:%:%:%' THEN 1 END) as l3_keys
   FROM sticky_sessions;
   ```
   
   **预期**: L1/L2/L3 数量比例约 1:1:1（因为同时记录）

3. **内存使用**:
   ```bash
   kubectl -n pms-test top pod -l app=llm-gateway-go
   ```
   
   **预期**: 内存增长 2-3 倍（相对之前的单层sticky）

---

## 回滚方案

如果发现问题，可以快速回滚：

### 方式1: 回滚到上一个版本

```bash
# 主仓库回滚
cd __DEV_HOME__/workspace/official-deploy
git revert HEAD~2..HEAD  # 回滚最近2次提交（submodule + audit）
git push

# 子模块回滚
cd services/llm-gateway-go
git revert ee72c966  # 回滚audit报告
git revert b1703ccb  # 回滚多级sticky实现
git push

# 重新部署
cd __DEV_HOME__/workspace/official-deploy
./scripts/deploy-llm-gateway-go-184.sh --only app
```

### 方式2: 回滚到已知好的commit

```bash
cd __DEV_HOME__/workspace/official-deploy/services/llm-gateway-go
git checkout c6ee414b  # 多级sticky之前的最后一个commit
cd ../..
git add services/llm-gateway-go
git commit -m "chore: rollback llm-gateway-go to c6ee414b"
git push
./scripts/deploy-llm-gateway-go-184.sh --only app
```

### 方式3: 仅清空sticky数据（保留代码）

```bash
ssh root@__INTERNAL_K8S_HOST__
docker exec llm-gateway-pg psql -U kxuser -d llm_gateway -c "TRUNCATE TABLE sticky_sessions;"
```

**影响**: 清空所有sticky绑定，用户下次请求会重新路由

---

## 已知问题

1. **kubectl 连接超时**:
   ```
   dial tcp __INTERNAL_PUBLIC_IP__:6443: connect: operation timed out
   ```
   - **原因**: 本地网络到184的6443端口连接问题
   - **影响**: 无法直接使用 `kubectl` 查看日志
   - **解决**: 通过SSH到184后使用kubectl，或直接测试API端点

2. **瞬时502错误**:
   - **现象**: 部署后短暂出现502 Bad Gateway
   - **原因**: Pod滚动更新期间的瞬时不可用
   - **持续时间**: <30秒
   - **当前状态**: 已恢复正常

---

## 下一步

### 1. 观察期（1-2小时）

- ✅ 服务正常运行
- ⏳ 等待真实流量验证
- ⏳ 监控错误率、延迟
- ⏳ 检查sticky命中率分布

### 2. 运行功能测试

```bash
export LLMGW_API_KEY="your-api-key"
./scripts/test-multi-level-sticky.sh
```

### 3. 部署到71（如果184验证通过）

```bash
cd __DEV_HOME__/workspace/official-deploy
./scripts/deploy-llm-gateway-go-71.sh
```

**注意**: 71是host docker模式，需要：
- 确认184稳定运行1-2小时
- 用户报告问题已修复
- 无新的错误或异常

---

## 验证清单

- [x] 编译通过
- [x] 单元测试通过（7/7）
- [x] 审计通过（租户隔离、SQL安全、SSOT）
- [x] 部署成功（184 k3s）
- [x] 健康检查通过
- [x] 版本确认（ee72c966）
- [ ] 功能测试（待用户执行）
- [ ] 真实流量验证（观察期1-2h）
- [ ] Sticky命中率统计
- [ ] 原问题修复确认
- [ ] 部署到71

---

## 签名

**部署人**: OpenCode AI Agent  
**部署时间**: 2026-06-25 21:23 UTC+8  
**验证状态**: ✅ 部署成功，服务正常，等待功能验证  
**风险等级**: 低（向后兼容，可快速回滚）  

---

## 附录

### 相关文档

- 设计文档: `docs/2026-06-25-multi-level-sticky-fix.md`
- 实现报告: `docs/2026-06-25-multi-level-sticky-implementation-report.md`
- 审计报告: `docs/2026-06-25-multi-level-sticky-audit-report.md`
- 本验证报告: `docs/2026-06-25-multi-level-sticky-deployment-verification.md`

### Git Commits

- `b1703ccb` - feat(routing): multi-level sticky routing implementation
- `ee72c966` - docs: add audit report
- `52d143f5` - chore: update submodule (main repo)

### 测试脚本

- `scripts/test-multi-level-sticky.sh` - 自动化功能验证脚本
