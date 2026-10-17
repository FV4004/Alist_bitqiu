"""Test the complete official login frontend, including NAS navigation and downloads.

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


async def check_nas_navigation(browser, prefix):
    context = await browser.new_context()
    await context.add_init_script("localStorage.setItem('token', 'fixture-token')")
    page = await context.new_page()
    errors, listed, documents = [], [], []
    page.on('console', lambda message: errors.append(message.text) if message.type == 'error' and message.text.startswith('error ') else None)
    page.on('pageerror', lambda error: errors.append(error.stack))

    async def route(request):
        url = urlparse(request.request.url)
        if url.hostname != 'nas.example':
            await request.fulfill(status=204)
            return
        path = url.path.removeprefix(prefix) if prefix else url.path
        if request.request.is_navigation_request():
            documents.append(path)
        if path.startswith('/api/'):
            response = {'code': 200, 'data': []}
            if path == '/api/public/settings':
                response['data'] = {'site_title': 'AList', 'pagination_type': 'all', 'default_page_size': '50', 'version': 'fixture', 'logo': '', 'favicon': ''}
            elif path == '/api/me':
                response['data'] = {'id': 1, 'username': 'admin', 'role': [2], 'base_path': '/', 'permission': 65535}
            elif path == '/api/fs/get':
                response['data'] = {'name': 'cloud', 'is_dir': True, 'size': 0, 'modified': '2026-01-01T00:00:00Z', 'created': '2026-01-01T00:00:00Z', 'provider': 'BitQiu', 'related': [], 'raw_url': ''}
            elif path == '/api/fs/list':
                body = request.request.post_data_json
                listed.append(body['path'])
                if body['path'] == '/@nas':
                    response = {'code': 500, 'message': 'failed get storage: storage not found; rawPath: /@nas', 'data': None}
                else:
                    response['data'] = {'content': [{'name': 'fixture.txt', 'is_dir': False, 'size': 123, 'size_known': True, 'modified': '2026-01-01T00:00:00Z', 'created': '2026-01-01T00:00:00Z', 'type': 0, 'sign': ''}], 'total': 1, 'filtered_total': 1, 'has_more': False, 'page': 1, 'per_page': -1, 'pages_total': 1, 'readme': '', 'header': '', 'write': False, 'provider': 'BitQiu'}
            elif path == '/api/admin/bitqiu/nas/targets':
                response['data'] = {'cloud': [{'path': '/cloud', 'label': '/cloud'}], 'targets': [{'path': '/NAS下载', 'label': '/NAS下载'}], 'downloads_ready': True, 'downloads_enabled': True}
            elif path == '/api/admin/bitqiu/nas/tasks':
                response['data'] = {'active': [], 'history': []}
            elif path == '/api/admin/bitqiu/nas/prepare':
                response = {'code': 200, 'data': {'plan_id':'fixture-plan','conflicts':[]}}
            elif path == '/api/admin/bitqiu/nas/commit':
                response['data'] = {'tasks': [{'id': 'fixture-download'}]}
            await request.fulfill(content_type='application/json', body=json.dumps(response))
            return
        if path == '/@nas':
            file = ROOT / 'server/nasui/web/index.html'
        elif path.startswith('/bitqiu-assets/'):
            file = ROOT / 'server/nasui/web' / path.rsplit('/', 1)[-1]
        elif path.startswith(('/assets/', '/static/', '/images/')):
            file = ROOT / 'public/dist' / path.lstrip('/')
        else:
            file = ROOT / 'public/dist/index.html'
        if not file.is_file():
            await request.fulfill(status=404)
            return
        body = file.read_bytes()
        if file == ROOT / 'public/dist/index.html':
            body = body.replace(b'cdn: undefined', ('cdn: ' + json.dumps(prefix)).encode(), 1)
            body = body.replace(b'base_path: undefined', ('base_path: ' + json.dumps(prefix or '/')).encode(), 1)
            body = body.replace(b'</head>', ('<script async src="' + prefix + '/bitqiu-assets/nav.js"></script></head>').encode(), 1)
        kind = {'.js': 'application/javascript', '.css': 'text/css', '.html': 'text/html', '.json': 'application/json'}.get(file.suffix, 'application/octet-stream')
        await request.fulfill(content_type=kind, body=body)

    await page.route('**/*', route)
    await page.goto('https://nas.example' + prefix + '/cloud', wait_until='networkidle')
    await page.locator('#alist-nas-download').wait_for(state='visible')
    assert '/cloud' in listed, listed
    await page.locator('#alist-nas-download').click()
    try:
        await page.locator('#source-storage').wait_for(state='visible', timeout=5000)
    except PlaywrightTimeout:
        raise AssertionError(f"NAS button was intercepted: documents={documents}, listed={listed}, body={(await page.locator('body').inner_text())[:500]}")
    assert '/@nas' in documents, documents
    assert '/@nas' not in listed, listed
    await page.locator('#source-files input[type=checkbox]').first.check()
    await page.locator('#download').click()
    await page.wait_for_function("document.getElementById('notice').textContent.includes('已提交 1 个后台任务')")
    await page.locator('#home').click()
    await page.wait_for_function("!document.getElementById('source-storage') && !!document.getElementById('root')")
    assert not errors, errors
    print(f'PASS official frontend -> NAS page -> download task -> AList: prefix={prefix!r}')
    await context.close()


async def main(demonstrate, navigation_only=False):
    if not (ROOT / "public/dist/index.html").is_file():
        raise SystemExit("Download the matching official frontend to public/dist first.")
    async with async_playwright() as p:
        executable = os.environ.get("CHROMIUM_PATH")
        browser = await p.chromium.launch(executable_path=executable, args=["--no-sandbox", "--disable-dev-shm-usage"])
        try:
            if not navigation_only:
                await check(browser, "")
                await check(browser, "", unavailable=True)
                await check(browser, "/alist")
            if demonstrate:
                await check(browser, "", blocking=True)
            await check_nas_navigation(browser, "")
            await check_nas_navigation(browser, "/alist")
        finally:
            await browser.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--demonstrate-regression", action="store_true")
    parser.add_argument("--navigation-only", action="store_true")
    args = parser.parse_args()
    asyncio.run(main(args.demonstrate_regression, args.navigation_only))
