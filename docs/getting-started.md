# Getting Started with AI Native Gateway

This guide will help you deploy AI Native Gateway locally in under 10 minutes.

## Prerequisites

- Docker and Docker Compose (or Podman with compose)
- 4GB+ RAM available
- 10GB+ disk space
- PostgreSQL 14+ and Redis 7+ (included in quick start stack)

## Quick Start with Docker Compose

### 1. Clone and Configure

```bash
# Clone the repository
git clone https://github.com/halfking/ai-native-gateway-core.git
cd ai-native-gateway-core

# Copy and edit environment file
cp .env.quickstart.example .env

# Generate secure random keys
export POSTGRES_PASSWORD=$(openssl rand -hex 16)
export REDIS_PASSWORD=$(openssl rand -hex 16)
export CREDENTIAL_ENCRYPTION_KEY=$(openssl rand -hex 32)
export SECRET_KEY=$(openssl rand -base64 32)
export API_KEY=sk-$(openssl rand -hex 16)
export ADMIN_API_KEY=admin-$(openssl rand -hex 16)

# Save to .env file
cat > .env << ENVEOF
POSTGRES_PASSWORD=$POSTGRES_PASSWORD
REDIS_PASSWORD=$REDIS_PASSWORD
CREDENTIAL_ENCRYPTION_KEY=$CREDENTIAL_ENCRYPTION_KEY
SECRET_KEY=$SECRET_KEY
API_KEY=$API_KEY
ADMIN_API_KEY=$ADMIN_API_KEY
ENVEOF

echo "✓ Generated secure keys in .env"
```

### 2. Start Services

```bash
docker-compose -f docker-compose.quickstart.yml up -d
```

This starts:
- PostgreSQL (localhost:5432)
- Redis (localhost:6379) 
- AI Native Gateway (localhost:8781)

### 3. Verify Health

```bash
# Check gateway health
curl http://localhost:8781/healthz
# Expected: {"status":"ok"}

# Check readiness (DB + Redis)
curl http://localhost:8781/readyz
# Expected: {"status":"ready"}

# Check version
curl http://localhost:8781/version
```

### 4. Initialize Database

On first startup, the gateway will automatically run migrations. Check logs:

```bash
docker-compose -f docker-compose.quickstart.yml logs gateway | grep migration
```

### 5. Send Your First Request

```bash
# OpenAI-compatible chat completion
curl http://localhost:8781/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

**Note**: You need to configure upstream provider credentials before requests succeed. See [Environment & Configuration](environment.md).

### 6. Access Admin UI

Open http://localhost:8781/admin in your browser.

Default admin credentials are not set in quick start mode. See [Quick Reference](QUICK_REFERENCE.md) for creating the first admin user.

## Next Steps

- **Configure Providers**: Add OpenAI, Anthropic, or other provider credentials in Admin UI → Providers
- **Multi-Tenant Setup**: Create tenants and API keys in Admin UI → Tenants
- **Routing**: Configure intelligent routing policies
- **Monitoring**: View real-time request streams and metrics

## Common Issues

### "Database connection failed"

Wait 10-15 seconds for PostgreSQL to fully start, then check:

```bash
docker-compose -f docker-compose.quickstart.yml logs postgres
```

### "Redis connection refused"

Ensure Redis is healthy:

```bash
docker-compose -f docker-compose.quickstart.yml ps redis
```

### Ports already in use

Change port mappings in `docker-compose.quickstart.yml`:

```yaml
ports:
  - "127.0.0.1:8782:8781"  # Use 8782 instead of 8781
```

## Stopping and Cleanup

```bash
# Stop services (keeps data)
docker-compose -f docker-compose.quickstart.yml down

# Remove all data (CAUTION: deletes database)
docker-compose -f docker-compose.quickstart.yml down -v
```

## Production Deployment

This quick start is **not production-ready**. For production:

- Use external managed PostgreSQL and Redis
- Enable TLS/HTTPS
- Configure proper secrets management
- Set up backups and monitoring
- Review [Production Deployment](deployment/)

## Support

- [Troubleshooting](troubleshooting/)
- [Environment & Configuration](environment.md)
- [GitHub Issues](https://github.com/halfking/ai-native-gateway-core/issues)
