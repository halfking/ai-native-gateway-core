# Local Deployment Quick Reference

## Basic Usage

### Standard Deployment
```bash
# Full deployment with auto-discovery
./scripts/deploy-local.sh deploy

# Check status
./scripts/deploy-local.sh status

# View logs
./scripts/deploy-local.sh logs
```

### Minimal Deployment (Development Mode)
```bash
# Deploy without Redis (uses SQLite for sessions)
./scripts/deploy-local.sh deploy --minimal

# Check minimal deployment status
./scripts/deploy-local.sh status
# Output will show: redis=minimal, redis_container=none
```

## Redis Configuration

### Use Custom Redis Container
```bash
# Option 1: Environment variable
export LLM_GATEWAY_REDIS_CONTAINER=my-redis
./scripts/deploy-local.sh deploy

# Option 2: Inline
LLM_GATEWAY_REDIS_CONTAINER=memora-redis ./scripts/deploy-local.sh deploy
```

### Redis Auto-Discovery
The script automatically searches for Redis in this order:
1. **Environment variable**: `$LLM_GATEWAY_REDIS_CONTAINER`
2. **Common names**: nbjl-redis, llm-gateway-redis, redis, kx-redis
3. **Docker scan**: Any container with Redis-like image or port 6379
4. **System Redis**: Host Redis on ports 6379 or 16379

### Check Which Redis is Detected
```bash
# Dry run shows detected Redis container
./scripts/deploy-local.sh deploy --dry-run

# Status shows active Redis container
./scripts/deploy-local.sh status
```

## Common Scenarios

### Scenario 1: First Time Deployment
```bash
# Will auto-create PostgreSQL and Redis containers
./scripts/deploy-local.sh deploy
```

### Scenario 2: Using Existing Redis Container
```bash
# Script auto-detects and uses existing container
./scripts/deploy-local.sh deploy

# Or specify explicitly
LLM_GATEWAY_REDIS_CONTAINER=my-existing-redis ./scripts/deploy-local.sh deploy
```

### Scenario 3: Development Without Redis
```bash
# Minimal deployment for quick testing
./scripts/deploy-local.sh deploy --minimal --no-frontend
```

### Scenario 4: Test Deployment Plan
```bash
# See what will happen without making changes
./scripts/deploy-local.sh deploy --dry-run

# Test minimal mode
./scripts/deploy-local.sh deploy --minimal --dry-run
```

## Deployment Flags

| Flag | Description |
|------|-------------|
| `--minimal` | Deploy without Redis, use SQLite for sessions |
| `--dry-run` | Show deployment plan without executing |
| `--no-frontend` | Skip frontend build, reuse existing |
| `--root PATH` | Custom installation root |
| `--timeout SECS` | Health check timeout (default: 60) |
| `--cleanup-downloads` | Remove obsolete ~/Downloads copies |

## Environment Variables

### Redis Configuration
```bash
# Custom Redis container name
export LLM_GATEWAY_REDIS_CONTAINER=my-redis

# Custom Redis address (overrides auto-detection)
export LLM_GATEWAY_REDIS_ADDR=192.168.1.100:6379

# Redis password
export LLM_GATEWAY_REDIS_PASSWORD=secret

# Redis database number
export LLM_GATEWAY_REDIS_DB=5

# Redis host port mapping
export LLM_GATEWAY_REDIS_HOST_PORT=6380
```

### Database Configuration
```bash
# PostgreSQL connection string
export LLM_GATEWAY_DATABASE_URL=postgresql://user:pass@localhost:5432/dbname

# Alternative (compatibility)
export DATABASE_URL=postgresql://user:pass@localhost:5432/dbname
```

### Other Configuration
```bash
# Custom base images directory
export DOCKER_BASE_IMAGES_DIR=/path/to/images

# Custom Redis Docker image
export LLM_GATEWAY_REDIS_IMAGE=redis:7-alpine

# Custom PostgreSQL Docker image
export LLM_GATEWAY_PG_IMAGE=postgres:17-alpine

# Active listen port
export LLM_GATEWAY_ACTIVE_PORT=8782
```

## Troubleshooting

### Redis Not Detected
```bash
# Check running containers
docker ps --format '{{.Names}}\t{{.Image}}\t{{.Ports}}'

# Specify container explicitly
LLM_GATEWAY_REDIS_CONTAINER=your-redis-name ./scripts/deploy-local.sh status

# Use minimal mode if Redis not needed
./scripts/deploy-local.sh deploy --minimal
```

### Check Current Configuration
```bash
# Show full deployment status
./scripts/deploy-local.sh status

# See deployment plan
./scripts/deploy-local.sh deploy --dry-run
```

### Verify Redis Connection
```bash
# Check Redis container
docker ps | grep redis

# Test Redis connection
redis-cli -h 127.0.0.1 -p 6379 ping

# Check gateway logs for Redis errors
./scripts/deploy-local.sh logs | grep -i redis
```

### Reset to Minimal Mode
```bash
# Stop current deployment
./scripts/deploy-local.sh stop

# Deploy in minimal mode
./scripts/deploy-local.sh deploy --minimal

# Verify minimal mode active
./scripts/deploy-local.sh status
# Should show: redis=minimal
```

## Examples

### Example 1: Deploy with Memora Redis
```bash
LLM_GATEWAY_REDIS_CONTAINER=memora-redis ./scripts/deploy-local.sh deploy
```

### Example 2: Quick Development Build
```bash
./scripts/deploy-local.sh deploy --minimal --no-frontend
```

### Example 3: Deploy to Custom Root
```bash
./scripts/deploy-local.sh deploy --root /opt/my-gateway
```

### Example 4: Check Before Deploying
```bash
# See what will happen
./scripts/deploy-local.sh deploy --dry-run

# Check current state
./scripts/deploy-local.sh status

# Proceed with deployment
./scripts/deploy-local.sh deploy
```

## Getting Help

```bash
# Show all options
./scripts/deploy-local.sh --help

# View deployment summary
cat DEPLOYMENT_OPTIMIZATION_SUMMARY.md
```

## Migration from Old Scripts

If you're migrating from older deployment scripts:

1. **Redis container was hardcoded?**
   - Now auto-detected or set via `LLM_GATEWAY_REDIS_CONTAINER`

2. **Need SQLite instead of Redis?**
   - Use `--minimal` flag

3. **Multiple Redis instances on same host?**
   - Specify container name explicitly

4. **Testing deployments?**
   - Use `--dry-run` to preview changes

## Notes

- Minimal mode uses SQLite for session storage (suitable for dev/test only)
- Normal mode requires Redis for distributed sessions (production-ready)
- Auto-discovery searches Docker containers and host Redis instances
- All changes are backward compatible with existing deployments
