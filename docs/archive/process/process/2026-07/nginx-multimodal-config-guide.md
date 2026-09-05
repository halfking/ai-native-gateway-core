# Nginx 多模态请求配置指南

**版本**: 1.0  
**日期**: 2026-07-25  
**适用场景**: 支持多模态 AI 请求（图片、文档上传）

---

## 背景

随着多模态 AI 能力的普及，用户需要上传大型文件（图片、文档、音频）到 LLM Gateway：

- **文本 + 图片**：典型请求体 1-5 MB
- **多图场景**：可能达到 10-50 MB
- **文档分析**：PDF/Word 文档可能达到 64-256 MB
- **音频转录**：长音频文件可能超过 100 MB

**默认 Nginx 限制**: `client_max_body_size 1m`（仅 1MB）会导致 413 错误。

---

## 核心配置要求

### 1. `client_max_body_size`

**推荐值**: `256m`

```nginx
server {
    listen 443 ssl http2;
    server_name llm.kxpms.cn;
    
    # 关键配置：支持多模态大文件上传
    client_max_body_size 256m;
    
    # ... 其他配置
}
```

**说明**:
- `256m` 覆盖 99% 的多模态场景
- 对于专用文件上传域名（如 files.kxpms.cn），可配置 `1024m`
- 此配置应放在 `server {}` 块顶部，全局生效

### 2. Proxy Buffer 配置

对于流式响应（SSE），需要关闭缓冲：

```nginx
location ~ ^/v1/(chat/completions|messages)$ {
    proxy_pass http://llmgo_backend;
    
    # 流式响应必须关闭缓冲
    proxy_buffering off;
    proxy_cache off;
    
    # 超时配置（支持长时间推理）
    proxy_read_timeout 3600s;
    proxy_send_timeout 3600s;
    proxy_connect_timeout 60s;
    
    # 请求体大小限制继承 server 级别的 256m
    # 如果需要覆盖，在此处显式声明
    # client_max_body_size 256m;
}
```

### 3. 普通 API 路径

```nginx
location /api/ {
    proxy_pass http://llmgo_backend;
    
    # 标准超时
    proxy_read_timeout 120s;
    proxy_send_timeout 120s;
    proxy_connect_timeout 30s;
    
    # 请求体限制继承 server 级别
}
```

---

## 部署配置检查清单

### 所有服务器必检项

| 服务器 | 域名 | 配置文件路径 | 要求 |
|--------|------|-------------|------|
| 154 (生产) | llm.kxpms.cn | `/etc/nginx/conf.d/llm-kxpms-cn.conf` | `client_max_body_size 256m` |
| 252 (开发) | 内部访问 | `/etc/nginx/conf.d/kxpms-on-252.conf` | `client_max_body_size 256m` |
| 245 (预发) | llmgo.kxpms.cn | `/etc/nginx/conf.d/llmgo-245.conf` | `client_max_body_size 256m` ✅ |
| 252 (文件) | files.kxpms.cn | `/etc/nginx/conf.d/files-kxpms-cn.conf` | `client_max_body_size 1024m` ✅ |

### 检查命令

```bash
# 1. SSH 到目标服务器
ssh user@server_ip

# 2. 检查当前 Nginx 配置
sudo nginx -T 2>&1 | grep -A 5 "server_name.*llm.kxpms.cn"

# 3. 查找 client_max_body_size 设置
sudo nginx -T 2>&1 | grep "client_max_body_size"

# 4. 验证配置语法
sudo nginx -t

# 5. 重载配置（无需重启）
sudo nginx -s reload
```

---

## 标准配置模板

### 生产环境 (154)

```nginx
# /etc/nginx/conf.d/llm-kxpms-cn.conf
upstream llmgo_backend {
    server 127.0.0.1:8781 max_fails=3 fail_timeout=10s;
    keepalive 32;
}

server {
    listen 443 ssl http2;
    server_name llm.kxpms.cn;
    
    # SSL 证书
    ssl_certificate /etc/letsencrypt/live/kxpms.cn/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/kxpms.cn/privkey.pem;
    
    # 多模态支持：256MB 请求体限制
    client_max_body_size 256m;
    
    # 安全头
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-XSS-Protection "1; mode=block" always;
    
    # 流式 API 端点
    location ~ ^/v1/(chat/completions|messages)$ {
        proxy_pass http://llmgo_backend;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        
        # 关闭缓冲以支持 SSE
        proxy_buffering off;
        proxy_cache off;
        
        # 超时配置
        proxy_connect_timeout 60s;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
    
    # 管理 API
    location /api/ {
        proxy_pass http://llmgo_backend;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        
        proxy_connect_timeout 30s;
        proxy_read_timeout 120s;
        proxy_send_timeout 120s;
    }
    
    # 静态资源
    location / {
        proxy_pass http://llmgo_backend;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}

# HTTP 重定向到 HTTPS
server {
    listen 80;
    server_name llm.kxpms.cn;
    return 301 https://$server_name$request_uri;
}
```

---

## 部署脚本

### 自动化部署脚本

创建 `deploy/scripts/update-nginx-multimodal.sh`:

