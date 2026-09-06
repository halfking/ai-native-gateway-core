# Smart Container Discovery Implementation

## Date: 2026-09-06

## Overview
Enhanced the local deployment script with intelligent PostgreSQL and Redis container discovery that validates connectivity and database availability before use.

## New Strategy

### PostgreSQL Discovery Priority
1. **llm-gateway-pg** (highest priority)
2. **Environment variable** `LLM_GATEWAY_PG_CONTAINER`
3. **Common names**: postgres, kx-citus
4. **Image scan**: Any container with pg17 image

### Redis Discovery Priority
1. **llm-gateway-redis** (highest priority)
2. **Environment variable** `LLM_GATEWAY_REDIS_CONTAINER`
3. **Common names**: redis, kx-redis, nbjl-redis, memora-redis
4. **Image scan**: Any container with redis/valkey image or port 6379

## Validation Logic

### PostgreSQL Container Validation
For each discovered container, the script:
1. **Starts container** if not running
2. **Checks port mapping** - must have host port exposed
3. **Tests connectivity** - attempts to connect with credentials
4. **Checks database** - looks for `llm_gateway` database
5. **Creates database** if missing (and connection works)

### Redis Container Validation
For each discovered container, the script:
1. **Starts container** if not running
2. **Tests PING** - attempts `redis-cli PING` command
3. **Falls back to port check** if redis-cli not available
4. **Verifies usability** before accepting

## Database Creation

When a PostgreSQL container is found but lacks the `llm_gateway` database:
- Tests if we can connect with existing credentials
- If yes: Creates `llm_gateway` database
- Creates `llm_gateway` user if needed
- Grants all privileges on the database
- Stores credentials for deployment

## Implementation

### File Structure
```
scripts/
├── deploy-local.sh                    # Main script with integration
└── deploy-lib/
    └── smart-discovery.sh            # Smart discovery functions
```

### Key Functions

#### `detect_postgres_container()`
Searches for usable PostgreSQL containers with priority order

#### `pg_container_usable(container_name)`
Validates if a PostgreSQL container can be used:
- Checks connectivity
- Verifies or creates `llm_gateway` database
- Returns 0 if usable, 1 if not

#### `detect_redis_container()`
Searches for usable Redis containers with priority order

#### `redis_container_usable(container_name)`
Validates if a Redis container can be used:
- Tests PING command
- Falls back to port inspection
- Returns 0 if usable, 1 if not

#### `configure_postgres_container()`
Configures PostgreSQL connection:
- Creates database if needed
- Sets up user and permissions
- Exports connection string environment variables

#### `configure_redis_container()`
Configures Redis connection:
- Determines connection address
- Exports Redis address environment variable

### Integration

The `detect_existing_containers()` function now:
1. Sources `smart-discovery.sh` if available
2. Runs smart discovery for PostgreSQL and Redis
3. Falls back to legacy discovery if smart-discovery.sh not found
4. Provides backward compatibility

## Usage Examples

### Standard Deployment (Auto-Discovery)
```bash
./scripts/deploy-local.sh deploy
# Output:
# [deploy-local] PostgreSQL: container llm-gateway-pg has llm_gateway database ✓
# [deploy-local] PostgreSQL: using priority container llm-gateway-pg
# [deploy-local] Redis: container nbjl-redis is usable ✓
# [deploy-local] Redis: using common name container nbjl-redis
```

### Custom Container Names
```bash
# PostgreSQL
export LLM_GATEWAY_PG_CONTAINER=my-postgres-17
./scripts/deploy-local.sh deploy

# Redis
export LLM_GATEWAY_REDIS_CONTAINER=my-redis
./scripts/deploy-local.sh deploy
```

### Check Current Discovery
```bash
./scripts/deploy-local.sh status
# Shows discovered containers and validation results
```

## Benefits

### 1. Automatic Database Creation
- No manual setup required
- Uses any existing pg17 container
- Creates `llm_gateway` database automatically

### 2. Connection Validation
- Only uses containers that actually work
- Tests connectivity before proceeding
- Skips broken or inaccessible containers

### 3. Flexible Container Names
- Works with any container name
- Priority-based discovery
- Environment variable override

### 4. Better Error Messages
- Clear logging of discovery process
- Shows why containers are rejected
- Helps debugging connection issues

### 5. Backward Compatibility
- Falls back to legacy discovery
- No breaking changes
- Works with existing deployments

## Discovery Flow

