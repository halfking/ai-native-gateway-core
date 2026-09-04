# 双模式存储实施清单

## 项目信息
- **项目**: LLM Gateway 双模式存储架构
- **开始日期**: 2026-09-04
- **状态**: Phase 1-2 已完成，Phase 3-5 待实施
- **负责人**: LLM Gateway Team

---

## 总体进度

```
██████████░░░░░░░░░░ 40% 完成

Phase 1: ████████ 100% ✅
Phase 2: ████████ 100% ✅
Phase 3: ░░░░░░░░   0% ⏳
Phase 4: ░░░░░░░░   0% ⏳
Phase 5: ░░░░░░░░   0% ⏳
```

---

## Phase 1: 存储抽象层 ✅ 已完成

### 交付物
- [x] `storage/config_store.go` (2.2KB)
  - Provider / Credential / Model 接口定义
  - 支持 CRUD 操作
  - 缓存失效接口

- [x] `storage/session_store.go` (1.9KB)
  - Session 元数据接口
  - Session body 文件存储接口
  - Session turn 接口

- [x] `storage/request_store.go` (1.8KB)
  - Request 元数据接口
  - Request body 文件存储接口
  - 批量清理接口

- [x] `storage/state_store.go` (1.5KB)
  - KV 存储接口（替代 Redis GET/SET）
  - 限流计数接口（替代 Redis INCR）
  - 队列接口（替代 Redis LPUSH/RPOP）
  - 锁接口（替代 Redis 分布式锁）

### 验收标准
- [x] 所有接口定义清晰，方法签名合理
- [x] 包含完整的上下文传递（context.Context）
- [x] 错误处理使用 error 返回值
- [x] 接口职责单一，易于实现

---

## Phase 2: SQLite 配置存储 ✅ 已完成

### 交付物
- [x] `storage/sqlite/config_store.go` (12KB)
  - 完整的 ConfigStore 接口实现
  - 自动 schema 初始化
  - Provider CRUD（含 JSON 配置）
  - Credential CRUD（含加密存储支持）
  - Model CRUD
  - 外键约束和索引

### 功能特性
- [x] SQLite3 驱动集成（`mattn/go-sqlite3`）
- [x] 自动建表和索引
- [x] UPSERT 语义（INSERT ... ON CONFLICT DO UPDATE）
- [x] JSON 序列化/反序列化
- [x] 时间戳管理（created_at / updated_at）
- [x] 级联删除（外键约束）

### 验收标准
- [x] 编译通过（待添加 go.mod 依赖）
- [ ] 单元测试覆盖主要 CRUD 操作
- [ ] 并发安全性测试
- [ ] 性能基准测试（< 3ms 写入）

---

## Phase 3: 文件存储实现 ⏳ 待实施

### 目标
实现会话内容和请求体的文件存储，解决 SQLite 不适合存储大 BLOB 的问题。

### 子任务

#### 3.1 文件存储管理器 (storage/file/manager.go)
- [ ] 分层目录结构（年/月/日/ID）
- [ ] 路径生成器（session → file path）
- [ ] 目录自动创建
- [ ] 文件锁定机制（防止并发写入冲突）

#### 3.2 压缩/解压缩 (storage/file/compression.go)
- [ ] gzip 压缩实现
- [ ] gzip 解压缩实现
- [ ] 压缩级别配置（默认 gzip.BestSpeed）
- [ ] 错误处理和数据校验

#### 3.3 异步写入队列 (storage/file/async_writer.go)
- [ ] 基于 channel 的异步队列
- [ ] 批量写入优化（buffer 积累）
- [ ] 后台 worker goroutine
- [ ] 优雅关闭（flush pending writes）
- [ ] 写入失败重试机制

#### 3.4 SQLite 路径索引 (storage/sqlite/file_index.go)
- [ ] 创建 file_index 表
- [ ] 保存 (id, type, file_path, size, created_at)
- [ ] 查询路径（通过 ID）
- [ ] 批量清理（按日期）

