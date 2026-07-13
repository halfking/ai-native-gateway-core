#!/usr/bin/env python3
"""
verify_route_incident_phase2_ui.py — Playwright visual verification
for Phase 2 action / audit / evidence export sections.

Captures screenshots that prove:
  - The action grid renders 7 buttons (recover, reprobe, etc.)
  - The confirm modal collects reason + confirmation token
  - The audit log section lists recent entries with outcome pills
  - The evidence export banner shows the integrity checksum

Output files: ui-verify-route-incident-phase2-{scenario}-*.png
"""

import os
import sys
import time
from pathlib import Path

from playwright.sync_api import sync_playwright

REPO_ROOT = Path(__file__).resolve().parent
DEMO_HTML = REPO_ROOT / "route-incident-phase2-demo.html"
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

        # Section 8: action panel.
        page.locator("#case-actions").screenshot(
            path=str(
                SCREENSHOTS_DIR / f"ui-verify-route-incident-phase2-actions-{ts}.png"
            )
        )
        print(f"  ✓ actions-{ts}.png")

        # Section 9: audit log.
        page.locator("#case-audit").screenshot(
            path=str(
                SCREENSHOTS_DIR / f"ui-verify-route-incident-phase2-audit-{ts}.png"
            )
        )
        print(f"  ✓ audit-{ts}.png")

        # Confirm modal.
        page.locator("#case-confirm").screenshot(
            path=str(
                SCREENSHOTS_DIR / f"ui-verify-route-incident-phase2-confirm-{ts}.png"
            )
        )
        print(f"  ✓ confirm-{ts}.png")

        # Evidence export banner.
        page.locator("#case-export").screenshot(
            path=str(
                SCREENSHOTS_DIR / f"ui-verify-route-incident-phase2-export-{ts}.png"
            )
        )
        print(f"  ✓ export-{ts}.png")

        # Full-page overview.
        page.screenshot(
            path=str(
                SCREENSHOTS_DIR / f"ui-verify-route-incident-phase2-overview-{ts}.png"
            ),
            full_page=True,
        )
        print(f"  ✓ overview-{ts}.png")

        context.close()
        browser.close()

    print("\nDone. All screenshots written next to the demo HTML.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
