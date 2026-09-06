# Architecture Overview

AI Native Gateway is a private-deployment LLM gateway written in Go, providing protocol normalization, intelligent routing, multi-tenancy, and comprehensive observability for AI applications.

## High-Level Architecture

```mermaid
graph TB
    Client[Client Application]
    Agent[AI Agent/IDE]
    Browser[Admin Browser]
    
    Client --> |HTTP/SSE| DataPlane[Data Plane :8781]
    Agent --> |HTTP/SSE| DataPlane
    Browser --> |HTTPS| ControlPlane[Control Plane]
    
    DataPlane --> Auth[Authentication]
    Auth --> Protocol[Protocol Normalization]
    Protocol --> IR[Intermediate Representation]
    IR --> Router[Intelligent Router]
    Router --> ResourceGate[Resource Gate]
    ResourceGate --> Upstream[Upstream Relay]
    Upstream --> |SSE Stream| Providers[LLM Providers]
    
    ControlPlane --> AdminAPI[Admin API]
    ControlPlane --> VueSPA[Vue.js SPA]
    
    DataPlane -.-> PG[(PostgreSQL)]
    DataPlane -.-> Redis[(Redis)]
    ControlPlane -.-> PG
    ControlPlane -.-> Redis
    
    DataPlane -.-> OTel[OpenTelemetry]
    OTel -.-> Prometheus[Prometheus]
    
    Workers[Background Workers] -.-> PG
    Workers -.-> Redis
    
    Providers --> OpenAI[OpenAI]
    Providers --> Anthropic[Anthropic]
    Providers --> Gemini[Google Gemini]
    Providers --> Others[Other Providers]
```

## Core Components

### Data Plane

**Entry Point**: `cmd/gateway/main.go`

Handles all user-facing LLM requests with OpenAI/Anthropic/Responses/Gemini compatibility.

**Request Flow**:
1. **Authentication**: API key validation, tenant isolation
2. **Protocol Normalization**: Convert various formats to IR (Intermediate Representation)
3. **Routing**: Select optimal credential(s) based on availability, cost, quality, sticky sessions
4. **Resource Gate**: Check quotas, rate limits, concurrency slots
5. **Upstream Relay**: Stream responses with integrity checks and retry logic
6. **Audit**: Log requests, responses, tokens, costs to PostgreSQL

**Endpoints**:
- `/v1/chat/completions` - OpenAI Chat Completions
- `/v1/completions` - OpenAI Legacy Completions
- `/v1/messages` - Anthropic Messages API
- `/v1/responses` - Responses API
- `/v1/embeddings` - Embeddings (OpenAI-compatible)
- `/v1/models` - Available models
- Gemini endpoints (`/v1/models/{model}:generateContent`, etc.)

### Control Plane

**Admin API**: REST API for gateway management  
**Admin UI**: Embedded Vue.js SPA at `/admin`

**Capabilities**:
- Provider and credential management
- Tenant, user, and API key administration
- Real-time request stream monitoring
- Routing analytics (heatmaps, Sankey diagrams, decision replay)
- Cost and pricing management
- Data lifecycle and compliance tools
- System health and diagnostics

### Routing Engine

**Location**: `domains/streaming/executors/`

Requests flow through **two routing layers**, both observable in the Routing Panorama admin page:

**L1 — Model Selection** (which model should serve this request?):
1. Prompt auto-classification into **work types** (code generation, conversation, summarization, translation, CRM follow-up, …) — 10 built-in types, configurable per type in the admin UI
2. **6-dimension scoring** of candidate models against the classified task
3. Model profile locking (explicit `model` parameters bypass L1)

**L2 — Credential Selection** (which upstream credential executes it?):
1. Availability filter (health status, admin protection)
2. Tenant and protocol compatibility
3. **Tier-based fallback** (primary → secondary → tertiary credentials)
4. Billing mode preference (metered → free → quota)
5. **Sticky sessions** (session → credential binding, survives model switches)
6. **P2C scoring** (Power-of-Two-Choices over latency, success rate, concurrency)

**Dynamic switching**: credential health is continuously scored from rolling request outcomes and background probes. Failing credentials are automatically degraded (rate-limited / cooling down / unreachable → suspended) and traffic shifts to healthy candidates; recovery is automatic after cooldown. Routing policies and settings hot-reload at runtime (~5s) without restarts.

**Current Status**:
- ✅ Work-type classification, availability, tenant, protocol, tier, billing, sticky, P2C baseline
- ✅ Health-aware degradation/recovery, hot config reload
- 🚧 Cost/quality/context-aware scoring (shadow mode, not default active)
- 🔬 Advanced bandit algorithms (exploration phase)

### Intelligent Context Compression

**Location**: `domains/streaming/executors/compression_strategy.go`

- **Trigger**: when the resolved prompt approaches the target model's actual context window (~80% by default), or when the body exceeds the gateway prompt budget
- **Action**: message-level compression strategies run before dispatch — long agent sessions fit into smaller context windows instead of failing with context-length errors
- **Budget controls**: gateway-wide `gateway.max_prompt_tokens` (hot-reloadable, ~5s) plus per-model context-window overrides
- **Audit**: every compression is recorded on the request (strategy, threshold trigger, before/after token counts) and displayed in the request detail view

### Security & Data Masking

**Location**: `domains/secretmask/`