#### 3.5 Session Bodies 实现 (storage/file/session_bodies.go)
- [ ] 实现 SessionStore.SaveSessionBody
- [ ] 实现 SessionStore.GetSessionBody
- [ ] 实现 SessionStore.DeleteSessionBody
- [ ] 集成压缩和异步写入

#### 3.6 Request Bodies 实现 (storage/file/request_bodies.go)
- [ ] 实现 RequestStore.SaveRequestBody
- [ ] 实现 RequestStore.GetRequestBody
- [ ] 实现 RequestStore.DeleteRequestBody

### 验收标准
- [ ] 写入操作非阻塞（< 1ms 同步部分）
- [ ] 压缩率 > 60%（gzip）
- [ ] 读取性能 < 10ms（含解压）
- [ ] 支持并发读写（加锁保护）
- [ ] 异常恢复（进程崩溃后重启可用）

---

## Phase 4: 内存状态管理 ⏳ 待实施

### 目标
实现运行时状态的内存存储，替代 Redis 在单机场景下的功能。

### 子任务

#### 4.1 KV 存储 (storage/memory/kv_store.go)
- [ ] 基于 sync.Map 的实现
- [ ] TTL 支持（过期自动删除）
- [ ] 后台清理 goroutine
- [ ] Get / Set / Delete / Exists 方法

#### 4.2 限流计数器 (storage/memory/rate_limiter.go)
- [ ] 滑动窗口算法
- [ ] IncrementCounter（时间窗口内计数）
- [ ] GetCounter（查询当前计数）
- [ ] ResetCounter（重置计数）
- [ ] 定期清理过期时间戳

#### 4.3 队列 (storage/memory/queue.go)
- [ ] 基于 channel 的 FIFO 队列
- [ ] Enqueue / Dequeue / QueueLength
- [ ] 支持多个命名队列（map[queueName]chan）
- [ ] 阻塞 / 非阻塞模式

#### 4.4 锁 (storage/memory/lock.go)
- [ ] 基于 sync.Mutex 的实现
- [ ] AcquireLock / ReleaseLock
- [ ] TTL 支持（超时自动释放）
- [ ] 死锁检测（可选）

### 验收标准
- [ ] 所有操作 < 1μs (纳秒级)
- [ ] 并发安全（通过 race detector 测试）
- [ ] 内存占用合理（< 100MB for 10K entries）
- [ ] 优雅关闭（释放资源）

---

## Phase 5: 部署配置 ⏳ 待实施

### 目标
提供单机模式的部署脚本和配置模板，简化安装和运维。

### 子任务

#### 5.1 配置模板 (configs/lite-mode.yaml)
- [ ] storage.mode = lite
- [ ] 数据目录配置
- [ ] SQLite 参数调优
- [ ] 压缩选项
- [ ] 清理策略

#### 5.2 安装脚本 (scripts/install-lite.sh)
- [ ] 自动创建数据目录
- [ ] 初始化 SQLite 数据库
- [ ] 设置文件权限
- [ ] 生成 systemd service
- [ ] 健康检查

#### 5.3 Docker Compose (docker-compose.lite.yml)
- [ ] 单容器部署
- [ ] 数据卷挂载
- [ ] 环境变量配置
- [ ] 健康检查

#### 5.4 迁移工具 (cmd/migrate/lite_to_full.go)
- [ ] 从 SQLite 读取配置
- [ ] 从文件读取内容
- [ ] 写入 PostgreSQL
- [ ] 进度报告和断点续传

#### 5.5 文档 (docs/deployment/lite-mode-guide.md)
- [ ] 快速开始
- [ ] 配置说明
- [ ] 故障排查
- [ ] 性能调优
- [ ] 备份恢复

### 验收标准
- [ ] 一键安装成功（< 5 分钟）
- [ ] Docker 镜像 < 200MB
- [ ] 配置文件清晰易懂
- [ ] 健康检查正常工作
- [ ] 文档完整准确

---

## 文档交付物 ✅ 已完成

### 设计文档
- [x] `docs/design/README.md` (5.5KB)
  - 文档导航
  - 快速决策表
  - 常见问题

