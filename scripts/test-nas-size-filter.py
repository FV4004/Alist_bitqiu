"""Size filter acceptance: real controls, local filtering, cache and fixed scope."""
from pathlib import Path
from urllib.parse import urlparse
import json
import os
from playwright.sync_api import sync_playwright, expect

ROOT = Path(__file__).resolve().parents[1] / 'server/nasui/web'
OUT = Path('/tmp/alist-nas-ui')
OUT.mkdir(exist_ok=True)
MB = 1024 * 1024

with sync_playwright() as p:
    for name in os.environ.get('NAS_TEST_ENGINES', 'chromium,webkit').split(','):
        engine = getattr(p, name)
        launch = {'headless': True}
        if name == 'chromium':
            chromium = os.environ.get('CHROMIUM_PATH') or ('/usr/bin/chromium' if Path('/usr/bin/chromium').exists() else None)
            if chromium:
                launch.update(executable_path=chromium, args=['--no-sandbox', '--disable-dev-shm-usage'])
        browser = engine.launch(**launch)
        for prefix in ['', '/alist']:
            context = browser.new_context(viewport={'width': 1440, 'height': 1000})
            context.add_init_script("localStorage.setItem('token','fixture-token');localStorage.setItem('alist_client_id','fixture-device')")
            page = context.new_page()
            errors, calls = [], []
            page.on('pageerror', lambda e: errors.append(str(e)))
            files = [{'name': 'Films', 'is_dir': True}] + [
                {'name': filename, 'is_dir': False, 'size': size, 'size_known': known}
                for filename, size, known in [
                    ('below.mp4', 80*MB-1, True), ('edge.mp4', 80*MB, True),
                    ('mid.pdf', 120*MB, True), ('large.zip', 500*MB, True),
                    ('huge.mkv', 12000*MB, True), ('unknown.bin', 0, False), ('empty.txt', 0, True)
                ]
            ]
            child = [{'name': 'small.mp4', 'size': MB, 'size_known': True}, {'name': 'big.mp4', 'size': 200*MB, 'size_known': True}]

            def reply(r, data):
                r.fulfill(content_type='application/json', body=json.dumps({'code': 200, 'data': data}, ensure_ascii=False))

            def route(r):
                path = urlparse(r.request.url).path.removeprefix(prefix)
                if path.startswith('/api/'):
                    body = r.request.post_data_json if r.request.post_data else {}
                    calls.append((path, body))
                    assert r.request.headers.get('authorization') == 'fixture-token'
                    if path.endswith('/targets'):
                        return reply(r, {'cloud': [{'path': '/cloud', 'label': 'cloud'}], 'targets': [{'path': '/NAS', 'label': 'NAS'}], 'downloads_enabled': True})
                    if path.endswith('/list-stream'):
                        content = files if body['path'] == '/cloud' else child
                        return r.fulfill(content_type='application/x-ndjson', body=''.join(json.dumps(v) + '\n' for v in [
                            {'event': 'start'}, {'event': 'batch', 'content': content}, {'event': 'done', 'total': len(content)}]))
                    if path.endswith('/entries'):
                        return reply(r, [dict(v, path=v['name'], selectable=bool(v.get('is_dir'))) for v in files])
                    if path.endswith('/tasks'):
                        return reply(r, {'active': [], 'history': [], 'concurrency': {'limit': 5, 'running': 0}})
                    if path.endswith('/scan/status'):
                        return reply(r, {'job': None, 'result': None})
                    if path.endswith('/scan/start'):
                        assert body['names'] == ['edge.mp4'], body
                        assert 'size' not in body
                        return reply(r, {'job': {'id': 'scan', 'state': 'completed', 'percent': 100, 'source': '/cloud', 'destination': '/NAS', 'names': body['names']}, 'result': None})
                    if path.endswith('/stats'):
                        return reply(r, {'known_bytes': 80*MB, 'unknown_count': 0, 'file_count': 1})
                    raise AssertionError(path)
                file = ROOT / ('index.html' if path == '/@nas' else path.rsplit('/', 1)[-1])
                assert file.is_file(), path
                return r.fulfill(path=str(file), content_type='text/css' if file.suffix == '.css' else 'text/html' if file.suffix == '.html' else 'application/javascript')

            page.route('**/*', route)
            page.goto('https://nas.example' + prefix + '/@nas')
            source = page.locator('#source-files input[type=checkbox]')
            target = page.locator('#target-folders button')
            minimum, maximum = page.locator('#size-min'), page.locator('#size-max')
            expect(source).to_have_count(5)
            expect(target).to_have_count(5)
            expect(minimum).to_have_value('80')
            expect(maximum).to_have_value('')
            expect(page.locator('#source-size-status')).to_contain_text('1 项')
            expect(page.locator('#source-files')).to_contain_text('Films')
            a, b = page.locator('.size-slider').bounding_box(), page.locator('.size-manual').bounding_box()
            assert abs(a['width'] - b['width']) < 1, (a, b)
            assert abs(a['y'] + a['height']/2 - b['y'] - b['height']/2) < 2, (a, b)
            page.screenshot(path=str(OUT / f'size-filter-{name}-{prefix.replace("/", "") or "root"}.png'), full_page=True)
            listing_count = lambda: len([x for x in calls if x[0].endswith(('/list-stream', '/entries'))])
            initial_calls = listing_count()
            maximum.fill('120'); expect(source).to_have_count(3); expect(target).to_have_count(3)
            minimum.fill(''); expect(source).to_have_count(5)
            maximum.fill(''); expect(source).to_have_count(8); expect(target).to_have_count(8)
            expect(page.locator('#source-size-status')).not_to_be_visible()
            minimum.fill('0'); maximum.fill('0'); expect(source).to_have_count(2)
            expect(page.locator('#source-files')).to_contain_text('empty.txt')
            page.locator('#size-clear').click(); minimum.fill('80')
            maximum.fill('79'); expect(page.locator('#size-error')).to_be_visible(); expect(source).to_have_count(5)
            maximum.fill('120'); expect(page.locator('#size-error')).not_to_be_visible(); expect(source).to_have_count(3)
            minimum.fill('-1'); expect(page.locator('#size-error')).to_be_visible(); expect(source).to_have_count(3)
            minimum.fill('80'); maximum.fill('')
            page.locator('#size-source').uncheck(); expect(source).to_have_count(8); expect(target).to_have_count(5)
            page.locator('#size-source').check(); page.locator('#size-target').uncheck()
            minimum.fill('500'); expect(source).to_have_count(3); expect(target).to_have_count(8)
            page.locator('#size-target').check(); expect(target).to_have_count(3)
            page.locator('#picker-search').fill('edge'); expect(source).to_have_count(0)
            minimum.fill('80'); expect(source).to_have_count(1); expect(target).to_have_count(1)
            page.locator('#target-filter-summary').click()
            for option in page.locator('#target-filter-options input').all():
                if option.get_attribute('value') != 'document': option.uncheck()
            expect(source).to_have_count(0)
            page.locator('#target-filter-summary').click()
            page.locator('#picker-search-clear').click(); expect(source).to_have_count(1)
            expect(page.locator('#source-files')).to_contain_text('mid.pdf')
            page.locator('#target-filter-summary').click(); page.locator('#target-filter-reset').click()
            page.locator('#target-filter-summary').click(); expect(source).to_have_count(5)
            assert listing_count() == initial_calls, calls
            page.get_by_role('checkbox', name='选择 edge.mp4', exact=True).check()
            minimum.fill('500'); expect(page.locator('#source-count')).to_contain_text('筛选外 1 项')
            expect(page.locator('#download')).to_contain_text('已选 1 项')
            page.locator('#clear-selection').click(); minimum.fill('80')
            page.get_by_role('button', name='Films', exact=True).click()
            expect(page.locator('#source-path')).to_have_text('/cloud/Films'); expect(source).to_have_count(1)
            minimum.fill('0'); expect(source).to_have_count(2)
            page.locator('#source-up').click(); expect(source).to_have_count(7)
            assert len([x for x in calls if x[0].endswith('/list-stream') and x[1]['path'] == '/cloud']) == 1
            minimum.fill('80')
            # Drag the actual native handle, then verify numeric synchronization.
            slider = page.locator('#size-min-slider'); slider.scroll_into_view_if_needed(); box = slider.bounding_box()
            start = box['x'] + 11 + (box['width']-22)*.15
            finish = box['x'] + 11 + (box['width']-22)*.35
            page.mouse.move(start, box['y']+22); page.mouse.down(); page.mouse.move(finish, box['y']+22, steps=12); page.mouse.up()
            assert 490 <= float(minimum.input_value()) <= 510, minimum.input_value()
            max_slider = page.locator('#size-max-slider'); max_slider.focus(); max_slider.press('ArrowLeft')
            assert maximum.input_value() != '', 'keyboard trapped at unlimited endpoint'
            max_slider.press('End'); expect(maximum).to_have_value('')
            minimum.fill('20000'); expect(slider).to_have_attribute('aria-valuetext', '20,000 MB')
            page.locator('#size-clear').click(); expect(source).to_have_count(8)
            for width in [320, 390, 768, 1440]:
                page.set_viewport_size({'width': width, 'height': 900})
                assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), width
                a, b = page.locator('.size-slider').bounding_box(), page.locator('.size-manual').bounding_box()
                assert abs(a['width'] - b['width']) < 1, (width, a, b)
            page.set_viewport_size({'width': 390, 'height': 844})
            minimum.fill('80'); page.locator('#size-filter').scroll_into_view_if_needed()
            page.screenshot(path=str(OUT / f'size-filter-{name}-{prefix.replace("/", "") or "root"}-mobile.png'))
            page.set_viewport_size({'width': 1440, 'height': 1000})
            page.get_by_role('checkbox', name='选择 edge.mp4', exact=True).check(); minimum.fill('500')
            page.locator('#compare-start').click()
            expect(page.locator('#compare-progress')).to_contain_text('扫描完成')
            assert any(path.endswith('/scan/start') for path, _ in calls)
            assert not errors, errors
            context.close()
            print(f'PASS size filter: default/bounds/unknown/scopes/combinations/cache/selection/drag/keyboard/layout, {name} prefix={prefix!r}')
        browser.close()
