"""Test the complete official login frontend, including delayed NAS navigation.

Requires public/dist from the matching official release and Python Playwright.
Use --demonstrate-regression to also reproduce the old parser-blocking bug.
All APIs are fixtures; no credentials, running AList server or NAS are needed.
"""
import argparse
import asyncio
import json
import os
from pathlib import Path
from urllib.parse import urlparse

from playwright.async_api import async_playwright, TimeoutError as PlaywrightTimeout

ROOT = Path(__file__).resolve().parents[1]


async def check(browser, prefix, unavailable=False, blocking=False):
    context = await browser.new_context()
    page = await context.new_page()
    errors = []
    page.on("pageerror", lambda error: errors.append(str(error)))

    async def route(request):
        url = urlparse(request.request.url)
        if url.hostname != "nas.example":
            await request.fulfill(status=204)
            return
        path = url.path.removeprefix(prefix) if prefix else url.path
        if path.startswith("/api/"):
            if path == "/api/public/settings":
                response = {"code": 200, "data": {
                    "site_title": "AList", "pagination_type": "all",
                    "default_page_size": "50", "version": "v3.65.0-bitqiu.8",
                    "logo": "", "favicon": "", "allow_password_login": "true",
                }}
            elif path == "/api/public/archive_extensions":
                response = {"code": 200, "data": [".zip", ".7z", ".tar"]}
            else:
                response = {"code": 401, "message": "Login required", "data": None}
            await request.fulfill(content_type="application/json", body=json.dumps(response))
            return
        if path == "/bitqiu-assets/nav.js":
            if unavailable:
                await request.abort("failed")
                return
            # Reproduce slow/uncached navigation with a fast/cached main module.
            await asyncio.sleep(3)
            file = ROOT / "server/nasui/web/nav.js"
        elif path.startswith(("/assets/", "/static/", "/images/")):
            file = ROOT / "public/dist" / path.lstrip("/")
        else:
            file = ROOT / "public/dist/index.html"
        if not file.is_file():
            await request.fulfill(status=404)
            return
        body = file.read_bytes()
        if file.name == "index.html":
            # Match getSiteConfig and NavigationScript; the Go test locks down
            # the async invariant in the real HTML injection helper.
            body = body.replace(b"cdn: undefined", ("cdn: " + json.dumps(prefix)).encode(), 1)
            body = body.replace(b"base_path: undefined", ("base_path: " + json.dumps(prefix or "/")).encode(), 1)
            script = '<script ' + ('' if blocking else 'async ') + 'src="' + prefix + '/bitqiu-assets/nav.js"></script>'
            body = body.replace(b"</head>", script.encode() + b"</head>", 1)
        kind = {".js": "application/javascript", ".css": "text/css", ".html": "text/html", ".json": "application/json"}.get(file.suffix, "application/octet-stream")
        await request.fulfill(content_type=kind, body=body)

    await page.route("**/*", route)
    await page.goto("https://nas.example" + prefix + "/@login", wait_until="commit")
    rendered = True
    try:
        await page.locator('input[type="password"]').first.wait_for(state="visible", timeout=2500)
    except PlaywrightTimeout:
        rendered = False
    await page.wait_for_load_state("networkidle")
    if blocking:
        assert not rendered and errors, "old blocking script did not reproduce the regression"
        print("REPRODUCED: blocking navigation leaves login blank:", errors[0])
    else:
        assert rendered, f"login failed before navigation loaded: {errors}"
        assert not errors, errors
        print(f"PASS official login: prefix={prefix!r}, navigation={'unavailable' if unavailable else 'delayed 3s'}")
    await context.close()


async def main(demonstrate):
    if not (ROOT / "public/dist/index.html").is_file():
        raise SystemExit("Download the matching official frontend to public/dist first.")
    async with async_playwright() as p:
        executable = os.environ.get("CHROMIUM_PATH")
        browser = await p.chromium.launch(executable_path=executable, args=["--no-sandbox", "--disable-dev-shm-usage"])
        try:
            if demonstrate:
                await check(browser, "", blocking=True)
            await check(browser, "")
            await check(browser, "", unavailable=True)
            await check(browser, "/alist")
        finally:
            await browser.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--demonstrate-regression", action="store_true")
    asyncio.run(main(parser.parse_args().demonstrate_regression))
