const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const script=fs.readFileSync('server/nasui/web/nav.js','utf8');
function browser(prefix=''){
 class XHR{open(method,url){this.url=url;}addEventListener(event,callback){this[event]=callback;}send(body){this.sent=body;}}
 const context={URL,XMLHttpRequest:XHR,window:{},document:{currentScript:{src:'https://nas.example'+prefix+'/bitqiu-assets/nav.js'},readyState:'loading',addEventListener(){}},location:{href:'https://nas.example'+prefix+'/cloud',origin:'https://nas.example'},setInterval(){}};
 vm.runInNewContext(script,context);
 function settings(mode){const req=new XHR;req.open('GET',prefix+'/api/public/settings');req.response={code:200,data:{pagination_type:mode}};req.load();}
 function list(body,url=prefix+'/api/fs/list'){const req=new XHR;req.open('POST',url);req.send(JSON.stringify(body));return JSON.parse(req.sent);}
 return {settings,list,context};
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
