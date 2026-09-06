# Contributing to AI Native Gateway

Thank you for considering contributing! 🎉

This guide will help you get started with development, testing, and submitting changes.

---

## Code of Conduct

Please read and follow our [Code of Conduct](CODE_OF_CONDUCT.md).

---

## Quick Start for Contributors

### 1. Development Environment

**Prerequisites:**
- Go 1.21+ ([install](https://go.dev/dl/))
- PostgreSQL 14+ (local or Docker)
- Redis 7+ (local or Docker)
- Node.js 18+ and pnpm 8+ (for frontend)
- Git

**Setup:**

```bash
# Fork the repository on GitHub, then:
git clone https://github.com/YOUR_USERNAME/ai-native-gateway-core.git
cd ai-native-gateway-core

# Start local dependencies
docker-compose -f docker-compose.quickstart.yml up -d postgres redis

# Run database migrations
make db-migrate  # or ./scripts/db-migrate.sh

# Build and run
go build -o gateway ./cmd/gateway
./gateway
```

### 2. Run Tests

```bash
# Unit tests
make test

# With race detector (recommended before PR)
make test-race-core

# Frontend tests
cd web && pnpm install && pnpm test
```

### 3. Make Changes

```bash
git checkout -b feature/your-feature-name

# Make your changes...

# Format and lint
go fmt ./...
golangci-lint run

# Run tests
make test
```

### 4. Submit Pull Request

```bash
git push origin feature/your-feature-name
# Then open PR on GitHub
```

---

## Development Workflow

### Branch Naming

- `feature/` - New features
- `fix/` - Bug fixes
- `docs/` - Documentation only
- `refactor/` - Code refactoring
- `test/` - Test improvements

Examples:
- `feature/add-ollama-provider`
- `fix/routing-deadlock`
- `docs/update-architecture-diagram`

### Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <short description>

[optional body]

[optional footer]
```

**Types:**
- `feat` - New feature
- `fix` - Bug fix
- `docs` - Documentation changes
- `refactor` - Code refactoring
- `test` - Test additions/improvements
- `chore` - Build, dependencies, tooling

**Examples:**

```
feat(routing): add cost-aware credential selection

Implements P2C routing with cost weighting. When multiple 
credentials are available, prefer lower-cost options while
maintaining health-based failover.

Closes #123
```

```
fix(streaming): prevent goroutine leak on client disconnect

Ensure context cancellation propagates to upstream request
when client disconnects mid-stream.
```

---

## Code Guidelines

### Go Style

- Follow [Effective Go](https://go.dev/doc/effective_go)
- Use `gofmt` (automatic with most editors)
- Write doc comments for exported symbols
- Keep functions small and focused
- Prefer explicit error handling over panics

**Example:**

```go
// ProcessRequest handles an incoming LLM request with routing and retry logic.
// It returns the response or an error if all retry attempts fail.
func ProcessRequest(ctx context.Context, req *Request) (*Response, error) {
    if err := validateRequest(req); err != nil {
        return nil, fmt.Errorf("validation failed: %w", err)
    }
    // ...
}
```

### Package Structure

```
cmd/          # Main applications
  gateway/    # Gateway server entry point
domains/      # Business logic domains
  streaming/  # Request streaming and routing
  credential/ # Credential management
  tenant/     # Multi-tenancy
admin/        # Admin API handlers
web/          # Vue.js frontend
docs/         # Documentation
```

### Testing Requirements

**Unit Tests:**
- Test file name: `*_test.go`
- Function name: `TestFunctionName(t *testing.T)`
- Use table-driven tests for multiple cases
- Mock external dependencies

**Example:**

```go
func TestRouterSelect(t *testing.T) {
    tests := []struct {
        name    string
        input   []Credential
        want    *Credential
        wantErr bool
    }{
        {
            name:  "selects healthy credential",
            input: []Credential{{ID: 1, Healthy: true}},
            want:  &Credential{ID: 1, Healthy: true},
        },
        // ...
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := RouterSelect(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("unexpected error: %v", err)
            }
            if !reflect.DeepEqual(got, tt.want) {
                t.Errorf("got %v, want %v", got, tt.want)
            }
        })
    }
}
```

### Frontend Guidelines

**Location:** `web/`

- Vue 3 + TypeScript + Composition API
- Element Plus for UI components
- ECharts for visualizations
- Follow existing component structure

**Commands:**

```bash
cd web
pnpm install        # Install dependencies
pnpm dev            # Development server
pnpm build          # Production build
pnpm typecheck      # Type checking
pnpm test           # Run tests
```

---

## Multi-Tenancy Requirements

**CRITICAL**: All database operations must respect tenant isolation.

### Rules

1. **Every tenant-scoped table has `tenant_id UUID NOT NULL`**
2. **PostgreSQL RLS is enabled on all tenant tables**
3. **Queries use `tenant_id` filter or rely on RLS**
4. **Cross-tenant reads must return `ErrNotFound`, not 403**

### Verification

Before PR, run:

```bash
# Check RLS is enabled on new tables
make lint-pg-rls

# Check tenant scope compliance
make lint-tenant-scope-llmgw

# Check OTel tenant attributes
make lint-otel-tenant
```

### Example

```go
// ❌ BAD: No tenant filtering
rows, err := db.Query("SELECT * FROM api_keys WHERE user_id = $1", userID)

// ✅ GOOD: Explicit tenant filter (or rely on RLS)
rows, err := db.Query(
    "SELECT * FROM api_keys WHERE tenant_id = $1 AND user_id = $2",
    tenantID, userID,
)
```

---

## Pull Request Process

### Before Opening PR

- [ ] Tests pass locally (`make test`)
- [ ] Code is formatted (`go fmt ./...`)
- [ ] Linters pass (`golangci-lint run`)
- [ ] Multi-tenancy linters pass (if applicable)
- [ ] Documentation updated (if adding features)
- [ ] Commit messages follow Conventional Commits

### PR Description Template

```markdown
## Description
Brief description of changes.

## Type of Change
- [ ] Bug fix
- [ ] New feature
- [ ] Breaking change
- [ ] Documentation update

## Testing
How have you tested this? Please describe.

## Checklist
- [ ] Tests added/updated
- [ ] Documentation updated
- [ ] Multi-tenancy compliance verified (if DB changes)
- [ ] No sensitive data in code/logs
```

### Review Process

1. Automated CI checks must pass
2. At least one maintainer review required
3. Multi-tenancy changes require security review
4. Squash merge to `main` after approval

---

## Getting Help

- **Questions**: Open a [GitHub Discussion](https://github.com/halfking/ai-native-gateway-core/discussions)
- **Bugs**: Open a [GitHub Issue](https://github.com/halfking/ai-native-gateway-core/issues)
- **Chat**: Join discussions in issue comments

---

## Documentation

When adding features, update:
- `/docs/` - Architecture, configuration, guides
- `README.md` - If adding major capability
- `CHANGELOG.md` - For release notes
- Code comments - Explain complex logic

---

## Release Process

**For Maintainers Only**

1. Update `VERSION` file
2. Update `CHANGELOG.md`
3. Create Git tag: `git tag v2.x.x`
4. Push tag: `git push origin v2.x.x`
5. GitHub Actions builds release artifacts
6. Create GitHub Release with notes

---

## License

By contributing, you agree that your contributions will be licensed under the [Apache License 2.0](LICENSE).

---

## Recognition

Contributors are listed in release notes and [CONTRIBUTORS.md](CONTRIBUTORS.md).

Thank you for making AI Native Gateway better! 🚀
