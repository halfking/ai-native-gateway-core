# LLM Gateway Domain Registry

SSOT for all upstream/production/management domains referenced in code and config.
Update this file when adding/changing a domain; run `scripts/check-domain-drift.sh` to
detect unregistered hardcoded domains.

## Production domains (kxpms.cn)

| Subdomain | Purpose | Config source | Deploy host | Go file:line |
|---|---|---|---|---|
| `llm.kxpms.cn` | LLM Gateway API / master | `internal/opsreporter/reporter.go:22` `installer/.../*.go` | 154 | `cmd/gateway/main.go:913` |
| `llmgo.kxpms.cn` | Pre-prod management / anomaly | `cmd/gateway/logging_init.go:63` | 245 | `cmd/gateway/logging_init.go:127` |
| `files.kxpms.cn` | Cloudreve attachment storage | `domains/session/v2/*.go` | 154 | — |
| `download.kxpms.cn` | CDN for offline artifacts | `deploy/download.kxpms.cn.nginx.conf` | 154 | — |
| `registry.kxpms.cn` | Internal Docker registry | `installer/internal/imgsrc/auth.go:26` | 184 | `cmd/license-authority/manifest_handler.go:80` |
| `maintain.kxpms.cn` | Maintenance proxy redirect | `cmd/gateway/maintain_proxy.go:78` | 154 | — |
| `cloudreve.kxpms.cn` | Cloudreve base URL (optional) | — | — | `domains/attachments/*.go:40`(comment) |

## Data-plane domains (itestu.cn)

| Subdomain | Purpose | Config source | Deploy host | Go file:line |
|---|---|---|---|---|
| `llm.itestu.cn` | LLM Gateway API (K8s) | `deploy/k8s/*.yaml:31` | 252 | — |
| `registry.itestu.cn` | K8s Docker registry | `deploy/k8s/*.yaml:31` | 252 | — |
| `pg-dev.itestu.cn` | Dev PostgreSQL via NPS tunnel | `configs/env-kaixuan1.sh:33` | tunnel | — |
| `files.itestu.cn` | Cloudreve upload (data plane) | — | 252 | `admin/session_export.go:17`(comment) |
| `s60.itestu.cn` | Load-test mock suppliers | `docs/.../s60-mock-suppliers.conf` | 252 | — |
| `res.itestu.cn` | CORS cross-origin resource | — | 252 | — |
| `llmgo.itestu.cn` | Management endpoint fallback | — | 252 | — |

## LLM provider domains (domestic, bypass proxy)

Defined in `internal/upstream/proxy_resolver.go:21-40`:

- `api.minimax.chat`, `api.minimaxi.com` — MiniMax
- `api.deepseek.com` — DeepSeek
- `api.moonshot.cn` — Moonshot
- `api.scnet.cn` — SenseTime
- `api.coze.cn` — Coze (ByteDance)
- `dashscope.aliyuncs.com` — Aliyun DashScope
- `open.bigmodel.cn` — Zhipu GLM
- `spark-api-open.xf-yun.com` — iFlytek Spark
- `hunyuan.tencent.com` — Tencent Hunyuan
- `qianfan.baidubce.com` — Baidu Qianfan
- `api.lkeap.cloud.tencent.com` — Tencent LKEAP
- `aip.baidubce.com` — Baidu AI
- `mg-new.evolai.cn` — Evol AI
- `llmgateway.internal.example.com`, `internal.example.com` — Placeholder
- `localhost`, `127.0.0.1` — Local

## Third-party API domains

| Domain | Purpose | File |
|---|---|---|
| `open.feishu.cn` | Lark/Feishu bot notification | `domains/notification/lark.go:35` |
| `registry.cn-hangzhou.aliyuncs.com` | Aliyun public registry mirror | `installer/internal/imgsrc/source.go:4` |

## Change checklist

When adding/removing/changing a domain:

1. Update this registry
2. Search for the old domain: `rg '<pattern>' --type go --type sh --type yaml`
3. If adding a provider domain to `proxy_resolver.go`, also update the domestic list above
