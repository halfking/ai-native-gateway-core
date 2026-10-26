# active-20261027 — 245 llmgo.kxpms.cn vhost（ASM 站点融合轮）

本归档记录 245 `/etc/nginx/conf.d/llmgo.kxpms.cn.conf` 在 **2026-10-27 站点融合轮**
之后的完整内容（服务器上的改前备份：`llmgo.kxpms.cn.conf.pre-asm-fusion-20261027`）。

## 本轮相对上一版（active-20261004 之后的服务器实况）的增量

1. **`upstream asm_local_245`**：`include /opt/asm/run/active-upstream.conf;`
   —— ai-session-manager（ASM）的蓝绿 active 端口指针，与 `llmgo_local_245`
   同一惯例；ASM 部署脚本（asm 仓 `scripts/deploy-remote.sh`）在晋级/回滚时
   自动重写该文件，nginx 只 reload（甚至不必 reload：upstream include 在
   reload 时重读）。
2. **`location ^~ /plugins/ai-session-manager/assets/`**：静态资产
   `alias /opt/asm/bin/current/web/assets/`（immutable 7d）。
3. **`location ^~ /plugins/ai-session-manager/api/`**：API 反代
   `proxy_pass http://asm_local_245/api/;`（前缀重写：浏览器侧
   `/plugins/ai-session-manager/api/v1/*` → ASM 进程的 `/api/v1/*`）。
4. **`location ^~ /plugins/ai-session-manager/`**：SPA（vite
   `base=/plugins/ai-session-manager/`）+ `try_files $uri
   /plugins/ai-session-manager/index.html` 客户端路由兜底。

## 权运维护注意

- `/opt/asm` 目录本身是 **0700**（`.env` 含密钥）；nginx worker 只依赖
  `o+x` 遍历位 + web 子树的 `a+rX`。ASM 部署脚本已固化
  `chmod o+x "$ROOT"` 与 `chmod -R a+rX "$candidate/web"`
  （2026-10-27 实测：漏了 o+x 时整个融合 SPA 500，而 root 视角探针全绿）。
- ASM 蓝绿对 = **8790/8791**（不占本 vhost 上游网关的契约候选口 8782）。
- maintain 块（`/maintain/`、`/maintain-api/`）与网关块本轮未动。
