# 附件存储故障排查指南

**版本**: v1.0  
**日期**: 2026-07-02  
**维护**: 运维团队

---

## 目录

- [快速诊断](#快速诊断)
- [常见问题](#常见问题)
- [错误码参考](#错误码参考)
- [日志分析](#日志分析)
- [性能问题](#性能问题)
- [监控指标](#监控指标)
- [紧急恢复](#紧急恢复)

---

## 快速诊断

### 健康检查

```bash
# 1. 检查存储后端健康状态
curl -s http://localhost:8781/api/admin/storage/health | jq .

# 正常输出：
{
  "healthy": true,
  "backend_type": "oss",
  "location": "oss-cn-hangzhou.aliyuncs.com",
  "metadata": {
    "bucket": "llm-gateway-attachments"
  }
}

# 异常输出示例：
{
  "healthy": false,
  "backend_type": "oss",
  "error": "oss storage: bucket not accessible: connection timeout"
}
```

### 服务日志

```bash
# 查看最近 100 行日志
sudo journalctl -u llm-gateway -n 100

# 实时跟踪日志
sudo journalctl -u llm-gateway -f

# 过滤存储相关错误
sudo journalctl -u llm-gateway | grep -i "storage\|attachment\|oss\|s3"
```

### 配置检查

```bash
# 查看当前存储配置
psql -U postgres -d llm_gateway -c "
SELECT key, value 
FROM settings_kv 
WHERE category = 'storage' 
ORDER BY key;
"

# 应该看到类似：
#           key           |               value
# ------------------------+------------------------------------
#  type                   | "oss"
#  oss.endpoint           | "oss-cn-hangzhou.aliyuncs.com"
#  oss.bucket             | "llm-gateway-attachments"
#  oss.access_key_id      | "LTAI5t..."
#  oss.access_key_secret  | "xxxxxxxxxxxxx"
```

---

## 常见问题

### 问题 1: 附件上传失败

**症状**：
- 用户报告图片上传失败
- 日志显示 "attachment save failed"
- 聊天请求返回 500 错误

**诊断步骤**：

```bash
# 1. 检查健康状态
curl http://localhost:8781/api/admin/storage/health

# 2. 检查日志错误
sudo journalctl -u llm-gateway | grep "attachment save failed" -A 5

# 3. 手动测试上传
curl -X POST http://localhost:8781/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{
      "role": "user",
      "content": [
        {"type": "text", "text": "测试"},
        {"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KG..."}}
      ]
    }]
  }'
```

**常见原因及解决方案**：

#### 1.1 本地存储：目录权限问题

```bash
# 检查目录权限
ls -ld /data/attachments
# 应该是：drwxr-xr-x llm-gateway llm-gateway

# 修复权限
sudo chown -R llm-gateway:llm-gateway /data/attachments
sudo chmod -R 755 /data/attachments

# 测试写入
sudo -u llm-gateway touch /data/attachments/.test
sudo -u llm-gateway rm /data/attachments/.test
```

#### 1.2 本地存储：磁盘空间不足

```bash
# 检查磁盘空间
df -h /data/attachments
# 如果 Use% > 90%，需要清理

# 查看占用最大的目录
du -h --max-depth=2 /data/attachments | sort -hr | head -20

# 清理过期附件（保留最近 90 天）
find /data/attachments -type f -mtime +90 -delete
```

#### 1.3 OSS: 认证失败

```bash
# 错误日志示例：
# oss storage: put object: AccessDenied

# 验证 AccessKey
# 1. 登录阿里云控制台
# 2. RAM 访问控制 → 用户 → 查看权限
# 3. 确认有 AliyunOSSFullAccess 或至少 PutObject 权限

# 测试 AccessKey（使用 ossutil）
ossutil config \
  -e oss-cn-hangzhou.aliyuncs.com \
  -i LTAI5t... \
  -k xxxxxxxxxxxxx

ossutil ls oss://llm-gateway-attachments/
# 如果能列出文件，说明 AK 正常
```

#### 1.4 OSS: 网络不通

```bash
# Ping OSS endpoint
ping oss-cn-hangzhou.aliyuncs.com
# 如果 ping 不通，检查网络/防火墙

# Telnet 测试端口
telnet oss-cn-hangzhou.aliyuncs.com 443
# 应该连接成功

# 使用内网 endpoint（如果是阿里云 ECS）
# 修改配置：oss-cn-hangzhou-internal.aliyuncs.com
```

#### 1.5 OSS: Bucket 不存在或无权限

```bash
# 错误日志示例：
# oss storage: bucket not accessible: NoSuchBucket

# 检查 Bucket 是否存在
ossutil ls oss://llm-gateway-attachments/

# 如果不存在，创建 Bucket
ossutil mb oss://llm-gateway-attachments/

# 检查 Bucket ACL
ossutil stat oss://llm-gateway-attachments/
```

#### 1.6 S3: 签名错误

```bash
# 错误日志示例：
# s3 storage: put object: SignatureDoesNotMatch

# 常见原因：
# 1. 时间不同步
sudo ntpdate -u pool.ntp.org

# 2. Region 配置错误
# 确认 Bucket 的实际 Region
aws s3api get-bucket-location --bucket llm-gateway-attachments

# 3. Endpoint 配置错误（MinIO）
# MinIO 必须使用 Path-style，确保配置中设置了正确的 endpoint
```

#### 1.7 文件大小超限

```bash
# 错误日志示例：
# attachment too large: 25165824 bytes > 20971520 limit

# 检查当前限制
curl http://localhost:8781/api/admin/storage/config | jq .max_file_size

# 调整限制（20MB → 50MB）
psql -U postgres -d llm_gateway -c "
UPDATE settings_kv 
SET value = '52428800' 
WHERE category = 'storage' AND key = 'max_file_size';
"

# 或使用环境变量
export LLM_GATEWAY_ATTACHMENT_MAX_SIZE=52428800
sudo systemctl restart llm-gateway
```

---

### 问题 2: 附件下载失败

**症状**：
- 下载附件返回 404 或 500
- 聊天记录中图片无法显示

**诊断步骤**：

```bash
# 1. 查看请求日志
sudo journalctl -u llm-gateway | grep "GET.*attachments" -A 3

# 2. 检查文件是否存在
# 本地存储：
ls -lh /data/attachments/2024/01/req_abc123/hash.png

# OSS：
ossutil stat oss://llm-gateway-attachments/2024/01/req_abc123/hash.png

# S3：
aws s3 ls s3://llm-gateway-attachments/2024/01/req_abc123/hash.png
```

**解决方案**：

#### 2.1 文件路径不正确

```bash
# 检查实际存储的路径格式
# 应该是：YYYY/MM/req_xxxxx/hash.ext

# 如果路径格式不对，可能是旧版本遗留问题
# 需要手动迁移或调整路径解析逻辑
```

#### 2.2 OSS/S3 签名 URL 过期

```bash
# 如果使用预签名 URL 下载，检查过期时间
# 默认 15 分钟，可以调整

# 在代码中增加过期时间（暂无配置项，需修改代码）
```

---

### 问题 3: 性能下降

**症状**：
- 上传/下载速度变慢
- API 响应时间增加
- P99 延迟超过 1s

**诊断步骤**：

```bash
# 1. 监控指标
curl http://localhost:8781/metrics | grep storage

# 2. 测试上传速度
time curl -X POST http://localhost:8781/v1/chat/completions \
  -H "Authorization: Bearer test-key" \
  -d @test-large-image.json

# 3. 测试下载速度
time curl http://localhost:8781/api/admin/attachments/2024/01/req_abc/hash.png \
  -o /dev/null
```

**解决方案**：

#### 3.1 网络延迟高

```bash
# 测试到 OSS 的延迟
ping -c 10 oss-cn-hangzhou.aliyuncs.com

# 如果延迟 > 50ms：
# 1. 使用内网 endpoint（阿里云 ECS）
# 2. 选择就近的 Region
# 3. 启用 CDN 加速
```

#### 3.2 并发数不足

```bash
# 检查当前并发连接数
netstat -an | grep ESTABLISHED | grep ":443" | wc -l

# OSS/S3 SDK 默认连接池较小，可以调整
# （需要修改代码，增加连接池配置）
```

#### 3.3 大文件传输慢

```bash
# 对于 > 5MB 的文件，使用分块上传
# （当前实现是全量上传，可优化为分块上传）

# 临时方案：增加超时时间
# 修改 HTTP 客户端超时配置
```

---

### 问题 4: 数据不一致

**症状**：
- 本地有文件，云端没有（或相反）
- 文件大小不一致
- MD5/SHA256 不匹配

**诊断步骤**：

```bash
# 1. 对比文件列表
# 本地
find /data/attachments -type f > local-files.txt

# OSS
ossutil ls oss://llm-gateway-attachments/ -r > oss-files.txt

# 比较差异
diff local-files.txt oss-files.txt

# 2. 对比文件大小
stat /data/attachments/2024/01/req_abc/hash.png
ossutil stat oss://llm-gateway-attachments/2024/01/req_abc/hash.png

# 3. 对比文件内容
md5sum /data/attachments/2024/01/req_abc/hash.png
# 与云端文件的 ETag 比较（注意：ETag 不一定是 MD5）
```

**解决方案**：

#### 4.1 迁移不完整

```bash
# 重新运行迁移工具（跳过已存在）
./storage-migrate ... --skip-exists=true
```

#### 4.2 双写失败

```bash
# 如果实现了双写，检查异步写入日志
sudo journalctl -u llm-gateway | grep "secondary write failed"

# 手动补齐缺失文件
```

---

## 错误码参考

### HTTP 状态码

| 状态码 | 含义 | 常见原因 |
|-------|------|---------|
| 400 | Bad Request | 请求格式错误、文件格式不支持 |
| 403 | Forbidden | 权限不足、Bucket ACL 限制 |
| 404 | Not Found | 文件不存在、路径错误 |
| 413 | Payload Too Large | 文件超过大小限制 |
| 500 | Internal Server Error | 存储后端异常、网络错误 |
| 503 | Service Unavailable | 存储后端不可用、健康检查失败 |

### 存储后端错误

#### 本地存储

| 错误信息 | 原因 | 解决方案 |
|---------|------|---------|
| `permission denied` | 目录权限不足 | `chmod 755` + `chown` |
| `no space left on device` | 磁盘满 | 清理空间或扩容 |
| `path escapes base dir` | 路径遍历攻击 | 检查客户端请求 |

#### 阿里云 OSS

| 错误码 | 含义 | 解决方案 |
|-------|------|---------|
| `AccessDenied` | 无权限 | 检查 RAM 权限策略 |
| `NoSuchBucket` | Bucket 不存在 | 创建 Bucket 或修正配置 |
| `InvalidAccessKeyId` | AK 错误 | 检查 AccessKey 是否正确 |
| `SignatureDoesNotMatch` | 签名错误 | 检查 SK、时间同步 |
| `RequestTimeout` | 超时 | 检查网络、增加超时时间 |

#### AWS S3

| 错误码 | 含义 | 解决方案 |
|-------|------|---------|
| `NoSuchBucket` | Bucket 不存在 | 创建 Bucket 或修正 Region |
| `InvalidAccessKeyId` | AK 错误 | 检查 IAM 凭证 |
| `SignatureDoesNotMatch` | 签名错误 | 检查 SK、Region、时间同步 |
| `NoSuchKey` | 对象不存在 | 检查文件路径 |

---

## 日志分析

### 关键日志

```bash
# 1. 存储初始化日志
sudo journalctl -u llm-gateway | grep "storage backend"
# 期望：INFO using OSS storage backend

# 2. 健康检查日志
sudo journalctl -u llm-gateway | grep "storage health"
# 如果频繁出现 WARN，说明后端不稳定

# 3. 上传失败日志
sudo journalctl -u llm-gateway | grep "attachment save failed"

# 4. 下载失败日志
sudo journalctl -u llm-gateway | grep "attachment load failed"
```

### 日志级别调整

```bash
# 临时开启 DEBUG 日志
export LLM_GATEWAY_LOG_LEVEL=debug
sudo systemctl restart llm-gateway

# 持久化配置
psql -U postgres -d llm_gateway -c "
UPDATE settings_kv 
SET value = '\"debug\"' 
WHERE category = 'system' AND key = 'log_level';
"
```

### 结构化日志查询

```bash
# 使用 jq 分析 JSON 日志（如果启用了 JSON 格式）
sudo journalctl -u llm-gateway -o json | jq 'select(.msg | contains("storage"))'

# 统计错误类型
sudo journalctl -u llm-gateway | grep "ERROR" | awk '{print $NF}' | sort | uniq -c | sort -rn
```

---

## 性能问题

### 性能指标

```bash
# 1. 上传延迟（P50/P95/P99）
curl http://localhost:8781/metrics | grep 'storage_operation_duration_seconds{op="save"}'

# 2. 下载延迟
curl http://localhost:8781/metrics | grep 'storage_operation_duration_seconds{op="load"}'

# 3. 错误率
curl http://localhost:8781/metrics | grep 'storage_operation_errors_total'

# 4. 吞吐量
curl http://localhost:8781/metrics | grep 'storage_operation_total'
```

### 性能优化

#### 优化 1: 使用内网 endpoint

```sql
-- 阿里云 ECS 访问 OSS，使用内网 endpoint 可降低延迟 50%+
UPDATE settings_kv 
SET value = '"oss-cn-hangzhou-internal.aliyuncs.com"' 
WHERE category = 'storage' AND key = 'oss.endpoint';
```

#### 优化 2: 增加连接池

```go
// 在 OSS/S3 客户端初始化时增加连接池
// （需要修改代码）
client, err := oss.New(endpoint, ak, sk, 
    oss.HTTPClient(&http.Client{
        Transport: &http.Transport{
            MaxIdleConns:        100,
            MaxIdleConnsPerHost: 20,
        },
    }),
)
```

#### 优化 3: 启用本地缓存

```go
// 实现 LRU 缓存层（需要开发）
type CachedBackend struct {
    backend StorageBackend
    cache   *lru.Cache
}
```

#### 优化 4: CDN 加速

```bash
# 为 OSS Bucket 配置 CDN
# 1. 阿里云控制台 → CDN
# 2. 添加域名：cdn.example.com
# 3. 源站：llm-gateway-attachments.oss-cn-hangzhou.aliyuncs.com
# 4. 配置 CNAME 解析
```

---

## 监控指标

### Prometheus Metrics

```prometheus
# 操作计数
storage_operation_total{op="save",backend="oss",success="true"} 12345

# 操作延迟（秒）
storage_operation_duration_seconds{op="save",backend="oss",quantile="0.5"} 0.05
storage_operation_duration_seconds{op="save",backend="oss",quantile="0.95"} 0.15
storage_operation_duration_seconds{op="save",backend="oss",quantile="0.99"} 0.30

# 错误计数
storage_operation_errors_total{op="save",backend="oss"} 3

# 传输字节数
storage_bytes_transferred_total{op="save",backend="oss"} 1073741824

# 健康检查
storage_health_check_total{backend="oss",success="true"} 100
```

### 告警规则

```yaml
groups:
  - name: storage
    rules:
      # 健康检查失败告警
      - alert: StorageUnhealthy
        expr: storage_health_check_total{success="false"} > 0
        for: 5m
        annotations:
          summary: "存储后端不健康"
          description: "{{ $labels.backend }} 健康检查失败"

      # 高错误率告警
      - alert: StorageHighErrorRate
        expr: |
          rate(storage_operation_errors_total[5m]) 
          / rate(storage_operation_total[5m]) > 0.05
        for: 5m
        annotations:
          summary: "存储错误率过高"
          description: "{{ $labels.backend }} 错误率 > 5%"

      # 高延迟告警
      - alert: StorageHighLatency
        expr: |
          storage_operation_duration_seconds{quantile="0.99"} > 1.0
        for: 10m
        annotations:
          summary: "存储延迟过高"
          description: "{{ $labels.op }} P99 延迟 > 1s"
```

---

## 紧急恢复

### 场景 1: 云存储完全不可用

```bash
# 1. 立即切换回本地存储
psql -U postgres -d llm_gateway -c "
UPDATE settings_kv SET value = '\"local\"' 
WHERE category = 'storage' AND key = 'type';
"

# 2. 重启服务
sudo systemctl restart llm-gateway

# 3. 从备份恢复本地文件（如果已清理）
rsync -avz backup-server:/backups/attachments/ /data/attachments/
```

### 场景 2: 部分文件损坏

```bash
# 1. 从迁移报告中找到损坏文件
grep "FAIL" migration-report.txt

# 2. 从备份恢复单个文件
scp backup-server:/backups/attachments/2024/01/req_abc/hash.png \
    /data/attachments/2024/01/req_abc/

# 3. 重新上传到云存储
./storage-migrate --source-type=local --source-dir=/data/attachments/2024/01/req_abc ...
```

### 场景 3: 配置错误导致服务无法启动

```bash
# 1. 回滚配置到上一个版本
psql -U postgres -d llm_gateway -c "
SELECT * FROM settings_kv 
WHERE category = 'storage' 
ORDER BY updated_at DESC 
LIMIT 20;
"

# 找到正确的配置，手动恢复

# 2. 或者删除错误配置，使用默认本地存储
psql -U postgres -d llm_gateway -c "
DELETE FROM settings_kv 
WHERE category = 'storage' AND key = 'type';
"

# 3. 重启服务
sudo systemctl restart llm-gateway
```

---

## 联系支持

### 提供以下信息

1. **环境信息**
   - 服务版本：`llm-gateway --version`
   - 操作系统：`uname -a`
   - 存储类型：`curl http://localhost:8781/api/admin/storage/health`

2. **错误日志**
   ```bash
   sudo journalctl -u llm-gateway --since "1 hour ago" > logs.txt
   ```

3. **配置信息**（脱敏后）
   ```bash
   psql -U postgres -d llm_gateway -c "
   SELECT key, 
          CASE WHEN key LIKE '%secret%' OR key LIKE '%password%' 
               THEN '***' 
               ELSE value 
          END as value
   FROM settings_kv 
   WHERE category = 'storage';
   " > config.txt
   ```

4. **复现步骤**
   - 请求示例（curl 命令）
   - 预期结果 vs 实际结果

---

**相关文档**：
- [存储后端实现报告](../STORAGE-BACKEND-IMPLEMENTATION.md)
- [迁移指南](./STORAGE-MIGRATION-GUIDE.md)
- [配置参考](../README.md#存储配置)
