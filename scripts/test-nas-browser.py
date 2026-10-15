"""Browser acceptance tests using deterministic APIs; real transfers are tested in Go."""
from pathlib import Path
from playwright.sync_api import sync_playwright, expect
import json
import os
from datetime import datetime, timedelta, timezone
root=Path(__file__).resolve().parents[1]/'server/nasui/web'
output=Path(os.environ.get('NAS_UI_ARTIFACTS','/tmp/alist-nas-ui'))
output.mkdir(parents=True,exist_ok=True)
with sync_playwright() as p:
 browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_PATH') or ('/usr/bin/chromium' if Path('/usr/bin/chromium').exists() else None),headless=True,args=['--no-sandbox','--disable-dev-shm-usage'])
 for prefix in ['', '/alist']:
  context=browser.new_context(viewport={'width':1440,'height':1000})
  context.add_init_script("localStorage.setItem('token','fixture-token')")
  requests=[];errors=[];controls=[]
  tasks=[{'id':id,'name':name,'state':state,'progress':25,'total_bytes':104857600,'transferred_bytes':26214400,'speed_bytes_per_second':2097152,'start_time':'2026-01-02T00:00:00Z','restart_required':id=='restart'} for id,name,state in [('active1','下载 fixture','downloading'),('paused1','暂停 fixture','paused'),('waiting1','等待 fixture','waiting'),('restart','不支持续传 fixture','paused')]]
  tasks.extend({'id':'done'+str(i),'name':'历史 fixture '+str(i),'state':s,'completion_reason':reason,'progress':100 if s=='completed' else 30,'total_bytes':1024,'end_time':f'2026-01-01T00:00:0{i}Z','error':'context deadline exceeded' if reason=='timeout' else ''} for i,(s,reason) in enumerate([('completed','completed'),('canceled','user_canceled'),('failed','timeout'),('failed','failed')]))
  tasks.extend({'id':'older'+str(i),'name':'更早任务 '+str(i),'state':'completed','completion_reason':'completed','end_time':'2025-01-01T00:00:00Z'} for i in range(41))
  def caps(t):
   return dict(t,can_start=t['state'] in ['waiting','paused','failed'],can_pause=t['state']=='downloading',can_cancel=t['state'] not in ['completed','canceled','canceling'])
  page=context.new_page();page.on('pageerror',lambda e:errors.append(str(e)))
  def route(r):
   path=r.request.url.split('https://nas.example',1)[-1]
   if '/api/' in path:
    endpoint=path.split('/api',1)[1];body=r.request.post_data_json if r.request.post_data else None;data=None
    if endpoint=='/admin/bitqiu/nas/targets':data={'cloud':[{'path':'/网盘','label':'/网盘'}],'targets':[{'path':'/NAS下载','label':'/NAS下载'}],'downloads_ready':True,'downloads_enabled':True}
    elif endpoint=='/fs/list':
     requests.append(body);n,size=body['page'],body['per_page']
     assert body['order_by'] in ['modified','name','size'] and body['order_direction'] in ['asc','desc'],body
     files=[{'name':f'file-{i:03d}.txt','is_dir':i==235,'size':0 if i==1 else 12345+i,'size_known':i!=0,'modified':(datetime(2026,1,1,tzinfo=timezone.utc)+timedelta(hours=i)).isoformat()} for i in range(237)]
     files.sort(key=lambda f:f[body['order_by']],reverse=body['order_direction']=='desc')
     data={'content':files if size==-1 else files[(n-1)*size:n*size],'total':237,'filtered_total':237,'has_more':False if size==-1 else n*size<237}
    elif endpoint=='/admin/bitqiu/nas/stats':requests.append(body);data={'selected_count':len(body['names']),'file_count':3,'known_bytes':1048576,'unknown_count':1}
    elif endpoint=='/admin/bitqiu/nas/dirs':data=[{'path':'电影','label':'电影'}] if body['path']=='/NAS下载' else []
    elif endpoint=='/admin/bitqiu/nas/download':requests.append(body);data={'tasks':[{'id':'fixture-task'}]}
    elif endpoint=='/admin/bitqiu/nas/tasks':data={'active':[caps(t) for t in tasks if t['state'] not in ['completed','canceled','failed']],'history':[caps(t) for t in tasks if t['state'] in ['completed','canceled','failed']]}
    elif endpoint=='/admin/bitqiu/nas/control':
     controls.append(body);assert body['scope'] in ['all','selected'];success=skipped=failed=0
     for task in tasks:
      if body['scope']=='selected' and task['id'] not in body['ids']:continue
      action=body['action'];eligible=caps(task)['can_'+action] or action=='pause' and body['scope']=='all' and task['state']=='waiting'
      if not eligible:skipped+=1;continue
      if action=='start' and task.get('restart_required') and not body.get('restart'):failed+=1;continue
      success+=1;task['state']={'start':'downloading','pause':'paused','cancel':'canceled'}[action]
      if action=='cancel':assert body['confirmed'];task.update(completion_reason='user_canceled',end_time='2030-01-01T00:00:00Z')
     data={'success':success,'skipped':skipped,'failed':failed,'details':[]}
    elif endpoint.endswith('/mkdir'):data=None
    else:raise AssertionError(endpoint)
    r.fulfill(status=200,content_type='application/json',body=json.dumps({'code':200,'data':data},ensure_ascii=False));return
   name=path.split('?',1)[0].split('/')[-1];name='index.html' if name=='@nas' else name
   r.fulfill(status=200,content_type='text/html' if name.endswith('.html') else 'text/css' if name.endswith('.css') else 'application/javascript',body=(root/name).read_bytes())
  page.route('https://nas.example/**',route);page.goto('https://nas.example'+prefix+'/@nas')
  expect(page.locator('#source-count')).to_have_text('共 237 项 · 已选 0 项')
  expect(page.locator('#source-files input')).to_have_count(100)
  # TEST-01/02: all directory entries across three pages, individual deselection.
  page.locator('#select-page').check();expect(page.locator('#source-count')).to_have_text('共 237 项 · 已选 237 项')
  expect(page.locator('#source-files input:checked')).to_have_count(100)
  page.get_by_role('checkbox',name='选择 file-236.txt',exact=True).uncheck()
  assert page.locator('#select-page').evaluate('(e)=>e.indeterminate')
  page.get_by_role('checkbox',name='选择 file-236.txt',exact=True).check();expect(page.locator('#select-page')).to_be_checked()
  page.locator('#select-page').uncheck();expect(page.locator('#source-files input:checked')).to_have_count(0)
  assert not page.locator('#select-page').evaluate('(e)=>e.indeterminate')
  # TEST-09/10: known total, recursive folders and missing size metadata.
  page.get_by_role('checkbox',name='选择 file-236.txt',exact=True).check()
  expect(page.locator('#selection-total')).to_have_text('本次下载总大小：12.3 KB')
  page.get_by_role('checkbox',name='选择 file-235.txt',exact=True).check()
  expect(page.locator('#selection-count')).to_have_text('已选择：2 项（包含 3 个文件）')
  expect(page.locator('#selection-total')).to_contain_text('另有 1 个文件大小未知')
  page.get_by_role('checkbox',name='选择 file-235.txt',exact=True).uncheck()
  page.locator('#source-next').click();expect(page.locator('#source-page')).to_have_text('第 2 / 3 页')
  page.get_by_role('checkbox',name='选择 file-136.txt',exact=True).check()
  page.locator('#source-next').click();expect(page.locator('#source-page')).to_have_text('第 3 / 3 页')
  expect(page.locator('#source-files input')).to_have_count(37);expect(page.locator('#source-next')).to_be_disabled()
  page.get_by_role('checkbox',name='选择 file-000.txt',exact=True).check();expect(page.locator('#selection-total')).to_contain_text('另有 1 个文件大小未知')
  page.locator('#target-folders button').click();expect(page.locator('#save-to')).to_have_text('/NAS下载/电影')
  page.screenshot(path=str(output/'nas-download-preview.png'),full_page=True)
  page.locator('#download').click();expect(page.locator('#notice')).to_contain_text('已提交 1 个后台任务')
  payload=next(x for x in requests if 'dst_dir' in x)
  assert payload=={'src_dir':'/网盘','dst_dir':'/NAS下载/电影','names':['file-236.txt','file-136.txt','file-000.txt'],'overwrite':False,'skip_existing':False},payload
  def card(id):return page.locator('[data-task-id="'+id+'"]')
  expect(card('active1').locator('.task-speed')).to_have_text('2.0 MB/s')
  expect(card('active1').get_by_role('button',name='开始',exact=True)).to_be_disabled()
  expect(card('waiting1').get_by_role('button',name='暂停',exact=True)).to_be_disabled()
  # TEST-03/04: single task controls leave other tasks unchanged.
  card('active1').get_by_role('button',name='暂停',exact=True).click();expect(card('active1').locator('.task-speed')).to_have_text('已暂停')
  assert next(t for t in tasks if t['id']=='waiting1')['state']=='waiting'
  card('active1').get_by_role('button',name='开始',exact=True).click();expect(card('active1').locator('.task-speed')).to_have_text('2.0 MB/s')
  # TEST-05: selected batch scope and checkbox indeterminate state.
  card('active1').get_by_role('checkbox').check();card('paused1').get_by_role('checkbox').check()
  assert page.locator('#task-select-all').evaluate('(e)=>e.indeterminate')
  page.locator('#batch-start').click();expect(card('paused1').locator('.task-speed')).to_have_text('2.0 MB/s')
  assert set(controls[-1]['ids'])=={'active1','paused1'} and controls[-1]['scope']=='selected'
  assert next(t for t in tasks if t['id']=='waiting1')['state']=='waiting'
  page.locator('#history-tab').click()
  for reason in ['正常结束','用户取消','超时','失败']:assert reason in page.locator('#history-tasks').inner_text()
  page.locator('#active-tab').click()
  # TEST-06/11: all pause also stops the waiting queue; state survives reload.
  page.locator('#all-pause').click();expect(card('waiting1').locator('.task-speed')).to_have_text('已暂停')
  assert all(t['state']=='paused' for t in tasks if t['id'] in ['active1','paused1','waiting1','restart'])
  page.reload();expect(card('active1').locator('.task-speed')).to_have_text('已暂停')
  # Explicit confirmation required before throwing away an unsupported partial.
  page.once('dialog',lambda d:d.dismiss());card('restart').get_by_role('button',name='从头重新下载').click()
  assert not controls[-1].get('restart')
  page.once('dialog',lambda d:d.accept());card('restart').get_by_role('button',name='从头重新下载').click()
  expect(card('restart').locator('.task-speed')).to_have_text('2.0 MB/s');assert controls[-1]['restart'] is True
  # TEST-07/08: dismiss confirmation sends nothing; accept cancels unfinished only.
  count=len(controls);page.once('dialog',lambda d:d.dismiss());page.locator('#all-cancel').click();assert len(controls)==count
  page.once('dialog',lambda d:d.accept());page.locator('#all-cancel').click();expect(page.locator('#active-count')).to_have_text('0')
  assert controls[-1]['scope']=='all' and controls[-1]['confirmed']
  page.locator('#history-tab').click();expect(page.locator('#history-count')).to_have_text('49')
  for reason in ['正常结束','用户取消']:assert reason in page.locator('#history-tasks').inner_text()
  expect(page.locator('#history-tasks .task')).to_have_count(20)
  page.locator('#history-next').click();page.locator('#history-next').click();expect(page.locator('#history-tasks .task')).to_have_count(9)
  expect(page.locator('#history-next')).to_be_disabled()
  for sort,name in [('name:asc','file-000.txt'),('modified:asc','file-000.txt'),('size:desc','file-236.txt')]:
   page.locator('#sort-order').select_option(sort);expect(page.locator('#source-files tr').first.locator('.file-heading')).to_contain_text(name)
  expect(page.locator('#source-files .file-meta').first).to_contain_text('KB · 修改于')
  page.set_viewport_size({'width':390,'height':844});page.screenshot(path=str(output/'nas-download-mobile-preview.png'),full_page=True)
  assert page.evaluate('document.documentElement.scrollWidth <= window.innerWidth'),'horizontal overflow'
  assert not errors,errors
  print('PASS TEST-01/02/03/04/05/06/07/09/10/11/12 UI; backend termination/resume verified separately in Go; prefix='+repr(prefix));context.close()
 browser.close()