- **Secret masking**: API keys and tokens are masked before persistence — OpenAI (`sk-…`), Anthropic (`sk-ant-…`), AWS (`AKIA…`), generic Bearer tokens, and `x-api-key` header forms; applied to request logs, session summaries, and archived request/response bodies
- **Encrypted credentials**: upstream provider keys are encrypted at rest (Fernet/AES) and rendered masked (`sk-****`) in the admin UI
- **Multi-tenancy**: PostgreSQL Row-Level Security on 38+ tables; cross-tenant reads return not-found rather than forbidden (no information leak)

### Multi-Tenancy

**Isolation**: PostgreSQL Row-Level Security (RLS) on 38+ tables

**Key Concepts**:
- **Tenant**: Top-level isolation boundary
- **User**: Belongs to one tenant
- **API Key**: Belongs to one user, used for data plane authentication
- **Credential**: Upstream provider API key, may be tenant-specific or shared

All queries automatically enforce `tenant_id` filtering at database level.

### Storage

**PostgreSQL 14+**:
- Core data (tenants, users, api_keys, credentials, providers, models)
- Request logs and audit trail
- Session metadata and summaries
- Pricing and cost records

**Redis 7+**:
- Session state and sticky routing cache
- Live request stream (FIFO queue with dedup)
- Rate limit buckets (token/TPM/RPM)
- Credential availability cache (30s TTL)

### Observability

**Metrics**: Prometheus exposition at `/metrics`
- Request counts, latencies, errors by provider/model/tenant
- Credential health scores and availability
- Routing decisions and retries
- Database and Redis connection pools

**Traces**: OpenTelemetry integration (optional)
- Distributed tracing across routing and upstream calls
- Tenant ID, session ID, request ID propagation

**Logs**: Structured JSON logs (slog)
- Request/response bodies archived to disk or object storage
- Sensitive data masking (API keys, credentials)

### Background Workers

**Location**: `bg/`

~50 workers handle asynchronous tasks:
- Credential health probing (every 5 minutes)
- Session summarization (LLM-based)
- Cost aggregation and billing
- Data lifecycle (archival, cleanup)
- Provider model catalog sync
- System monitor (Redis queue, 30s dedup)

## Deployment Modes

| Mode | Description | Status |
|------|-------------|--------|
| **Minimal local** | Single machine: Docker Compose with PostgreSQL + Redis + gateway | ✅ CURRENT (`docker-compose.quickstart.yml`) |
| **M1** | Binary + systemd (external PG/Redis) | ✅ CURRENT (with installer) |
| **M2** | Docker Compose | ✅ CURRENT (quickstart provided) |
| **M3** | Kubernetes deployment/sidecar | ✅ PARTIAL (test-grade manifests) |
| **M4** | Kubernetes Operator (CRD) | 🔬 TARGET (not implemented) |

**Local minimal deployment**: `docker-compose.quickstart.yml` brings up PostgreSQL, Redis, and the gateway (with embedded admin UI) as a self-contained single-machine stack with persistent volumes and health checks.

**Production**: Requires external PostgreSQL/Redis, TLS, secrets management, backups. See [Production Deployment](deployment/).

## Security Architecture

- **Authentication**: Bearer token (API keys) for data plane, separate admin API keys for control plane
- **Multi-Tenancy**: PostgreSQL RLS enforces tenant isolation at database level
- **Encryption**: Upstream credentials encrypted at rest (Fernet/AES-256-GCM)
- **Secrets**: Environment variables or SOPS-encrypted configs (not included in quick start)
- **Network**: Services bind to localhost by default; use reverse proxy for external access
- **Audit**: Full request/response logging with sensitive field masking

## Technology Stack

**Backend**:
- Go 1.21+
- PostgreSQL 14+ (with RLS)
- Redis 7+

**Frontend**:
- Vue 3 + TypeScript
- Element Plus UI
- ECharts for visualization
- Vite build system

**Deployment**:
- Docker / Docker Compose
- Kubernetes (basic manifests)
- systemd (binary mode)

**Monitoring**:
- Prometheus
- OpenTelemetry (optional)
- Grafana (dashboards not included)

## Design Principles

1. **Private Deployment First**: All data stays within your infrastructure
2. **Protocol Agnostic**: Support OpenAI, Anthropic, Gemini, and custom protocols
3. **Production Stability**: Fail-closed on errors, graceful degradation, extensive testing
4. **Observability**: Every request traced, every decision auditable
5. **Multi-Tenancy**: Tenant isolation enforced at database and application levels
6. **Extensibility**: Background workers, plugin hooks, custom routing policies

## Limitations and Constraints

- M3 Kubernetes manifests are test-grade; production K8s deployment requires additional hardening
- Advanced routing features (cost/quality optimization) are in shadow mode
- MCP gateway integration and A2A protocol are in research phase
- Binary installer and quick start do not include all enterprise features (e.g., offline activation, upgrade automation)

## Further Reading

- [Getting Started](getting-started.md) - Deploy in 10 minutes
- [Environment & Configuration](environment.md) - Environment variables and settings
- [Routing Analytics](deployment/routing-analytics-mv-deployment-guide.md) - Deep dive into routing logic
- [Project Overview](PROJECT_OVERVIEW.md) - RLS and tenant isolation
- [API Documentation](api/) - Admin and data plane APIs