### PostgreSQL Discovery Flow
```
1. Check llm-gateway-pg
   ├─ Found & Usable? → Use it ✓
   └─ Not found/unusable → Continue

2. Check LLM_GATEWAY_PG_CONTAINER env var
   ├─ Set & Usable? → Use it ✓
   └─ Not set/unusable → Continue

3. Check common names (postgres, kx-citus)
   ├─ Found & Usable? → Use it ✓
   └─ Not found/unusable → Continue

4. Scan for pg17 images
   ├─ Found & Usable? → Use it ✓
   └─ Not found → Create new container
```

### Redis Discovery Flow
```
1. Check llm-gateway-redis
   ├─ Found & Usable? → Use it ✓
   └─ Not found/unusable → Continue

2. Check LLM_GATEWAY_REDIS_CONTAINER env var
   ├─ Set & Usable? → Use it ✓
   └─ Not set/unusable → Continue

3. Check common names (redis, kx-redis, nbjl-redis, memora-redis)
   ├─ Found & Usable? → Use it ✓
   └─ Not found/unusable → Continue

4. Scan for redis/valkey images
   ├─ Found & Usable? → Use it ✓
   └─ Not found → Create new container
```

## Testing Results

### Test 1: Priority Container Discovery
```bash
$ ./scripts/deploy-local.sh status
[deploy-local] PostgreSQL: container llm-gateway-pg has llm_gateway database ✓
[deploy-local] PostgreSQL: using priority container llm-gateway-pg
[deploy-local] Redis: container nbjl-redis is usable ✓
[deploy-local] Redis: using common name container nbjl-redis
✅ PASSED
```

### Test 2: Database Validation
- Verified containers are tested for connectivity
- Database existence is checked before use
- Unusable containers are skipped

### Test 3: Database Creation
- Automatically creates `llm_gateway` database
- Creates user with proper permissions
- Handles existing database gracefully

### Test 4: Full Deployment
- Successfully deployed with discovered containers
- All health checks passed
- Credential verification succeeded

## Troubleshooting

### PostgreSQL Not Discovered
1. Check if container is running: `docker ps`
2. Check if port 5432 is mapped: `docker port <container>`
3. Check credentials allow connection
4. Check logs: `./scripts/deploy-local.sh status`

### Redis Not Discovered
1. Check if container is running: `docker ps`
2. Check if port 6379 is mapped: `docker port <container>`
3. Test PING: `docker exec <container> redis-cli PING`
4. Check logs: `./scripts/deploy-local.sh status`

### Database Creation Failed
1. Check PostgreSQL logs: `docker logs <pg-container>`
2. Verify user has CREATE DATABASE permission
3. Check if database already exists with different owner
4. Try manual creation: `docker exec -it <pg-container> psql -U postgres -c "CREATE DATABASE llm_gateway"`

## Migration Guide

### From Hardcoded Names
Old:
```bash
# Had to rename container to llm-gateway-pg or nbjl-redis
docker rename my-postgres llm-gateway-pg
```

New:
```bash
# Just set environment variable
export LLM_GATEWAY_PG_CONTAINER=my-postgres
export LLM_GATEWAY_REDIS_CONTAINER=my-redis
./scripts/deploy-local.sh deploy
```

### From Manual Database Setup
Old:
```bash
# Had to manually create database
docker exec -it llm-gateway-pg psql -U postgres -c "CREATE DATABASE llm_gateway"
./scripts/deploy-local.sh deploy
```

New:
```bash
# Automatic database creation
./scripts/deploy-local.sh deploy
# Database created automatically if needed
```

## Future Enhancements

1. **PostgreSQL Cluster Support**: Detect and use pg_pool or patroni clusters
2. **Redis Sentinel**: Support Redis Sentinel for high availability
3. **Health Monitoring**: Periodic health checks of discovered containers
4. **Multi-Database**: Support for multiple databases per instance
5. **Connection Pooling**: Auto-configure pgbouncer if available

## Related Documentation

- [DEPLOYMENT_OPTIMIZATION_SUMMARY.md](./DEPLOYMENT_OPTIMIZATION_SUMMARY.md) - Previous optimization work
- [DEPLOYMENT_QUICK_REFERENCE.md](./DEPLOYMENT_QUICK_REFERENCE.md) - Usage examples
- [DEPLOYMENT_COMPLETION_REPORT.md](./DEPLOYMENT_COMPLETION_REPORT.md) - First phase completion

## Conclusion

The smart discovery feature significantly improves the deployment experience by:
- Automatically finding and validating containers
- Creating databases as needed
- Providing clear feedback on discovery results
- Maintaining backward compatibility

All functionality is production-ready and has been verified through testing.
