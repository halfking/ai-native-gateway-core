# Comparison with Other LLM Gateways

This comparison focuses on **architecturally distinguishing features** rather than popularity metrics. All information is based on publicly available documentation as of 2026-09-07.

## Quick Comparison

| Feature | AI Native Gateway | LiteLLM | OmniRoute | Portkey | Kong AI Gateway |
|---------|-------------------|---------|-----------|---------|-----------------|
| **Deployment** | Private (self-hosted) | SaaS + OSS | Self-hosted (Node) | SaaS + OSS | OSS (Enterprise avail) |
| **Protocol Support** | OpenAI, Anthropic, Gemini, Responses | 100+ providers | Multi-provider catalog (project-declared 290+) | OpenAI, Anthropic, Azure | Provider-agnostic plugins |
| **Multi-Tenancy** | Native (PG RLS) | Basic | Single-node oriented | Full (SaaS) | Via Kong auth plugins |
| **Streaming** | SSE with integrity checks | Yes | Yes | Yes | Yes |
| **Routing** | Two-layer (model + credential), health-aware | Fallback + load balance | Cost/cache/context/headroom scoring | Basic fallback | Load balancing |
| **Admin UI** | Embedded Vue SPA | CLI + proxy | Web UI | Full SaaS UI | Kong Manager |
| **Observability** | Prometheus + OTel + logs | Prometheus | Built-in dashboards | Full SaaS analytics | Prometheus plugins |
| **Cost Tracking** | Per-tenant token accounting | Basic | Savings-oriented tracking | Advanced (SaaS) | Via plugins |
| **Data Residency** | 100% private | Depends on mode | 100% private | SaaS = cloud | Self-hosted option |
| **License** | Apache 2.0 | MIT | See upstream repo | Proprietary (SaaS) | Apache 2.0 |

## Detailed Feature Comparison

### LiteLLM

**Repository**: https://github.com/BerriAI/litellm (18k+ stars as of 2026-09-06)

**What LiteLLM Does Best**:
- Unified interface for 100+ LLM providers
- Python-based proxy with simple deployment
- OpenAI SDK drop-in replacement
- Active community and frequent updates
- Cost tracking and budgets

**What AI Native Gateway Emphasizes Differently**:
- **Private deployment focus**: No SaaS dependencies, all data stays local
- **Multi-tenancy depth**: PostgreSQL RLS enforces tenant isolation at database level
- **Embedded admin UI**: Full-featured Vue.js SPA, no separate dashboard service
- **Credential health monitoring**: Real-time provider health checks with heatmap visualization
- **Go performance**: Compiled binary with lower memory footprint vs Python runtime
- **Session forensics**: Built-in session analysis and debugging tools

**When to Choose LiteLLM**:
- You need maximum provider coverage (100+ providers)
- Python ecosystem fits your stack
- You want simpler setup with fewer moving parts
- You're comfortable with Python proxy performance characteristics

**When to Choose AI Native Gateway**:
- You require deep multi-tenancy with database-level isolation
- Go/compiled binary deployment is preferred
- You need embedded admin UI without additional services
- Private deployment with zero external dependencies is critical
- You want credential health monitoring and routing analytics built-in

### Portkey

**Website**: https://portkey.ai/

**What Portkey Does Best**:
- Full SaaS platform with managed infrastructure
- Advanced observability and analytics
- Prompt management and versioning
- Guardrails and content filtering
- Enterprise support

**What AI Native Gateway Emphasizes Differently**:
- **100% private deployment**: No data leaves your infrastructure
- **Open source**: Apache 2.0 license, community-driven
- **Database ownership**: You own all request logs and metadata
- **No vendor lock-in**: Self-hosted with full control
- **Cost-effective**: No per-token SaaS fees

**When to Choose Portkey**:
- You want managed service with zero ops overhead
- You need enterprise features like prompt playground
- You're comfortable sending LLM traffic through third-party service
- Budget allows for SaaS pricing model

**When to Choose AI Native Gateway**:
- Data residency requirements mandate private deployment
- You want to avoid per-token SaaS costs
- You need full control over infrastructure and data
- Open source and community-driven is important
- You have ops capacity for self-hosting

### Kong AI Gateway

**Repository**: https://github.com/Kong/kong (39k+ stars as of 2026-09-06)

**What Kong AI Gateway Does Best**:
- Part of mature Kong Gateway ecosystem
- Enterprise-grade scalability and reliability
- Rich plugin ecosystem for auth, rate limiting, observability
- Kubernetes-native with Ingress Controller
- Multi-protocol support (not just LLM)

**What AI Native Gateway Emphasizes Differently**:
- **LLM-specific design**: Purpose-built for AI/LLM use cases
- **Credential management**: Built-in provider credential pool with health checks
- **Routing intelligence**: Sticky sessions, task-aware routing, tier fallback
- **Admin UI**: LLM-focused dashboards (request streams, routing analytics, cost tracking)
- **Simpler for LLM-only**: No need to configure generic API gateway features

**When to Choose Kong AI Gateway**:
- You already use Kong Gateway infrastructure
- You need general API gateway + LLM in one platform
- Kubernetes-native deployment is required
- You want enterprise support from Kong Inc
- Multi-protocol routing beyond LLM is needed

**When to Choose AI Native Gateway**:
- You need LLM-specific gateway without general API gateway overhead
- Built-in credential health and routing analytics are priorities
- Simpler deployment for LLM-only use case
- Embedded admin UI is preferred over separate management services
- Cost-effective for small to medium LLM traffic

