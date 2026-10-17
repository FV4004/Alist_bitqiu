"""Directory picker acceptance: fuzzy selection, cache, back/forward and refresh."""
from pathlib import Path
from collections import Counter
from urllib.parse import urlparse
import json
from playwright.sync_api import sync_playwright, expect
ROOT=Path(__file__).resolve().parents[1]/'server/nasui/web'
with sync_playwright() as p:
 for engine in [p.chromium,p.webkit]:
  browser=engine.launch(headless=True)
  for prefix in ['', '/alist']:
   context=browser.new_context(viewport={'width':390,'height':844},is_mobile=True,has_touch=True)
   context.add_init_script("localStorage.setItem('token','fixture-token');localStorage.setItem('alist_client_id','fixture-device')")
   page=context.new_page();errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
   calls=Counter();cfg={'fail_child':False,'hold_target':False};held=[]
   files=[{'name':f'file-{i:03d}.txt','is_dir':False,'size':100,'size_known':True} for i in range(50)]+[{'name':'file-电视剧','is_dir':True},{'name':'报告2026.PDF','is_dir':False,'size':123,'size_known':True}]
   targets=[{'name':'电视剧2026','path':'电视剧2026','is_dir':True,'selectable':True},{'name':'电视剧预告.mp4','path':'电视剧预告.mp4','is_dir':False,'selectable':False}]+[{'name':f'目录{i:02d}','path':f'目录{i:02d}','is_dir':True,'selectable':True} for i in range(30)]
   def reply(r,data):r.fulfill(content_type='application/json',body=json.dumps({'code':200,'data':data},ensure_ascii=False))
   def route(r):
    path=urlparse(r.request.url).path.removeprefix(prefix)
    if path.startswith('/api/'):
     assert r.request.headers.get('authorization')=='fixture-token'
     assert r.request.headers.get('client-id')=='fixture-device'
     body=r.request.post_data_json if r.request.post_data else {}
     if path.endswith('/targets'):return reply(r,{'cloud':[{'path':'/cloud','label':'cloud'}],'targets':[{'path':'/NAS下载','label':'NAS'}],'downloads_enabled':True})
     if path.endswith('/list-stream'):
      calls['source:'+body['path']]+=1
      content=files if body['path']=='/cloud' else [{'name':'子目录.txt','is_dir':False,'size':1,'size_known':True}]
      frames=[{'event':'start'},{'event':'batch','content':content}]
      if cfg['fail_child'] and body['path']!='/cloud':frames.append({'event':'error','message':'fixture incomplete directory'})
      else:frames.append({'event':'done','total':len(content)})
      r.fulfill(content_type='application/x-ndjson',body=''.join(json.dumps(x,ensure_ascii=False)+'\n' for x in frames));return
     if path.endswith('/entries'):
      calls['target:'+body['path']]+=1
      if cfg['hold_target'] and body['path']!='/NAS下载':held.append(r);return
      return reply(r,targets if body['path']=='/NAS下载' else [{'name':'第二季','path':'第二季','is_dir':True,'selectable':True}])
     if path.endswith('/tasks'):return reply(r,{'active':[],'history':[],'concurrency':{'limit':5,'running':0}})
     if path.endswith('/stats'):return reply(r,{'known_bytes':100,'unknown_count':0,'file_count':1})
     if path.endswith('/mkdir'):
      targets.append({'name':body['name'],'path':body['name'],'is_dir':True,'selectable':True});return reply(r,None)
     if path.endswith('/prepare'):return reply(r,{'plan_id':'fixture-plan','conflicts':[]})
     if path.endswith('/commit'):
      targets.append({'name':'任务新目录','path':'任务新目录','is_dir':True,'selectable':True});return reply(r,{'tasks':[]})
     raise AssertionError(path)
    name='index.html' if path=='/@nas' else path.rsplit('/',1)[-1]
    f=ROOT/name;assert f.is_file(),path
    r.fulfill(content_type={'.html':'text/html','.js':'application/javascript','.css':'text/css'}[f.suffix],body=f.read_bytes())
   context.route('https://nas.example/**',route);page.goto('https://nas.example'+prefix+'/@nas')
   expect(page.locator('#source-files input')).to_have_count(52);expect(page.locator('#select-page')).to_be_enabled()
   page.locator('#source-search').fill('报26pdf');expect(page.locator('#source-files input')).to_have_count(1)
   page.locator('#select-page').check();expect(page.locator('#selection-count')).to_have_text('已选择：1 项')
   page.locator('#source-search-clear').click();expect(page.locator('#source-files input')).to_have_count(52)
   page.locator('#source-search').fill('file');page.get_by_role('checkbox',name='选择 file-001.txt',exact=True).check()
   expect(page.locator('#source-count')).to_contain_text('筛选外 1 项');page.locator('#sort-order').select_option('name:asc')
   page.locator('#source-files').evaluate("e=>e.closest('.table-scroll').scrollTop=120")
   folder=page.get_by_role('button',name='file-电视剧',exact=True)
   folder.evaluate('e=>e.click()');expect(page.locator('#source-path')).to_have_text('/cloud/file-电视剧');expect(page.locator('#select-page')).to_be_enabled()
   expect(page.locator('#source-search')).to_have_value('');page.locator('#source-up').click()
   expect(page.locator('#source-search')).to_have_value('file');expect(page.locator('#sort-order')).to_have_value('name:asc')
   expect(page.locator('#source-count')).to_contain_text('已选 2 项');assert page.locator('#source-files').evaluate("e=>e.closest('.table-scroll').scrollTop")==120
   assert calls['source:/cloud']==1 and calls['source:/cloud/file-电视剧']==1,calls
   page.go_back();expect(page.locator('#source-path')).to_have_text('/cloud/file-电视剧')
   page.go_back();expect(page.locator('#source-path')).to_have_text('/cloud');expect(page.locator('#source-search')).to_have_value('file')
   assert calls['source:/cloud']==1 and calls['source:/cloud/file-电视剧']==1,calls
   page.go_forward();expect(page.locator('#source-path')).to_have_text('/cloud/file-电视剧')
   cfg['fail_child']=True;page.locator('#source-refresh').click();expect(page.locator('#error-message')).to_contain_text('incomplete directory');page.locator('#error-close').click()
   page.locator('#source-up').click();cfg['fail_child']=False
   folder.evaluate('e=>e.click()');expect(page.locator('#select-page')).to_be_enabled();assert calls['source:/cloud/file-电视剧']==3,calls
   page.locator('#source-up').click();page.locator('#source-refresh').click();expect(page.locator('#select-page')).to_be_enabled()
   assert calls['source:/cloud']==2,calls;expect(page.locator('#source-search')).to_have_value('file')
   # Search and type filtering compose, and files remain disabled.
   page.locator('#target-search').fill('电剧');expect(page.locator('#target-folders button')).to_have_count(2)
   expect(page.get_by_role('button',name='不可选择文件 电视剧预告.mp4',exact=True)).to_be_disabled()
   page.locator('#target-filter-summary').click()
   for check in page.locator('#target-filter-options input').all():check.uncheck()
   page.locator('#target-filter-options').get_by_role('checkbox',name='文件夹',exact=True).check();page.locator('#target-filter-summary').click()
   expect(page.locator('#target-folders button')).to_have_count(1)
   page.get_by_role('button',name='打开 NAS 文件夹 电视剧2026',exact=True).click();expect(page.locator('#target-path')).to_have_text('/NAS下载/电视剧2026')
   page.locator('#target-up').click();expect(page.locator('#target-search')).to_have_value('电剧');expect(page.locator('#target-count')).to_have_text('显示 1 / 32 项')
   assert calls['target:/NAS下载']==1,calls
   page.locator('#target-filter-summary').click();page.locator('#target-filter-reset').click();page.locator('#target-filter-summary').click();page.locator('#target-search-clear').click()
   page.locator('#target-folders').evaluate('e=>e.scrollTop=150')
   page.get_by_role('button',name='打开 NAS 文件夹 目录00',exact=True).evaluate('e=>e.click()');expect(page.locator('#target-path')).to_have_text('/NAS下载/目录00')
   page.locator('#target-up').click();assert page.locator('#target-folders').evaluate('e=>e.scrollTop')==150
   assert calls['target:/NAS下载']==1,calls
   # A late response must not overwrite the parent restored from cache.
   cfg['hold_target']=True;page.get_by_role('button',name='打开 NAS 文件夹 目录01',exact=True).click();expect(page.locator('#target-path')).to_have_text('/NAS下载/目录01')
   page.locator('#target-up').click();assert held
   reply(held.pop(),[{'name':'过期响应','path':'过期响应','is_dir':True,'selectable':True}]);cfg['hold_target']=False
   expect(page.locator('#target-path')).to_have_text('/NAS下载');expect(page.get_by_role('button',name='打开 NAS 文件夹 过期响应',exact=True)).to_have_count(0)
   page.locator('#target-refresh').click();expect(page.locator('#target-count')).to_have_text('显示 32 / 32 项');assert calls['target:/NAS下载']==2,calls
   page.locator('#new-folder-name').fill('测试新增');page.locator('#mkdir').click();expect(page.get_by_role('button',name='打开 NAS 文件夹 测试新增',exact=True)).to_be_visible();assert calls['target:/NAS下载']==3,calls
   page.get_by_role('checkbox',name='选择 file-001.txt',exact=True).check();page.locator('#download').click();expect(page.locator('#notice')).to_contain_text('已提交')
   page.get_by_role('button',name='打开 NAS 文件夹 目录00',exact=True).evaluate('e=>e.click()');expect(page.locator('#target-path')).to_have_text('/NAS下载/目录00')
   page.locator('#target-up').click();expect(page.get_by_role('button',name='打开 NAS 文件夹 任务新目录',exact=True)).to_be_visible();assert calls['target:/NAS下载']==4,calls
   assert page.evaluate('document.documentElement.scrollWidth<=innerWidth'),engine.name
   assert not errors,errors
   print('PASS NAS picker: fuzzy search, filtered selection, cached up/back/forward, scroll, refresh, incomplete/race safety; '+engine.name+' prefix='+repr(prefix))
   context.close()
  browser.close()
