#!/usr/bin/env python3
"""
Browser test for www.kxpms.cn after HSTS outage fix.

Uses Playwright + a fresh browser context to simulate a brand-new visitor
(no HSTS cache). Verifies the SPA loads + cert is valid.
"""

import sys
import os
from playwright.sync_api import sync_playwright


def run(playwright):
    url = "https://www.kxpms.cn/"
    print(f"=== Loading {url} in fresh Chromium ===")

    # Fresh browser (no HSTS cache from previous runs)
    browser = playwright.chromium.launch(headless=True, args=["--no-sandbox"])

    failures = []

    try:
        # Use a NEW context so no HSTS cache from previous runs
        ctx = browser.new_context(ignore_https_errors=False)  # Strict cert validation
        page = ctx.new_page()

        network_errors = []
        console_errors = []

        def on_response(resp):
            if resp.status >= 400:
                network_errors.append(f"{resp.status} {resp.url}")

        page.on("response", on_response)
        page.on("pageerror", lambda exc: console_errors.append(f"pageerror: {exc}"))

        try:
            page.goto(url, wait_until="domcontentloaded", timeout=20000)
            print(f"    ✅ page loaded: status={page.url}")

            # Title
            title = page.title()
            print(f"    page.title() = {title!r}")

            # Check visible content
            body_text = page.evaluate(
                "() => document.body ? document.body.innerText.slice(0, 200) : ''"
            )
            print(f"    body[:200] = {body_text!r}")

            # Screenshot
            ss = "/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/.scratch/2026-07-15-files-kxpms-cn-deploy/www-kxpms-cn-spa.png"
            os.makedirs(os.path.dirname(ss), exist_ok=True)
            page.screenshot(path=ss, full_page=True)
            print(f"    📷 screenshot saved: {ss}")

            if not title.strip():
                failures.append("title empty")
            if not body_text.strip():
                failures.append("body empty (might be loading or blank)")

        except Exception as e:
            print(f"    ❌ page error: {e}")
            failures.append(f"page error: {e}")

        # Network errors
        if network_errors:
            print(f"\n    ⚠️  {len(network_errors)} network errors:")
            for err in network_errors[:5]:
                print(f"      {err}")
        else:
            print("    ✅ no 4xx/5xx network errors")

        if console_errors:
            print(f"\n    ⚠️  {len(console_errors)} console errors:")
            for err in console_errors[:5]:
                print(f"      {err}")
        else:
            print("    ✅ no JS console errors")

    finally:
        browser.close()

    return 1 if failures else 0


def main():
    with sync_playwright() as p:
        return run(p)


if __name__ == "__main__":
    sys.exit(main())
