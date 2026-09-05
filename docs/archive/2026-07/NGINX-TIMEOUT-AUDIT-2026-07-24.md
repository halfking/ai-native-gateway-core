---
archived_from: (legacy) docs/archive/2026-07/NGINX-TIMEOUT-AUDIT-2026-07-24.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190918
status: archived
note: legacy archive, frontmatter retroactively added
---

# Nginx 超时配置审查报告

## 🔍 当前配置状态

### 252 服务器 ⚠️ **需要修复**
**配置文件**: `/etc/nginx/conf.d/kxpms-on-252.conf`
**问题**: `/api/v1/` location 超时太短

```nginx
# NPS 端口 9443 (主入口) - llm.kxpms.cn
server {
    listen 9443 ssl http2 proxy_protocol;
    server_name llm.kxpms.cn;
    
    # ❌ 问题：/api/v1/ 只有 120 秒超时
    location ^~ /api/v1/ {
        proxy_pass http://kxpms_license_authority;
        proxy_connect_timeout 30s;
        proxy_read_timeout 120s;   # ⚠️ 太短！
        proxy_send_timeout 120s;   # ⚠️ 太短！
    }
}
```

**影响**: 如果请求被路由到 license-authority 接口，超过 120 秒会被断开。

---

### 154 服务器 ✅ **已经正确配置**
**配置文件**: 154 上的配置

```nginx
location ~ ^/v1/(chat/completions|messages) {
    proxy_pass http://llm_local;
    proxy_connect_timeout 60s;
    proxy_send_timeout 1200s;   # ✅ 20 分钟
    proxy_read_timeout 3600s;   # ✅ 60 分钟
    proxy_buffering off;
}
```

**状态**: ✅ 非常好！足够处理长时间的 reasoning 模型

---

### 245 服务器 ✅ **已经正确配置**
**配置文件**: 245 上的配置

```nginx
location ~ ^/v1/(chat/completions|messages) {
    proxy_pass http://llm_local_245;
    proxy_connect_timeout 60s;
    proxy_send_timeout 1200s;   # ✅ 20 分钟
    proxy_read_timeout 3600s;   # ✅ 60 分钟
    proxy_buffering off;
}
```

**状态**: ✅ 配置正确，与 154 一致

---

## 🎯 需要修复的问题

### 问题：252 服务器的 `/api/v1/` 超时配置

**当前值**: 
- `proxy_read_timeout 120s`
- `proxy_send_timeout 120s`

**建议值**:
- `proxy_read_timeout 300s` (5分钟，与 Gateway 的 ReadTimeout 一致)
- `proxy_send_timeout 300s`

**原因**: 
1. license-authority 可能需要更长的处理时间
2. 保持与其他超时配置一致
3. 避免不必要的连接断开

---

## 🔧 修复方案

### 方案 1：仅修复 license-authority 路径（推荐）

```bash
ssh 252 "sudo sed -i.bak-timeout-$(date +%Y%m%d) \
    -e 's/proxy_read_timeout 120s;/proxy_read_timeout 300s;/g' \
    -e 's/proxy_send_timeout 120s;/proxy_send_timeout 300s;/g' \
    /etc/nginx/conf.d/kxpms-on-252.conf && \
    sudo nginx -t && sudo systemctl reload nginx"
```

### 方案 2：手动修改（更安全）

```bash
ssh 252
sudo vim /etc/nginx/conf.d/kxpms-on-252.conf

# 找到这个 location 块：
location ^~ /api/v1/ {
    proxy_pass http://kxpms_license_authority;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_connect_timeout 30s;
    proxy_read_timeout 120s;   # 改成 300s
    proxy_send_timeout 120s;   # 改成 300s
    proxy_buffering off;
}

# 保存后测试并重载
sudo nginx -t
sudo systemctl reload nginx
```

---

## ✅ 验证修复

修复后运行：

```bash
ssh 252 "nginx -T 2>&1 | grep -A 10 'location.*\/api\/v1'"
```

应该看到：
```nginx
proxy_read_timeout 300s;
proxy_send_timeout 300s;
```

---

## 📊 修复后的完整超时配置

### Gateway 服务器超时（Go 代码）
- `ReadTimeout: 300s`
- `WriteTimeout: 0` (无限制)
- `IdleTimeout: 300s`

### Nginx 代理超时
| 服务器 | 路径 | connect | send | read | 状态 |
|--------|------|---------|------|------|------|
| 252 | `/v1/chat/completions` | N/A | N/A | N/A | 直接转发到 NPS |
| 252 | `/api/v1/` | 30s | 120s→300s | 120s→300s | ⚠️ 待修复 |
| 154 | `/v1/chat/completions` | 60s | 1200s | 3600s | ✅ 正常 |
| 245 | `/v1/chat/completions` | 60s | 1200s | 3600s | ✅ 正常 |

---

## 🤔 关于 Copilot 请求的新发现

**重要**: 经过深入分析，**Copilot 请求不太可能是 Nginx 超时导致的**！

**原因**:
1. ✅ 154 和 245 的 `/v1/chat/completions` 路径已经有 3600 秒超时
2. ✅ Gateway 本身 `WriteTimeout=0`（无限制）
3. ⚠️ 252 的问题路径是 `/api/v1/`（license-authority），不是 `/v1/chat/completions`

**新假设**: 
- **可能是 NPS 隧道层的超时**
- 252 → NPS (9443端口) → 154/245
- 需要检查 NPS 的配置

---

## 📝 建议行动

### 立即执行
1. ✅ 修复 252 的 `/api/v1/` 超时（预防性修复）
2. 🔍 检查 NPS 配置的超时设置
3. 📊 收集实际的 Copilot 请求日志（修复后的完整日志）

### 等待验证
- 部署日志修复后，等待下一次 Copilot 请求
- 查看 `latency_ms` 和 `request_preview`
- 确认真正的超时原因

---

**创建时间**: 2026-07-24  
**状态**: 发现 154 和 245 已经正确配置，252 的 `/api/v1/` 需要修复
