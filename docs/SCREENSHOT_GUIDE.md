# Screenshot Capture Guide for Public Release

## Setup Demo Environment

### 1. Start Clean Stack

```bash
# Use quickstart compose
docker-compose -f docker-compose.quickstart.yml down -v
docker-compose -f docker-compose.quickstart.yml up -d

# Wait for health
curl http://localhost:8781/healthz
```

### 2. Seed Demo Data

```bash
# Create demo tenant
curl -X POST http://localhost:8781/api/admin/tenants \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Demo Tenant",
    "slug": "demo-tenant"
  }'

# Add demo provider credentials
# (Use fake/demo keys only)

# Generate synthetic requests
# Run load test script with demo data
```

### 3. Access Admin UI

```bash
# Open browser
open http://localhost:8781/admin

# Login with demo admin account
```

## Capture Screenshots

### Browser Setup

- **Resolution**: 1920x1080 or 1728x1080
- **Browser**: Chrome/Firefox with clean profile
- **Zoom**: 100%
- **Dark/Light**: Capture both if theme-dependent

### Tools

**Option 1: Browser DevTools**
- Press F12 → Device Toolbar
- Set viewport size
- Take screenshot (Cmd+Shift+P → "Capture screenshot")

**Option 2: macOS Screenshot**
- Press Cmd+Shift+4 → Space → Click window
- Or Cmd+Shift+5 for screen recording

**Option 3: Playwright Script**
```javascript
const { chromium } = require('playwright');

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1728, height: 1080 } });
  
  await page.goto('http://localhost:8781/admin/dashboard');
  await page.waitForSelector('.request-stream-loaded');
  await page.screenshot({ path: 'dashboard-stream.png', fullPage: false });
  
  await browser.close();
})();
```

## Priority Screenshots

### 1. Dashboard - Request Stream

**URL**: `http://localhost:8781/admin/dashboard` (Stream tab active)

**What to Show**:
- 5-10 request cards in stream
- Different providers (OpenAI, Anthropic, etc.)
- Mix of success/error states
- Filter controls visible

**Caption**: "Real-time request monitoring with multi-dimensional filtering"

### 2. Routing Dashboard - Sankey

**URL**: `http://localhost:8781/admin/routing-v2`

**What to Show**:
- Sankey diagram with Task → Model → Provider flows
- At least 3 task types
- Multiple providers
- Flow thickness showing volume

**Caption**: "Routing analytics: visualize how requests flow from tasks to models to providers"

### 3. Credential Monitor

**URL**: `http://localhost:8781/admin/routing-v2/credentials`

**What to Show**:
- Heatmap: Credentials (rows) × Models (columns)
- Color coding for availability
- Some green (available), some red (unavailable)
- Latency numbers

**Caption**: "Credential health matrix: real-time availability and latency monitoring"

### 4. Request Journey

**URL**: `http://localhost:8781/admin/request-registry/journey/:id`

**What to Show**:
- Processing pipeline visualization
- Queue states
- Decision points
- Timeline

**Caption**: "Request journey visualization: trace a request through routing, queues, and execution"

### 5. Session Detail

**URL**: `http://localhost:8781/admin/sessions/:id`

**What to Show**:
- Multi-turn conversation
- Turn metadata (tokens, model, provider)
- Generic chat content (avoid real data)

**Caption**: "Session detail: inspect multi-turn conversations with token and cost tracking"

## Post-Processing

### Sanitization Check

Before committing:
- [ ] No real API keys visible
- [ ] No internal domains
- [ ] No personal information
- [ ] No production request IDs
- [ ] Localhost or generic URLs only

### Optimization

```bash
# Compress PNG (optional)
optipng docs/assets/screenshots/*.png

# Or use imagemagick
mogrify -quality 85 docs/assets/screenshots/*.png
```

### File Naming

- `dashboard-request-stream.png`
- `routing-sankey-diagram.png`
- `credentials-health-heatmap.png`
- `request-journey-pipeline.png`
- `session-detail-multiurn.png`

## Integration

### README

Add to README.md:

```markdown
## Screenshots

![Request Stream](docs/assets/screenshots/dashboard-request-stream.png)
*Real-time request monitoring*

![Routing Analytics](docs/assets/screenshots/routing-sankey-diagram.png)
*Sankey flow visualization*
```

### Docs

Add to getting-started.md or architecture.md as inline images.

---

**Important**: Never commit screenshots with real production data, credentials, or personal information.
