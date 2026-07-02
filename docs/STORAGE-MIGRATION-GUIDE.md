# 附件存储迁移指南

**版本**: v1.0  
**日期**: 2026-07-02  
**适用场景**: 从本地文件系统迁移到云存储（OSS/S3）

---

## 目录

- [概述](#概述)
- [迁移前准备](#迁移前准备)
- [迁移步骤](#迁移步骤)
- [回滚方案](#回滚方案)
- [验证清单](#验证清单)
- [常见问题](#常见问题)

---

## 概述

### 为什么要迁移到云存储

| 本地存储 | 云存储 (OSS/S3) |
|---------|----------------|
| 磁盘空间有限 | 无限扩展 |
| 单点故障风险 | 99.99% 可用性 |
| 需要手动备份 | 自动冗余备份 |
| 扩容需要停机 | 在线扩容 |
| 多地部署困难 | 全球分发 CDN |

### 迁移策略

我们提供两种迁移策略：

1. **停机迁移**（推荐，适合小规模）
   - 停止服务
   - 执行迁移
   - 切换配置
   - 重启服务
   - 总耗时：数据量 / 网速 + 10分钟

2. **热迁移**（适合大规模，零停机）
   - 配置双写（本地+云）
   - 后台迁移历史数据
   - 验证完整性
   - 切换为只写云
   - 总耗时：数小时到数天

本文主要介绍**停机迁移**方案。

---

## 迁移前准备

### 1. 评估当前存储

```bash
# 统计附件数量和大小
du -sh /data/attachments
find /data/attachments -type f | wc -l

# 查看最大文件
find /data/attachments -type f -exec ls -lh {} \; | sort -k5 -hr | head -10
```

**记录下来**：
- 总文件数：_______
- 总大小：_______ GB
- 最大文件：_______ MB
- 预计传输时间：总大小 / 网络带宽（建议预留 2 倍时间）

### 2. 准备云存储账号

#### 阿里云 OSS

1. 登录阿里云控制台
2. 创建 OSS Bucket
   - 名称：`llm-gateway-attachments`（全局唯一）
   - 区域：选择与服务器相同区域（降低延迟）
   - 存储类型：标准存储
   - 读写权限：私有
3. 创建 AccessKey
   - RAM 控制台 → 用户 → 创建用户
   - 勾选"编程访问"
   - 授予权限：`AliyunOSSFullAccess`
   - 保存 AccessKey ID 和 Secret

#### AWS S3 / MinIO

**AWS S3**:
1. 登录 AWS 控制台
2. 创建 S3 Bucket
   - 名称：`llm-gateway-attachments`
   - 区域：`us-west-2`（或就近区域）
   - 阻止公共访问：启用
3. 创建 IAM 用户
   - 策略：`AmazonS3FullAccess`
   - 生成访问密钥

**MinIO**:
```bash
# 启动 MinIO（Docker）
docker run -d \
  -p 9000:9000 -p 9001:9001 \
  -v /data/minio:/data \
  -e MINIO_ROOT_USER=minioadmin \
  -e MINIO_ROOT_PASSWORD=minioadmin \
  minio/minio server /data --console-address ":9001"

# 创建 Bucket
mc alias set myminio http://localhost:9000 minioadmin minioadmin
mc mb myminio/llm-gateway-attachments
```

### 3. 配置验证

使用健康检查工具验证配置：

```bash
# 临时修改配置（不重启）
cat > /tmp/test-config.yml <<EOF
storage:
  type: oss
  oss:
    endpoint: oss-cn-hangzhou.aliyuncs.com
    bucket: llm-gateway-attachments
    access_key_id: LTAI5t...
    access_key_secret: xxxxxxxxxxxxx
EOF

# 测试连接（使用迁移工具的 dry-run）
./storage-migrate \
  --source-type=local --source-dir=/data/attachments \
  --target-type=oss --target-oss-endpoint=oss-cn-hangzhou.aliyuncs.com \
  --target-oss-bucket=llm-gateway-attachments \
  --target-oss-ak=LTAI5t... --target-oss-sk=xxxxxxxxxxxxx \
  --dry-run --workers=1

# 看到 "✓ 源存储连接正常" 和 "✓ 目标存储连接正常" 表示配置正确
```

### 4. 备份当前数据

```bash
# 创建备份
tar -czf attachments-backup-$(date +%Y%m%d).tar.gz /data/attachments

# 或使用 rsync 到备份服务器
rsync -avz /data/attachments/ backup-server:/backups/attachments/
```

---

## 迁移步骤

### 方案 A: 停机迁移（推荐）

#### Step 1: 通知用户并停止服务

```bash
# 发送通知（邮件/Slack/钉钉）
echo "系统将于 2026-07-02 22:00 开始维护，预计 1 小时"

# 停止 LLM Gateway 服务
sudo systemctl stop llm-gateway
# 或
sudo supervisorctl stop llm-gateway

# 确认进程已停止
ps aux | grep llm-gateway
```

#### Step 2: 执行数据迁移

```bash
# 编译迁移工具（如果还没有）
cd /path/to/llm-gateway-go
go build -o bin/storage-migrate ./cmd/storage-migrate

# 执行迁移（阿里云 OSS 示例）
./bin/storage-migrate \
  --source-type=local \
  --source-dir=/data/attachments \
  --target-type=oss \
  --target-oss-endpoint=oss-cn-hangzhou.aliyuncs.com \
  --target-oss-bucket=llm-gateway-attachments \
  --target-oss-ak=LTAI5t... \
  --target-oss-sk=xxxxxxxxxxxxx \
  --workers=20 \
  --skip-exists=true \
  --report=migration-report-$(date +%Y%m%d-%H%M%S).txt

# 迁移过程会显示：
# 扫描源存储文件...
# ✓ 找到 12345 个文件
# 进度: 1000/12345 (8.1%)
# 进度: 2000/12345 (16.2%)
# ...
# ========== 迁移完成 ==========
# 总文件数: 12345
# 成功: 12340
# 跳过: 0
# 失败: 5
# 传输字节: 5368709120 (5120.00 MB)
# 耗时: 15m30s
# 平均速度: 5.51 MB/s
```

**参数说明**：
- `--workers=20`: 并发上传数，根据网络带宽调整（建议 10-50）
- `--skip-exists=true`: 断点续传，跳过已存在文件（推荐）
- `--report`: 生成详细报告，记录失败的文件

#### Step 3: 检查迁移报告

```bash
# 查看迁移报告
cat migration-report-*.txt

# 报告格式：
# OK    2024/01/req_abc123/hash1.png    1048576
# OK    2024/01/req_abc124/hash2.jpg    2097152
# FAIL  2024/02/req_abc125/hash3.pdf    network timeout
# SKIP  2024/03/req_abc126/hash4.png

# 统计失败文件
grep "^FAIL" migration-report-*.txt | wc -l

# 如果有失败文件，重新迁移它们
# （工具会自动跳过已成功的文件）
./bin/storage-migrate ... # 重复执行即可
```

#### Step 4: 更新服务配置

**方式 1: 环境变量（临时测试）**

```bash
# 编辑启动脚本
vim /etc/systemd/system/llm-gateway.service

# 添加环境变量
Environment="LLM_GATEWAY_STORAGE_TYPE=oss"
Environment="LLM_GATEWAY_STORAGE_OSS_ENDPOINT=oss-cn-hangzhou.aliyuncs.com"
Environment="LLM_GATEWAY_STORAGE_OSS_BUCKET=llm-gateway-attachments"
Environment="LLM_GATEWAY_STORAGE_OSS_AK=LTAI5t..."
Environment="LLM_GATEWAY_STORAGE_OSS_SK=xxxxxxxxxxxxx"

# 重新加载
sudo systemctl daemon-reload
```

**方式 2: 数据库配置（推荐）**

```sql
-- 连接到数据库
psql -U postgres -d llm_gateway

-- 插入存储配置
INSERT INTO settings_kv (scope, category, key, value, created_at, updated_at)
VALUES
('platform', 'storage', 'type', '"oss"', NOW(), NOW()),
('platform', 'storage', 'oss.endpoint', '"oss-cn-hangzhou.aliyuncs.com"', NOW(), NOW()),
('platform', 'storage', 'oss.bucket', '"llm-gateway-attachments"', NOW(), NOW()),
('platform', 'storage', 'oss.access_key_id', '"LTAI5t..."', NOW(), NOW()),
('platform', 'storage', 'oss.access_key_secret', '"xxxxxxxxxxxxx"', NOW(), NOW())
ON CONFLICT (scope, category, key)
DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();
```

#### Step 5: 启动服务并验证

```bash
# 启动服务
sudo systemctl start llm-gateway

# 查看日志，确认使用 OSS 后端
sudo journalctl -u llm-gateway -f

# 应该看到类似日志：
# INFO using OSS storage backend endpoint=oss-cn-hangzhou.aliyuncs.com bucket=llm-gateway-attachments
# INFO attachment extractor enabled (cloud storage) max_size_mb=20
```

#### Step 6: 功能验证

```bash
# 1. 健康检查
curl -s http://localhost:8781/api/admin/storage/health | jq .
# 期望输出：
# {
#   "healthy": true,
#   "backend_type": "oss",
#   "location": "oss-cn-hangzhou.aliyuncs.com",
#   "metadata": {
#     "bucket": "llm-gateway-attachments"
#   }
# }

# 2. 测试上传附件
# 发送一个带图片的聊天请求
curl -X POST http://localhost:8781/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{
      "role": "user",
      "content": [
        {"type": "text", "text": "这是什么？"},
        {"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KG..."}}
      ]
    }]
  }'

# 3. 在 OSS 控制台查看文件是否已上传
# 路径格式：YYYY/MM/req_xxxxx/hash.ext

# 4. 测试下载附件（如果有 admin 下载接口）
curl -s http://localhost:8781/api/admin/attachments/2024/01/req_abc/hash.png -o /tmp/test.png
file /tmp/test.png
# 应该显示：PNG image data
```

#### Step 7: 清理本地文件（可选）

**⚠️ 警告：确认云存储正常运行至少 7 天后再清理本地文件**

```bash
# 先移动到备份目录，而不是直接删除
sudo mv /data/attachments /data/attachments.backup.$(date +%Y%m%d)

# 30 天后，如果一切正常，再删除
# sudo rm -rf /data/attachments.backup.*
```

---

### 方案 B: 热迁移（高级，零停机）

#### 原理

1. 修改代码，实现双写（本地 + 云）
2. 后台迁移历史数据
3. 验证数据一致性
4. 切换为只写云
5. 停止双写，删除本地

#### 实现步骤

**Step 1: 实现双写适配器**

```go
// 创建 MultiBackend 适配器
type MultiBackend struct {
    primary   StorageBackend // 本地（暂时保留）
    secondary StorageBackend // 云（目标）
    mu        sync.Mutex
}

func (b *MultiBackend) SaveFile(relPath string, data []byte) error {
    // 先写主存储（本地）
    if err := b.primary.SaveFile(relPath, data); err != nil {
        return err
    }
    
    // 异步写入次存储（云）
    go func() {
        if err := b.secondary.SaveFile(relPath, data); err != nil {
            log.Printf("secondary write failed: %v", err)
        }
    }()
    
    return nil
}

func (b *MultiBackend) LoadFile(relPath string) ([]byte, error) {
    // 先尝试从云读取
    data, err := b.secondary.LoadFile(relPath)
    if err == nil {
        return data, nil
    }
    
    // 回退到本地
    return b.primary.LoadFile(relPath)
}
```

**Step 2: 部署双写版本**

```bash
# 编译并部署双写版本
go build -tags multi_backend -o llm-gateway ./cmd/gateway
sudo systemctl restart llm-gateway
```

**Step 3: 后台迁移历史数据**

```bash
# 使用迁移工具迁移历史数据（与方案 A 相同）
./bin/storage-migrate ... --workers=50
```

**Step 4: 验证数据一致性**

```bash
# 随机抽样验证
for i in {1..100}; do
    file=$(find /data/attachments -type f | shuf -n 1)
    relpath=$(realpath --relative-to=/data/attachments $file)
    
    # 比较本地和云端的 MD5
    local_md5=$(md5sum $file | awk '{print $1}')
    cloud_md5=$(./check-cloud-md5.sh "$relpath")
    
    if [ "$local_md5" != "$cloud_md5" ]; then
        echo "MISMATCH: $relpath"
    fi
done
```

**Step 5: 切换为只写云**

```sql
-- 更新配置，切换为云存储
UPDATE settings_kv SET value = '"oss"' 
WHERE scope = 'platform' AND category = 'storage' AND key = 'type';
```

```bash
# 重启服务
sudo systemctl restart llm-gateway
```

**Step 6: 观察并清理**

- 观察 7 天，确认无问题
- 清理本地文件

---

## 回滚方案

### 场景 1: 迁移过程中发现问题

```bash
# 1. 停止迁移工具（Ctrl+C）
# 2. 不修改服务配置，直接启动服务
sudo systemctl start llm-gateway
# 服务将继续使用本地存储
```

### 场景 2: 切换后发现问题

```sql
-- 立即回滚到本地存储
UPDATE settings_kv SET value = '"local"' 
WHERE scope = 'platform' AND category = 'storage' AND key = 'type';
```

```bash
# 重启服务
sudo systemctl restart llm-gateway

# 确认使用本地存储
curl http://localhost:8781/api/admin/storage/health | jq .backend_type
# 应该返回 "local"
```

### 场景 3: 数据丢失紧急恢复

```bash
# 从备份恢复
tar -xzf attachments-backup-20260702.tar.gz -C /

# 或从备份服务器同步
rsync -avz backup-server:/backups/attachments/ /data/attachments/

# 回滚配置并重启
sudo systemctl restart llm-gateway
```

---

## 验证清单

### 迁移前

- [ ] 已评估当前存储大小和文件数量
- [ ] 已创建云存储 Bucket 并配置权限
- [ ] 已生成 AccessKey 并测试连接
- [ ] 已备份当前数据
- [ ] 已通知用户维护时间
- [ ] 已准备回滚方案

### 迁移中

- [ ] 迁移工具连接成功（健康检查通过）
- [ ] 迁移进度正常（无大量失败）
- [ ] 网络带宽充足（速度合理）
- [ ] 服务器负载正常（CPU/内存）

### 迁移后

- [ ] 迁移报告显示成功率 > 99.9%
- [ ] 失败文件已重新迁移
- [ ] 服务配置已更新
- [ ] 健康检查 API 返回 healthy=true
- [ ] 上传附件功能正常
- [ ] 下载附件功能正常
- [ ] 日志无错误
- [ ] 监控指标正常

### 稳定运行

- [ ] 运行 7 天无故障
- [ ] 用户无投诉
- [ ] 性能指标正常
- [ ] 成本在预算内
- [ ] 已清理本地备份

---

## 常见问题

### Q1: 迁移速度太慢怎么办？

**A**: 调整并发数和网络配置

```bash
# 1. 增加并发 workers
--workers=50  # 或更高，根据带宽调整

# 2. 使用内网 endpoint（阿里云 OSS）
--target-oss-endpoint=oss-cn-hangzhou-internal.aliyuncs.com

# 3. 压缩传输（MinIO）
# 启用 MinIO 的压缩功能

# 4. 多台机器并行迁移
# 将文件列表拆分，多台机器同时执行
```

### Q2: 部分文件迁移失败怎么办？

**A**: 查看报告，重新迁移失败文件

```bash
# 查看失败原因
grep "^FAIL" migration-report.txt

# 常见原因：
# 1. 网络超时 → 重新执行迁移（会跳过已成功文件）
# 2. 文件权限 → chmod 644 失败的文件
# 3. 文件损坏 → 从备份恢复

# 重新迁移（自动跳过已成功）
./storage-migrate ... --skip-exists=true
```

### Q3: 云存储成本太高怎么办？

**A**: 优化存储策略

```bash
# 1. 启用生命周期管理（OSS/S3）
# - 30 天后转为低频访问存储（降低 50% 成本）
# - 90 天后转为归档存储（降低 80% 成本）

# 2. 启用压缩（应用层）
# 在 SaveFile 前压缩图片（lossy/lossless）

# 3. 去重（已实现 SHA256 去重）

# 4. 定期清理过期附件
# 设置保留策略，删除 180 天前的附件
```

### Q4: 如何验证数据完整性？

**A**: 使用 MD5/SHA256 校验

```bash
# 迁移工具已经记录文件大小，可以对比
awk '{print $2, $3}' migration-report.txt > cloud-sizes.txt

# 对比本地和云端大小
find /data/attachments -type f -exec stat -f "%N %z" {} \; > local-sizes.txt

# 检查差异
diff local-sizes.txt cloud-sizes.txt
```

### Q5: 切换后性能下降怎么办？

**A**: 性能优化建议

```bash
# 1. 使用内网 endpoint（降低延迟）
oss-cn-hangzhou-internal.aliyuncs.com

# 2. 启用 CDN 加速下载

# 3. 本地缓存热点文件
# 实现 LRU 缓存层

# 4. 监控延迟指标
# 添加 metrics 埋点，观察 P99 延迟
```

### Q6: 如何在生产环境测试？

**A**: 灰度发布策略

```bash
# 1. 先在测试环境完整验证
# 2. 生产环境先对 10% 流量启用云存储
# 3. 观察 1 小时，无问题扩大到 50%
# 4. 再观察 1 小时，最后全量切换

# 实现方式：按 request_id hash 分流
if hash(request_id) % 100 < 10 {
    use_cloud_storage()
} else {
    use_local_storage()
}
```

---

## 总结

### 推荐配置

| 场景 | 存储类型 | 配置建议 |
|-----|---------|---------|
| 小规模（< 1GB） | 本地存储 | 简单，无需迁移 |
| 中规模（1-100GB） | 阿里云 OSS | 性价比高，迁移简单 |
| 大规模（> 100GB） | AWS S3 | 全球分发，高可用 |
| 私有化部署 | MinIO | 完全可控，兼容 S3 |

### 迁移时间估算

| 数据量 | 网络带宽 | 预计时间 |
|-------|---------|---------|
| 10 GB | 100 Mbps | 15 分钟 |
| 50 GB | 100 Mbps | 1.5 小时 |
| 100 GB | 100 Mbps | 3 小时 |
| 500 GB | 100 Mbps | 15 小时 |
| 1 TB | 1 Gbps | 3 小时 |

### 注意事项

1. **备份第一**：迁移前务必备份
2. **先测后迁**：测试环境先验证
3. **选择低峰**：凌晨 2-6 点流量最少
4. **监控告警**：设置关键指标告警
5. **保留本地**：云存储稳定运行 7 天后再删除本地文件

---

**相关文档**：
- [存储后端实现报告](../STORAGE-BACKEND-IMPLEMENTATION.md)
- [故障排查指南](./STORAGE-TROUBLESHOOTING.md)
- [配置参考](../README.md#存储配置)
