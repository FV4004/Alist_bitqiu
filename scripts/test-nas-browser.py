# Browser integration fixtures; requires Python Playwright and Chromium.
from pathlib import Path
from playwright.sync_api import sync_playwright, expect
import json
import os
from datetime import datetime, timedelta, timezone
root=Path(__file__).resolve().parents[1]/'server/nasui/web'
output=Path(os.environ.get('NAS_UI_ARTIFACTS', '/tmp/alist-nas-ui'))
output.mkdir(parents=True,exist_ok=True)
with sync_playwright() as p:
 browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_PATH') or ('/usr/bin/chromium' if Path('/usr/bin/chromium').exists() else None),headless=True,args=['--no-sandbox','--disable-dev-shm-usage'])
 for prefix in ['', '/alist']:
  context=browser.new_context(viewport={'width':1440,'height':1000})
  context.add_init_script("localStorage.setItem('token','fixture-token')")
  requests=[]
  errors=[]
  active=[{'id':'active1','name':'正在下载 fixture','state':1,'progress':25,'total_bytes':104857600,'transferred_bytes':26214400,'speed_bytes_per_second':2097152,'start_time':'2026-01-02T00:00:00Z'}]
  history=[{'id':'done'+str(i),'name':'历史 fixture '+str(i),'state':s,'completion_reason':reason,'progress':100 if s==2 else 30,'total_bytes':1024,'end_time':f'2026-01-01T00:00:0{i}Z','error':'context deadline exceeded' if reason=='timeout' else ''} for i,(s,reason) in enumerate([(2,'completed'),(4,'user_canceled'),(7,'timeout'),(7,'failed')])]
  history.extend({'id':'older'+str(i),'name':'更早任务 '+str(i),'state':2,'completion_reason':'completed','end_time':'2025-01-01T00:00:00Z'} for i in range(41))
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
     assert body['order_by'] in ['modified','name','size'] and body['order_direction'] in ['asc','desc'],body
     files=[{'name':f'file-{i:03d}.txt','is_dir':False,'size':12345+i,'modified':(datetime(2026,1,1,tzinfo=timezone.utc)+timedelta(hours=i)).isoformat()} for i in range(237)]
     field={'modified':'modified','size':'size','name':'name'}[body['order_by']]
     files.sort(key=lambda f:f[field],reverse=body['order_direction']=='desc')
     data={'content':files[(n-1)*size:n*size], 'total':237,'filtered_total':237,'has_more':n*size<237}
    elif endpoint=='/admin/bitqiu/nas/dirs': data=[{'path':'电影','label':'电影'}] if body['path']=='/NAS下载' else []
    elif endpoint=='/admin/bitqiu/nas/download': requests.append(body); data={'tasks':[{'id':'fixture-task'}]}
    elif endpoint=='/task/copy/undone':data=active
    elif endpoint=='/task/copy/done':data=history
    elif endpoint.startswith('/task/copy/cancel?'):
     task=active.pop(0);task.update(state=4,completion_reason='user_canceled',end_time='2030-01-01T00:00:00Z');history.append(task);data=None
    elif endpoint.startswith('/task/'):data=None
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
  page.get_by_role('checkbox',name='选择 file-236.txt',exact=True).check()
  page.locator('#source-next').click()
  expect(page.locator('#source-page')).to_have_text('第 2 / 3 页')
  page.get_by_role('checkbox',name='选择 file-136.txt',exact=True).check()
  page.locator('#source-next').click()
  expect(page.locator('#source-page')).to_have_text('第 3 / 3 页')
  expect(page.locator('#source-files input')).to_have_count(37)
  expect(page.locator('#source-next')).to_be_disabled()
  page.get_by_role('checkbox',name='选择 file-000.txt',exact=True).check()
  expect(page.locator('#source-count')).to_have_text('共 237 项 · 已选 3 项')
  page.locator('#target-folders button').click()
  expect(page.locator('#save-to')).to_have_text('/NAS下载/电影')
  page.screenshot(path=str(output/'nas-download-preview.png'),full_page=True)
  page.locator('#download').click()
  expect(page.locator('#notice')).to_contain_text('已提交 1 个后台任务')
  payload=next(x for x in requests if 'names' in x)
  assert payload=={'src_dir':'/网盘','dst_dir':'/NAS下载/电影','names':['file-236.txt','file-136.txt','file-000.txt'],'overwrite':False,'skip_existing':False},payload
  expect(page.locator('#active-tasks .task-speed')).to_have_text('2.0 MB/s')
  expect(page.locator('#active-count')).to_have_text('1')
  page.locator('#history-tab').click()
  expect(page.locator('#history-count')).to_have_text('45')
  for reason in ['正常结束','用户取消','超时','失败']:
   assert reason in page.locator('#history-tasks').inner_text()
  expect(page.locator('#history-tasks .task')).to_have_count(20)
  page.locator('#history-next').click();page.locator('#history-next').click()
  expect(page.locator('#history-tasks .task')).to_have_count(5)
  expect(page.locator('#history-next')).to_be_disabled()
  page.locator('#active-tab').click()
  page.locator('#active-tasks button').click()
  expect(page.locator('#active-count')).to_have_text('0')
  expect(page.locator('#history-count')).to_have_text('46')
  for sort,name in [('name:asc','file-000.txt'),('modified:asc','file-000.txt'),('size:desc','file-236.txt')]:
   page.locator('#sort-order').select_option(sort)
   expect(page.locator('#source-files tr').first.locator('.file-heading')).to_contain_text(name)
  expect(page.locator('#source-files .file-meta').first).to_contain_text('KB · 修改于')
  page.locator('#sort-order').select_option('modified:desc')
  page.set_viewport_size({'width':390,'height':844})
  page.screenshot(path=str(output/'nas-download-mobile-preview.png'),full_page=True)
  assert page.evaluate('document.documentElement.scrollWidth <= window.innerWidth'), 'horizontal overflow'
  assert not errors,errors
  print('PASS sort / sizes / speed / history outcomes and paging / cancellation / 237 entries / three pages / cross-page selection / visual NAS target / queue / mobile; prefix='+repr(prefix))
  context.close()
 browser.close()
