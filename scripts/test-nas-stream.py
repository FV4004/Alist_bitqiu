"""Real HTTP streaming proves first paint before completion; fixture data only."""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from threading import Thread, Event
import json
import os
from playwright.sync_api import sync_playwright, expect

ROOT=Path(__file__).resolve().parents[1]/'server/nasui/web'
for prefix in ['', '/alist']:
 gate=Event();first=Event();cfg={'mode':'done','requests':0}
 class Handler(BaseHTTPRequestHandler):
  def log_message(self,*args):pass
  def json(self,data):
   raw=json.dumps({'code':200,'data':data}).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
  def do_GET(self):
   path=self.path.removeprefix(prefix).split('?',1)[0]
   if path=='/api/admin/bitqiu/nas/targets':return self.json({'cloud':[{'path':'/cloud','label':'cloud'}],'targets':[{'path':'/NAS','label':'NAS'}],'downloads_enabled':True})
   if path=='/api/admin/bitqiu/nas/tasks':return self.json({'active':[],'history':[]})
   name='index.html' if path=='/@nas' else path.rsplit('/',1)[-1]
   file=ROOT/name
   if not file.is_file():self.send_error(404);return
   raw=file.read_bytes();self.send_response(200);self.send_header('Content-Type',{'.html':'text/html','.css':'text/css','.js':'application/javascript'}.get(file.suffix,'text/plain'));self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
  def do_POST(self):
   body=json.loads(self.rfile.read(int(self.headers.get('Content-Length','0'))));path=self.path.removeprefix(prefix)
   if path.endswith('/targets'):return self.json({'cloud':[{'path':'/cloud','label':'cloud'}],'targets':[{'path':'/NAS','label':'NAS'}],'downloads_enabled':True})
   if path.endswith('/entries'):return self.json([])
   if not path.endswith('/list-stream'):self.send_error(404);return
   cfg['requests']+=1;mode=cfg['mode']
   self.send_response(200);self.send_header('Content-Type','application/x-ndjson');self.send_header('Cache-Control','no-store');self.send_header('X-Content-Type-Options','nosniff');self.end_headers()
   def send(value):self.wfile.write((json.dumps(value,ensure_ascii=False)+'\n').encode());self.wfile.flush()
   try:
    send({'event':'start'});send({'event':'batch','content':[{'name':'文件夹','is_dir':True,'size':0},{'name':'第一份文件.mp4','is_dir':False,'size':100,'size_known':True}]});first.set()
    if not gate.wait(10):return
    if mode=='error':send({'event':'error','message':'fixture 第二批失败'});return
    if mode=='truncate':return
    send({'event':'batch','content':[{'name':'最后一份文件.pdf','is_dir':False,'size':200,'size_known':True}]});send({'event':'done','total':3})
   except (BrokenPipeError,ConnectionResetError):pass
 server=ThreadingHTTPServer(('127.0.0.1',0),Handler);thread=Thread(target=server.serve_forever,daemon=True);thread.start()
 with sync_playwright() as p:
  browser=p.chromium.launch(executable_path=os.environ.get('CHROMIUM_PATH') or ('/usr/bin/chromium' if Path('/usr/bin/chromium').exists() else None),headless=True,args=['--no-sandbox','--disable-dev-shm-usage'])
  page=browser.new_page();errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
  page.goto(f'http://127.0.0.1:{server.server_port}'+prefix+'/@nas')
  try:expect(page.locator('#source-files input')).to_have_count(2)
  except Exception:
   print('STREAM DIAGNOSTIC',cfg,first.is_set(),page.locator('body').inner_text()[:2500],errors);raise
  assert first.is_set() and not gate.is_set()
  expect(page.locator('#source-loading')).to_contain_text('正在加载')
  expect(page.locator('#select-page')).to_be_disabled();expect(page.locator('#download')).to_be_disabled()
  gate.set();expect(page.locator('#source-count')).to_have_text('共 3 项 · 已选 0 项')
  expect(page.locator('#source-files input')).to_have_count(3)
  page.get_by_role('checkbox',name='选择 第一份文件.mp4',exact=True).check();expect(page.locator('#download')).to_be_enabled()
  for mode in ['error','truncate']:
   cfg['mode']=mode;gate.clear();first.clear();page.locator('#source-refresh').click()
   expect(page.locator('#source-files input')).to_have_count(2)
   expect(page.locator('#source-loading')).to_contain_text('正在加载')
   gate.set();expect(page.locator('#error-dialog')).to_be_visible();page.locator('#error-close').click()
   expect(page.locator('#source-loading')).to_contain_text('当前结果不完整')
   expect(page.locator('#select-page')).to_be_disabled();expect(page.locator('#download')).to_be_disabled()
  assert not errors,errors
  browser.close()
 gate.set();server.shutdown();server.server_close();thread.join()
 print('PASS real streaming: early paint, UTF-8, completion/error/truncation, prefix='+repr(prefix))
