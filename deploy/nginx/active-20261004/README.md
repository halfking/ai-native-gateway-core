# Nginx Deployment Archive: 2026-10-04 — 统一入口（移动端公网可达）

## Scope

统一入口轮（/ 按 UA 302 /m/）是网关二进制层能力，但公网入口 nginx
（llmgateway.internal.example.com vhost）在 2026-09-24 authfix 轮已把 apex `location = /`
固定 302 `/maintain/home`（`?login` 例外回 PC SPA），且 `location /` 直接
从磁盘 `current/web` 出 PC SPA——**不代理网关**，导致：

1. 网关侧 UA 分流在公网链路不可见（只有直打 8781/8782 端口才生效）；
2. 公网 `/m/` 被 SPA fallback 吃掉，返回的是 PC index.html。

本归档记录 245 `/etc/nginx/conf.d/llmgateway.internal.example.com.conf` 的修补
（改前备份 `llmgateway.internal.example.com.conf.pre-unified-entry-20261004` 在服务器上）。

## 变更内容（llmgo-245.internal.example.com.conf）

1. **新增三个 location**（置于 `location = /` 之前）：
   - `location = /m` → 302 `/m/`
   - `location ^~ /m/` → proxy `llmgo_local_245`（网关持有移动 SPA 与
     fallback；`^~` 前缀保证压过 `current/web` 的 SPA fallback 与
     assets 正则，但让位给更长的 `/maintain*` 前缀）
   - `location ^~ /m-assets/` → proxy `llmgo_local_245`
   - 三者均带 UPGRADING 503 门（与既有 API location 一致）
2. **`location = /` 追加两条规则**（原 2026-09-24 语义对桌面端原样保留）：
   - `if ($args ~ '(^|&)desktop(=|$)') { return 418; }` —— 逃生口，
     与网关侧「参数存在即停」同判（418 → error_page → /index.html = PC SPA）
   - `if ($http_user_agent ~* "(iphone|ipod|ipad|android|windows phone|iemobile|blackberry|bb10|opera mini|opera mobi|mobile safari)") { return 302 /m/; }`
     —— token 列表**逐字镜像** `cmd/gateway/mobile_static.go` 的
     `mobileUserAgentRe`，两端口径必须同步改

## 两个实测踩坑

- **裸 `?desktop`（无值）**：`$arg_desktop` 只取值，裸参数为空串，
  `!= ""` 判否——必须用 `$args` 正则判参数存在性。
- **nginx 双引号字符串内 `$)` 会被变量插值破坏**：正则必须用
  单引号 `'(^|&)desktop(=|$)'`，双引号下裸参数判定静默失效。

## 待办（154 侧）

~~154 生产域 llmgateway.internal.example.com 若有同款 apex 产品决策，需同口径补丁~~
已于同日完成：`llm-154.kxpms-cn.conf`（upstream 名为 `llm_local`，其余
与 245 同构；服务器备份 `llm-kxpms-cn.conf.pre-unified-entry-20261004`）。
154 seq2451 首次携带移动端，deploy-seamless 预绑软链后**免重启**直接
挂载 /m（245 seq2450 无预绑，靠手动 restart 激活——正是该坑的对照组）。
