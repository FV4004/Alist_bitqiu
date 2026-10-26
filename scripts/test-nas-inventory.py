"""Six-view inventory/review browser acceptance; deterministic API fixtures only."""
from nas_browser_helpers import UNRESTRICTED_SIZE
from pathlib import Path
from urllib.parse import urlparse
import copy
import json
from playwright.sync_api import sync_playwright, expect
ROOT=Path(__file__).resolve().parents[1]/'server/nasui/web'
OUT=Path('/tmp/alist-nas-ui');OUT.mkdir(exist_ok=True)
with sync_playwright() as p:
 for engine in [p.chromium,p.webkit]:
  browser=engine.launch(headless=True)
  for prefix in ['', '/alist']:
   context=browser.new_context(viewport={'width':1280,'height':900},accept_downloads=True)
   context.add_init_script("localStorage.setItem('token','fixture-token');localStorage.setItem('alist_client_id','fixture-device')")
   context.add_init_script(UNRESTRICTED_SIZE)
   page=context.new_page();errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
   obj={'path':'missing.txt','object_id':'cloud-1','is_dir':False,'size':10,'size_known':True,'modified':'2026-10-26T00:00:00Z','scanned_at':'2026-10-26T00:01:00Z','hashes':{},'hash_verified':False}
   detail={'source':obj,'candidates':[],'reasons':['范围内未发现同路径或候选'],'fingerprint':'evidence-1','reviews':[],'stale':False}
   item={'id':'missing','relative':'missing.txt','category':'suspected_missing','is_dir':False,'size':10,'blocked':False,'detail':detail}
   options={'enabled':True,'nas_only':True,'cross_directory':False,'full_hash':False}
   result={'scan_id':'scan-1','source':'/cloud','destination':'/NAS','names':['missing.txt'],'options':options,'completed_at':'2026-10-26T00:01:00Z','items':[item],'issues':[],'inventory':[{**obj,'side':'bitqiu'}]}
   job={'id':'scan-1','source':'/cloud','destination':'/NAS','names':['missing.txt'],'options':options,'state':'completed','percent':100}
   cfg={'started':False,'prepares':0,'commits':0};calls=[]
   def reply(r,data):r.fulfill(content_type='application/json',body=json.dumps({'code':200,'data':data},ensure_ascii=False))
   def route(r):
    path=urlparse(r.request.url).path.removeprefix(prefix)
    if path.startswith('/api/'):
     body=r.request.post_data_json if r.request.post_data else {};calls.append((path,body))
     if path.endswith('/targets'):return reply(r,{'cloud':[{'path':'/cloud','label':'cloud'}],'targets':[{'path':'/NAS','label':'NAS'}],'downloads_enabled':True})
     if path.endswith('/list-stream'):return r.fulfill(content_type='application/x-ndjson',body=json.dumps({'event':'batch','content':[{'name':'missing.txt','size':10,'size_known':True,'is_dir':False}],'provisional':True})+'\n'+json.dumps({'event':'done','total':1,'complete':True})+'\n')
     if path.endswith('/entries'):return reply(r,[])
     if path.endswith('/stats'):return reply(r,{'bytes':10,'files':1,'directories':0,'unknown':0})
     if path.endswith('/tasks'):return reply(r,{'active':[],'history':[],'concurrency':{'limit':5,'maximum':5}})
     if path.endswith('/scan/status'):return reply(r,{'job':job if cfg['started'] else None,'result':result if cfg['started'] else None})
     if path.endswith('/scan/start'):
      assert body['options']['enabled'];cfg['started']=True;return reply(r,{'job':job,'result':result})
     if path.endswith('/scan/review'):
      assert body['ids']==['missing'];assert body['fingerprints']=={'missing':'evidence-1'}
      item['detail']['reviews'].append({'decision':body['decision'],'note':body['note'],'reviewer_name':'admin','at':'2026-10-26T00:02:00Z','fingerprint':'evidence-1'})
      return reply(r,{'job':job,'result':result})
     if path.endswith('/scan/prepare'):
      assert item['detail']['reviews'][-1]['decision']=='missing';assert body['ids']==['missing'];cfg['prepares']+=1
      return reply(r,{'plan_id':'plan-1','source':'/cloud','destination':'/NAS','tasks':[{'relative':'missing.txt','size':10,'is_dir':False}],'skipped':[],'files':1,'directories':0,'known_bytes':10,'unknown_count':0})
     if path.endswith('/scan/commit'):
      assert body['confirmed'];cfg['commits']+=1;return reply(r,{'queued':1,'skipped':[]})
     if path.endswith('/scan/export'):
      assert body['groups'];return r.fulfill(content_type='text/csv',headers={'Content-Disposition':'attachment; filename=nas-inventory.csv'},body='relative_path,category\nmissing.txt,suspected_missing\n')
     return reply(r,{})
    if path in ['/@nas','/']:return r.fulfill(path=str(ROOT/'index.html'),content_type='text/html')
    if path.startswith('/bitqiu-assets/'):
     file=ROOT/path.rsplit('/',1)[1];return r.fulfill(path=str(file),content_type='text/css' if file.suffix=='.css' else 'application/javascript')
    r.fulfill(status=404)
   page.route('**/*',route);page.goto('http://nas.example'+prefix+'/@nas')
   expect(page.locator('#source-files input[type=checkbox]')).to_have_count(1)
   page.locator('#source-files input[type=checkbox]').check();page.locator('#compare-start').click()
   module=page.locator('#inventory-module');expect(module.get_by_role('tab')).to_have_count(6)
   module.get_by_role('tab',name='2 双端盘点').click();expect(module).to_contain_text('1 个文件')
   module.get_by_role('tab',name='3 差异匹配').click();expect(module).to_contain_text('疑似缺失')
   module.get_by_role('tab',name='6 补缺下载').click();expect(module.locator('.inventory-row')).to_have_count(0)
   module.get_by_role('tab',name='4 人工复核').click();expect(module.get_by_role('checkbox',name='missing.txt',exact=True)).not_to_be_checked()
   module.get_by_text('并列信息、候选与复核记录',exact=True).click()
   expect(module).to_contain_text('完整性语义未验证')
   module.get_by_role('textbox',name='复核备注',exact=True).fill('人工核对')
   module.get_by_role('button',name='已确认缺失',exact=True).click()
   expect(module).to_contain_text('已确认缺失')
   module.get_by_role('tab',name='5 报告导出').click()
   with page.expect_download() as download:module.get_by_role('button',name='导出 CSV').click()
   assert download.value.suggested_filename=='nas-inventory.csv'
   module.get_by_role('tab',name='6 补缺下载').click();expect(module.get_by_role('checkbox',name='missing.txt',exact=True)).not_to_be_checked()
   module.get_by_role('checkbox',name='missing.txt',exact=True).check();module.get_by_role('button',name='生成最终去重清单').click()
   expect(page.locator('#compare-confirm')).to_be_visible();expect(page.locator('#compare-submit')).to_be_disabled();assert cfg['commits']==0
   page.locator('#compare-back').click();assert cfg['commits']==0
   module.get_by_role('button',name='生成最终去重清单').click();page.locator('#compare-confirm-check').check();page.locator('#compare-submit').click();expect(page.locator('#compare-confirm')).not_to_be_visible();assert cfg['commits']==1
   page.screenshot(path=str(OUT/f'inventory-{engine.name}-{prefix.replace("/","") or "root"}.png'),full_page=True)
   assert not errors,errors
   context.close()
  browser.close()
print('PASS: six inventory views, explicit review/selection, export, two-phase supplement (Chromium/WebKit, root/prefix)')
