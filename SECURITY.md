# Security Policy

## Supported Versions

Security updates are provided for the following versions:

| Version | Supported |
|---------|-----------|
| `main` branch | ✅ Active development |
| Latest 3 minor releases | ✅ Security fixes |
| Older versions | ❌ No longer supported |

---

## Reporting a Vulnerability

We take security seriously. **Please do NOT report security vulnerabilities through public GitHub issues.**

### Private Reporting (Recommended)

Use GitHub's private vulnerability reporting:

1. Go to [Security Advisories](https://github.com/halfking/ai-native-gateway-core/security/advisories)
2. Click "Report a vulnerability"
3. Fill in the details

### Email Reporting

Alternatively, email: **security@example.com** (TODO: Update with actual contact)

### What to Include

Please provide:

1. **Description**: Brief summary of the vulnerability
2. **Reproduction Steps**: Detailed steps to reproduce
3. **Impact**: Which components/versions are affected
4. **Severity**: Your assessment (CVSS score if possible)
5. **Disclosure Timeline**: Your intended disclosure date (if any)

### Response Timeline

| Stage | Timeframe |
|-------|-----------|
| Initial acknowledgment | 3 business days |
| Impact assessment | 5 business days |
| Fix development & testing | Severity-dependent:<br>🔴 Critical (RCE/Auth bypass): 24-72h<br>🟡 Medium: 14 days<br>🟢 Low: Next release |
| Public disclosure | After fix is released |

### Public Acknowledgment

After the fix is released, we will:
- Credit you in release notes (unless you prefer anonymity)
- Acknowledge your report in release notes (with your permission)
- Consider bounty/swag for critical findings (case-by-case)

---

## Security Best Practices

### Deployment Checklist

Before production deployment, verify:

- [ ] All credentials use strong random values (32+ bytes)
- [ ] `.env` files and secrets are in `.gitignore`
- [ ] Database credentials use strong passwords
- [ ] `LLM_GATEWAY_ADMIN_API_KEY` is a strong random value
- [ ] HTTPS/TLS is enabled (reverse proxy or ingress)
- [ ] Database connections use TLS (remove `sslmode=disable` for production)
- [ ] Audit logging is enabled (not disabled)
- [ ] Regular backups are configured
- [ ] Credential rotation schedule is defined
- [ ] Monitoring and alerting is set up

### Environment Variables

**Never commit these to version control:**
- `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY`
- `LLM_GATEWAY_SECRET_KEY`
- `LLM_GATEWAY_API_KEY`
- `LLM_GATEWAY_ADMIN_API_KEY`
- `POSTGRES_PASSWORD`
- `REDIS_PASSWORD`
- Database connection strings with embedded passwords

Use environment variables, secrets managers (Vault, Kubernetes Secrets), or SOPS encryption.

### Network Security

- Bind services to `127.0.0.1` (localhost) by default
- Use reverse proxy (Nginx, Traefik) for external access
- Enable TLS/HTTPS for all external connections
- Use firewall rules to restrict database/Redis access
- Consider VPN or private network for multi-node deployments

### Database Security

- Enable PostgreSQL RLS (Row-Level Security) - **required for multi-tenancy**
- Use strong database passwords (20+ characters)
- Enable TLS for PostgreSQL connections
- Restrict database user permissions (no SUPERUSER)
- Regular backup and test restore procedures
- Monitor for unusual query patterns

### Credential Management

- Upstream provider credentials are encrypted at rest (Fernet/AES-256-GCM)
- Rotation is manual by default - establish a rotation schedule
- Use dedicated service accounts per provider when possible
- Monitor credential usage for anomalies
- Revoke credentials immediately when compromised

---

## Known Security Considerations

### Multi-Tenancy Enforcement

Multi-tenancy isolation relies on PostgreSQL RLS. **If RLS is disabled or bypassed, tenant isolation is compromised.**

Verify RLS is active:
```sql
SELECT schemaname, tablename, rowsecurity 
FROM pg_tables 
WHERE schemaname = 'public' 
  AND rowsecurity = false;
```

Should return no rows for tenant-scoped tables.

### API Key Security

- Data plane API keys are tenant/user-scoped
- Admin API keys have elevated privileges - guard carefully
- Keys are validated on every request
- Rotate keys regularly (30-90 days recommended)

### Sensitive Data Logging

Request/response bodies are logged by default for audit. Ensure:
- Logs are stored securely (encrypted at rest)
- Log access is restricted
- Sensitive fields are masked (implemented via `secretmask` package)
- Log retention complies with your data policy

---

## Vulnerability Disclosure Policy

### Coordinated Disclosure

We follow **coordinated disclosure**:
1. Researcher reports vulnerability privately
2. We acknowledge and assess impact
3. We develop and test fix
4. We release fix and advisory
5. Public disclosure after fix is available

### Embargo Period

Default embargo: **90 days** from initial report or until fix is released (whichever is sooner).

If you need to disclose earlier, please discuss with us first.

---

## Security Tooling

### Secret Scanning

Run before every commit to public repositories:

```bash
bash scripts/scan-secrets.sh --mode=strict
```

### Dependency Scanning

Check for vulnerable dependencies:

```bash
go list -m -json all | nancy sleuth
# or
govulncheck ./...
```

### Static Analysis

```bash
golangci-lint run --timeout=5m
```

---

## Past Security Advisories

See [Security Advisories](https://github.com/halfking/ai-native-gateway-core/security/advisories) for published CVEs and fixes.

---

## Contact

- **Security Reports**: security@example.com (TODO: Update)
- **Project Repository**: https://github.com/halfking/ai-native-gateway-core
- **General Support**: See [SUPPORT.md](SUPPORT.md)
