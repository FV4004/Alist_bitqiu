(() => {
  'use strict';
  const base = new URL(document.currentScript.src).pathname.split('/bitqiu-assets/')[0];
  const $ = id => document.getElementById(id);
  const state = {cloud:[],targets:[],source:'',sourceRoot:'',target:'',targetRoot:'',total:0,items:[],selected:new Set(),loading:false,submitting:false,sourceSeq:0,targetSeq:0,taskBusy:false,orderBy:"modified",orderDirection:"desc",history:[],historyPage:1,selectedMeta:new Map(),taskSelected:new Set(),active:[],historyTab:false,selectAllBusy:false,selectionEpoch:0,limitBusy:false,limitSeq:0,limit:5};
  const join = (p,n) => (p==='/'?'':p.replace(/\/$/,''))+'/'+n;
  const parent = (p,root) => p===root?root:(p.slice(0,p.lastIndexOf('/'))||root);
  const within = (p,root) => p===root||p.startsWith(root.replace(/\/$/,'')+'/');
  const el = (tag,text,cls) => {const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n;};
  const icon=window.NASIcons.icon;
  state.leaves=[];state.expanded=new Set();state.sourceComplete=false;state.sourceRows=new Map();state.sourceController=null;state.targetItems=[];
  const categories=[['folder','文件夹'],['video','视频'],['audio','音频'],['image','图片'],['document','文档'],['archive','压缩包'],['code','代码'],['file','其他']];
  state.filter={query:'',types:new Set(categories.map(x=>x[0])),searchScopes:new Set(['source','target']),typeScopes:new Set(['source','target'])};
  let lastFilterKeys=null;
  const {matches,DirectoryCache,sortItems}=window.NASPicker;
  state.filter.sortScopes=new Set(['source','target']);state.foldersFirst=true;
  const sortSpec=side=>state.filter.sortScopes.has(side)?[state.orderBy,state.orderDirection,state.foldersFirst]:side==='source'?['modified','desc',false]:['name','asc',true];
  const sourceCache=new DirectoryCache(),targetCache=new DirectoryCache();
  state.targetReady=false;
  const queryFor=side=>state.filter.searchScopes.has(side)?state.filter.query:'';
  const hasFilter=side=>!!queryFor(side).trim()||(state.filter.typeScopes.has(side)&&state.filter.types.size!==categories.length);
  const filterKey=side=>JSON.stringify([queryFor(side),state.filter.typeScopes.has(side)?[...state.filter.types].sort():null,sortSpec(side)]);
  const visible=(file,side)=>matches(file.name,queryFor(side))&&(!state.filter.typeScopes.has(side)||state.filter.types.has(window.NASIcons.type(file.name,file.is_dir)));
  const sourceVisible=()=>state.items.filter(file=>visible(file,'source'));
  function remember(side){
    const source=side==='source',cache=source?sourceCache:targetCache,root=state[side+'Root'],path=state[side];
    if(!path||state[side+'LoadedKey']!==cache.key(root,path)||(source?(!state.sourceComplete||state.loading):!state.targetReady))return;
    const snapshot={items:source?state.items:state.targetItems,filterKey:filterKey(side),scroll:source?$('source-files').closest('.table-scroll').scrollTop:$('target-folders').scrollTop};
    if(source)Object.assign(snapshot,{selected:new Set(state.selected),meta:new Map(state.selectedMeta)});
    cache.put(root,path,snapshot);
  }
  function pickerHistory(replace=false){
    const nasPicker={source:state.source,sourceRoot:state.sourceRoot,target:state.target,targetRoot:state.targetRoot};
    history[replace?'replaceState':'pushState']({...history.state,nasPicker},'',location.href);
  }
  async function navigate(side,path,root=state[side+'Root'],record=true){
    if(path===state[side]&&root===state[side+'Root'])return;
    remember(side);state[side+'Root']=root;state[side]=path;$(side+'-storage').value=root;
    if(side==='source'){state.selected.clear();state.selectedMeta.clear();selectionKey='';}
    if(record)pickerHistory();
    return side==='source'?loadSource(false):loadTarget();
  }
  window.addEventListener('popstate',event=>{const saved=event.state?.nasPicker;if(!saved)return;
    run(async()=>{const actions=[];for(const [side,roots]of [['source',state.cloud],['target',state.targets]]){
      const root=saved[side+'Root'],path=saved[side];if(roots.some(x=>x.path===root)&&typeof path==='string'&&within(path,root))actions.push(navigate(side,path,root,false));
    }await Promise.all(actions);})();
  });
  const bytes = n => {if(!Number.isFinite(n)||n<0)return '—';const u=['B','KB','MB','GB','TB'];let i=0;while(n>=1024&&i<4){n/=1024;i++;}return `${n.toFixed(i?1:0)} ${u[i]}`;};
  let errorOrigin;
  function showError(message,title='操作失败',context='',origin){
    const dialog=$('error-dialog');
    $('error-title').textContent=title;$('error-message').textContent=String(message||'操作失败，请稍后重试。');
    $('error-context').textContent=context;$('error-context').hidden=!context;
    $('error-login').hidden=!/登录|管理员/.test(message);$('error-login').href=base+'/@login';
    if(!dialog.open){errorOrigin=origin||document.activeElement;dialog.showModal();}
    $('error-close').focus({preventScroll:true});
  }
  $('error-close').onclick=()=>$('error-dialog').close();
  $('error-dialog').addEventListener('close',()=>{if(errorOrigin?.isConnected&&!errorOrigin.disabled)errorOrigin.focus({preventScroll:true});else $('tasks-refresh').focus({preventScroll:true});});
  function notice(message,error=false,origin){$('notice').hidden=false;$('notice').className='notice'+(error?' error':'');$('notice').replaceChildren(el('span',message));if(error)showError(message,'操作失败','',origin);}
  let handoffAuth=null;
  const handoffKey='alist_nas_auth:'+base;
  async function restoreLogin(){
    const fragment=new URLSearchParams(location.hash.slice(1)),ticket=fragment.get('nas-ticket');
    if(ticket){
      // Remove the one-time ticket before issuing any other request.
      history.replaceState(null,'',location.pathname+location.search);
      const response=await fetch(base+'/api/auth/nas-handoff',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({ticket})});
      const value=await response.json();
      if(value.code!==200||!value.data?.token||!value.data?.client_id)throw Error('NAS 登录交接已过期，请返回 AList 首页重新打开 NAS 下载。');
      handoffAuth=value.data;
      try{sessionStorage.setItem(handoffKey,JSON.stringify(handoffAuth));}catch{}
    }else{try{handoffAuth=JSON.parse(sessionStorage.getItem(handoffKey)||'null');}catch{}}
  }
  // Reuse the official frontend's device identity; never create a second session.
  function requestHeaders(){
    let token,clientID;
    try{token=localStorage.getItem('token')||'';clientID=localStorage.getItem('alist_client_id')||'';}catch{if(!handoffAuth)throw Error('当前浏览器无法读取 AList 登录状态。请允许网站存储，或在 Safari 中打开相同地址并登录。');}
    if(handoffAuth){token=handoffAuth.token;clientID=handoffAuth.client_id;}
    if(!token)throw Error('此页面未读取到 AList 登录状态。请在当前浏览器登录 AList 管理员账户；极空间 App 内仍失败时，可用 Safari 打开相同地址登录。');
    if(!clientID)throw Error('未读取到 AList 设备标识，请返回 AList 首页后重新登录，再打开 NAS 下载。');
    return {Authorization:token,'Client-Id':clientID,'Content-Type':'application/json'};
  }
  function apiError(value,status){
    const code=Number(value?.code||status),message=String(value?.message||'');
    if(message.includes('too many active devices'))return 'AList 活跃设备数量已达上限，请在会话管理中退出不再使用的设备后重试。';
    if(message.includes('session inactive'))return '当前 AList 设备会话已失效，请在当前浏览器重新登录。';
    if(code===401)return 'AList 登录已失效，请在当前浏览器重新登录。';
    if(code===403&&message.includes('You are not an admin'))return 'NAS 下载仅对 AList 管理员开放，请切换为具有管理员角色的账户。';
    if(code===403)return 'AList 拒绝了此请求：'+(message||'请检查设备会话及账户权限。');
    return message||'操作失败';
  }
  async function api(path,body,signal){const r=await fetch(base+'/api'+path,{method:body===undefined?'GET':'POST',headers:requestHeaders(),body:body===undefined?undefined:JSON.stringify(body),signal});let v;try{v=await r.json();}catch{throw Error('服务未返回 JSON，请确认容器已更新。');}if(v.code!==200)throw Error(apiError(v,r.status));return v.data;}
  const run = fn => (...args) => {const origin=document.activeElement;return Promise.resolve().then(()=>fn(...args)).catch(e=>notice(e.message,true,origin));};
  let selectionKey='',statsTimer,statsController,statsSeq=0;
  function clearSelection(){state.selectionEpoch++;state.selected.clear();state.selectedMeta.clear();selectionKey='';selectedUI();}
  function choose(file,on){if(on){state.selected.add(file.name);state.selectedMeta.set(file.name,file);}else{state.selected.delete(file.name);state.selectedMeta.delete(file.name);}}
  function selectionSummary(){
    const key=JSON.stringify([state.source,...state.selected]);if(key===selectionKey)return;selectionKey=key;const seq=++statsSeq;clearTimeout(statsTimer);statsController?.abort();
    const files=[...state.selected].map(name=>state.selectedMeta.get(name));$('selection-count').textContent=`已选择：${files.length} 项`;
    if(files.some(file=>file?.is_dir)){
      $('selection-total').textContent='本次下载总大小：正在递归统计文件夹…';
      statsTimer=setTimeout(async()=>{statsController=new AbortController();try{const stats=await api('/admin/bitqiu/nas/stats',{src_dir:state.source,names:[...state.selected]},statsController.signal);if(seq!==statsSeq)return;$('selection-count').textContent=`已选择：${state.selected.size} 项（包含 ${stats.file_count} 个文件）`;showTotal(stats.known_bytes,stats.unknown_count);}catch(error){if(seq===statsSeq&&error.name!=='AbortError')$('selection-total').textContent='总大小暂不可用：统计失败，'+error.message;}},250);
    }else{let known=0,unknown=0;for(const file of files){if(file?.size_known===true&&Number.isFinite(file.size)&&file.size>=0)known+=file.size;else unknown++;}showTotal(known,unknown);}
  }
  function showTotal(known,unknown){$('selection-total').textContent=unknown?`已知大小：${bytes(known)}，另有 ${unknown} 个文件大小未知`:`本次下载总大小：${bytes(known)}`;}
  function selectedUI(){const n=state.selected.size,visible=sourceVisible(),marked=visible.filter(file=>state.selected.has(file.name)).length,query=hasFilter('source');$('source-count').textContent=`${state.sourceComplete?'共':'已读取'} ${state.total} 项${query?' · 匹配 '+visible.length+' 项':''} · 已选 ${n} 项${query&&n>marked?'（筛选外 '+(n-marked)+' 项）':''}`;$('download').textContent=`下载已选 ${n} 项到 NAS`;$('download').disabled=!n||!state.target||state.loading||!state.sourceComplete||state.submitting||state.selectAllBusy;$('save-to').textContent=state.target||'尚未选择目标目录';$('select-page').disabled=state.selectAllBusy||state.loading||!state.sourceComplete||!visible.length;$('select-page').checked=state.selectAllBusy||(visible.length>0&&marked===visible.length);$('select-page').indeterminate=state.selectAllBusy||(marked>0&&marked<visible.length);$('select-page').parentElement.lastChild.textContent=query?'全选筛选结果':'全选当前目录';for(const check of $('source-files').querySelectorAll('input[type=checkbox]')){check.checked=state.selected.has(check.dataset.name);check.disabled=state.loading||!state.sourceComplete;}selectionSummary();}
  function options(id,items,placeholder){const select=$(id);select.replaceChildren();if(!items.length){const o=el('option',placeholder);o.value='';select.append(o);}for(const t of items){const o=el('option',t.label);o.value=t.path;select.append(o);}}
  async function targets(){sourceCache.clear();targetCache.clear();const data=await api('/admin/bitqiu/nas/targets');state.cloud=data.cloud||[];state.targets=data.targets||[];options('source-storage',state.cloud,'尚未配置 BitQiu 存储');options('target-storage',state.targets,'尚未启用本地目录');$('setup').hidden=!!data.downloads_enabled;$('enable').disabled=!data.downloads_ready;$('enable').title=data.downloads_ready?'将 /downloads 添加为本地存储':'尚未检测到 /downloads 挂载目录';const requested=new URLSearchParams(location.search).get('path')||'';const src=state.cloud.find(x=>within(requested,x.path))||state.cloud[0];state.source='';state.sourceRoot='';state.target='';state.targetRoot='';state.selected.clear();state.selectedMeta.clear();if(src){state.sourceRoot=src.path;state.source=within(requested,src.path)?requested:src.path;$('source-storage').value=src.path;}const preferred=state.targets.find(x=>x.path==='/NAS下载')||state.targets[0];if(preferred){state.targetRoot=preferred.path;state.target=state.targetRoot;$('target-storage').value=state.targetRoot;}await Promise.all([loadSource(false),loadTarget(),loadTasks()]);pickerHistory(true);if(!state.cloud.length)notice('请先在 AList 存储管理中配置并登录 BitQiu。',true);}
  function sourceRow(file){
    const tr=el('tr'),checkCell=el('td'),check=el('input');check.type='checkbox';check.dataset.name=file.name;check.setAttribute('aria-label','选择 '+file.name);check.onchange=()=>{choose(file,check.checked);selectedUI();};const label=el('label',undefined,'file-check');label.append(check);checkCell.append(label);
    const nameCell=el('td'),heading=el('div',undefined,'file-heading');
    if(file.is_dir){const button=el('button',undefined,'file-name');button.append(icon(file.name,true),el('span',file.name));button.onclick=run(()=>navigate('source',join(state.source,file.name)));heading.append(button);}else heading.append(icon(file.name,false),el('span',file.name));
    nameCell.append(heading,el('div',`${file.is_dir?'文件夹':file.size_known===false?'大小未知':bytes(file.size)} · 修改于 ${dateText(file.modified)}`,'file-meta'));tr.append(checkCell,nameCell);return tr;
  }
  function renderSource(){
    const items=sortItems(sourceVisible(),...sortSpec('source'));
    const fragment=document.createDocumentFragment(),scroll=$('source-files').closest('.table-scroll'),top=scroll.scrollTop;
    for(const file of items){let row=state.sourceRows.get(file.name);if(!row){row=sourceRow(file);state.sourceRows.set(file.name,row);}fragment.append(row);}
    if(!items.length){const row=el('tr'),cell=el('td',hasFilter('source')?'没有匹配项，请调整搜索或分类筛选。':state.loading?'正在读取目录，内容将逐批显示…':state.sourceComplete?'此目录为空':'目录未加载完成，请刷新重试','empty');cell.colSpan=2;row.append(cell);fragment.append(row);}
    $('source-files').replaceChildren(fragment);scroll.scrollTop=top;selectedUI();
  }
  async function loadSource(refresh=false){
    const seq=++state.sourceSeq;state.sourceController?.abort();state.sourceController=new AbortController();const signal=state.sourceController.signal;
    const key=sourceCache.key(state.sourceRoot,state.source);state.sourceLoadedKey=key;
    if(refresh)sourceCache.delete(state.sourceRoot,state.source);
    const saved=refresh?null:sourceCache.get(state.sourceRoot,state.source);
    if(saved){state.items=saved.items;state.total=saved.items.length;state.sourceRows=new Map();state.sourceComplete=true;state.loading=false;state.selected=new Set(saved.selected);state.selectedMeta=new Map(saved.meta);$('source-path').textContent=state.source;$('source-up').disabled=state.source===state.sourceRoot;$('source-loading').textContent=`已恢复目录 · ${state.total} 项（每个文件夹仅计 1 项；刷新可获取最新内容）`;renderSource();$('source-files').closest('.table-scroll').scrollTop=saved.filterKey===filterKey('source')?saved.scroll:0;return;}
    $('source-files').closest('.table-scroll').scrollTop=0;
    state.items=[];state.total=0;state.sourceRows=new Map();state.sourceComplete=false;state.loading=!!state.source;$('source-path').textContent=state.source;$('source-up').disabled=!state.source||state.source===state.sourceRoot;
    $('source-loading').textContent=state.source?'正在连接，目录内容将逐批显示…':'请选择 BitQiu 存储';renderSource();if(!state.source)return;
    let reader;
    try{
      const response=await fetch(base+'/api/admin/bitqiu/nas/list-stream',{method:'POST',headers:requestHeaders(),body:JSON.stringify({path:state.source,refresh}),signal});
      if(!response.ok||!(response.headers.get('content-type')||'').includes('application/x-ndjson')){let value;try{value=await response.json();}catch{};throw Error(value?apiError(value,response.status):'目录服务未返回有效内容，请确认容器已更新。');}
      if(!response.body)throw Error('当前浏览器无法读取目录，请更新浏览器后重试。');
      reader=response.body.getReader();const decoder=new TextDecoder(),known=new Map();let pending='',doneFrame=null;
      const frame=line=>{if(!line.trim())return;const value=JSON.parse(line);if(doneFrame)throw Error('目录响应异常，请刷新重试。');if(value.event==='error')throw Error(value.message||'目录读取未完成，请刷新重试。');if(value.event==='batch'){if(!Array.isArray(value.content))throw Error('目录内容格式异常');for(const file of value.content){if(typeof file.name!=='string')throw Error('目录名称格式异常');known.set(file.name,file);}state.items=[...known.values()];state.total=known.size;$('source-loading').textContent=`正在加载 · 已读取 ${state.total} 项（仅统计当前目录，不包含子目录内部文件）`;renderSource();}else if(value.event==='done')doneFrame=value;else if(value.event!=='start')throw Error('目录响应格式异常');};
      for(;;){const part=await reader.read();if(seq!==state.sourceSeq)return;pending+=part.done?decoder.decode():decoder.decode(part.value,{stream:true});if(pending.length>8*1024*1024)throw Error('目录响应过大，请重试');let end;while((end=pending.indexOf('\n'))>=0){const line=pending.slice(0,end);pending=pending.slice(end+1);frame(line);}if(part.done){if(pending.trim())frame(pending);break;}}
      if(!doneFrame||doneFrame.total!==known.size)throw Error('目录连接中断，内容尚未加载完整，请刷新重试。');
      if(seq!==state.sourceSeq)return;state.sourceComplete=true;$('source-loading').textContent=`加载完成 · ${state.total} 项（每个文件夹仅计 1 项，不计其内部文件）`;
    }catch(error){if(seq!==state.sourceSeq||error.name==='AbortError')return;$('source-loading').textContent=`加载失败 · 已读取 ${state.total} 项，当前结果不完整，请刷新重试`;throw error;}
    finally{if(reader){try{await reader.cancel();}catch{}reader.releaseLock();}if(seq===state.sourceSeq){state.loading=false;renderSource();remember('source');}}
  }
  function renderTarget(){
    $('target-folders').replaceChildren();const shown=state.targetItems.filter(item=>visible(item,'target'));
    shown.splice(0,shown.length,...sortItems(shown,...sortSpec('target')));
    for(const item of shown){const button=el('button',undefined,'folder'+(!item.selectable?' unavailable-file':''));button.disabled=!item.selectable;button.setAttribute('aria-label',item.selectable?'打开 NAS 文件夹 '+item.name:'不可选择文件 '+item.name);button.append(icon(item.name,item.is_dir),el('span',item.name),el('span',item.selectable?'›':item.is_dir?'不可选择':item.size_known?bytes(item.size):'文件 · 不可选择'));if(item.selectable)button.onclick=run(()=>navigate('target',join(state.target,item.path)));$('target-folders').append(button);}
    if(!shown.length)$('target-folders').append(el('p',state.targetItems.length?(hasFilter('target')?'没有匹配的名称，请调整搜索或分类筛选。':'当前分类没有内容，请调整筛选或重置为全选。'):'此目录为空，可以直接保存到这里。','empty'));
    $('target-count').textContent=`显示 ${shown.length} / ${state.targetItems.length} 项`;
    $('target-filter-summary').textContent=state.filter.types.size===categories.length?'分类：全部':state.filter.types.size===0?'分类：未选':`分类：已选 ${state.filter.types.size} 类`;
    for(const check of $('target-filter-options').querySelectorAll('input'))check.checked=state.filter.types.has(check.value);
  }
  async function loadTarget(refresh=false){
    const seq=++state.targetSeq;state.targetReady=false;state.targetLoadedKey=targetCache.key(state.targetRoot,state.target);
    if(refresh)targetCache.delete(state.targetRoot,state.target);
    const saved=refresh?null:targetCache.get(state.targetRoot,state.target);
    $('target-path').textContent=state.target||'请选择挂载后的本地目录';$('target-up').disabled=!state.target||state.target===state.targetRoot;
    if(saved){state.targetItems=saved.items;state.targetReady=true;}
    selectedUI();
    if(saved){renderTarget();$('target-folders').scrollTop=saved.filterKey===filterKey('target')?saved.scroll:0;return;}
    state.targetItems=[];$('target-folders').scrollTop=0;$('target-folders').replaceChildren(el('p','正在读取 NAS 目录…','empty'));
    if(!state.target){$('target-folders').replaceChildren();$('target-count').textContent='';return;}
    const entries=await api('/admin/bitqiu/nas/entries',{path:state.target});if(seq!==state.targetSeq)return;state.targetItems=entries||[];state.targetReady=true;renderTarget();remember('target');
  }
  function dateText(value){const d=new Date(value);return value&&Number.isFinite(d.getTime())&&d.getUTCFullYear()>1?d.toLocaleString('zh-CN',{hour12:false}):'未知时间';}
  const taskStates={waiting:'等待中',downloading:'下载中',pausing:'暂停中',paused:'已暂停',completed:'已完成',failed:'下载失败',canceling:'取消中',canceled:'已取消'};
  const reasons={skipped:'已跳过',partial_failed:'部分失败',partial_canceled:'部分取消',completed:'正常结束',user_canceled:'用户取消',canceled:'已取消',timeout:'超时',failed:'失败'};
  const allTasks=()=>[...state.active,...state.history].flatMap(t=>t.children?[t,...t.children]:[t]);
  const taskIDs=t=>t.children?t.children.map(x=>x.id):[t.id];
  const historical=t=>['completed','failed','canceled'].includes(t.state);
  function taskGroup(){return state.historyTab?state.history.slice((state.historyPage-1)*20,state.historyPage*20):state.active;}
  function groupTasks(leaves){
    const groups=new Map(),result=[];
    for(const t of leaves){if(!t.group_id){result.push(t);continue;}let g=groups.get(t.group_id);if(!g){g={id:t.group_id,name:t.group_name||'文件夹',is_dir:true,children:[]};groups.set(t.group_id,g);result.push(g);}g.children.push(t);}
    for(const g of groups.values()){
      const c=g.children,files=c.filter(t=>!t.is_dir),required=files.filter(t=>t.completion_reason!=='skipped');
      g.unknown=required.some(t=>!(t.total_bytes>=0));g.total_bytes=g.unknown?-1:required.reduce((n,t)=>n+t.total_bytes,0);g.transferred_bytes=required.reduce((n,t)=>n+(t.transferred_bytes||0),0);
      g.completed_files=files.filter(t=>t.state==='completed'&&t.completion_reason!=='skipped').length;g.skipped_files=files.filter(t=>t.completion_reason==='skipped').length;g.file_count=files.length;
      g.failed_files=c.filter(t=>t.state==='failed').length;g.speed_bytes_per_second=c.reduce((n,t)=>n+(t.speed_bytes_per_second||0),0);
      const ongoing=c.filter(t=>!historical(t));g.state=ongoing.length?(['downloading','pausing','canceling','waiting','paused'].find(s=>ongoing.some(t=>t.state===s))||'waiting'):c.some(t=>t.state==='failed')?'failed':c.some(t=>t.state==='canceled')?'canceled':'completed';
      g.progress=g.total_bytes>0?Math.min(100,g.transferred_bytes*100/g.total_bytes):g.state==='completed'?100:0;
      g.completion_reason=g.state==='failed'?'partial_failed':g.state==='canceled'?'partial_canceled':g.state==='completed'?'completed':'';
      g.start_time=c.map(t=>t.start_time).filter(Boolean).sort()[0];g.end_time=c.map(t=>t.end_time).filter(Boolean).sort().at(-1);
      for(const action of ['start','pause','cancel'])g['can_'+action]=c.some(t=>t['can_'+action]);
    }
    return result;
  }
  function taskSelectionUI(){const tasks=state.leaves,group=taskGroup(),selected=tasks.filter(t=>state.taskSelected.has(t.id));$('task-selected-count').textContent=`已选择 ${selected.length} 项`;const ids=group.flatMap(taskIDs),marked=ids.filter(id=>state.taskSelected.has(id)).length;$('task-select-all').checked=ids.length>0&&marked===ids.length;$('task-select-all').indeterminate=marked>0&&marked<ids.length;for(const action of ['start','pause','cancel']){$('batch-'+action).disabled=!selected.some(t=>t['can_'+action]);$('all-'+action).disabled=!tasks.some(t=>t['can_'+action]||(action==='pause'&&t.state==='waiting'));}for(const check of document.querySelectorAll('[data-task-check]')){const t=allTasks().find(t=>t.id===check.dataset.taskCheck);if(!t)continue;const ids=taskIDs(t),count=ids.filter(id=>state.taskSelected.has(id)).length;check.checked=count===ids.length;check.indeterminate=count>0&&count<ids.length;}$('history-clear-selected').disabled=!state.history.some(t=>taskIDs(t).some(id=>state.taskSelected.has(id)));$('history-clear-all').disabled=!state.history.length;}
  async function taskAction(action,scope,ids){
    const tasks=state.leaves,chosen=scope==='all'?tasks:tasks.filter(t=>ids.includes(t.id));let restart=false;
    if(action==='cancel'&&!window.confirm(scope==='all'?'确认取消所有未完成任务？后端传输将终止，临时文件将删除。':`确认取消选中的 ${chosen.length} 项任务？临时文件将删除。`))return;
    if(action==='start'&&scope==='selected'&&chosen.length===1&&chosen[0].restart_required){if(!window.confirm('下载源无法安全续传。确认丢弃该任务的临时进度，从头重新下载？'))return;restart=true;}
    const result=await api('/admin/bitqiu/nas/control',{action,scope,ids:scope==='all'?[]:ids,confirmed:action==='cancel',restart});
    const errors=(result.details||[]).map(x=>x.error).join('；');notice(`操作完成：成功 ${result.success} 项，失败 ${result.failed} 项，跳过 ${result.skipped} 项。${errors}`,result.failed>0);await loadTasks();
  }
  function taskCard(task,history=false){
    const card=el('div',undefined,'task controlled-task');card.dataset.taskId=task.id;
    const check=el('input');check.type='checkbox';check.dataset.taskCheck=task.id;check.checked=state.taskSelected.has(task.id);check.setAttribute('aria-label','选择任务 '+task.name);check.onchange=()=>{for(const id of taskIDs(task)){if(check.checked)state.taskSelected.add(id);else state.taskSelected.delete(id);}taskSelectionUI();};
    const detail=el('div'),title=el(task.children?'button':'div',undefined,'task-name'+(task.children?' task-folder-toggle':''));title.append(icon(task.name,task.is_dir),el('span',task.name));if(task.children){title.setAttribute('aria-expanded',String(state.expanded.has(task.id)));title.prepend(el('span',state.expanded.has(task.id)?'⌄':'›','folder-chevron'));title.onclick=()=>{if(state.expanded.has(task.id))state.expanded.delete(task.id);else state.expanded.add(task.id);renderTasks();};}detail.append(title,el('div',taskStates[task.state]||task.state,'task-status'));
    if(task.children)detail.append(el('div',`已完成 ${task.completed_files} / ${task.file_count} 个文件 · 跳过 ${task.skipped_files} 项${task.failed_files?' · 失败 '+task.failed_files+' 项':''}`,'task-status'));
    if(task.completion_reason==='skipped')detail.append(el('span','已跳过','result-badge'));
    if(history){const reason=task.completion_reason||({completed:'completed',failed:'failed',canceled:'canceled'}[task.state]);detail.append(el('span',reasons[reason]||'失败','result-badge result-'+reason),el('div',`结束时间：${dateText(task.end_time)}`,'task-status'));}
    if(task.error){detail.append(el('div',task.error,'task-error'));const more=el('button','查看详情','secondary error-details');more.onclick=()=>showError(task.error,'下载错误',task.name);detail.append(more);}
    const meter=el('div');const progress=el('progress');progress.max=100;progress.value=Number.isFinite(task.progress)?Math.max(0,Math.min(100,task.progress)):0;progress.setAttribute('aria-label','下载进度');meter.append(progress,el('div',`${task.unknown?'总大小未知':Math.round(progress.value)+'%'} · ${bytes(task.transferred_bytes||0)} / ${task.total_bytes>=0?bytes(task.total_bytes):'大小未知'}`,'task-status'));const speed=Number.isFinite(task.speed_bytes_per_second)?Math.max(0,task.speed_bytes_per_second):0;if(!history)meter.append(el('div',task.state==='downloading'?`${bytes(speed)}/s`:taskStates[task.state]||'等待传输','task-speed'));
    const actions=el('div',undefined,'task-actions');for(const [action,label]of [['start',task.restart_required?'从头重新下载':'开始'],['pause','暂停'],['cancel','取消']]){const button=el('button',label,'secondary');button.disabled=!task['can_'+action];button.onclick=run(async()=>{button.disabled=true;try{await taskAction(action,'selected',taskIDs(task));}finally{button.disabled=!task['can_'+action];}});actions.append(button);}
    if(history){const clear=el('button','清理记录','secondary');clear.onclick=run(()=>clearHistory('selected',taskIDs(task)));actions.append(clear);}
    const checkLabel=el('label',undefined,'task-check');checkLabel.append(check);card.append(checkLabel,detail,meter,actions);if(task.children){const wrap=el('div',undefined,'task-group');wrap.append(card);if(state.expanded.has(task.id)){const children=el('div',undefined,'task-children');for(const child of task.children)children.append(taskCard(child,history));wrap.append(children);}return wrap;}return card;
  }
  function renderHistory(){const pages=Math.max(1,Math.ceil(state.history.length/20));state.historyPage=Math.min(state.historyPage,pages);$('history-tasks').replaceChildren();const tasks=state.history.slice((state.historyPage-1)*20,state.historyPage*20);for(const task of tasks)$('history-tasks').append(taskCard(task,true));if(!tasks.length)$('history-tasks').append(el('p','暂无历史任务','empty'));$('history-page').textContent=`第 ${state.historyPage} / ${pages} 页`;$('history-prev').disabled=state.historyPage<=1;$('history-next').disabled=state.historyPage>=pages;taskSelectionUI();}
  function taskTab(history){state.historyTab=history;$('active-panel').hidden=history;$('history-panel').hidden=!history;$('active-tab').setAttribute('aria-selected',String(!history));$('history-tab').setAttribute('aria-selected',String(history));taskSelectionUI();}
  function renderTasks(){const present=new Set(state.leaves.map(t=>t.id));for(const id of state.taskSelected)if(!present.has(id))state.taskSelected.delete(id);$('active-count').textContent=state.active.length;$('history-count').textContent=state.history.length;$('active-tasks').replaceChildren();for(const task of state.active)$('active-tasks').append(taskCard(task));if(!state.active.length)$('active-tasks').append(el('p','暂无正在进行的任务','empty'));renderHistory();}
  async function loadTasks(){if(state.taskBusy)return;const limitSeq=state.limitSeq;state.taskBusy=true;try{const data=await api('/admin/bitqiu/nas/tasks');if(limitSeq===state.limitSeq&&!state.limitBusy&&data.concurrency){state.limit=data.concurrency.limit;$('nas-concurrency').value=String(state.limit);$('nas-concurrency').disabled=false;$('concurrency-status').textContent=`下载中 ${data.concurrency.running} 个 / 上限 ${state.limit} 个`;}state.leaves=[...(data.active||[]),...(data.history||[])];const tasks=groupTasks(state.leaves);state.active=tasks.filter(t=>!historical(t)).sort((a,b)=>new Date(b.start_time||0)-new Date(a.start_time||0));state.history=tasks.filter(historical).sort((a,b)=>new Date(b.end_time||b.start_time||0)-new Date(a.end_time||a.start_time||0));renderTasks();}finally{state.taskBusy=false;}}
  function askConflict(conflict){return new Promise(resolve=>{const d=$('conflict-dialog');let choice=null;const origin=document.activeElement;$('conflict-path').textContent=conflict.path;$('conflict-info').replaceChildren(el('p',`NAS 已有：${bytes(conflict.existing_size)} · 修改于 ${dateText(conflict.existing_modified)}`),el('p',`本次下载：${conflict.incoming_size>=0?bytes(conflict.incoming_size):'大小未知'} · 修改于 ${dateText(conflict.incoming_modified)}`));$('conflict-replace').disabled=!conflict.can_replace;$('conflict-type').textContent=conflict.can_replace?'替换将在完整下载成功后执行，下载失败保留旧文件。':'目标类型不匹配，不能替换；可跳过或另选保存目录。';$('conflict-remember').checked=false;const close=value=>{choice=value;d.close();};$('conflict-cancel').onclick=()=>close(null);$('conflict-skip').onclick=()=>close('skip');$('conflict-replace').onclick=()=>close('replace');d.addEventListener('close',()=>{resolve(choice?{choice,remember:$('conflict-remember').checked}:null);if(origin?.isConnected&&!origin.disabled)origin.focus({preventScroll:true});},{once:true});d.showModal();});}
  async function submitDownload(){const plan=await api('/admin/bitqiu/nas/prepare',{src_dir:state.source,dst_dir:state.target,names:[...state.selected]});const choices={};let remembered=null;for(const conflict of plan.conflicts||[]){if(remembered&&(remembered==='skip'||conflict.can_replace)){choices[conflict.id]=remembered;continue;}const answer=await askConflict(conflict);if(!answer)return null;choices[conflict.id]=answer.choice;if(answer.remember)remembered=answer.choice;}return api('/admin/bitqiu/nas/commit',{plan_id:plan.plan_id,choices});}
  async function clearHistory(scope,ids=[]){const d=$('history-dialog');$('history-scope').textContent=scope==='all'?'清空全部可清理的历史记录？':`清理选中的 ${ids.length} 项历史（文件夹会整组清理）？`;$('history-remove-partials').checked=false;let confirmed=false;const answer=await new Promise(resolve=>{const origin=document.activeElement;$('history-dismiss').onclick=()=>d.close();$('history-confirm').onclick=()=>{confirmed=true;d.close();};d.addEventListener('close',()=>{resolve(confirmed);if(origin?.isConnected)origin.focus({preventScroll:true});},{once:true});d.showModal();});if(!answer)return;const result=await api('/admin/bitqiu/nas/history/clear',{scope,ids,confirmed:true,remove_partials:$('history-remove-partials').checked});notice(`已清理 ${result.deleted} 条记录，跳过 ${result.skipped} 条。${result.partial_failures?'有 '+result.partial_failures+' 个临时文件清理失败，请检查 NAS 权限。':''}`,result.partial_failures>0);await loadTasks();}
  for(const [value,label]of categories){const wrap=el('label',undefined,'check'),check=el('input');check.type='checkbox';check.value=value;check.checked=true;check.onchange=()=>{if(check.checked)state.filter.types.add(value);else state.filter.types.delete(value);applyFilters();};wrap.append(check,el('span',label));$('target-filter-options').append(wrap);}
  $('target-filter-reset').onclick=()=>{state.filter.types=new Set(categories.map(x=>x[0]));applyFilters();};
  document.addEventListener('pointerdown',event=>{const filter=$('target-filter');if(filter.open&&!filter.contains(event.target))filter.open=false;});
  $('target-filter').addEventListener('keydown',event=>{if(event.key==='Escape'){$('target-filter').open=false;$('target-filter-summary').focus();}});
  function applyFilters(){
    $('picker-search-clear').disabled=!state.filter.query;
    for(const [kind,key]of [['search','searchScopes'],['type','typeScopes'],['sort','sortScopes']]){
      const scopes=state.filter[key];$(''+kind+'-scope-status').textContent=scopes.size===0?'未应用':scopes.size===2?'已应用于两侧':'仅应用于'+(scopes.has('source')?'网盘':'NAS');
    }
    const keys={source:filterKey('source'),target:filterKey('target')},sourceTop=$('source-files').closest('.table-scroll').scrollTop,targetTop=$('target-folders').scrollTop;
    renderSource();renderTarget();$('source-files').closest('.table-scroll').scrollTop=lastFilterKeys?.source===keys.source?sourceTop:0;$('target-folders').scrollTop=lastFilterKeys?.target===keys.target?targetTop:0;lastFilterKeys=keys;
  }
  const updateSearch=()=>{state.filter.query=$('picker-search').value;applyFilters();};
  $('picker-search').oninput=event=>{if(!event.isComposing)updateSearch();};$('picker-search').oncompositionend=updateSearch;
  $('picker-search-clear').onclick=()=>{$('picker-search').value='';updateSearch();$('picker-search').focus();};
  for(const [kind,key]of [['search','searchScopes'],['type','typeScopes'],['sort','sortScopes']])for(const side of ['source','target'])$(kind+'-'+side).onchange=()=>{if($(kind+'-'+side).checked)state.filter[key].add(side);else state.filter[key].delete(side);applyFilters();};
  applyFilters();
  $('home').href=base+'/';
  $('source-storage').onchange=run(()=>navigate('source',$('source-storage').value,$('source-storage').value));
  $('target-storage').onchange=run(()=>navigate('target',$('target-storage').value,$('target-storage').value));
  $('source-up').onclick=run(()=>navigate('source',parent(state.source,state.sourceRoot)));
  $('target-up').onclick=run(()=>navigate('target',parent(state.target,state.targetRoot)));
  $('target-refresh').onclick=run(()=>loadTarget(true));
  $('source-refresh').onclick=run(()=>{state.selected.clear();state.selectedMeta.clear();return loadSource(true);});
  $('sort-order').onchange=()=>{[state.orderBy,state.orderDirection]=$('sort-order').value.split(':');applyFilters();};
  $('folders-first').onchange=()=>{state.foldersFirst=$('folders-first').checked;applyFilters();};
  $('select-page').onchange=run(()=>{if(!state.sourceComplete||state.loading)return;const visible=sourceVisible();if(visible.length>50000)throw Error('结果超过 50000 项，请缩小搜索范围或分批选择。');for(const file of visible)choose(file,$('select-page').checked);selectedUI();});
  $('clear-selection').onclick=clearSelection;
  $('enable').onclick=run(async()=>{await api('/admin/bitqiu/nas/enable',{});notice('NAS 目录已启用，可以逐级选择保存位置。');await targets();});
  $('reload').onclick=run(targets);
  $('mkdir').onclick=run(async()=>{if(!state.target)throw Error('请先选择 NAS 保存目录。');const name=$('new-folder-name').value.trim();if(!name)throw Error('请输入新文件夹名称。');await api('/admin/bitqiu/nas/mkdir',{path:state.target,name});$('new-folder-name').value='';await loadTarget(true);});
  $('download').onclick=run(async()=>{if(!state.selected.size||!state.target||state.submitting)return;state.submitting=true;selectedUI();try{const data=await submitDownload();if(!data)return;targetCache.clear();state.targetReady=false;state.selected.clear();state.selectedMeta.clear();selectedUI();notice(`已提交 ${(data.tasks||[]).length} 个后台任务。文件将保存到 ${state.target}。`);await loadTasks();}finally{state.submitting=false;selectedUI();}});
  for(const scope of ['all','batch'])for(const action of ['start','pause','cancel'])$(scope+'-'+action).onclick=run(()=>taskAction(action,scope==='all'?'all':'selected',[...state.taskSelected]));
  $('task-select-all').onchange=()=>{for(const task of taskGroup())for(const id of taskIDs(task)){if($('task-select-all').checked)state.taskSelected.add(id);else state.taskSelected.delete(id);}taskSelectionUI();};
  $('active-tab').onclick=()=>taskTab(false);
  $('history-tab').onclick=()=>taskTab(true);
  $('history-prev').onclick=()=>{if(state.historyPage>1)state.historyPage--;renderHistory();};
  $('history-next').onclick=()=>{state.historyPage++;renderHistory();};
  $('nas-concurrency').onchange=run(async()=>{const limit=Number($('nas-concurrency').value);state.limitBusy=true;state.limitSeq++;$('nas-concurrency').disabled=true;try{const result=await api('/admin/bitqiu/nas/concurrency',{limit});state.limit=result.limit;notice(result.warning||`同时下载上限已保存为 ${result.limit} 个，自动暂停 ${result.paused} 个最晚开始的任务。`,!!result.warning);$('concurrency-status').textContent=`下载中 ${result.running} 个 / 上限 ${result.limit} 个`;await loadTasks();}finally{state.limitBusy=false;$('nas-concurrency').value=String(state.limit);$('nas-concurrency').disabled=false;}});
  $('history-clear-all').onclick=run(()=>clearHistory('all'));
  $('history-clear-selected').onclick=run(()=>clearHistory('selected',state.history.flatMap(taskIDs).filter(id=>state.taskSelected.has(id))));
  $('tasks-refresh').onclick=run(loadTasks);
  const comparison={items:[],issues:[],selected:new Set(),tab:'files',limit:200,seq:0,running:false,controller:null,scanID:'',plan:null,finalLimit:200,busy:false};
  function compareRows(){return comparison.items.filter(item=>item.category===comparison.tab);}
  function compareSelection(){const rows=compareRows().filter(x=>!x.blocked),marked=rows.filter(x=>comparison.selected.has(x.id)).length;$('compare-select-all').checked=rows.length>0&&marked===rows.length;$('compare-select-all').indeterminate=marked>0&&marked<rows.length;$('compare-select-all').disabled=!rows.length;$('compare-selected').textContent=`已选 ${comparison.selected.size} 项（文件夹及其文件将在清单中去重）`;$('compare-prepare').disabled=!comparison.selected.size||comparison.running||comparison.busy;}
  function renderComparison(){
    const rows=compareRows();for(const kind of ['files','folders','review']){const button=$('compare-tab-'+kind);button.setAttribute('aria-selected',String(comparison.tab===kind));button.textContent=({files:'缺失文件',folders:'缺失文件夹',review:'待核对缺失项'}[kind])+' '+comparison.items.filter(x=>x.category===kind).length;}
    const list=$('compare-list');list.replaceChildren();
    for(const item of rows.slice(0,comparison.limit)){
      const row=el('div',undefined,'compare-row'),check=el('input');check.type='checkbox';check.checked=comparison.selected.has(item.id);check.disabled=!!item.blocked;check.setAttribute('aria-label','补缺选择 '+item.relative);check.onchange=()=>{if(check.checked)comparison.selected.add(item.id);else comparison.selected.delete(item.id);compareSelection();};
      const label=el('label',undefined,'check');label.append(check);const content=el('div'),name=el('div',undefined,'file-heading');name.append(icon(item.relative,item.is_dir),el('span',item.relative));content.append(name,el('p',item.is_dir?'创建缺失文件夹及选中的内部缺失项目':item.size>=0?bytes(item.size):'大小未知','hint'));
      if(item.candidates?.length)content.append(el('p','疑似 NAS 对象：'+item.candidates.join('、')+'；请明确核对后再勾选。','hint'));
      if(item.blocked)content.append(el('p','此文件夹未完整扫描，禁止整体选择；请查看扫描问题。','task-error'));
      if(item.is_dir){const details=el('details'),summary=el('summary','查看内部缺失项目');details.append(summary);details.ontoggle=()=>{if(!details.open||details.dataset.ready)return;details.dataset.ready='1';const children=comparison.items.filter(x=>x.relative.startsWith(item.relative+'/'));for(const child of children.slice(0,200))details.append(el('p',child.relative+(child.category==='review'?'（待核对）':''),'hint'));if(children.length>200)details.append(el('p',`此处显示前 200 / ${children.length} 项，全部文件请在相应选项卡复核。`,'hint'));if(!children.length)details.append(el('p','无已确认的内部缺失文件；可能为空文件夹，请同时查看扫描问题。','hint'));};content.append(details);}
      row.append(label,content);list.append(row);
    }
    if(!rows.length)list.append(el('p','当前结果分类无缺失项目。','empty'));
    $('compare-more').hidden=rows.length<=comparison.limit;compareSelection();
  }
  async function scanComparison(){
    if(comparison.running)return;if(!state.source||!state.target)throw Error('请先选择网盘来源和 NAS 目录。');
    const source=state.source,destination=state.target,seq=++comparison.seq;comparison.controller?.abort();comparison.controller=new AbortController();comparison.running=true;comparison.items=[];comparison.issues=[];comparison.scanID='';comparison.selected.clear();$('compare-results').hidden=true;$('compare-start').disabled=true;$('compare-cancel').disabled=false;$('compare-progress').textContent='正在扫描…';$('compare-roots').textContent=`网盘：${source} → NAS：${destination}`;
    let reader,done=false;
    try{
      const response=await fetch(base+'/api/admin/bitqiu/nas/compare',{method:'POST',headers:requestHeaders(),body:JSON.stringify({src_dir:source,dst_dir:destination}),signal:comparison.controller.signal});
      if(!response.ok||!(response.headers.get('content-type')||'').includes('application/x-ndjson')){const value=await response.json();throw Error(apiError(value,response.status));}
      if(!response.body)throw Error('当前浏览器不支持扫描结果流，请更新浏览器。');reader=response.body.getReader();const decoder=new TextDecoder();let pending='';
      const frame=line=>{if(!line.trim())return;const data=JSON.parse(line);if(done)throw Error('扫描响应异常，请重新扫描。');if(data.event==='error')throw Error(data.message||'扫描失败');if(data.event==='progress'){$('compare-progress').textContent=`已检查 ${data.visited} 项 · ${data.relative||'根目录'}`;return;}if(data.event!=='done'||!data.scan_id||!Array.isArray(data.items))throw Error('扫描结果不完整');done=true;comparison.scanID=data.scan_id;comparison.items=data.items;comparison.issues=data.issues||[];};
      for(;;){const part=await reader.read();if(seq!==comparison.seq)return;pending+=part.done?decoder.decode():decoder.decode(part.value,{stream:true});if(pending.length>32*1024*1024)throw Error('扫描结果过大，请缩小目录范围。');let end;while((end=pending.indexOf('\n'))>=0){frame(pending.slice(0,end));pending=pending.slice(end+1);}if(part.done){if(pending.trim())frame(pending);break;}}
      if(!done)throw Error('扫描连接中断，结果未完成，请重新扫描。');
      comparison.tab='files';comparison.limit=200;$('compare-issue-count').textContent=String(comparison.issues.length);$('compare-issues').replaceChildren();for(const issue of comparison.issues)$('compare-issues').append(el('p',`${issue.relative||'根目录'}：${issue.message}`,'task-error'));
      $('compare-results').hidden=false;$('compare-progress').textContent=`扫描完成：${comparison.items.length} 个缺失/待核对项目，${comparison.issues.length} 个扫描问题。`;renderComparison();
    }catch(error){if(seq!==comparison.seq)return;comparison.scanID='';comparison.items=[];$('compare-results').hidden=true;$('compare-progress').textContent=error.name==='AbortError'?'扫描已取消，未生成可下载结果。':'扫描失败，未生成可下载结果。';if(error.name!=='AbortError')throw error;}
    finally{if(reader){try{await reader.cancel();}catch{}reader.releaseLock();}if(seq===comparison.seq){comparison.running=false;$('compare-start').disabled=false;$('compare-cancel').disabled=true;compareSelection();}}
  }
  function renderFinalComparison(){const plan=comparison.plan;if(!plan)return;$('compare-final-list').replaceChildren();for(const task of plan.tasks.slice(0,comparison.finalLimit)){$('compare-final-list').append(el('p',`${task.relative} · ${task.is_dir?'创建文件夹':task.size>=0?bytes(task.size):'大小未知'}`));}$('compare-final-more').hidden=plan.tasks.length<=comparison.finalLimit;}
  $('compare-start').onclick=run(scanComparison);$('compare-cancel').onclick=()=>comparison.controller?.abort();
  for(const kind of ['files','folders','review'])$('compare-tab-'+kind).onclick=()=>{comparison.tab=kind;comparison.limit=200;renderComparison();};
  $('compare-select-all').onchange=()=>{for(const item of compareRows()){if(item.blocked)continue;if($('compare-select-all').checked)comparison.selected.add(item.id);else comparison.selected.delete(item.id);}renderComparison();};
  $('compare-clear').onclick=()=>{comparison.selected.clear();renderComparison();};$('compare-more').onclick=()=>{comparison.limit+=200;renderComparison();};
  $('compare-prepare').onclick=run(async()=>{if(comparison.busy)return;comparison.busy=true;compareSelection();try{const plan=await api('/admin/bitqiu/nas/compare/prepare',{scan_id:comparison.scanID,ids:[...comparison.selected]});comparison.plan=plan;comparison.finalLimit=200;$('compare-confirm-roots').textContent=`网盘：${plan.source} → NAS：${plan.destination}`;$('compare-confirm-summary').textContent=`去重后：${plan.files} 个文件、${plan.directories} 个文件夹；已知大小 ${bytes(plan.known_bytes)}${plan.unknown_count?'，另有 '+plan.unknown_count+' 个文件大小未知':''}。跳过 ${(plan.skipped||[]).length} 项。`;$('compare-skip-count').textContent=String((plan.skipped||[]).length);$('compare-skipped').replaceChildren();for(const item of plan.skipped||[])$('compare-skipped').append(el('p',item.relative+'：'+item.reason,'hint'));$('compare-confirm-check').checked=false;$('compare-submit').disabled=true;renderFinalComparison();$('compare-confirm').showModal();}finally{comparison.busy=false;compareSelection();}});
  $('compare-final-more').onclick=()=>{comparison.finalLimit+=200;renderFinalComparison();};$('compare-back').onclick=()=>$('compare-confirm').close();
  $('compare-confirm-check').onchange=()=>{$('compare-submit').disabled=!$('compare-confirm-check').checked||!comparison.plan?.tasks.length||comparison.busy;};
  $('compare-submit').onclick=run(async()=>{if(comparison.busy||!$('compare-confirm-check').checked||!comparison.plan?.tasks.length)return;comparison.busy=true;$('compare-submit').disabled=true;$('compare-back').disabled=true;try{const result=await api('/admin/bitqiu/nas/compare/commit',{plan_id:comparison.plan.plan_id,confirmed:true});$('compare-confirm').close();comparison.plan=null;comparison.selected.clear();comparison.scanID='';$('compare-results').hidden=true;targetCache.clear();state.targetReady=false;$('compare-progress').textContent=`已提交 ${result.queued} 项，确认时跳过 ${(result.skipped||[]).length} 项。再次补缺请重新扫描。`;notice($('compare-progress').textContent);await loadTasks();}finally{comparison.busy=false;$('compare-back').disabled=false;$('compare-submit').disabled=!comparison.plan?.tasks.length||!$('compare-confirm-check').checked;compareSelection();}});
  run(async()=>{await restoreLogin();await targets();setInterval(()=>{if(!document.hidden)loadTasks().catch(()=>{});},4000);})();
})();
