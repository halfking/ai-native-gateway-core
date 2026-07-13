#!/usr/bin/env python3
"""
verify_route_incident_ui.py — Playwright-based visual verification
for the Phase 1 read-only diagnosis drawer.

Captures desktop + mobile screenshots that prove:
  - the diagnose button appears on a swim lane
  - the "Other" lane shows a disabled control with tooltip
  - the drawer renders all 7 sections
  - the empty-data state is shown when timeline / findings are absent
  - mobile layout uses full viewport

Output files follow the spec naming convention:
  ui-verify-route-incident-{scenario}-{timestamp}.png

Usage:
  python3 verify_route_incident_ui.py
"""

import os
import sys
import time
from pathlib import Path

from playwright.sync_api import sync_playwright

REPO_ROOT = Path(__file__).resolve().parent
DEMO_HTML = REPO_ROOT / "route-incident-demo.html"
SCREENSHOTS_DIR = REPO_ROOT


def timestamp() -> str:
    return time.strftime("%Y%m%d-%H%M%S")


def main() -> int:
    if not DEMO_HTML.exists():
        print(f"ERROR: {DEMO_HTML} not found")
        return 1

    url = "file://" + str(DEMO_HTML)
    print(f"Loading {url}")

    with sync_playwright() as p:
        browser = p.chromium.launch()

        # Desktop viewport.
        context = browser.new_context(
            viewport={"width": 1280, "height": 900},
            device_scale_factor=2,
        )
        page = context.new_page()
        page.goto(url)
        page.wait_for_load_state("networkidle")
        ts = timestamp()

        # Scenario 1: active incident (failure streak = 3).
        page.locator("#case-active").screenshot(
            path=str(SCREENSHOTS_DIR / f"ui-verify-route-incident-active-{ts}.png")
        )
        print(f"  ✓ active-{ts}.png")

        # Scenario 2: recovering (Recovery 2/5).
        page.locator("#case-recovering").screenshot(
            path=str(SCREENSHOTS_DIR / f"ui-verify-route-incident-recovering-{ts}.png")
        )
        print(f"  ✓ recovering-{ts}.png")

        # Scenario 3: "Other" lane disabled.
        page.locator("#case-other").screenshot(
            path=str(
                SCREENSHOTS_DIR / f"ui-verify-route-incident-other-disabled-{ts}.png"
            )
        )
        print(f"  ✓ other-disabled-{ts}.png")

        # Scenario 4: insufficient data.
        page.locator("#case-empty").screenshot(
            path=str(
                SCREENSHOTS_DIR
                / f"ui-verify-route-incident-drawer-insufficient-data-{ts}.png"
            )
        )
        print(f"  ✓ drawer-insufficient-data-{ts}.png")

        # Full-page overview.
        page.screenshot(
            path=str(SCREENSHOTS_DIR / f"ui-verify-route-incident-overview-{ts}.png"),
            full_page=True,
        )
        print(f"  ✓ overview-{ts}.png")

        context.close()

        # Mobile viewport (375x812 — iPhone X).
        mobile = browser.new_context(
            viewport={"width": 375, "height": 812},
            device_scale_factor=3,
            is_mobile=True,
            has_touch=True,
        )
        m_page = mobile.new_page()
        m_page.goto(url)
        m_page.wait_for_load_state("networkidle")
        ts2 = timestamp()
        m_page.locator("#case-mobile").screenshot(
            path=str(
                SCREENSHOTS_DIR / f"ui-verify-route-incident-mobile-drawer-{ts2}.png"
            )
        )
        print(f"  ✓ mobile-drawer-{ts2}.png")
        mobile.close()

        browser.close()

    print("\nDone. All screenshots written next to the demo HTML.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
