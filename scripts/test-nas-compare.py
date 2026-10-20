"""One-way NAS compare review, explicit dedup confirmation and shared sorting."""
from pathlib import Path
from urllib.parse import urlparse
import json
from playwright.sync_api import sync_playwright,expect
ROOT=Path(__file__).resolve().parents[1]/'server/nasui/web'
OUT=Path('/tmp/alist-nas-ui');OUT.mkdir(exist_ok=True)
with sync_playwright() as p:
 for engine in [p.chromium,p.webkit]:
  browser=engine.launch(headless=True)
  for prefix in ['', '/alist']:
   context=browser.new_context(viewport={'width':390,'height':844},is_mobile=True,has_touch=True)
   context.add_init_script("localStorage.setItem('token','fixture-token');localStorage.setItem('alist_client_id','fixture-device')")
   page=context.new_page();errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
   cfg={'mode':'done','plans':0};calls=[];held=[]
   source=[{'name':'z.txt','is_dir':False,'size':1,'size_known':True,'modified':'2026-01-01T00:00:00Z'},{'name':'a.txt','is_dir':False,'size':500,'size_known':True,'modified':'2026-01-03T00:00:00Z'},{'name':'folder','is_dir':True,'modified':'2026-01-02T00:00:00Z'}]
   targets=[{'name':'A-folder','path':'A-folder','is_dir':True,'selectable':True,'modified':'2026-01-03T00:00:00Z'},{'name':'B.txt','is_dir':False,'selectable':False,'size':200,'size_known':True,'modified':'2026-01-02T00:00:00Z'},{'name':'A.txt','is_dir':False,'selectable':False,'size':5,'size_known':True,'modified':'2026-01-04T00:00:00Z'},{'name':'Unknown.txt','is_dir':False,'selectable':False,'size_known':False}]
   items=[{'id':'f1','relative':'missingFolder/child.dat','is_dir':False,'category':'files','size':100},{'id':'f2','relative':'lonely.bin','is_dir':False,'category':'files','size':-1},{'id':'d1','relative':'missingFolder','is_dir':True,'category':'folders','size':-1},{'id':'d2','relative':'emptyDir','is_dir':True,'category':'folders','size':-1},{'id':'bad','relative':'badFolder','is_dir':True,'category':'folders','size':-1,'blocked':True},{'id':'review','relative':'rename_file.txt','is_dir':False,'category':'review','size':10,'candidates':['rename-file.txt']}]
   def reply(r,data):r.fulfill(content_type='application/json',body=json.dumps({'code':200,'data':data},ensure_ascii=False))
   def frames(r,values):r.fulfill(content_type='application/x-ndjson',body=''.join(json.dumps(v,ensure_ascii=False)+'\n' for v in values))
   def route(r):
    path=urlparse(r.request.url).path.removeprefix(prefix)
    if path.startswith('/api/'):
     assert r.request.headers.get('authorization')=='fixture-token'
     assert r.request.headers.get('client-id')=='fixture-device'
     body=r.request.post_data_json if r.request.post_data else {};calls.append((path,body))
     if path.endswith('/targets'):return reply(r,{'cloud':[{'path':'/cloud','label':'cloud'}],'targets':[{'path':'/NAS','label':'NAS'}],'downloads_enabled':True})
     if path.endswith('/list-stream'):return frames(r,[{'event':'start'},{'event':'batch','content':source},{'event':'done','total':3}])
     if path.endswith('/entries'):return reply(r,targets)
     if path.endswith('/tasks'):return reply(r,{'active':[],'history':[],'concurrency':{'limit':5,'running':0}})
     if path.endswith('/compare'):
      assert body=={'src_dir':'/cloud','dst_dir':'/NAS'}
      values=[{'event':'progress','visited':20,'relative':'missingFolder'}]
      if cfg['mode']=='hold':held.append(r);return
      if cfg['mode']=='error':return frames(r,values+[{'event':'error','message':'fixture scan error'}])
      if cfg['mode']=='truncate':return frames(r,values)
      return frames(r,values+[{'event':'done','scan_id':'fixture-scan','items':items,'issues':[{'relative':'badFolder','message':'网盘读取失败，未判定缺失'}]}])
     if path.endswith('/compare/prepare'):
      assert body['scan_id']=='fixture-scan'
      assert set(body['ids'])=={'f1','f2','d1','d2'},body
      cfg['plans']+=1
      return reply(r,{'plan_id':'plan-'+str(cfg['plans']),'source':'/cloud','destination':'/NAS','tasks':[{'id':'t1','relative':'missingFolder','is_dir':True,'size':0},{'id':'t2','relative':'missingFolder/child.dat','is_dir':False,'size':100},{'id':'t3','relative':'lonely.bin','is_dir':False,'size':-1}],'skipped':[{'relative':'emptyDir','reason':'已存在，跳过'}],'files':2,'directories':1,'known_bytes':100,'unknown_count':1})
     if path.endswith('/compare/commit'):
      assert body.get('confirmed') is True
      return reply(r,{'queued':2,'skipped':[{'relative':'lonely.bin','reason':'确认时已存在，跳过'}]})
     raise AssertionError(path)
    name='index.html' if path=='/@nas' else path.rsplit('/',1)[-1];f=ROOT/name;assert f.is_file(),path
    r.fulfill(content_type={'.html':'text/html','.js':'application/javascript','.css':'text/css'}[f.suffix],body=f.read_bytes())
   context.route('https://nas.example/**',route);page.goto('https://nas.example'+prefix+'/@nas')
   expect(page.locator('#source-files input')).to_have_count(3)
   expect(page.locator('#source-files tr').first).to_contain_text('folder')
   expect(page.locator('#target-folders button').first).to_contain_text('A-folder')
   count=len([x for x,_ in calls if x.endswith('/entries') or x.endswith('/list-stream')])
   page.locator('#folders-first').uncheck();page.locator('#sort-order').select_option('size:asc')
   expect(page.locator('#source-files tr').first).to_contain_text('z.txt');expect(page.locator('#target-folders button').first).to_contain_text('A.txt');expect(page.locator('#target-folders button').last).to_contain_text('Unknown.txt')
   page.locator('#sort-target').uncheck();expect(page.locator('#target-folders button').first).to_contain_text('A-folder');expect(page.locator('#source-files tr').first).to_contain_text('z.txt')
   page.locator('#sort-source').uncheck();expect(page.locator('#source-files tr').first).to_contain_text('a.txt');expect(page.locator('#sort-scope-status')).to_have_text('未应用')
   page.locator('#sort-order').select_option('name:desc');expect(page.locator('#source-files tr').first).to_contain_text('a.txt');expect(page.locator('#target-folders button').first).to_contain_text('A-folder')
   page.locator('#sort-source').check();expect(page.locator('#source-files tr').first).to_contain_text('z.txt')
   page.locator('#sort-target').check();expect(page.locator('#target-folders button').first).to_contain_text('Unknown.txt')
   page.locator('#folders-first').check();expect(page.locator('#source-files tr').first).to_contain_text('folder');expect(page.locator('#target-folders button').first).to_contain_text('A-folder')
   assert count==len([x for x,_ in calls if x.endswith('/entries') or x.endswith('/list-stream')])
   # Comparison deliberately scans the full chosen roots, independently of toolbar filters.
   page.locator('#picker-search').fill('no-match');expect(page.locator('#source-files input')).to_have_count(0)
   page.locator('#compare-start').click();expect(page.locator('#compare-progress')).to_contain_text('扫描完成')
   expect(page.locator('#compare-roots')).to_contain_text('/cloud → NAS：/NAS')
   expect(page.locator('#compare-list .compare-row')).to_have_count(2)
   expect(page.locator('#compare-selected')).to_contain_text('已选 0 项')
   page.locator('#compare-select-all').check();expect(page.locator('#compare-selected')).to_contain_text('已选 2 项')
   page.locator('#compare-tab-folders').click();expect(page.locator('#compare-list .compare-row')).to_have_count(3)
   expect(page.get_by_role('checkbox',name='补缺选择 badFolder',exact=True)).to_be_disabled()
   page.locator('#compare-select-all').check();expect(page.locator('#compare-selected')).to_contain_text('已选 4 项')
   page.locator('#compare-tab-review').click();expect(page.locator('#compare-list .compare-row')).to_have_count(1)
   expect(page.get_by_role('checkbox',name='补缺选择 rename_file.txt',exact=True)).not_to_be_checked()
   expect(page.locator('#compare-list')).not_to_contain_text('lonely.bin')
   page.locator('#compare-prepare').click();expect(page.locator('#compare-confirm')).to_be_visible()
   expect(page.locator('#compare-final-list p')).to_have_count(3);expect(page.locator('#compare-confirm-summary')).to_contain_text('另有 1 个文件大小未知')
   expect(page.locator('#compare-submit')).to_be_disabled()
   assert not any(path.endswith('/compare/commit') for path,_ in calls)
   page.locator('#compare-back').click();expect(page.locator('#compare-confirm')).not_to_be_visible()
   assert not any(path.endswith('/compare/commit') for path,_ in calls)
   page.locator('#compare-prepare').click();expect(page.locator('#compare-confirm')).to_be_visible();expect(page.locator('#compare-submit')).to_be_disabled()
   if not prefix:
    page.screenshot(path=str(OUT/'nas-compare-mobile-confirm.png'))
    page.set_viewport_size({'width':1440,'height':1000});page.screenshot(path=str(OUT/'nas-compare-desktop-confirm.png'));page.set_viewport_size({'width':390,'height':844})
   page.locator('#compare-confirm-check').check();page.locator('#compare-submit').evaluate('e=>{e.click();e.click()}')
   expect(page.locator('#compare-confirm')).not_to_be_visible();expect(page.locator('#compare-progress')).to_contain_text('已提交 2 项，确认时跳过 1 项')
   assert len([1 for path,_ in calls if path.endswith('/compare/commit')])==1
   for mode,wanted in [('error','fixture scan error'),('truncate','扫描连接中断')]:
    cfg['mode']=mode;page.locator('#compare-start').click();expect(page.locator('#error-message')).to_contain_text(wanted);expect(page.locator('#compare-results')).to_be_hidden();page.locator('#error-close').click()
   cfg['mode']='hold';page.locator('#compare-start').click();expect(page.locator('#compare-cancel')).to_be_enabled();page.locator('#compare-cancel').click();expect(page.locator('#compare-progress')).to_contain_text('扫描已取消');expect(page.locator('#compare-results')).to_be_hidden()
   assert len([1 for path,_ in calls if path.endswith('/compare/commit')])==1
   assert page.evaluate('document.documentElement.scrollWidth<=innerWidth'),engine.name
   assert not errors,errors
   print('PASS NAS compare/sort: scoped sort, unknown sizes, isolated result tabs, explicit dedup confirmation, no early/double queue, error/truncation/cancel; '+engine.name+' prefix='+repr(prefix))
   context.close()
  browser.close()
