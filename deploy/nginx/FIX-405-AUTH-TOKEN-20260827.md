# 修复 llm.kxpms.cn 登录 405 错误 (2026-08-27)

## 问题描述

**错误现象**：
```
Request URL: https://llm.kxpms.cn/api/auth/token
Request Method: POST
Status Code: 405 Method Not Allow
```

## 根本原因

154 服务器上的 nginx 配置 `llm-kxpms-cn-154.conf.20260821-spa-fallback-only` 缺少通用 `/api/` 路由规则。

**问题分析**：
1. 配置中只有特定的 API 路由：
   - `/api/admin/live-stream` (WebSocket)
   - `/api/v1/ops/` (转发到 8443)
   
2. `/api/auth/token` 没有匹配的规则，被 SPA fallback（`location /`）拦截

3. SPA fallback 的 `try_files $uri $uri/ /index.html` 尝试返回静态文件，导致 POST 请求返回 405

## 修复方案

在 `/api/v1/ops/` 之后、SPA fallback 之前添加通用 `/api/` 路由，确保所有 API 请求都转发到后端服务。

**添加的路由块**：
- `location ^~ /api/` - 通用 API 路由（包括 `/api/auth/token` 等认证端点）
- `location ^~ /v1/` - OpenAI 兼容端点
- `location ^~ /v1beta/` - Beta 版本端点
- `location ^~ /v2/` - V2 版本端点
- `location = /metrics` - Prometheus 指标端点
- `location = /admin/config/reload` - 运维配置重载端点

**路由优先级**（nginx 匹配顺序）：
1. 精确匹配 `=` 优先级最高（如 `/healthz`, `/metrics`）
2. 正则匹配 `~` 次优先（如 `^/v1/(chat/completions|messages)`）
3. 前缀匹配 `^~` 再次优先（如 `/api/`, `/v1/`）
4. 普通前缀匹配（如 `/api/v1/ops/`）
5. SPA fallback `location /` 最后匹配

## 部署步骤

### 1. 备份当前配置
```bash
ssh root@<env:HOST_154_IP>
cd /etc/nginx/conf.d
cp llm.kxpms.cn.conf llm.kxpms.cn.conf.backup-20260827
```

### 2. 更新配置文件

将修复后的配置上传到服务器：
```bash
# 在本地执行
scp deploy/nginx/active-20260821/llm-kxpms-cn-154.conf.20260821-spa-fallback-only \
    root@<env:HOST_154_IP>:/etc/nginx/conf.d/llm.kxpms.cn.conf
```

### 3. 验证配置语法
```bash
ssh root@<env:HOST_154_IP> 'nginx -t'
```

预期输出：
```
nginx: the configuration file /etc/nginx/nginx.conf syntax is ok
nginx: configuration file /etc/nginx/nginx.conf test is successful
```

### 4. 重载 nginx
```bash
ssh root@<env:HOST_154_IP> 'systemctl reload nginx'
```

### 5. 验证修复

**测试登录接口**：
```bash
curl -X POST https://llm.kxpms.cn/api/auth/token \
  -H "Content-Type: application/json" \
  -d '{"username":"test","password":"test"}' \
  -v
```

预期：
- 不再返回 405
- 返回 401 Unauthorized（用户名密码错误）或 200 OK（登录成功）

**测试其他 API 端点**：
```bash
# 健康检查
curl https://llm.kxpms.cn/healthz

# 测试完整流程：浏览器访问
open https://llm.kxpms.cn
```

## 配置对比

**修复前**（缺失通用 /api/ 路由）：
```nginx
location /api/v1/ops/ { ... }
location / {  # SPA fallback 拦截所有未匹配路径
    try_files $uri $uri/ /index.html;
}
```

**修复后**（添加完整 API 路由）：
```nginx
location /api/v1/ops/ { ... }
location ^~ /api/ { proxy_pass http://llm_local; }
location ^~ /v1/ { proxy_pass http://llm_local; }
location ^~ /v1beta/ { proxy_pass http://llm_local; }
location ^~ /v2/ { proxy_pass http://llm_local; }
location = /metrics { proxy_pass http://llm_local; }
location = /admin/config/reload { proxy_pass http://llm_local; }
location / {  # SPA fallback 只处理前端路由
    try_files $uri $uri/ /index.html;
}
```

## 相关配置文件

- **154 (llm.kxpms.cn)**: `deploy/nginx/active-20260821/llm-kxpms-cn-154.conf.20260821-spa-fallback-only`
- **245 (llmgo.kxpms.cn)**: `deploy/llmgo-245.nginx.conf` (已包含完整路由)
- **252 (kxpms.cn)**: `deploy/nginx/active-20260821/kxpms-on-252.conf.20260821-spa-fallback-only`

## 注意事项

1. **维护模式兼容性**：所有新增的 API 路由都包含 `UPGRADING` 标记检查，在升级期间返回 503
2. **超时配置**：API 路由使用 1200s 读写超时，支持长时间运行的请求
3. **缓冲禁用**：所有 API 路由禁用 `proxy_buffering` 和 `proxy_cache`，确保流式响应正常工作
4. **WebSocket 路由**：`/api/admin/live-stream` 的精确匹配优先级高于通用 `/api/`，保持 WebSocket 配置不受影响

## 回滚方案

如果出现问题，快速回滚：
```bash
ssh root@<env:HOST_154_IP> 'cp /etc/nginx/conf.d/llm.kxpms.cn.conf.backup-20260827 /etc/nginx/conf.d/llm.kxpms.cn.conf && nginx -t && systemctl reload nginx'
```

## 验收标准

- [x] 配置文件已更新
- [ ] nginx 语法验证通过
- [ ] nginx 重载成功
- [ ] `/api/auth/token` POST 请求不再返回 405
- [ ] 用户可以正常登录
- [ ] WebSocket 连接正常（`/api/admin/live-stream`）
- [ ] OpenAI API 端点正常（`/v1/chat/completions`）
- [ ] 静态资源加载正常（前端页面）
- [ ] SPA 路由正常（如 `/admin/turns`）

## 相关 Commit

- 修复配置：`fix(nginx): add missing /api/ routes for llm.kxpms.cn to resolve 405 on /api/auth/token`
- 问题追溯：该问题在 2026-08-21 SPA fallback 重构时引入，当时只测试了前端路由，未覆盖所有 API 端点
