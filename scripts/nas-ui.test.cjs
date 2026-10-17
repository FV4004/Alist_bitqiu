const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const script=fs.readFileSync('server/nasui/web/nav.js','utf8');
function browser(prefix=''){
 const timers=[],events={};
 class XHR{open(method,url){this.url=url;}addEventListener(event,callback){this[event]=callback;}send(body){this.sent=body;}}
 const context={URL,XMLHttpRequest:XHR,window:{},document:{currentScript:{src:'https://nas.example'+prefix+'/bitqiu-assets/nav.js'},readyState:'loading',addEventListener(name,fn){events[name]=fn;}},location:{href:'https://nas.example'+prefix+'/cloud',origin:'https://nas.example'},setInterval(fn){timers.push(fn);}};
 vm.runInNewContext(script,context);
 function settings(mode){const req=new XHR;req.open('GET',prefix+'/api/public/settings');req.response={code:200,data:{pagination_type:mode}};req.load();}
 function list(body,url=prefix+'/api/fs/list'){const req=new XHR;req.open('POST',url);req.send(JSON.stringify(body));return JSON.parse(req.sent);}
 return {settings,list,context,events,timers};
}
test('all mode fixes legacy fifty-item truncation while preserving body fields',()=>{
 const b=browser();b.settings('all');assert.deepEqual(b.list({path:'/cloud',page:1,per_page:50,refresh:true}),{path:'/cloud',page:1,per_page:-1,refresh:true});
});
test('pagination and load-more retain real page requests',()=>{
 for(const mode of ['pagination','load_more','auto_load_more']){const b=browser();b.settings(mode);assert.deepEqual(b.list({page:3,per_page:100}),{page:3,per_page:100});}
});
test('unknown settings and external endpoints are untouched',()=>{
 const b=browser();assert.equal(b.list({page:2,per_page:50}).per_page,50);b.settings('all');assert.equal(b.list({per_page:50},'https://other.example/api/fs/list').per_page,50);
});
test('base URL prefix works and bridge is installed once',()=>{
 const b=browser('/alist');b.settings('all');assert.equal(b.list({page:1,per_page:50}).per_page,-1);
 const send=b.context.XMLHttpRequest.prototype.send;vm.runInNewContext(script,b.context);assert.equal(b.context.XMLHttpRequest.prototype.send,send);
});

test('failed identity checks wait for the polling interval instead of recursively retrying',async()=>{
 const b=browser();let calls=0;
 b.context.localStorage={getItem(){return 'stale-fixture-token';}};
 b.context.document.getElementById=()=>null;
 b.context.fetch=()=>{calls++;return calls>2?new Promise(()=>{}):Promise.resolve({json:()=>Promise.reject(new Error('non-JSON response'))});};
 b.events.DOMContentLoaded();
 await new Promise(resolve=>setImmediate(resolve));
 assert.equal(calls,1);
 b.timers[0]();
 await new Promise(resolve=>setImmediate(resolve));
 assert.equal(calls,2);
});

test('navigation reuses the official device identity, including after it changes',async()=>{
 const b=browser('/alist');const headers=[];let id='device-one';
 b.context.localStorage={getItem(key){return key==='token'?'fixture-token':id;}};
 b.context.document.getElementById=()=>null;
 b.context.fetch=(url,options)=>{headers.push(options.headers);return Promise.resolve({json:()=>Promise.resolve({code:403})});};
 b.events.DOMContentLoaded();await new Promise(resolve=>setImmediate(resolve));
 assert.equal(headers[0]['Client-Id'],'device-one');assert.equal(headers[0].Authorization,'fixture-token');
 id='device-two';b.timers[0]();await new Promise(resolve=>setImmediate(resolve));
 assert.equal(headers.length,2);assert.equal(headers[1]['Client-Id'],'device-two');
});
test('navigation waits for the official frontend to create a device ID',async()=>{
 const b=browser();let id='',calls=0;
 b.context.localStorage={getItem(key){return key==='token'?'fixture-token':id;}};
 b.context.document.getElementById=()=>null;
 b.context.fetch=()=>{calls++;return Promise.resolve({json:()=>Promise.resolve({code:403})});};
 b.events.DOMContentLoaded();await new Promise(resolve=>setImmediate(resolve));assert.equal(calls,0);
 id='official-device';b.timers[0]();await new Promise(resolve=>setImmediate(resolve));assert.equal(calls,1);
});
