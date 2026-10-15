# Browser integration fixtures; requires Python Playwright and Chromium.
from pathlib import Path
from playwright.sync_api import sync_playwright, expect
import json
import os
root=Path(__file__).resolve().parents[1]/'server/nasui/web'
output=Path(os.environ.get('NAS_UI_ARTIFACTS', '/tmp/alist-nas-ui'))
output.mkdir(parents=True,exist_ok=True)
with sync_playwright() as p:
 browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_PATH','/usr/bin/chromium'),headless=True,args=['--no-sandbox','--disable-dev-shm-usage'])
 for prefix in ['', '/alist']:
  context=browser.new_context(viewport={'width':1440,'height':1000})
  context.add_init_script("localStorage.setItem('token','fixture-token')")
  requests=[]
  errors=[]
  page=context.new_page()
  page.on('pageerror',lambda e: errors.append(str(e)))
  def route(r):
   url=r.request.url
   path=url.split('https://nas.example',1)[-1]
   if '/api/' in path:
    endpoint=path.split('/api',1)[1]
    body=r.request.post_data_json if r.request.post_data else None
    data=None
    if endpoint=='/admin/bitqiu/nas/targets': data={'cloud':[{'path':'/网盘','label':'/网盘'}],'targets':[{'path':'/NAS下载','label':'/NAS下载'}],'downloads_ready':True,'downloads_enabled':True}
    elif endpoint=='/fs/list':
     requests.append(body)
     n,size=body['page'],body['per_page']
     data={'content':[{'name':f'file-{i:03d}.txt','is_dir':False,'size':12345} for i in range((n-1)*size,min(n*size,237))], 'total':237,'filtered_total':237,'has_more':n*size<237}
    elif endpoint=='/admin/bitqiu/nas/dirs': data=[{'path':'电影','label':'电影'}] if body['path']=='/NAS下载' else []
    elif endpoint=='/admin/bitqiu/nas/download': requests.append(body); data={'tasks':[{'id':'fixture-task'}]}
    elif endpoint.startswith('/task/'):data=[]
    elif endpoint.endswith('/mkdir'): data=None
    else: raise AssertionError(endpoint)
    r.fulfill(status=200,content_type='application/json',body=json.dumps({'code':200,'data':data},ensure_ascii=False));return
   name=path.split('?',1)[0].split('/')[-1]
   name='index.html' if name=='@nas' else name
   data=(root/name).read_bytes()
   content='text/html' if name.endswith('.html') else 'text/css' if name.endswith('.css') else 'application/javascript'
   r.fulfill(status=200,content_type=content,body=data)
  page.route('https://nas.example/**',route)
  page.goto('https://nas.example'+prefix+'/@nas')
  expect(page.locator('#source-count')).to_have_text('共 237 项 · 已选 0 项')
  expect(page.locator('#source-files input')).to_have_count(100)
  page.get_by_role('checkbox',name='选择 file-000.txt',exact=True).check()
  page.locator('#source-next').click()
  expect(page.locator('#source-page')).to_have_text('第 2 / 3 页')
  page.get_by_role('checkbox',name='选择 file-100.txt',exact=True).check()
  page.locator('#source-next').click()
  expect(page.locator('#source-page')).to_have_text('第 3 / 3 页')
  expect(page.locator('#source-files input')).to_have_count(37)
  expect(page.locator('#source-next')).to_be_disabled()
  page.get_by_role('checkbox',name='选择 file-236.txt',exact=True).check()
  expect(page.locator('#source-count')).to_have_text('共 237 项 · 已选 3 项')
  page.locator('#target-folders button').click()
  expect(page.locator('#save-to')).to_have_text('/NAS下载/电影')
  page.screenshot(path=str(output/'nas-download-preview.png'),full_page=True)
  page.locator('#download').click()
  expect(page.locator('#notice')).to_contain_text('已提交 1 个后台任务')
  payload=next(x for x in requests if 'names' in x)
  assert payload=={'src_dir':'/网盘','dst_dir':'/NAS下载/电影','names':['file-000.txt','file-100.txt','file-236.txt'],'overwrite':False,'skip_existing':False},payload
  page.set_viewport_size({'width':390,'height':844})
  page.screenshot(path=str(output/'nas-download-mobile-preview.png'),full_page=True)
  assert page.evaluate('document.documentElement.scrollWidth <= window.innerWidth'), 'horizontal overflow'
  assert not errors,errors
  print('PASS browser 237 entries / three pages / cross-page selection / visual NAS target / queue / mobile; prefix='+repr(prefix))
  context.close()
 browser.close()
