# maishouai.top provider 502 探测分析

## 现状
- maishouai.top provider (id=5990) 已添加到 252 PG, health_status=healthy
- 7 个 model_offers 全部 routable
- credential #30 的 API key: `sk-KnQ3x4VgHkOwfSHScwE7NDHbjAtC9cWhRhl9P7P4Z2bKsQwx`
- 探测请求全返回 502

## 网络路径
```
用户 → llmgo.kxpms.cn → 252 nginx (port 9444) → NPS tunnel (port 10010) → kaixuan-1 k3s gateway
```

- 252 上的 llm-gateway-go systemd service 处于 inactive 状态（非启用）
- `llmgo.itestu.cn`（指向 252 localhost:8780）也不可达

## 根因分析
1. **API key 确认正确** — 已从 DB 解密验证，与手工测试用 key 一致
2. **maishouai.top 可达** — 从 252 服务器 curl 返回 200（含 claude-fable-5 响应）
3. **502 源头** — 运行中的 k3s gateway（kaixuan-1）在探测时向 maishouai.top 发请求，收到 502

**最可能的根因：k3s gateway 无法访问 maishouai.top**（GFW 拦截或缺少代理出口）

## 参考
- 252 上的旧 systemd unit 配置了 `HTTP_PROXY=http://kaixuan-184:KaixuanEgress2026@172.31.0.2:7890`，但该实例未运行
- k3s gateway 可能没有配置代理，而 maishouai.top 在国内可能被 GFW 拦截