```bash
#!/bin/bash
# 更新 Nginx 配置以支持多模态大请求
# 使用方法: ./update-nginx-multimodal.sh <server_ip> <config_file>

set -e

SERVER_IP="${1:-154}"
CONFIG_FILE="${2:-/etc/nginx/conf.d/llm-kxpms-cn.conf}"
BACKUP_DIR="/etc/nginx/conf.d/backups"

echo "📦 更新 Nginx 多模态配置"
echo "服务器: $SERVER_IP"
echo "配置文件: $CONFIG_FILE"
echo ""

# 1. 备份现有配置
echo "1️⃣ 备份现有配置..."
ssh root@$SERVER_IP "mkdir -p $BACKUP_DIR && \
  cp $CONFIG_FILE $BACKUP_DIR/$(basename $CONFIG_FILE).$(date +%Y%m%d_%H%M%S).bak"

# 2. 检查是否已有 client_max_body_size 配置
echo "2️⃣ 检查现有配置..."
EXISTING=$(ssh root@$SERVER_IP "grep -c 'client_max_body_size' $CONFIG_FILE || true")

if [ "$EXISTING" -gt 0 ]; then
  echo "✅ 已存在 client_max_body_size 配置"
  ssh root@$SERVER_IP "grep 'client_max_body_size' $CONFIG_FILE"
  
  read -p "是否需要更新？(y/N) " -n 1 -r
  echo
  if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "跳过更新"
    exit 0
  fi
fi

# 3. 更新配置（在 server 块的 ssl 配置后插入）
echo "3️⃣ 更新配置..."
ssh root@$SERVER_IP "sed -i '/ssl_certificate_key/a \    \n    # 多模态支持：256MB 请求体限制 (2026-07-25)\n    client_max_body_size 256m;' $CONFIG_FILE"

# 4. 测试配置
echo "4️⃣ 测试 Nginx 配置..."
ssh root@$SERVER_IP "nginx -t"

# 5. 重载 Nginx
echo "5️⃣ 重载 Nginx..."
ssh root@$SERVER_IP "nginx -s reload"

echo ""
echo "✅ 配置更新完成！"
echo ""
echo "验证配置:"
echo "  ssh root@$SERVER_IP 'nginx -T 2>&1 | grep client_max_body_size'"
```

---

## 验证测试

### 1. 配置验证

```bash
# 登录到服务器
ssh root@154

# 查看生效的配置
nginx -T 2>&1 | grep -A 3 "server_name.*llm.kxpms.cn" | grep "client_max_body_size"

# 预期输出:
# client_max_body_size 256m;
```

### 2. 功能测试

使用 curl 测试大请求：

```bash
# 生成 5MB 测试文件
dd if=/dev/urandom of=/tmp/test_5mb.json bs=1M count=5

# 测试上传（替换为实际 API key）
curl -X POST https://llm.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer sk-xxx" \
  -H "Content-Type: application/json" \
  --data-binary @/tmp/test_5mb.json \
  -w "\nHTTP Status: %{http_code}\n"

# 预期结果：
# - 200/400 (业务逻辑错误，但不是 413)
# - 如果返回 413 Request Entity Too Large，说明配置未生效
```

### 3. 多模态真实测试

```bash
# 测试带图片的请求（base64 编码）
curl -X POST https://llm.kxpms.cn/v1/chat/completions \
  -H "Authorization: Bearer sk-xxx" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [
      {
        "role": "user",
        "content": [
          {"type": "text", "text": "这张图片里有什么？"},
          {"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,..."}}
        ]
      }
    ]
  }'
```

---

## 故障排查

### 问题 1: 413 Request Entity Too Large

**原因**: Nginx `client_max_body_size` 限制

**解决方案**:
```bash
# 1. 确认配置已添加
sudo nginx -T 2>&1 | grep "client_max_body_size"

# 2. 如果没有，手动添加
sudo vim /etc/nginx/conf.d/llm-kxpms-cn.conf
# 在 server 块中添加: client_max_body_size 256m;

# 3. 测试并重载
sudo nginx -t && sudo nginx -s reload
```

### 问题 2: 配置未生效

**原因**: 可能有多个配置文件，优先级问题

**解决方案**:
```bash
# 查看所有配置文件
sudo nginx -T 2>&1 | less

# 检查是否有其他位置覆盖了设置
sudo grep -r "client_max_body_size" /etc/nginx/

# 确保在正确的 server 块中设置
```

### 问题 3: Go handler 仍然限制

**原因**: Go 代码中的 `maxBodySize` 硬编码

**当前状态**: 
- handler.go:57 设置为 128MB（足够多数场景）
- 如需更大，修改 `maxBodySize` 常量

---

## 监控建议

### 1. Nginx 日志监控

```bash
# 监控 413 错误
tail -f /var/log/nginx/error.log | grep "413"

# 统计大请求
awk '$10 > 10000000 {print $10/1048576 "MB", $7}' /var/log/nginx/access.log | sort -rn | head -20
```

### 2. 看板监控

访问 LLM Gateway 管理面板查看实时统计：
- **平均请求体大小**
- **峰值请求体大小**
- **平均响应体大小**
- **峰值响应体大小**

这些指标在首页看板实时展示（已在 2026-07-25 实现）。

---

## 总结

**关键配置项**:
```nginx
client_max_body_size 256m;  # server 级别
proxy_buffering off;         # SSE 端点
proxy_read_timeout 3600s;    # 长时间推理
```

**部署顺序**:
1. ✅ 备份现有配置
2. ✅ 添加 `client_max_body_size 256m`
3. ✅ 测试配置 (`nginx -t`)
4. ✅ 重载 Nginx (`nginx -s reload`)
5. ✅ 功能验证（curl 测试）

**参考**:
- Nginx 官方文档: http://nginx.org/en/docs/http/ngx_http_core_module.html#client_max_body_size
- LLM Gateway 看板统计: https://llm.kxpms.cn/dashboard
