"""Authentication regression: existing device identity, errors, and WebKit WebView."""
from pathlib import Path
from playwright.sync_api import sync_playwright, expect
import json

ROOT=Path(__file__).resolve().parents[1]/'server/nasui/web'
with sync_playwright() as p:
 for engine in [p.chromium,p.webkit]:
  browser=engine.launch(headless=True)
  for prefix in ['', '/alist']:
   context=browser.new_context(viewport={'width':390,'height':844},is_mobile=True,has_touch=True)
   context.add_init_script("localStorage.setItem('token','fixture-token');localStorage.setItem('alist_client_id','fixture-device')")
   cfg={'code':200,'message':'','isolate':True};calls=[];errors=[]
   page=context.new_page();page.on('pageerror',lambda e:errors.append(str(e)))
   def route(r):
    path=r.request.url.split('https://nas.example',1)[1].split('?',1)[0].removeprefix(prefix)
    if path.startswith('/api/'):
     headers=r.request.headers;calls.append((path,headers))
     # A missing/wrong identity represents an extra device denied at the limit.
     if path=='/api/auth/nas-handoff':data={'code':200,'data':{'token':'fixture-token','client_id':'fixture-device'}}
     elif headers.get('authorization')!='fixture-token':data={'code':401,'message':'unauthorized'}
     elif headers.get('client-id')!='fixture-device':data={'code':403,'message':'too many active devices'}
     elif cfg['code']!=200:data={'code':cfg['code'],'message':cfg['message']}
     elif path.endswith('/handoff'):data={'code':200,'data':{'ticket':'fixture-ticket'}}
     elif path=='/api/me':data={'code':200,'data':{'role':[2]}}
     elif path.endswith('/targets'):data={'code':200,'data':{'cloud':[{'path':'/cloud','label':'cloud'}],'targets':[{'path':'/NAS','label':'NAS'}],'downloads_enabled':True}}
     elif path.endswith('/entries'):data={'code':200,'data':[]}
     elif path.endswith('/tasks'):data={'code':200,'data':{'active':[],'history':[]}}
     elif path.endswith('/list-stream'):
      r.fulfill(content_type='application/x-ndjson',body='{"event":"start"}\n{"event":"done","total":0}\n');return
     else:raise AssertionError(path)
     r.fulfill(content_type='application/json',body=json.dumps(data));return
    if path=='/cloud':
     r.fulfill(content_type='text/html',body=f'<script async src="{prefix}/bitqiu-assets/nav.js"></script><body>Cloud</body>');return
    name='index.html' if path=='/@nas' else path.rsplit('/',1)[-1]
    file=ROOT/name
    assert file.is_file(),path
    body=file.read_bytes()
    if name=='index.html' and cfg['isolate']:
     body=body.replace(b'</head>',b"<script>localStorage.clear();</script></head>")
    if name=='index.html' and cfg.get('blocked'):
     body=body.replace(b'</head>',b"<script>for(const key of ['localStorage','sessionStorage'])Object.defineProperty(window,key,{get(){throw new DOMException('blocked','SecurityError')}})</script></head>")
    r.fulfill(content_type={'.js':'application/javascript','.html':'text/html','.css':'text/css'}[file.suffix],body=body)
   context.route('https://nas.example/**',route)
   page.goto('https://nas.example'+prefix+'/cloud')
   page.locator('#alist-nas-download').click()
   expect(page.locator('#source-loading')).to_contain_text('加载完成')
   assert any(path.endswith('/list-stream') for path,_ in calls)
   assert all(h.get('client-id')=='fixture-device' for path,h in calls if path!='/api/auth/nas-handoff')
   assert page.evaluate('location.hash')==''
   assert page.evaluate("localStorage.getItem('token')") is None
   page.reload();expect(page.locator('#source-loading')).to_contain_text('加载完成')
   assert not errors,errors
   for code,message,wanted in [(403,'too many active devices','设备数量已达上限'),(401,'session inactive','设备会话已失效'),(401,'invalid token','登录已失效'),(403,'You are not an admin','具有管理员角色'),(403,'policy denied','policy denied')]:
    cfg.update(code=code,message=message)
    page.locator('#tasks-refresh').click()
    expect(page.locator('#error-message')).to_contain_text(wanted)
    page.locator('#error-close').click()
   cfg.update(code=200,message='')
   page.evaluate('sessionStorage.clear()');page.reload()
   expect(page.locator('#error-message')).to_contain_text('未读取到 AList 登录状态');page.locator('#error-close').click()
   before=len(calls);page.locator('#tasks-refresh').click()
   expect(page.locator('#error-message')).to_contain_text('未读取到 AList 登录状态')
   assert len(calls)==before
   page.locator('#error-close').click()
   page.evaluate("localStorage.setItem('token','fixture-token');localStorage.removeItem('alist_client_id')")
   page.locator('#tasks-refresh').click();expect(page.locator('#error-message')).to_contain_text('未读取到 AList 设备标识')
   page.locator('#error-close').click()
   page.evaluate("Object.defineProperty(window,'localStorage',{get(){throw new DOMException('blocked','SecurityError')}})")
   page.locator('#tasks-refresh').click();expect(page.locator('#error-message')).to_contain_text('无法读取 AList 登录状态')
   assert not errors,errors
   cfg['blocked']=True
   page.goto('https://nas.example'+prefix+'/@nas#nas-ticket=fixture-ticket')
   expect(page.locator('#source-loading')).to_contain_text('加载完成')
   assert page.evaluate('location.hash')==''
   assert not errors,errors
   context.close()
   print('PASS NAS auth: '+engine.name+' prefix='+repr(prefix))
  browser.close()
