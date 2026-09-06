# AI Native Gateway

> Private-deployment LLM gateway with intelligent routing, multi-tenancy, and comprehensive observability

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go Report](https://img.shields.io/badge/Go-1.27+-00ADD8.svg)](https://golang.org)

[Quick Start](#quick-start) • [Features](#features) • [Architecture](docs/architecture.md) • [Comparison](docs/comparison.md) • [Roadmap](ROADMAP.md)

---

## What is AI Native Gateway?

AI Native Gateway is an **open-source, self-hosted LLM gateway** that provides:

- **Protocol Normalization**: OpenAI, Anthropic, Gemini, and Responses API compatibility
- **Intelligent Routing**: Sticky sessions, health-based failover, tier fallback
- **Multi-Tenancy**: PostgreSQL RLS-enforced tenant isolation
- **Credential Management**: Provider health monitoring and credential pools
- **Observability**: Real-time request streams, routing analytics, cost tracking
- **Privacy**: 100% private deployment - all data stays in your infrastructure

Built with **Go + PostgreSQL + Redis**, designed for organizations that need full control over their LLM infrastructure.

---

## Quick Start

Deploy locally in under 10 minutes:

```bash
# Clone repository
git clone https://github.com/halfking/ai-native-gateway-core.git
cd ai-native-gateway-core

# Generate secure keys
cp .env.quickstart.example .env
# Edit .env with secure random values (see file for generation commands)

# Start stack (PostgreSQL + Redis + Gateway)
docker-compose -f docker-compose.quickstart.yml up -d

# Verify health
curl http://localhost:8781/healthz
# {"status":"ok"}

# Access admin UI
open http://localhost:8781/admin
```

See [Getting Started Guide](docs/getting-started.md) for detailed instructions.

---

## Screenshots

All screenshots are captured from a live local deployment (1728×1050, after full data load).

### Dashboard — Real-Time Request Stream (Queue Perspective)

![Dashboard Request Stream](docs/assets/screenshots/dashboard-request-stream.png)
*Live request stream grouped by processing queue, with dispatch-chain stats (in-flight, p50/p95 latency, node availability) and per-model node health*

### Routing Panorama — Two-Layer Routing Analytics

![Routing Panorama](docs/assets/screenshots/routing-panorama.png)
*L1 model selection (task classification → 6-dimension scoring) + L2 credential selection (tier fallback → billing round → P2C scoring), with task×model heatmap and Sankey flow (task → model → provider)*

### Credential Monitor

![Credential Monitor](docs/assets/screenshots/credential-monitor.png)
*64 upstream credentials with availability state (ready/suspended/disabled), health grade, model coverage, success rate, and concurrency slots*

### Work-Type Configuration

![Work Types](docs/assets/screenshots/work-types.png)
*Task auto-classification (10 work types) with 24h distribution, top models, and routing decision stats*

### Free Resource Pool

![Free Pool](docs/assets/screenshots/free-pool.png)
*Free-model resource pool: bring-your-own keys, provider templates (Groq, Google AI Studio, OpenRouter, SiliconFlow, Zhipu), and per-model routing priority*

### Request Session Detail

![Request Detail](docs/assets/screenshots/request-detail.png)
*Full request inspection: Q&A replay, dispatch waterfall, routing & retry trail, trace, compression/masking record (note the masked API key `sk-****`), token & cache stats*

---

## Features

### Core Gateway Capabilities

- **Multi-Protocol Support**: OpenAI Chat/Completions, Anthropic Messages, Responses API, Gemini
- **Streaming**: HTTP SSE relay with incremental integrity checks
- **Authentication**: Bearer token validation with tenant isolation
- **Rate Limiting**: Token-based quotas (TPM/RPM) with Redis-backed enforcement
- **Audit Trail**: Request/response logging to PostgreSQL with sensitive data masking

### Intelligent Routing (Two-Layer)

- **L1 — Model Selection**: Prompt is auto-classified into work types (code generation, conversation, summarization…), then scored across 6 dimensions to lock a model profile — configurable per work type in the admin UI
- **L2 — Credential Selection**: model resolution → tier fallback (primary → secondary → tertiary) → billing-round preference → P2C scoring (latency + success rate) → execute / circuit-break
- **Sticky Sessions**: session-to-credential binding for conversation continuity across model switches
- **Health-Aware Dynamic Switching**: credentials are automatically degraded (rate-limited, cooling down, unreachable) and recovered based on rolling success rates and probe results — traffic shifts to healthy candidates without manual intervention
- **Hot Configuration**: routing policies, prompt-budget limits, and work-type settings reload at runtime (~5s) — no restart required

### Intelligent Context Compression

- **Automatic Prompt Compression**: when a request approaches the provider's actual context window (default trigger at ~80%), message-level compression runs before dispatch — long agent sessions fit smaller windows instead of failing
- **Configurable Budget**: gateway-wide prompt acceptance limit (`gateway.max_prompt_tokens`, hot-reloadable) plus per-model context-window overrides
- **Transparent Audit**: every compression event is recorded on the request (strategy, threshold, before/after sizes) and visible in the request detail view
- **Multi-Mode Strategies**: selector pipeline per dispatch mode, with a forced-compression path for oversized bodies

### Security & Data Masking

- **Secret Masking**: API keys and tokens (OpenAI `sk-…`, Anthropic `sk-ant-…`, AWS `AKIA…`, generic Bearer / `x-api-key` headers) are masked in logs, session summaries, and archived request bodies before they hit storage
- **Encrypted Credentials**: upstream provider keys are encrypted at rest (Fernet/AES) and shown masked (`sk-****`) in the UI
- **Audit Trail**: full request/response logging with sensitive-field masking, OpenTelemetry + Prometheus integration

### Multi-Tenancy

- **Database-Level Isolation**: PostgreSQL Row-Level Security on 38+ tables
- **Tenant/User/API Key hierarchy**: Secure credential management
- **Per-Tenant Quotas**: Token limits, rate limits, cost caps
- **Cross-Tenant Security**: RLS prevents data leakage at query level

### Observability

- **Real-Time Request Stream**: Live dashboard with multi-dimensional filtering
- **Routing Analytics**: Task/model heatmaps, Sankey flow diagrams, decision replay
- **Credential Monitor**: Provider health matrix with latency and success rate
- **Cost Tracking**: Per-tenant token and cost accounting
- **Prometheus Metrics**: `/metrics` endpoint for external monitoring

### Admin Console

Embedded Vue.js SPA (no separate frontend deployment):
- Provider and credential management
- Tenant/user/API key administration  
- Request logs and session viewer
- Routing dashboards and analytics
- Data lifecycle management

---

## Architecture

```
Client → Gateway :8781 → [Auth → Protocol → IR → Router → Upstream]
                              ↓           ↓
                        PostgreSQL    Redis
```

- **Data Plane**: Go binary handling LLM requests
- **Control Plane**: Embedded Vue.js admin UI + REST API
- **Storage**: PostgreSQL (durable state) + Redis (hot cache)
- **Workers**: Background jobs for health probes, session summarization, billing

See [Architecture Documentation](docs/architecture.md) for details.

---

## Deployment Modes

AI Native Gateway supports both **full production stacks** and a **minimal single-machine local deployment**:

### Full Deployment (production)

| Mode | Description | Status |
|------|-------------|--------|
| **Docker Compose** | Quick start with included PostgreSQL/Redis | ✅ Recommended for evaluation |
| **Binary + systemd** | Production deployment on Linux hosts | ✅ Supported with installer |
| **Kubernetes** | Deployment + ConfigMap + Service manifests | ⚠️ Test-grade (not production-validated) |

**Production stack**: Gateway + PostgreSQL 14+ (durable state, RLS) + Redis 7+ (hot state, rate limits) + embedded admin UI, with optional Prometheus/Grafana monitoring. Requires TLS termination, secrets management, and backups.

### Local Minimal Deployment (single machine)

Everything runs on one machine with one command — no external services to provision:

```bash
docker compose -f docker-compose.quickstart.yml up -d
```

This brings up PostgreSQL + Redis + the gateway (with embedded admin UI) as a self-contained local stack with persistent volumes and health checks. It is the same binary and schema as production — moving to production means pointing at managed PostgreSQL/Redis and adding TLS, not changing configuration semantics.

**Production Requirements**:
- External PostgreSQL 14+ and Redis 7+
- TLS termination (reverse proxy)
- Secrets management
- Backup and monitoring

See [Production Deployment](docs/deployment/) for details.

---

## Comparison with Alternatives

| Feature | AI Native Gateway | LiteLLM | OmniRoute | Portkey | Kong AI |
|---------|-------------------|---------|-----------|---------|---------|
| **Deployment** | Private (self-hosted) | SaaS + OSS | Self-hosted (Node) | SaaS | OSS |
| **Multi-Tenancy** | Native (PG RLS) | Basic | Single-node oriented | Full (SaaS) | Via plugins |
| **Admin UI** | Embedded Vue SPA | CLI | Web UI | SaaS UI | Kong Manager |
| **Data Residency** | 100% private | Depends | 100% private | Cloud (SaaS) | Self-hosted |
| **License** | Apache 2.0 | MIT | See upstream | Proprietary | Apache 2.0 |

**Choose AI Native Gateway if you need**:
- Full data residency control (no external SaaS dependencies)
- Deep multi-tenancy with database-level isolation
- Embedded admin UI in single binary
- Go performance and compiled artifact deployment

**Choose alternatives if you need**:
- Maximum provider coverage (100+ providers) → LiteLLM
- Node.js-based self-hosted gateway with embedded DB → OmniRoute
- Zero-ops managed service → Portkey
- General API gateway + LLM → Kong

See [Detailed Comparison](docs/comparison.md) for more.

---

## Roadmap

**Current (v2.x)**:
- ✅ OpenAI/Anthropic/Gemini/Responses protocol support
- ✅ Multi-tenant isolation with PostgreSQL RLS
- ✅ Intelligent routing with sticky sessions
- ✅ Vue.js admin console
- ✅ Docker Compose quick start

**Next (3-6 months)**:
- 🚧 Enhanced cost/quality-aware routing
- 🚧 Production-grade Kubernetes Helm charts
- 🚧 Grafana dashboard templates
- 🚧 Advanced observability (session forensics, decision replay)

**Exploring (6-12+ months)**:
- 🔬 MCP (Model Context Protocol) gateway integration
- 🔬 Agent-to-Agent protocol support
- 🔬 Kubernetes Operator (CRD-based deployment)

See [ROADMAP.md](ROADMAP.md) for full details.

---

## Documentation

- [Getting Started](docs/getting-started.md) - Deploy in 10 minutes
- [Project Overview](docs/PROJECT_OVERVIEW.md) - Features and module map
- [Architecture Overview](docs/architecture.md) - System design and components
- [Environment & Configuration](docs/environment.md) - Deployment environments and variables
- [API Documentation](docs/03-design/01-architecture/architecture/API.md) - Data plane and admin API specs
- [Quick Reference](docs/QUICK_REFERENCE.md) - Common commands, endpoints, troubleshooting
- [Comparison](docs/comparison.md) - vs LiteLLM, Portkey, Kong
- [Troubleshooting](docs/troubleshooting/) - Common issues and solutions
- [Documentation Index](docs/INDEX.md) - Full docs navigation

---

## Community & Support

- **Issues**: [GitHub Issues](https://github.com/halfking/ai-native-gateway-core/issues) for bugs and feature requests
- **Discussions**: [GitHub Discussions](https://github.com/halfking/ai-native-gateway-core/discussions) for questions
- **Security**: See [SECURITY.md](SECURITY.md) for responsible disclosure
- **Contributing**: See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup

---

## Contributing

We welcome contributions! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for:
- Development environment setup
- Code style and testing requirements
- Pull request process
- Multi-tenancy security requirements

---

## License

Licensed under the [Apache License 2.0](LICENSE).

See [NOTICE](NOTICE) for required attributions when redistributing this software.

**Commercial Use**: Apache 2.0 allows commercial use. When redistributing (as source or binary), you must retain copyright notices and the NOTICE file. Internal commercial use without redistribution does not require additional attribution beyond license compliance.

---

## Acknowledgments

AI Native Gateway incorporates components from the following open-source projects:
- Go standard library (BSD-3-Clause)
- PostgreSQL driver (MIT)
- Redis client (BSD-2-Clause)
- Vue.js and Element Plus (MIT)
- See [NOTICE](NOTICE) for complete list

---

Built with ❤️ by the AI Native Gateway community
