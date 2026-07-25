# Nginx 多模态配置脚本

本目录包含用于配置 Nginx 支持多模态大请求（256MB）的自动化脚本。

## 快速开始

### 使用场景

当用户上传大型文件到 LLM Gateway 时（图片、文档、音频），需要确保 Nginx 配置允许大请求体：

- 文本 + 图片: 1-5 MB
- 多图场景: 10-50 MB
- 文档分析: 64-256 MB
- 音频文件: 100+ MB

### 自动化脚本

```bash
# 更新生产环境 (154)
./update-nginx-multimodal.sh 154 /etc/nginx/conf.d/llm-kxpms-cn.conf

# 更新开发环境 (252)
./update-nginx-multimodal.sh 252 /etc/nginx/conf.d/kxpms-on-252.conf

# 更新预发环境 (245)
./update-nginx-multimodal.sh 245 /etc/nginx/conf.d/llmgo-245.conf
```

### 手动配置

如果自动化脚本失败，可以手动编辑 Nginx 配置：

```nginx
server {
    listen 443 ssl http2;
    server_name llm.kxpms.cn;
    
    # 添加这一行
    client_max_body_size 256m;
    
    # ... 其他配置
}
```

然后测试并重载：

```bash
sudo nginx -t
sudo nginx -s reload
```

## 验证

```bash
# 检查配置
ssh root@154 "nginx -T 2>&1 | grep client_max_body_size"

# 预期输出
# client_max_body_size 256m;
```

## 详细文档

完整的配置指南、故障排查和最佳实践，请查看：

📖 **[Nginx 多模态请求配置指南](../../docs/nginx-multimodal-config-guide.md)**

## 相关文件

- `update-nginx-multimodal.sh` - 自动化配置脚本
- `../../docs/nginx-multimodal-config-guide.md` - 完整配置指南
- `../../docs/deployment-checklist.md` - 部署检查清单（已包含 Nginx 配置检查）

## 支持

如有问题，请查看：
1. 配置指南中的故障排查章节
2. LLM Gateway 看板统计: https://llm.kxpms.cn/dashboard
3. Nginx 错误日志: `/var/log/nginx/error.log`
