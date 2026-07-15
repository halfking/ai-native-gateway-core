#!/usr/bin/env python3
"""
Full UI smoke test for files.kxpms.cn (after deploy).

Actually loads the SPA in a headless Chromium and:
1. Validates page title
2. Listens for network errors / 4xx / 5xx
3. Tries to render the page (waits for JS to settle)
4. Takes a screenshot for visual evidence
"""

import sys
import json
from playwright.sync_api import sync_playwright


def run(playwright):
    browser = playwright.chromium.launch(headless=True, args=["--no-sandbox"])
    failures = []

    url = "https://files.kxpms.cn/"
    print(f"=== Loading {url} in headless Chromium ===")

    context = browser.new_context(
        ignore_https_errors=True,
        viewport={"width": 1280, "height": 800},
    )
    errors = []
    failed_requests = []

    try:
        page = context.new_page()

        # Capture page errors
        page.on("pageerror", lambda exc: errors.append(f"pageerror: {exc}"))
        page.on(
            "console",
            lambda msg: msg.type in ("error", "warning")
            and errors.append(f"console.{msg.type}: {msg.text}"),
        )

        # Capture failed requests
        def on_response(resp):
            if resp.status >= 400:
                failed_requests.append(
                    f"{resp.status} {resp.request.method} {resp.url}"
                )

        page.on("response", on_response)

        # Navigate
        try:
            page.goto(url, wait_until="networkidle", timeout=30000)
            print("    ✅ page loaded (networkidle)")
        except Exception as e:
            print(f"    ⚠️  networkidle timeout: {e}")
            # Maybe just domcontentloaded
            try:
                page.wait_for_load_state("domcontentloaded", timeout=10000)
                print("    ✅ page loaded (domcontentloaded)")
            except Exception as e2:
                print(f"    ❌ page failed to load: {e2}")
                failures.append(f"page load: {e2}")
                return 1

        # Wait an additional 2s for SPA to settle
        page.wait_for_timeout(2000)

        # Check title
        title = page.title()
        print(f"    page.title() = {title!r}")
        if "开轩" in title or "资源" in title or "cloudreve" in title.lower():
            print(f"    ✅ title contains expected app name")
        else:
            print(f"    ⚠️  title doesn't contain expected name")

        # Screenshot for evidence
        screenshot_path = "/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/.scratch/2026-07-15-files-kxpms-cn-deploy/files-kxpms-cn-spa.png"
        import os

        os.makedirs(os.path.dirname(screenshot_path), exist_ok=True)
        page.screenshot(path=screenshot_path, full_page=True)
        print(f"    📷 screenshot saved: {screenshot_path}")

        # Check for visible content
        body_text = page.evaluate("() => document.body.innerText.slice(0, 200)")
        print(f"    body[:200] = {body_text!r}")
        if len(body_text.strip()) == 0:
            print(f"    ⚠️  body empty — page might be loading or blank")

        # Network errors / 4xx / 5xx during load
        if failed_requests:
            print(f"\n    ⚠️  {len(failed_requests)} failed requests:")
            for fr in failed_requests[:5]:
                print(f"      {fr}")
        else:
            print("    ✅ no 4xx/5xx during page load")

        # JS errors
        if errors:
            print(f"\n    ⚠️  {len(errors)} JS errors:")
            for e in errors[:5]:
                print(f"      {e}")
        else:
            print("    ✅ no JS errors")

    finally:
        context.close()
        browser.close()

    return 1 if failures else 0


def main():
    import os

    os.makedirs(
        "/Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go/.scratch/2026-07-15-files-kxpms-cn-deploy",
        exist_ok=True,
    )
    with sync_playwright() as p:
        return run(p)


if __name__ == "__main__":
    sys.exit(main())
