# 2026-07-15 — www.kxpms.cn HSTS outage 修复报告

## 概要

用户报告：`www.kxpms.cn` 提示「此网站使用了 HSTS」无法访问。诊断发现是 **cert 没包含 www.kxpms.cn SAN**，导致 TLS 主机名校验失败，浏览器按 HSTS 策略拒绝降级到 HTTP。

## 根因

154 nginx 的 kxpms-cn-www.conf 用了 live/kxpms.cn/fullchain.pem 这个 cert，但该 cert 的 SAN 列表只有：

```
DNS:kxpms.cn, DNS:llm.kxpms.cn
```

**没有 www.kxpms.cn**。

Host 路由是这样的：
- 154:443 收到 Host: www.kxpms.cn -> nginx 用 kxpms.cn cert 应答 -> 浏览器校验 www.kxpms.cn 不在 SAN 里 -> SSL 握手失败
- 154:80 把 http 重定向到 https -> 浏览器知道要走 HTTPS
- 154 这个 vhost 之前一直在发 HSTS includeSubDomains -> 浏览器把 *.kxpms.cn 都锁住只走 HTTPS
- -> **浏览器再也没有降级机会**，HSTS 锁死 + cert 不匹配 = 永久无法访问

## 修复

**不动 nginx 的 listening，也不改 DNS**（DNS 本来就是 154 是对的，是 cert 问题）：

1. certbot nginx plugin 在 154 上为 www.kxpms.cn 补发独立 cert
2. 把 kxpms-cn-www.conf 的 ssl_certificate 切到 /etc/letsencrypt/live/www.kxpms.cn/...
3. nginx reload

```bash
ssh root@47.97.111.154 -p 25022 "
certbot certonly --nginx \
  -d www.kxpms.cn \
  --non-interactive --agree-tos --email ops@kaixuan.ai

# 之后手动 patch nginx vhost:
sed -i 's|ssl_certificate.*kxpms.cn/fullchain.pem;|ssl_certificate     /etc/letsencrypt/live/www.kxpms.cn/fullchain.pem;|' /etc/nginx/conf.d/kxpms-cn-www.conf
sed -i 's|ssl_certificate_key.*kxpms.cn/privkey.pem;|ssl_certificate_key /etc/letsencrypt/live/www.kxpms.cn/privkey.pem;|' /etc/nginx/conf.d/kxpms-cn-www.conf
nginx -t && nginx -s reload
"
```

## 关键事实

| 项 | 修复前 | 修复后 |
|---|---|---|
| cert SAN 覆盖 www.kxpms.cn | 不覆盖 (只有 kxpms.cn, llm.kxpms.cn) | 覆盖 (单独 cert for www.kxpms.cn) |
| cert 颁发方 | Let's Encrypt YR1 (kxpms.cn 那个) | Let's Encrypt YR1 (新独立 cert) |
| cert 下次到期 | 2026-10-09 (kxpms.cn) | **2026-10-13** (www.kxpms.cn 独立 cert) |
| DNS 指向 | 154 (47.97.111.154) 不变 | 154 不变 |
| HSTS header | 还是发 (这本身没错，是浏览器缓存的问题) | 同样发 |
| certbot renewal 自动 | 原 kxpms.cn 没 renewal conf (pre-existing 漏洞) | www.kxpms.cn 有 renewal conf (newly created) |
| Browser 实测 (Playwright) | 失败 | **PASS** - title "开轩启圭 WorkOS 企业智能工作台", 全 SPA 加载, 0 errors |

## certbot renewal 设置 (重要!)

新增的 cert 自动创建了 renewal conf：

```
/etc/letsencrypt/renewal/www.kxpms.cn.conf
```

但 **154 之前的 cert (kxpms.cn) 没有 renewal conf**（pre-existing 漏洞）：

```
$ ls /etc/letsencrypt/renewal/
res.itestu.cn.conf
www.kxpms.cn.conf    # 新
# 注意：没有 kxpms.cn.conf
```

意思是 kxpms.cn cert 到 2026-10-09 不会自动续期。建议下一步：
- 把 kxpms.cn 也加 renewal conf（certbot certonly --nginx -d kxpms.cn -d llm.kxpms.cn 会在 renewal 目录里加一个 conf，nginx 那边不动）
- 或者把 kxpms.cn + llm.kxpms.cn + www.kxpms.cn 合并成 1 个 cert（需要在 252 也做 cert renew，因为 252 也用 kxpms.cn 那个 cert；只能 1 台机器上 certbot renew）

## 浏览器缓存警告 (给用户)

你的浏览器 HSTS 缓存了 修复前 的失败状态。即使服务器现在 HTTPS 200，某些浏览器仍然拒绝访问（直到 HSTS 缓存过期 / 你手动清缓存）。

清缓存方法：

| 浏览器 | 步骤 |
|---|---|
| Chrome | chrome://net-internals/#hsts - 在 "Delete domain security policies" 输入 www.kxpms.cn - Delete |
| Firefox | 关闭所有窗口 -> about:config -> new -> network.stricttransportsecurity.preloadlist -> false (清 HSTS 缓存) |
| Safari | Develop -> Empty Caches + 重启 |
| Edge | edge://net-internals/#hsts - 同 Chrome |

更简单的方法：换一台没访问过这个站点的设备访问 https://www.kxpms.cn/ 验证（fresh device 没 HSTS 缓存）。或者使用 incognito 窗口（不共享 HSTS 缓存）。

我用 Playwright headless Chromium 自动测试（fresh context, 严格 cert validation enabled）已经通过：

```
=== Loading https://www.kxpms.cn/ in fresh Chromium ===
    page.title() = '开轩启圭 WorkOS 企业智能工作台'
    body[:200] = '启圭WM\n联系咨询\n首页\n结构\n产品\n路径\nFAQ\n产品\n▼\n登录\n\n开轩启圭 WorkOS · Memora\n...'
    ✅ no 4xx/5xx network errors
    ✅ no JS console errors
```

## 验证清单

- [x] cert 颁发 (Let's Encrypt, 2026-10-13 到期)
- [x] nginx vhost 切换到新 cert
- [x] nginx -t + reload
- [x] curl 外部 HTTPS 200
- [x] curl 外部 HTTP 301 -> HTTPS
- [x] openssl s_client cert chain 验证
- [x] Playwright headless Chrome 完整加载
- [x] certbot --dry-run 续期配置

## 业务方 follow-up

1. **用户清浏览器 HSTS 缓存** (按上表)
2. **下个 sprint 修复 kxpms.cn cert 的 renewal 漏洞** (在 252 上加 certbot renew schedule)
3. **考虑合并 cert**: kxpms.cn + llm.kxpms.cn + www.kxpms.cn 用 1 个 cert (renewal 统一)
4. **可以加 www.kxpms.cn 到 kxpms.cn cert 的 SAN 里** (在 renewal 续期时加 -d www.kxpms.cn -d kxpms.cn -d llm.kxpms.cn，但需要先在 252 上做 certbot，因为 DNS 指向那里)

## 截图

![www.kxpms.cn SPA loaded by Playwright](./www-kxpms-cn-spa.png)
