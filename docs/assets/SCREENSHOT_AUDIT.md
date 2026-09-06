# Screenshot Audit for Public Release

## Audit Criteria

Before including any screenshot in public documentation:

- [ ] No real user names, emails, or personal information
- [ ] No real API keys, credentials, or tokens
- [ ] No internal IP addresses or hostnames
- [ ] No production request IDs or session IDs
- [ ] No real tenant names or company identifiers
- [ ] No sensitive request/response content
- [ ] No internal domain names (*.kxpms.cn, internal URLs)
- [ ] Image quality suitable for public display
- [ ] Demonstrates actual implemented feature

## Screenshots Status

### Approved for Public Use

None yet - all existing screenshots require manual review or regeneration with demo data.

### Requires Review

All screenshots in:
- `gui-test-screenshots/*.png` (9 files)
- `docs/screenshots/*.png` (17+ files)

### Action Required

1. **Manual Review**: Inspect each screenshot for sensitive data
2. **Demo Data Setup**: Create sanitized demo environment
3. **Regenerate**: Capture new screenshots with:
   - Generic tenant names ("Demo Tenant", "Example Corp")
   - Fake but realistic request data
   - No real API keys or credentials
   - Localhost URLs only
   - Generic timestamps

### Priority Screenshots to Capture

For README and documentation:

1. **Dashboard - Real-time Request Stream**
   - Path: `/admin/dashboard` (Stream tab)
   - Shows: Live request cards with provider, model, status
   - Demo data: 5-10 synthetic requests

2. **Routing Dashboard - Sankey Diagram**
   - Path: `/admin/routing-v2`
   - Shows: Task → Model → Provider flow visualization
   - Demo data: Multiple task types routing to different models

3. **Credential Monitor - Health Heatmap**
   - Path: `/admin/routing-v2/credentials`
   - Shows: Credential × Model availability matrix
   - Demo data: 3-4 providers, 6-8 models

4. **Request Journey**
   - Path: `/admin/request-registry/journey/:id`
   - Shows: Request processing pipeline visualization
   - Demo data: Single request with queues and decisions

5. **Session Detail**
   - Path: `/admin/sessions/:id`
   - Shows: Multi-turn conversation view
   - Demo data: 3-4 turn conversation (generic content)

### Storage

Approved screenshots go in: `docs/assets/screenshots/`

File naming: `{feature}-{aspect}.png`
Examples:
- `dashboard-request-stream.png`
- `routing-sankey-diagram.png`
- `credentials-health-heatmap.png`

### Usage in Markdown

```markdown
![Dashboard Request Stream](docs/assets/screenshots/dashboard-request-stream.png)
*Real-time request monitoring with provider and model visibility*
```

---

**Current Status**: ⚠️ No screenshots approved for public release yet.

**Next Step**: Manual review of existing screenshots OR regenerate with demo data.
