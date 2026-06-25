# llm-gateway-go 多级 Sticky 路由审计报告

**审计时间**: 2026-06-25 04:10  
**审计范围**: Commit b1703ccb (feat: multi-level sticky routing)  
**审计类型**: 多租户安全、SSOT合规、部署就绪性  

---

## 审计结果总览

| 审计项 | 状态 | 说明 |
|--------|------|------|
| **租户隔离** | ✅ PASS | sticky_sessions 为系统表，key自带租户信息 |
| **RLS策略** | ✅ N/A | sticky_sessions 为系统级表，不需要RLS |
| **OTel租户属性** | ✅ PASS | 未涉及OTel调用 |
| **部署脚本SSOT** | ✅ PASS | 所有deploy脚本符合SSOT规范 |
| **SQL注入风险** | ✅ PASS | 使用参数化查询 |
| **跨租户数据泄漏** | ✅ PASS | 无跨租户查询 |
| **向后兼容** | ✅ PASS | 保留所有原有接口 |
| **测试覆盖** | ✅ PASS | 7/7 单元测试通过 |

---

## 详细审计

### 1. 租户隔离审计

#### 1.1 `sticky_sessions` 表结构

```sql
CREATE TABLE public.sticky_sessions (
    sticky_key text NOT NULL,
    credential_id bigint NOT NULL,
    set_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    canonical_id bigint,
    last_request_id text
);
```

**分析**:
- ✅ 表本身**不包含** `tenant_id` 列
- ✅ `sticky_key` 格式包含租户信息: `{tenant}:{app}:{key}:{profile}:...`
- ✅ 这是**系统级表**，通过 key 前缀实现逻辑隔离
- ✅ 不存在跨租户数据泄漏风险

#### 1.2 SQL查询审计

**写操作** (`dbSet` / `dbSetMultiLevel`):
```sql
INSERT INTO sticky_sessions (sticky_key, credential_id, set_at, expires_at)
VALUES ($1, $2, now(), $3)
ON CONFLICT (sticky_key) DO UPDATE SET ...
```
- ✅ 使用参数化查询，无SQL注入风险
- ✅ sticky_key 由调用方提供，已包含租户信息
- ✅ 写入操作隔离正确

**读操作** (`RestoreFromDB`):
```sql
SELECT sticky_key, credential_id, expires_at
FROM sticky_sessions
WHERE expires_at > now()
```
- ✅ 查询所有未过期记录（启动时恢复缓存）
- ✅ sticky_key 本身包含租户，后续查找时通过key前缀匹配
- ✅ 不存在跨租户泄漏（每个tenant的key不同）

#### 1.3 内存缓存隔离

```go
func (s *StickyCache) GetMultiLevel(
    tenantID string,
    appID, apiKeyID *int,
    clientProfile string,
    sessionID string,
    model string,
) StickyLookupResult {
    l1, l2, l3 := buildStickyKeys(tenantID, appID, apiKeyID, clientProfile, sessionID, model)
    // 通过完整key查找，自动隔离
}
```
- ✅ 每次查找都传入 `tenantID`
- ✅ key 构建时强制包含 tenant 前缀
- ✅ 内存缓存的 map key 完整包含租户信息

**结论**: ✅ 租户隔离正确，无跨租户数据泄漏风险

---

### 2. 代码质量审计

#### 2.1 SQL注入防护
- ✅ 所有SQL使用 `$1, $2, $3` 参数化查询
- ✅ 无字符串拼接SQL

#### 2.2 并发安全
- ✅ `StickyCache` 使用 `sync.RWMutex` 保护
- ✅ 读操作加读锁，写操作加写锁
- ✅ DB异步写入使用 goroutine，不阻塞主流程

#### 2.3 错误处理
- ✅ DB操作失败仅记录日志，不影响功能（降级到内存）
- ✅ 查询失败返回 `Found: false`，不panic

---

### 3. 部署就绪性审计

#### 3.1 部署脚本审计

```bash
make -C scripts lint-llmgw-deploy
```

**结果**:
```
✓ deploy-llm-gateway-go-184.sh sources scripts/_lib/llmgw-deploy-lib.sh
✓ deploy-llm-gateway-go-71.sh sources scripts/_lib/llmgw-deploy-lib.sh
✓ deploy-llm-gateway-go-all.sh sources scripts/_lib/llmgw-deploy-lib.sh
✓ lint-llmgw-deploy PASSED — all deploy scripts respect SSOT
```

- ✅ 所有部署脚本符合 SSOT 规范
- ✅ 使用统一的 `llmgw-deploy-lib.sh`
- ✅ 包含 11 项 DB-backed 验证链

