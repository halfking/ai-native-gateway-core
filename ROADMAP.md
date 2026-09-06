# AI Native Gateway Roadmap

This roadmap outlines current capabilities, near-term plans, and long-term exploration areas.

## Now (Current Release)

### Core Gateway Features ✅
- OpenAI, Anthropic, Responses, Gemini protocol compatibility
- HTTP/SSE streaming relay with incremental integrity checks
- Multi-tenant isolation with PostgreSQL RLS
- Credential pool management and health monitoring
- Intelligent routing with sticky sessions
- Token-based rate limiting and quota management
- Request/response audit trail with OTel/Prometheus integration
- Vue.js admin console (embedded SPA)

### Deployment & Operations ✅
- Docker Compose quick start
- Binary + systemd deployment
- Kubernetes manifests (test-grade, not production-validated)
- Health endpoints (/healthz, /readyz, /version)
- Database migrations
- Backup and rollback support

### Developer Experience ✅
- Go 1.21+ codebase with comprehensive tests
- Makefile targets for common tasks
- GitHub Actions CI (tests, integration, security)
- Multi-arch support (amd64, arm64)

## Next (3-6 Months)

### Enhanced Routing 🚧
- Cost-aware routing with real-time pricing
- Quality-based routing (model performance scoring)
- Context window optimization (automatic prompt compression)
- Advanced P2C (Power of Two Choices) with performance history

### Observability 🚧
- Enhanced credential monitor heatmap
- Route decision replay and debugging
- Session forensics and analysis tools
- Grafana dashboard templates

### Deployment Improvements 🚧
- Production-grade Kubernetes Helm charts
- One-command binary installer improvements
- Migration tooling for PostgreSQL schema updates
- High-availability configuration examples

### API Enhancements 🚧
- Enhanced error reporting with retry guidance
- Request priority and QoS levels
- Bulk request APIs
- Webhook support for async notifications

## Exploring (6-12+ Months)

These are research and exploration areas. No timeline or commitment to delivery.

### Agent-to-Agent (A2A) Protocol 🔬
- Cross-agent session context sharing
- Structured agent collaboration protocol
- Identity and capability negotiation

### Model Context Protocol (MCP) Gateway 🔬
- MCP server/client integration
- Tool discovery and invocation routing
- Context fusion across multiple tools

### Advanced Features Under Research 🔬
- Session V2 ownership migration (currently shadow mode)
- Operator pattern for Kubernetes (CRD-based deployment)
- Fusion routing (multi-model consensus)
- On-premise model support (Ollama, vLLM)
- Request caching beyond simple semantic cache

### Ecosystem 🔬
- Plugin system for custom authentication
- Webhook-based provider adapters
- Multi-region routing
- Edge deployment support

## Non-Goals

To maintain focus, the following are explicitly out of scope:

- ❌ Fine-tuning or model training infrastructure
- ❌ Vector database management (use dedicated solutions)
- ❌ General-purpose API gateway (Kong/Nginx)
- ❌ LLM model hosting (use Ollama/vLLM/provider APIs)

## Contributing to the Roadmap

We welcome community input! 

- **Vote** on existing feature requests in [GitHub Issues](https://github.com/halfking/ai-native-gateway-core/issues)
- **Propose** new features via feature request template
- **Contribute** implementations for Next-tier features

Priority is determined by:
1. Community demand (issue votes/comments)
2. Core maintainer bandwidth
3. Alignment with project vision
4. Implementation complexity vs value

---

**Legend:**
- ✅ Shipped and supported
- 🚧 Actively being developed or planned for next release
- 🔬 Research/exploration phase, no delivery commitment

Last updated: 2026-09-06
