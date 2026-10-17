(() => {
  'use strict';
  if(window.__alistNASBridge)return;
  window.__alistNASBridge=true;
  const base=new URL(document.currentScript.src).pathname.split('/bitqiu-assets/')[0];
  // Honor the configured browser mode when old frontend paths submit size 50.
  // Positive page sizes in pagination/load-more modes remain unchanged.
  let mode;
  const proto=XMLHttpRequest.prototype,open=proto.open,send=proto.send;
  proto.open=function(method,url,...rest){this.__alistNasURL=new URL(url,location.href);const result=open.call(this,method,url,...rest);if(this.__alistNasURL.origin===location.origin&&this.__alistNasURL.pathname===base+'/api/public/settings'){this.addEventListener('load',()=>{try{const r=typeof this.response==='object'&&this.response!==null?this.response:JSON.parse(this.responseText);if(r.code===200)mode=r.data.pagination_type||'all';}catch{}});}return result;};
  proto.send=function(body){try{if(mode==='all'&&this.__alistNasURL?.origin===location.origin&&this.__alistNasURL.pathname===base+'/api/fs/list'&&typeof body==='string'){const data=JSON.parse(body);data.page=1;data.per_page=-1;body=JSON.stringify(data);}}catch{}return send.call(this,body);};
  let checkedToken='',allowed=false,pending=false;
  function destination(){let path='/';try{path=decodeURIComponent(location.pathname.slice(base.length))||'/';}catch{}if(path.startsWith('/@'))path='/';return base+'/@nas?path='+encodeURIComponent(path);}
  function mount(){
    let token,clientID;try{token=localStorage.getItem('token')||'';clientID=localStorage.getItem('alist_client_id')||'';}catch{return;}
    const identity=token+'\n'+clientID;
    let anchor=document.getElementById('alist-nas-download');
    if(identity!==checkedToken){anchor?.remove();anchor=null;allowed=false;}
    if(!token||!clientID){anchor?.remove();checkedToken='';allowed=false;return;}
    if(checkedToken!==identity&&!pending){pending=true;checkedToken=identity;fetch(base+'/api/me',{headers:{Authorization:token,'Client-Id':clientID}}).then(r=>r.json()).then(r=>{if(checkedToken===identity)allowed=r.code===200&&r.data?.role?.includes(2);}).catch(()=>{checkedToken='';}).finally(()=>{pending=false;if(allowed)mount();});return;}
    if(!allowed)return;
    if(anchor){anchor.href=destination();return;}
    // AList's client router leaves anchors with a target to the browser.
    const a=document.createElement('a');a.id='alist-nas-download';a.textContent='↓ NAS 下载';a.href=destination();a.target='_self';a.onclick=()=>{a.href=destination();};a.title='选择网盘文件并下载到 NAS';a.style.cssText='position:fixed;right:18px;bottom:18px;z-index:9999;padding:10px 16px;border-radius:24px;background:#136b67;color:white;font:600 14px system-ui;text-decoration:none;box-shadow:0 3px 15px #0002';document.body.append(a);
  }
  if(document.readyState==='loading')document.addEventListener('DOMContentLoaded',mount);else mount();setInterval(mount,2500);
})();
