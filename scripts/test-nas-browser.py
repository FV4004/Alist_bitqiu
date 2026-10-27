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
  context.add_init_script("localStorage.setItem('token','fixture-token');localStorage.setItem('alist_client_id','fixture-device')")
  requests=[];errors=[];controls=[];cfg={"limit":5,"order":1,"fail_next":False,"duplicate":False,"control_failure":False}
  tasks=[{'id':id,'name':name,'state':state,'progress':25,'total_bytes':104857600,'transferred_bytes':26214400,'speed_bytes_per_second':2097152,'start_time':'2026-01-02T00:00:00Z','restart_required':id=='restart','start_order':1 if id=='active1' else 0} for id,name,state in [('active1','下载 fixture','downloading'),('paused1','暂停 fixture','paused'),('waiting1','等待 fixture','waiting'),('restart','不支持续传 fixture','paused')]]
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
    elif endpoint=='/admin/bitqiu/nas/list-stream':
     requests.append(body)
     files=[{'name':f'file-{i:03d}.txt','is_dir':i==235,'size':0 if i==1 else 12345+i,'size_known':i!=0,'modified':(datetime(2026,1,1,tzinfo=timezone.utc)+timedelta(hours=i)).isoformat()} for i in range(237)]
     frames=[{'event':'start'}]+[{'event':'batch','content':files[i:i+24]} for i in range(0,len(files),24)]+[{'event':'done','total':237}]
     if cfg.get('stream_fail'):frames=frames[:2]+[{'event':'error','message':'fixture 后续页失败'}]
     r.fulfill(status=200,content_type='application/x-ndjson',body=''.join(json.dumps(frame,ensure_ascii=False)+'\n' for frame in frames));return
    elif endpoint=='/admin/bitqiu/nas/stats':requests.append(body);data={'selected_count':len(body['names']),'file_count':3,'known_bytes':1048576,'unknown_count':1}
    elif endpoint=='/admin/bitqiu/nas/entries':data=[{'path':'电影','name':'电影','label':'电影','is_dir':True,'selectable':True},{'name':'已有视频.mp4','path':'已有视频.mp4','is_dir':False,'selectable':False,'size':1024,'size_known':True},{'name':'说明.pdf','path':'说明.pdf','is_dir':False,'selectable':False,'size':50,'size_known':True}] if body['path']=='/NAS下载' else []
    elif endpoint=='/admin/bitqiu/nas/prepare':
     requests.append(body)
     if cfg['duplicate']:
      r.fulfill(status=200,content_type='application/json',body=json.dumps({'code':409,'message':'重复任务：<文件> 已在下载队列中，请勿重复提交。'}));return
     data={'plan_id':'fixture-plan','conflicts':cfg.get('conflicts',[])}
    elif endpoint=='/admin/bitqiu/nas/commit':requests.append(body);data={'tasks':[{'id':'fixture-task'}]}
    elif endpoint=='/admin/bitqiu/nas/history/clear':
     requests.append(body);assert body['confirmed'];selected=set(body['ids']);removed=[t for t in tasks if t['state'] in ['completed','failed','canceled'] and (body['scope']=='all' or t['id'] in selected or t.get('group_id') in selected)];tasks[:]=[t for t in tasks if t not in removed];data={'deleted':len(removed),'skipped':0,'partial_failures':0}
    elif endpoint=='/admin/bitqiu/nas/tasks':data={'active':[caps(t) for t in tasks if t['state'] not in ['completed','canceled','failed']],'history':[caps(t) for t in tasks if t['state'] in ['completed','canceled','failed']],'concurrency':{'limit':cfg['limit'],'maximum':5,'running':sum(t['state']=='downloading' for t in tasks)}}
    elif endpoint=='/admin/bitqiu/nas/concurrency':
     if cfg['fail_next']:
      cfg['fail_next']=False;r.fulfill(status=200,content_type='application/json',body=json.dumps({'code':500,'message':'fixture 保存失败'}));return
     cfg['limit']=body['limit'];running=sorted((t for t in tasks if t['state']=='downloading'),key=lambda t:t['start_order'],reverse=True)
     paused=running[:max(0,len(running)-cfg['limit'])]
     for t in paused:t['state']='paused'
     data={'limit':cfg['limit'],'maximum':5,'running':len(running)-len(paused),'paused':len(paused),'pending':0}
    elif endpoint=='/admin/bitqiu/nas/control':
     if cfg['control_failure']:
      cfg['control_failure']=False
      r.fulfill(status=200,content_type='application/json',body=json.dumps({'code':200,'data':{'success':1,'failed':1,'skipped':2,'details':[{'error':'任务状态已变化，请刷新后重试'}]}}));return
     controls.append(body);assert body['scope'] in ['all','selected'];success=skipped=failed=0
     for task in tasks:
      if body['scope']=='selected' and task['id'] not in body['ids']:continue
      action=body['action'];eligible=caps(task)['can_'+action] or action=='pause' and body['scope']=='all' and task['state']=='waiting'
      if not eligible:skipped+=1;continue
      if action=='start' and task.get('restart_required') and not body.get('restart'):failed+=1;continue
      success+=1;task['state']={'start':'downloading','pause':'paused','cancel':'canceled'}[action]
      if action=='start':cfg['order']+=1;task['start_order']=cfg['order']
      if action=='cancel':assert body['confirmed'];task.update(completion_reason='user_canceled',end_time='2030-01-01T00:00:00Z')
     data={'success':success,'skipped':skipped,'failed':failed,'details':[]}
    elif endpoint.endswith('/mkdir'):data=None
    else:raise AssertionError(endpoint)
    r.fulfill(status=200,content_type='application/json',body=json.dumps({'code':200,'data':data},ensure_ascii=False));return
   name=path.split('?',1)[0].split('/')[-1];name='index.html' if name=='@nas' else name
   r.fulfill(status=200,content_type='text/html' if name.endswith('.html') else 'text/css' if name.endswith('.css') else 'application/javascript',body=(root/name).read_bytes())
  page.route('https://nas.example/**',route);page.goto('https://nas.example'+prefix+'/@nas')
  expect(page.locator('#source-count')).to_have_text('共 237 项 · 已选 0 项')
  expect(page.locator('#source-files input')).to_have_count(237)
  # TEST-01/02: all direct children in one streamed list, individual deselection.
  page.locator('#select-page').check();expect(page.locator('#source-count')).to_have_text('共 237 项 · 已选 237 项')
  expect(page.locator('#source-files input:checked')).to_have_count(237)
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
  page.get_by_role('checkbox',name='选择 file-136.txt',exact=True).check()
  page.get_by_role('checkbox',name='选择 file-000.txt',exact=True).check();expect(page.locator('#selection-total')).to_contain_text('另有 1 个文件大小未知')
  expect(page.locator('#page-size')).to_have_count(0)
  expect(page.locator('#source-next')).to_have_count(0)
  expect(page.locator('#source-prev')).to_have_count(0)
  expect(page.locator('#source-loading')).to_contain_text('每个文件夹仅计 1 项')
  expect(page.get_by_role('button',name='不可选择文件 已有视频.mp4')).to_be_disabled()
  page.locator('#type-source').uncheck()
  page.locator('#target-filter-summary').click()
  for check in page.locator('#target-filter-options input').all():check.uncheck()
  expect(page.locator('#target-count')).to_have_text('显示 0 / 3 项')
  page.locator('#target-filter-options').get_by_role('checkbox',name='视频',exact=True).check()
  page.locator('#target-filter-options').get_by_role('checkbox',name='文档',exact=True).check()
  expect(page.locator('#target-count')).to_have_text('显示 2 / 3 项')
  expect(page.locator('#target-folders button:enabled')).to_have_count(0)
  page.locator('#target-filter-reset').click()
  expect(page.locator('#target-filter-options input:checked')).to_have_count(8)
  expect(page.locator('#target-count')).to_have_text('显示 3 / 3 项')
  page.locator('#target-filter-summary').click()
  page.locator('#type-source').check()
  page.get_by_role('button',name='打开 NAS 文件夹 电影',exact=True).click();expect(page.locator('#save-to')).to_have_text('/NAS下载/电影')
  page.screenshot(path=str(output/'nas-download-preview.png'),full_page=True)
  cfg['duplicate']=True;page.locator('#download').click()
  expect(page.get_by_role('dialog',name='操作失败')).to_be_visible()
  expect(page.locator('#error-message')).to_have_text('重复任务：<文件> 已在下载队列中，请勿重复提交。')
  assert page.locator('#error-message').locator('*').count()==0
  expect(page.locator('#error-close')).to_be_focused()
  expect(page.locator('#download')).to_be_enabled()
  expect(page.locator('#selection-count')).to_contain_text('3 项')
  page.keyboard.press('Escape');expect(page.locator('#error-dialog')).not_to_be_visible()
  expect(page.locator('#download')).to_be_focused()
  cfg['duplicate']=False
  page.locator('#download').click();expect(page.locator('#notice')).to_contain_text('已提交 1 个后台任务')
  payload=next(x for x in requests if 'dst_dir' in x)
  assert payload=={'src_dir':'/网盘','dst_dir':'/NAS下载/电影','names':['file-236.txt','file-136.txt','file-000.txt']},payload
  # Explicit conflict decisions: remember only within one submission, cancel preserves selection.
  page.get_by_role('checkbox',name='选择 file-001.txt',exact=True).check()
  cfg['conflicts']=[{'id':'collision'+str(i),'path':'/NAS下载/电影/existing'+str(i)+'.mp4','existing_size':100,'incoming_size':200,'can_replace':True} for i in range(2)]
  page.locator('#download').click();expect(page.locator('#conflict-dialog')).to_be_visible()
  page.locator('#conflict-remember').check();page.locator('#conflict-replace').click()
  expect(page.locator('#notice')).to_contain_text('已提交 1 个后台任务')
  expect(page.locator('#selection-count')).to_have_text('已选择：0 项')
  assert requests[-1]=={'plan_id':'fixture-plan','choices':{'collision0':'replace','collision1':'replace'}},requests[-1]
  page.get_by_role('checkbox',name='选择 file-001.txt',exact=True).check()
  page.locator('#download').click();expect(page.locator('#conflict-dialog')).to_be_visible()
  expect(page.locator('#conflict-remember')).not_to_be_checked()
  page.keyboard.press('Escape');expect(page.locator('#selection-count')).to_have_text('已选择：1 项')
  cfg['conflicts'][0]['can_replace']=False
  page.locator('#download').click();expect(page.locator('#conflict-replace')).to_be_disabled()
  page.locator('#conflict-remember').check();page.locator('#conflict-skip').click()
  expect(page.locator('#notice')).to_contain_text('已提交 1 个后台任务')
  expect(page.locator('#selection-count')).to_have_text('已选择：0 项')
  assert requests[-1]['choices']=={'collision0':'skip','collision1':'skip'}
  cfg['conflicts']=[]
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
  cfg['control_failure']=True;page.locator('#batch-pause').click()
  expect(page.locator('#error-message')).to_contain_text('成功 1 项，失败 1 项，跳过 2 项')
  expect(page.locator('#error-message')).to_contain_text('任务状态已变化')
  page.locator('#error-close').click()
  page.locator('#batch-start').click();expect(card('paused1').locator('.task-speed')).to_have_text('2.0 MB/s')
  assert set(controls[-1]['ids'])=={'active1','paused1'} and controls[-1]['scope']=='selected'
  assert next(t for t in tasks if t['id']=='waiting1')['state']=='waiting'
  expect(page.locator('#nas-concurrency')).to_have_value('5')
  page.locator('#nas-concurrency').select_option('1')
  expect(page.locator('#notice')).to_contain_text('自动暂停 1 个最晚开始的任务')
  expect(card('paused1').locator('.task-speed')).to_have_text('已暂停')
  expect(card('active1').locator('.task-speed')).to_have_text('2.0 MB/s')
  page.reload();expect(page.locator('#nas-concurrency')).to_have_value('1')
  cfg['fail_next']=True;page.locator('#nas-concurrency').select_option('2')
  expect(page.locator('#notice')).to_contain_text('fixture 保存失败');expect(page.locator('#nas-concurrency')).to_have_value('1')
  expect(page.locator('#error-message')).to_have_text('fixture 保存失败');page.locator('#error-close').click()
  page.locator('#nas-concurrency').select_option('3');expect(page.locator('#notice')).to_contain_text('上限已保存为 3 个')
  expect(card('paused1').locator('.task-speed')).to_have_text('已暂停')
  page.locator('#history-tab').click()
  for reason in ['正常结束','用户取消','超时','失败']:assert reason in page.locator('#history-tasks').inner_text()
  card('done2').get_by_role('button',name='查看详情').click()
  expect(page.get_by_role('dialog',name='下载错误')).to_be_visible()
  expect(page.locator('#error-context')).to_have_text('历史 fixture 2')
  expect(page.locator('#error-message')).to_have_text('context deadline exceeded')
  page.locator('#error-close').click()
  page.locator('#tasks-refresh').click();expect(page.locator('#error-dialog')).not_to_be_visible()
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
  request_count=len([r for r in requests if 'refresh' in r])
  for sort,name in [('name:asc','file-000.txt'),('modified:asc','file-000.txt'),('size:desc','file-236.txt')]:
   page.locator('#sort-order').select_option(sort);expect(page.locator('#source-files tr').first.locator('.file-heading')).to_contain_text(name)
  assert len([r for r in requests if 'refresh' in r])==request_count
  expect(page.locator('#source-files .file-meta').first).to_contain_text('KB · 修改于')
  for width in [320,360,390,430,768]:
   page.set_viewport_size({'width':width,'height':844})
   assert page.evaluate('document.documentElement.scrollWidth <= window.innerWidth'),'horizontal overflow at '+str(width)
   metrics=page.evaluate('''()=>({width:innerWidth,document_height:document.documentElement.scrollHeight,min_button_height:Math.min(...[...document.querySelectorAll('button')].filter(x=>x.getClientRects().length).map(x=>x.getBoundingClientRect().height)),checkbox_width:document.querySelector('#select-page').getBoundingClientRect().width})''')
   print('PORTRAIT REGRESSION',json.dumps(metrics))
   if width<=430:
    assert metrics['min_button_height']>=44,metrics
    expect(page.locator('.mobile-nav')).to_be_visible()
    page.locator('.mobile-nav a').filter(has_text='保存目录').click()
    box=page.locator('#target-section').bounding_box();assert 60<=box['y']<844,box
    page.set_viewport_size({'width':width,'height':400})
    page.locator('#new-folder-name').fill('');page.locator('#mkdir').click()
    expect(page.locator('#error-dialog')).to_be_visible()
    if width==390:page.screenshot(path=str(output/'nas-error-mobile-preview.png'))
    page.locator('#error-message').evaluate("e=>e.textContent='很长的错误说明'.repeat(200)")
    assert page.locator('#error-dialog').evaluate('e=>e.scrollWidth<=e.clientWidth')
    page.locator('#error-close').click();expect(page.locator('#error-dialog')).not_to_be_visible()
    page.set_viewport_size({'width':width,'height':844})
    page.get_by_role('checkbox',name='选择 file-236.txt',exact=True).check()
    page.set_viewport_size({'width':844,'height':width})
    expect(page.get_by_role('checkbox',name='选择 file-236.txt',exact=True)).to_be_checked()
    page.set_viewport_size({'width':width,'height':844})
    page.locator('.mobile-nav a').filter(has_text='下载任务').click()
    expect(page.locator('#selection-count')).to_have_text('已选择：1 项')
    page.get_by_role('checkbox',name='选择 file-236.txt',exact=True).uncheck()
   if width==390:page.screenshot(path=str(output/'nas-download-mobile-preview.png'),full_page=True)

  page.set_viewport_size({'width':390,'height':844})
  tasks[:]=[{'id':'child-a','name':'海边.mp4','state':'downloading','group_id':'group-travel','group_name':'旅行记录','total_bytes':100,'transferred_bytes':25,'progress':25,'speed_bytes_per_second':10}, {'id':'child-b','name':'合照.jpg','state':'completed','group_id':'group-travel','group_name':'旅行记录','total_bytes':300,'transferred_bytes':300,'progress':100,'end_time':'2030-01-01T00:00:00Z'}]
  page.locator('#tasks-refresh').click();page.locator('#active-tab').click()
  expect(card('group-travel')).to_be_visible();expect(card('child-a')).to_have_count(0)
  expect(card('group-travel').locator('progress')).to_have_js_property('value',81.25)
  card('group-travel').get_by_role('button',name='旅行记录',exact=False).click()
  expect(card('child-a')).to_be_visible();expect(card('child-b')).to_be_visible()
  expect(card('child-a').locator('.type-video')).to_have_count(1)
  expect(card('child-b').locator('.type-image')).to_have_count(1)
  page.locator('#tasks-refresh').click();expect(card('child-a')).to_be_visible()
  tasks[0].update(state='failed',error='fixture failed',end_time='2030-01-01T00:00:00Z')
  page.locator('#tasks-refresh').click();page.locator('#history-tab').click()
  expect(page.locator('#history-count')).to_have_text('1')
  card('group-travel').get_by_role('button',name='清理记录',exact=True).click()
  expect(page.locator('#history-remove-partials')).not_to_be_checked()
  page.locator('#history-dismiss').click();expect(page.locator('#history-count')).to_have_text('1')
  page.locator('#history-clear-all').click();page.locator('#history-remove-partials').check();page.locator('#history-confirm').click()
  expect(page.locator('#history-count')).to_have_text('0')
  assert requests[-1]['remove_partials'] is True and requests[-1]['scope']=='all'
  for element in page.locator('#source-files .type-icon').all():assert element.get_attribute('viewBox')=='0 0 24 24'
  cfg['stream_fail']=True;page.locator('#source-refresh').click()
  expect(page.locator('#error-message')).to_have_text('fixture 后续页失败')
  page.locator('#error-close').click()
  expect(page.locator('#source-loading')).to_contain_text('当前结果不完整')
  expect(page.locator('#source-files input')).to_have_count(24)
  expect(page.locator('#select-page')).to_be_disabled();expect(page.locator('#download')).to_be_disabled()
  cfg['stream_fail']=False;page.locator('#source-refresh').click()
  expect(page.locator('#source-count')).to_have_text('共 237 项 · 已选 0 项')
  assert not errors,errors
  print('PASS TEST-01/02/03/04/05/06/07/09/10/11/12 UI; backend termination/resume verified separately in Go; prefix='+repr(prefix));context.close()
 browser.close()