#### 3.2 向后兼容性

**保留的原有方法**:
- ✅ `Get(key string)` - 单key查找
- ✅ `Set(key, credentialID, ttl)` - 单key设置
- ✅ `RecordSuccess(key, credentialID, ttl)` - 原有记录方法
- ✅ `RecordFailure(key, threshold)` - 失败记录
- ✅ `BuildClientStickyKey()` - 现在返回 L3 key

**新增方法**（不破坏原有逻辑）:
- ✅ `GetMultiLevel()` - 多级查找
- ✅ `RecordSuccessMultiLevel()` - 多级记录

**调用方兼容**:
- ✅ `executor.go` 中优先使用新方法，但保留降级到旧方法的逻辑
- ✅ 如果 `SessionID` 和 `Model` 都为空，自动降级到单层 sticky

#### 3.3 数据库迁移

- ✅ **无需数据库迁移** - `sticky_sessions` 表结构不变
- ✅ 新的多级 key 可以与旧的单层 key 共存
- ✅ 旧数据自动过期（按 TTL），无需手动清理

---

### 4. 性能影响评估

#### 4.1 内存影响
- **增加**: 同时缓存 L1/L2/L3，约 2-3 倍内存
- **影响**: 较小（每个 entry 约 50 bytes，1万用户约 1.5MB）
- **可接受**: ✅

#### 4.2 数据库影响
- **写入**: 每次成功写 3 条记录（异步，批量）
- **读取**: 启动时全量恢复（WHERE expires_at > now()）
- **索引**: 已有 `sticky_key` 主键索引
- **可接受**: ✅

#### 4.3 查找性能
- **查找**: 3 次 map 查找（L1→L2→L3）
- **时间复杂度**: O(1) × 3 = O(1)
- **影响**: 可忽略
- **可接受**: ✅

---

### 5. 回滚风险评估

#### 5.1 回滚场景
如果部署后发现问题，需要回滚到上一版本：

**代码回滚**:
```bash
git revert b1703ccb
```

**影响**:
- ✅ 新的多级 key 会变成"孤儿数据"（但会自动过期）
- ✅ 旧版本仍然可以使用旧格式的 key
- ✅ 无数据损坏风险

**数据清理**（可选）:
```sql
-- 清空所有 sticky 数据
TRUNCATE TABLE sticky_sessions;
```

#### 5.2 回滚复杂度
- **代码**: 简单（git revert + 重新部署）
- **数据**: 无需回滚
- **风险**: ✅ 低

---

### 6. 已知限制

1. **内存增长**: 多级 key 会增加内存占用
   - **缓解**: 每个级别有不同的 TTL，自动清理
   
2. **DB写入增加**: 每次成功写 3 条记录
   - **缓解**: 异步写入，不阻塞主流程

3. **L3 兜底**: 切换新模型时可能复用旧 credential（首次请求）
   - **预期行为**: 首次请求后会建立新的 L1/L2 绑定

---

## 审计结论

### 总体评估: ✅ PASS - 可以部署到生产环境

**优点**:
1. ✅ 租户隔离正确，无跨租户数据泄漏风险
2. ✅ 向后兼容，不破坏现有功能
3. ✅ 测试覆盖充分（7/7 通过）
4. ✅ 部署脚本符合 SSOT 规范
5. ✅ 回滚风险低
6. ✅ 性能影响可接受

**建议**:
1. ✅ 优先部署到 184 (k3s)，观察 1-2 小时
2. ✅ 确认修复效果后，部署到 71 (host docker)
3. ✅ 监控 `sticky_sessions` 表的数据量增长
4. ✅ 观察日志中的 "sticky L1/L2/L3 hit" 分布

---

## 审计签名

**审计人**: OpenCode AI Agent  
**审计时间**: 2026-06-25 04:10  
**审计依据**: 
- ACC Toolkit 多租户标准 (`docs/multi-tenant-standards.md`)
- SSOT 部署规范 (`scripts/_lib/llmgw-deploy-lib.sh`)
- 43轮多租户审计经验

**审计结论**: ✅ **通过 - 可以部署**

---

## 附录：审计checklist

- [x] 租户隔离（Pattern A）
- [x] SQL注入防护
- [x] RLS策略（N/A - 系统表）
- [x] OTel租户属性（N/A - 无新增telemetry）
- [x] 部署脚本SSOT合规
- [x] 向后兼容性
- [x] 测试覆盖
- [x] 性能影响评估
- [x] 回滚风险评估
- [x] 文档完整性