### OmniRoute

**Repository**: https://github.com/Rawbeew/omniroute (public project; feature notes below follow OmniRoute's own README claims — we have not independently verified its provider counts or savings figures)

**What OmniRoute Does Best** (per its public documentation):
- Self-hosted Node.js + SQLite architecture with a JS-based provider execution layer
- Large multi-provider catalog (project-declared 290+ providers)
- Request-aware routing strategies: cost, cache, context, and headroom scoring
- Multi-stage context compression pipeline
- Built-in MCP registry/policy concepts, A2A task semantics, and Fusion (multi-model coordination) as roadmap items
- Savings-oriented cost tracking

**What AI Native Gateway Emphasizes Differently**:
- **Go + PostgreSQL/Redis vs Node + SQLite**: compiled data plane, RLS-enforced multi-tenancy, and horizontal-grade storage instead of a single-node embedded database
- **Two-layer routing**: explicit model-selection layer (work-type classification + scoring) on top of credential selection — routing policy is observable and configurable per work type in the admin UI
- **Operational observability built in**: live request stream, credential health matrix, task×model heatmaps, Sankey flow, and request-level compression/masking audit as first-class UI
- **Tenancy and quotas**: tenant/user/API-key hierarchy with database-enforced isolation, per-tenant quotas and cost caps
- **Health-aware dynamic switching**: continuous credential scoring with automatic degradation and recovery

**Transparent disclosure — design influence**: AI Native Gateway's design explicitly studied OmniRoute as a reference (tracked in our internal integration-boundary review, snapshot 2026-08-21). We adopted *semantics* — cost/cache/context/headroom scoring dimensions, staged context compression, and MCP/A2A direction — through our own boundary review, and re-implemented them in Go on PostgreSQL; no Node/SQLite/JS-VM mechanics were ported, and upstream's declared numbers are treated as claims, not verified facts.

**When to Choose OmniRoute**:
- You want a Node.js-based self-hosted gateway with an embedded database
- Its provider catalog and compression staging match your workflow
- You prefer its JS execution model and tooling ecosystem

**When to Choose AI Native Gateway**:
- You need Go performance with PostgreSQL-grade durability and RLS multi-tenancy
- Database-enforced tenant isolation and per-tenant quotas are requirements
- You want deep routing/credential observability in an embedded admin UI
- You prefer a gateway whose advanced routing policies hot-reload without restarts

## Architecture Philosophy

| Aspect | AI Native Gateway | Typical Alternatives |
|--------|-------------------|---------------------|
| **Data Plane** | Single Go binary with embedded UI | Proxy + separate dashboard services |
| **Storage** | PostgreSQL + Redis | Various (some cloud-native KV stores) |
| **Multi-Tenancy** | Database-level (RLS) | Application-level or namespace-based |
| **Observability** | Built-in Prometheus + UI | Plug-in ecosystem or external SaaS |
| **Credential Mgmt** | Built-in health checks + rotation | Manual or external secrets manager |
| **Admin Experience** | Embedded Vue SPA (single binary) | Separate frontend deployment |

## Use Case Fit

**AI Native Gateway is Best For**:
- Organizations requiring full data residency control
- Multi-tenant SaaS platforms building on LLMs
- Teams with Go/PostgreSQL/Redis operational expertise
- Cost-sensitive deployments (no per-token fees)
- Private cloud or on-premises requirements
- Need for deep observability without SaaS dependencies

**LiteLLM is Best For**:
- Rapid prototyping with many providers
- Python-centric tech stacks
- Teams wanting maximum provider coverage
- Simpler deployment with fewer components

**OmniRoute is Best For**:
- Node.js-centric teams wanting a self-hosted gateway
- Single-machine deployments where an embedded database is acceptable
- Users aligned with its provider catalog and compression pipeline design

**Portkey/SaaS Gateways are Best For**:
- Teams wanting zero operational overhead
- Enterprise features like prompt management
- Willingness to use managed services
- Budget for SaaS/per-token pricing

**Kong AI Gateway is Best For**:
- Existing Kong Gateway users
- Multi-protocol API management needs
- Kubernetes-native architecture
- Enterprise support requirements

## Non-Goals

AI Native Gateway intentionally does **not** aim to:
- Replace general-purpose API gateways (Kong, Nginx)
- Provide managed SaaS platform
- Host or train models (use Ollama/vLLM/cloud providers)
- Manage vector databases (use dedicated solutions)

## Benchmark Disclaimer

We do not provide performance benchmarks in this comparison because:
1. Performance varies significantly by deployment environment
2. Workload characteristics differ across use cases
3. Fair comparison requires controlled conditions
4. Claims without reproducible methodology can be misleading

Users should conduct their own benchmarks with representative workloads.

## Further Information

- **LiteLLM**: https://docs.litellm.ai/
- **OmniRoute**: https://github.com/Rawbeew/omniroute
- **Portkey**: https://portkey.ai/docs
- **Kong AI Gateway**: https://docs.konghq.com/gateway/latest/
- **AI Native Gateway**: See [Getting Started](getting-started.md)

---

**Comparison Methodology**: Based on publicly available documentation, README files, and GitHub repositories as of 2026-09-06. Features marked as "available" are documented by respective projects; we have not independently verified all functionality. This comparison focuses on architectural differences and deployment models rather than subjective "better/worse" judgments.
