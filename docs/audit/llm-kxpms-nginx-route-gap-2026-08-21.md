# llm.kxpms.cn / llmgo.kxpms.cn Nginx Route Audit

## Scope

The Gateway serves a Vue SPA and Go HTTP APIs on the same origin. A catch-all
SPA `try_files ... /index.html` must not handle backend routes.

## Findings

Both 154 (`llm.kxpms.cn`) and 245 (`llmgo.kxpms.cn`) previously routed these
backend paths to the SPA fallback:

| Route family | Expected owner | Symptom before fix |
|---|---|---|
| `/api/*` | Go Gateway | `POST /api/auth/token` returned Nginx 405 HTML |
| `/healthz` | Go Gateway | HTTP 200 with `index.html` |
| `/readyz` | Go Gateway | HTTP 200 with `index.html` |
| `/v1/*` | Go Gateway | HTTP 200 with `index.html` |
| `/v1beta/*` | Go Gateway | vulnerable to the same fallback |
| `/v2/*` | Go Gateway | HTTP 200 with `index.html` |
| `/metrics` | Go Gateway | HTTP 200 with `index.html` |
| `/admin/*` | Go Gateway | HTTP 200 with `index.html` |

## Route Policy

The vhost must define these backend boundaries before the SPA fallback:

```text
= /healthz
= /readyz
^~ /api/
^~ /v1/
^~ /v1beta/
^~ /v2/
= /metrics
^~ /admin/
```

More-specific upstreams remain ahead of these boundaries, including the 154
`/api/v1/ops/` license-authority route and live-stream WebSocket locations.

## Verification Evidence

After reload on both hosts:

| Request | 154 | 245 |
|---|---:|---:|
| `POST /api/auth/token` with invalid credentials | 401 JSON | 401 JSON |
| `GET /healthz` | 200 JSON | 200 JSON |
| `GET /readyz` | 401 JSON | 401 JSON |
| `GET /v1/models` without auth | 401 JSON | 401 JSON |
| `GET /v1/embeddings` without auth | 401 JSON | 401 JSON |
| `GET /v1/sessions` without auth | 401 JSON | 401 JSON |
| `GET /v2/healthz` without auth | 401 JSON | 401 JSON |
| `GET /metrics` without auth | 401 JSON | 401 JSON |
| `GET /admin/config/reload` without auth | 401 JSON | 401 JSON |
| `OPTIONS /api/auth/token` | 204 | 204 |

## Rollback

Each remote edit created a timestamped backup beside the active vhost. Restore
the relevant backup, run `nginx -t`, then reload Nginx. The route changes do not
modify the Go service or database.
