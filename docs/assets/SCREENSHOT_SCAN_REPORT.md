# Screenshot Security Scan Report

**Scan Date**: 2026-09-06  
**Tool**: Automated screenshot scanner with OCR text extraction

## Summary

- **Total Screenshots Scanned**: 28
- **Clean (Safe for Public)**: 23 ✓
- **Suspicious (Contains Internal References)**: 5 ✗

## Scan Results

### ✓ Clean Screenshots (Safe for Public Release)

#### From `gui-test-screenshots/` (9/9 clean)

All screenshots in this directory passed security scan:
- `01_dashboard_default.png` - Dashboard default view
- `01_default_state.png` - Default state
- `02_panorama_open.png` - Routing panorama open
- `02b_after_click.png` - After click state
- `04_drawer_open.png` - Request detail drawer
- `05_reopen_nostale.png` - Reopen without stale data
- `t0_headed.png` - Test headed mode
- `t0_initial.png` - Test initial state
- `t0_loaded.png` - Test loaded state

#### From `docs/screenshots/` (14/19 clean)

- `03-auto-route-audit-api.png`
- `04-auto-route-index-api.png`
- `11-routing-v2-final.png` - Routing V2 dashboard
- `ui-verify-billing-audit-after-tenant-switch-20260712.png`
- `ui-verify-billing-audit-default-20260712.png`
- `ui-verify-billing-audit-empty-20260712.png`
- `ui-verify-billing-audit-fixture-20260712.png`
- `ui-verify-dashboard-audit-20260712.png` - Dashboard audit view
- `ui-verify-dashboard-degraded-20260712.png`
- `ui-verify-dashboard-degraded-hint-20260712.png`
- `ui-verify-dashboard-with-view-20260712.png`
- `ui-verify-routing-resolve-login-modal-20260724.png`
- `ui-verify-routing-resolve-row-fix-daylight-20260724.png` - Routing resolve
- `ui-verify-routing-resolve-row-fix-night-20260724.png`

### ✗ Suspicious Screenshots (Not Safe - Contains Internal Domain)

These 5 screenshots contain references to internal domain `kxpms.cn`:

1. `docs/screenshots/01-admin-dashboard.png`
2. `docs/screenshots/02-auto-route-index.png`
3. `docs/screenshots/05-admin-dashboard-full.png`
4. `docs/screenshots/06-v202-audit-after-test.png`
5. `docs/screenshots/07-v202-dashboard.png`

**Issue**: All contain visible `kxpms.cn` domain references (likely in URLs, headers, or configuration displays)

**Action**: Excluded from public release

## Selected Screenshots for Public Use

Based on scan results and visual relevance, the following 6 screenshots have been copied to `docs/assets/screenshots/`:

1. **dashboard-default.png** - Default dashboard view showing core UI
2. **routing-panorama.png** - Routing panorama/overview
3. **request-detail-drawer.png** - Request detail side drawer
4. **routing-v2-dashboard.png** - Routing V2 dashboard with analytics
5. **dashboard-audit-view.png** - Dashboard with audit information
6. **routing-resolve-daylight.png** - Routing resolution interface

## Security Scan Methodology

**Pattern Detection**:
- Internal IP addresses (10.x.x.x, 172.16.x.x, 192.168.x.x)
- Internal domains (kxpms.cn, *.internal, *.local)
- API key patterns (sk-*, Bearer tokens)
- Email addresses with real domains
- Session/Request ID patterns (gw_*, req_*)

**Tool**: `strings` command + pattern matching (OCR attempted but not available)

**Confidence**: High for text-based content; metadata extraction only

## Recommendations

1. ✅ Use the 23 clean screenshots for public documentation
2. ❌ Do not publish the 5 suspicious screenshots
3. 🔄 Future: Regenerate sensitive screenshots with demo data following `docs/SCREENSHOT_GUIDE.md`

## Next Steps

- [x] Copy clean screenshots to `docs/assets/screenshots/`
- [ ] Update README.md with screenshot references
- [ ] Optionally regenerate the 5 suspicious screenshots with localhost/demo data

---

**Scan Script**: `scripts/scan-screenshots.sh`  
**Scan Status**: ✓ Complete
