# 本地部署蓝绿优化 - README 补充

> 将以下内容添加到项目主 README.md 的"本地开发"章节

---

## 🚀 本地蓝绿部署（推荐）

**新特性**：零停机部署，切换时间 < 2 秒

### 快速开始

```bash
# 1. 初始化 Nginx 反向代理
bash scripts/setup-local-nginx.sh install

# 2. 蓝绿部署
bash scripts/local-host-deploy-bluegreen.sh deploy

# 3. 访问服务
curl http://localhost:8781/healthz
```

### 性能对比

| 指标 | 传统部署 | 蓝绿部署 | 提升 |
|------|----------|----------|------|
| 切换时间 | 20-30s | **< 2s** | **10-15x** |
| 停机时间 | 20-30s | **0s** | **消除** |
| 回滚速度 | 20-30s | **< 5s** | **4-6x** |

### 日常使用

```bash
# 部署新版本（零停机）
bash scripts/local-host-deploy-bluegreen.sh deploy

# 快速回滚
bash scripts/local-host-deploy-bluegreen.sh rollback <version>

# 查看状态
bash scripts/local-host-deploy-bluegreen.sh status
```

### 工作原理

```
客户端 → Nginx:8781 → [Gateway Blue:18781 ⇄ Gateway Green:18782] → PG/Redis
              ↑
         原子切换点（< 100ms）
```

1. **并行预热**：新版本在后台启动，旧版本继续服务
2. **原子切换**：Nginx 配置一次性切换流量
3. **零停机**：用户无感知

### 详细文档

- 📖 [完整技术方案](./LOCAL_DEPLOY_OPTIMIZATION.md)
- 🚀 [快速上手指南](./BLUEGREEN_QUICKSTART.md)
- 📊 [交付总结](./DELIVERY_SUMMARY.md)

---

## 传统部署（兼容模式）

如果不使用蓝绿部署，仍可使用传统方式：

```bash
bash scripts/local-host-deploy.sh deploy
```

---