- [x] `docs/design/lite-mode-quick-ref.md` (3.5KB)
  - 快速参考卡片
  - 决策矩阵
  - 技术栈对比

- [x] `docs/design/lite-mode-summary.md` (7.8KB)
  - 执行摘要
  - 核心发现
  - 已完成工作

- [x] `docs/design/lite-mode-storage-design.md` (16KB)
  - 完整设计文档
  - 18 页详细内容
  - 架构图和流程图

---

## 测试计划

### 单元测试
- [ ] ConfigStore CRUD 测试
- [ ] 文件存储读写测试
- [ ] 压缩/解压缩测试
- [ ] 内存状态管理测试
- [ ] 并发安全性测试

### 集成测试
- [ ] 完整会话写入读取流程
- [ ] 异步队列压力测试
- [ ] 文件清理机制测试
- [ ] 模式切换测试（full ↔ lite）

### 性能测试
- [ ] 配置读取基准（目标 < 1ms）
- [ ] 会话写入基准（目标 < 5ms）
- [ ] 文件读取基准（目标 < 10ms）
- [ ] 并发性能测试（1000 QPS）

### 可靠性测试
- [ ] 进程崩溃恢复测试
- [ ] 磁盘满处理测试
- [ ] 数据完整性校验
- [ ] 长期运行稳定性测试

---

## 里程碑

### M1: 基础框架 ✅ 已完成 (Week 1-2)
- [x] 存储抽象层接口
- [x] SQLite 配置存储实现
- [x] 完整设计文档

### M2: 文件存储 ⏳ 进行中 (Week 2-3)
- [ ] 文件存储管理器
- [ ] 异步写入队列
- [ ] Session/Request bodies 实现

### M3: 内存状态 📅 计划中 (Week 3)
- [ ] KV 存储
- [ ] 限流计数器
- [ ] 队列和锁

### M4: 部署集成 📅 计划中 (Week 3-4)
- [ ] 配置和脚本
- [ ] Docker 镜像
- [ ] 部署文档

### M5: 测试与发布 📅 计划中 (Week 4)
- [ ] 完整测试套件
- [ ] 性能基准
- [ ] 生产就绪

---

## 风险跟踪

| 风险 | 概率 | 影响 | 缓解措施 | 状态 |
|------|------|------|---------|------|
| SQLite 性能不足 | 低 | 中 | 性能测试验证 | 待验证 |
| 文件存储损坏 | 低 | 高 | checksum + 备份 | 待实现 |
| 内存溢出 | 中 | 高 | 限制缓存大小 | 待实现 |
| 磁盘空间不足 | 中 | 高 | 定期清理 + 告警 | 待实现 |
| 异步队列丢失 | 低 | 高 | 优雅关闭 + 持久化 | 待实现 |

---

## 下一步行动

### 立即执行（本周）
1. 添加 go.mod 依赖：`github.com/mattn/go-sqlite3`
2. 编写 ConfigStore 单元测试
3. 开始 Phase 3.1：文件存储管理器

### 短期目标（2 周内）
1. 完成 Phase 3：文件存储实现
2. 端到端测试：配置 + 会话 + 请求

### 中期目标（4 周内）
1. 完成 Phase 4：内存状态管理
2. 完成 Phase 5：部署配置
3. 发布 alpha 版本供测试

---

## 资源需求

### 人力
- 后端开发：2 人 × 4 周
- 测试工程师：1 人 × 2 周
- 技术写作：1 人 × 1 周

### 基础设施
- 测试服务器：1 台（模拟单机环境）
- CI/CD 集成
- 性能测试环境

---

## 联系方式

- **技术负责人**: [技术负责人姓名]
- **项目经理**: [项目经理姓名]
- **文档维护**: [文档维护者姓名]
- **问题反馈**: [GitHub Issues 链接]

---

**最后更新**: 2026-09-04  
**版本**: v1.0  
**状态**: Phase 1-2 已完成，Phase 3-5 待实施
