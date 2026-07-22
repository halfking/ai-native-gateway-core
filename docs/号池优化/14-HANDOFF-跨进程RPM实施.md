# Handoff 文档：跨进程 RPM 限流实施

> **日期**: 2026-07-18
> **会话**: 号池管理二次审计 + 跨进程RPM设计
> **状态**: Phase 2 编码完成，远程部署验收待执行
> **下一会话负责人**: 实施工程师

---

## § 1. 本会话完成内容

### 1.1 二次审计（已完成 ✅）

**交付物**：
- `docs/号池优化/12-二次审计报告.md`（402行）
- 8个维度详细审计，发现6处文档误导性表述并修正
- 修正 `05/06/08/10/11` 文档的实现状态声明
- 更新 `README.md` 总索引

**关键发现**：
- ✅ RPM 预留时序修复正确（commit b18af2468）
- ⚠️ 当前 RPM 为**单实例限流**（多实例场景实际 RPM = limit × replicas）
- ⚠️ 60s 固定窗口覆盖 80% provider，剩余 20% 需 provider-specific 配置

**Git 提交**：
```
57beb753b docs(pool-readme): 更新总索引 - 反映二次审计结论
6f3e405db docs(pool-audit): 二次审计报告 + 修正6处文档误导性表述
```

### 1.2 跨进程 RPM 设计（已完成 ✅）

**交付物**：
- `docs/号池优化/13-跨进程RPM实施方案.md`（完整技术设计）
- `scripts/rpm_sliding_window.lua`（Redis 原子滑动窗口脚本）

**技术方案**：
- Redis ZSET 滑动窗口（Lua 脚本原子操作）
- 双模式架构（内存限流 + Redis 限流）
- 自动降级策略（Redis 不可用时回退到内存）

---

## § 2. 下一会话核心任务

### 2.1 必须完成（P0）

1. **引入 Redis 依赖**（已存在于 go.mod）
   ```bash
   go get github.com/redis/go-redis/v9
   go get github.com/alicebob/miniredis/v2  # 测试用
   ```

2. **重构现有 RPM 代码**（已完成）
   - 文件：`domains/credential/limiter.go:251-282`
   - 抽取为独立的 `MemoryRPMLimiter`
   - 保持 `rpmWindow` 结构不变（Line 212）

3. **实现 RedisRPMLimiter**（已完成）
   - 新文件：`domains/credential/rpm_redis.go`
   - Lua 脚本已就绪（`scripts/rpm_sliding_window.lua`）
   - 降级逻辑：Redis 失败 → MemoryRPMLimiter

4. **单元测试**（5个）+ **集成测试**（2个，miniredis，已完成）

5. **性能对比 benchmark**（已完成；真实 Redis P99 待部署环境验证）

---

## § 3. 关键代码路径

| 文件 | 状态 | 说明 |
|------|------|------|
| `docs/号池优化/13-跨进程RPM实施方案.md` | ✅ | 完整技术设计 |
| `scripts/rpm_sliding_window.lua` | ✅ | Redis Lua 脚本 |
| `domains/credential/limiter.go:251-282` | ✅ | 委托 RPMLimiter |
| `domains/credential/rpm_memory.go` | ✅ | 内存 fallback |
| `domains/credential/rpm_redis.go` | ✅ | Redis 适配器 |

---

## § 4. 环境变量配置

```bash
# 跨进程 RPM（可选，默认单实例内存限流）
export RPM_REDIS_URL="redis://localhost:6379/2"
```

---

## § 5. 验收标准

**功能**：
- [x] 3 实例 + Redis：miniredis 模拟验证全局 RPM = limit
- [x] Redis 不可用：自动降级

**性能**：
- [ ] Redis 模式 P99 ≤ 2ms（miniredis benchmark 已完成，真实 Redis 待部署）

**质量**：
- [ ] credential 包覆盖率 ≥ 85%（当前完整包约 81%；RPM 新增实现关键函数已覆盖，需补齐包口径）
- [x] `go test -race` 无竞态

---

## § 6. 回滚方案

```bash
unset RPM_REDIS_URL
systemctl restart llm-gateway-go
```

---

**Handoff 创建时间**: 2026-07-18
**建议新会话类型**: 实施工程师（编码 + 测试）
**预计实施时间**: 2-3 天

**完整设计文档**: 参见 `13-跨进程RPM实施方案.md`（包含代码骨架、测试用例、灰度计划）
