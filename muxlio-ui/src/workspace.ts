// workspace.js — enhancements over the base index.html
// Adds: auto-refresh, connection status, pause/resume, improved event rendering, attempt cards.

let refreshTimer=null,refreshPaused=false,refreshBusy=false,refreshEpoch=0,lastRefresh=null;
let runAttempts=[],requestControllers=new Set<AbortController>(),renderedRun=null,detailRequest=0,eventRecords=[];
const RUN_DETAIL_PAGE_SIZE=50;
let outputOffset=0,eventCursors=[0],eventPageIndex=0,liveAfter=0,liveEventRecords=[],runDataLoaded=false;
function runPager(key,offset,more,label){return renderPagination({label:label+' pages',summary:`${label} · page ${Math.floor(offset/RUN_DETAIL_PAGE_SIZE)+1}`,previous:{attributes:{'data-run-page':key,'data-direction':-1},disabled:offset===0},next:{attributes:{'data-run-page':key,'data-direction':1},disabled:!more}})}
function renderRunPagers(graph,outputs,events){
 const outputsPager=(document.getElementById('outputsPager') as UIElement);if(outputsPager)outputsPager.innerHTML=runPager('outputs',outputOffset,outputs.hasMore,'Artifacts and outputs');
 const eventsPager=(document.getElementById('eventsPager') as UIElement);if(eventsPager)eventsPager.innerHTML=runPager('events',eventPageIndex*RUN_DETAIL_PAGE_SIZE,events.hasMore,'Event history');
 const list=(document.getElementById('wbArtifactList') as UIElement);if(list)list.innerHTML=(outputs.outputs||[]).map(item=>renderViewTemplate("workspace/list-inner-html-artifact-row", [viewValue(esc(item.name)), viewValue(esc(item.nodeId)), viewValue(esc(item.activationId)), viewValue(esc(item.name))])).join('')||renderPlaceholder("No outputs on this page.", "subtle");
}
document.addEventListener('click',async event=>{
 if((event.target as UIElement).closest<UIElement>('#focusCurrent')){const node=document.querySelector<UIElement>('.node-card.running,.node-card.waiting,.node-card.queued,.node-card.failed');if(node){for(let parent=node.parentElement;parent;parent=parent.parentElement)if(parent.tagName==='DETAILS')(parent as any).open=true;node.scrollIntoView({block:'center',inline:'center',behavior:'smooth'});node.focus({preventScroll:true});}return;}
 const output=(event.target as UIElement).closest<UIElement>('[data-open-output]');
 if(output){const act=await loadActivation(output.dataset.openOutput);if(act&&current===act.runId){const value=act.outputs?.[output.dataset.outputName];showArtifactOutput(artifactData(value),output.dataset.outputName,value);}return;}
 const button=(event.target as UIElement).closest<UIElement>('[data-run-page]');if(!button||button.disabled)return;
 if(['attempts','artifact-index'].includes(button.dataset.runPage))return;
 const direction=Number(button.dataset.direction);
 if(button.dataset.runPage==='outputs')outputOffset=Math.max(0,outputOffset+direction*RUN_DETAIL_PAGE_SIZE);
 if(button.dataset.runPage==='events')eventPageIndex=Math.max(0,eventPageIndex+direction);
 await openRun(current);
});
async function loadRunData(){
 if(runDataLoaded||!current)return;
 const id=current,data=await api(`/v1/runs/${encodeURIComponent(id)}`);
 if(id!==current)return;
 currentRunInput=data.input;currentRunOutput=data.output;runDataLoaded=true;
 replaceHTMLPreservingDetails((document.getElementById('runInputInspector') as UIElement),inputMarkup(data.input));
 replaceHTMLPreservingDetails((document.getElementById('runOutputInspector') as UIElement),outputMarkup(data.output,'run-output'));
}
let eventStreamController=null,eventStreamRun=null,eventStreamUnsupported=false,eventStreamRetry=null,eventStreamTerminal=false;

// ── event renderer ────────────────────────────────────────────────────────
// @ts-expect-error TS2630: classic scripts intentionally replace this shared view hook.
eventHtml=function(e){
  const label=eventLabel(e.type),
        tone=String(e.type||'').includes('fail')?'err':String(e.type||'').includes('succeed')?'ok':'',
        time=MuxlioFormat.timestamp(e.timestamp,true);
  return renderViewTemplate("workspace/event-html-ev", [viewValue(tone?' ev-'+tone:''), viewValue(esc(time)), viewValue(esc(label)), viewValue(e.subjectId?renderViewTemplate("workspace/event-html-ev-sub", [viewValue(esc(e.subjectId))]):''), viewValue(esc(e.seq)), viewValue(e.payload?renderViewTemplate("workspace/event-html-ev-data", [viewValue(esc(e.seq)), viewValue(esc(pretty(e.payload)))]):'')]);
};

// ── guarded api with abort + timeout ─────────────────────────────────────
// @ts-expect-error TS2630: classic scripts intentionally replace this shared view hook.
api=async function(path: string,opts: RequestInit={}){
  const epoch=refreshEpoch,controller=new AbortController();
  requestControllers.add(controller);
  const timeout=setTimeout(()=>controller.abort(),15000);
  try{
    const response=await uiFetch(path,{...opts,signal:controller.signal});
    const data=await response.json().catch(()=>({}));
    if(epoch!==refreshEpoch)throw Error('Navigation changed');
    if(!response.ok)throw Error(data.message||data.error||`HTTP ${response.status}`);
    if(epoch===refreshEpoch)connectionOK();
    return data;
  }catch(err){
    if(epoch===refreshEpoch)connectionError(err);
    throw err;
  }finally{
    clearTimeout(timeout);
    requestControllers.delete(controller);
  }
};

// ── event merge (dedupes by seq) ──────────────────────────────────────────
function mergeEvents(page){
 eventRecords=(page.events||[]).slice(0,RUN_DETAIL_PAGE_SIZE);
 after=page.nextAfter||0;hasMore=!!page.hasMore;
 eventCursors[eventPageIndex+1]=page.nextBefore;
 wbHasMore=hasMore;
}

function stopEventStream(){
  if(eventStreamController)eventStreamController.abort();
  eventStreamController=null;eventStreamRun=null;
  eventStreamTerminal=false;
  clearTimeout(eventStreamRetry);eventStreamRetry=null;
}
function renderLiveEvents(){
  const timeline=(document.getElementById('wbTimeline') as UIElement);
  // Keep historical pages fixed while the first page follows new events.
  if(!timeline||eventPageIndex!==0)return;
  const merged=new Map<any, any>(eventRecords.map(event=>[event.seq,event]));
  for(const event of liveEventRecords)merged.set(event.seq,event);
  eventRecords=[...merged.values()].sort((a,b)=>b.seq-a.seq).slice(0,RUN_DETAIL_PAGE_SIZE);
  if(eventRecords.length)eventCursors[1]=eventRecords[eventRecords.length-1].seq;
  replaceHTMLPreservingDetails(timeline,eventRecords.length?eventRecords.map(eventHtml).join(''):renderViewTemplate("workspace/render-live-events-subtle"));
}
async function startEventStream(id){
  if(eventStreamUnsupported||eventStreamRun===id)return;
  stopEventStream();eventStreamRun=id;
  const controller=new AbortController();eventStreamController=controller;
  try{
    const response=await uiFetch(`/v1/runs/${encodeURIComponent(id)}/events/stream?after=${liveAfter}`,{headers:{Accept:'text/event-stream'},signal:controller.signal});
    if(response.status===404||response.status===501){eventStreamUnsupported=true;stopEventStream();return;}
    if(!response.ok)throw Error(`HTTP ${response.status}`);
    if(!response.body){eventStreamUnsupported=true;stopEventStream();return;}
    connectionOK();
    const reader=response.body.getReader(),decoder=new TextDecoder();let buffer='';
    while(true){
      const {value,done}=await reader.read();
      if(done)break;
      buffer+=decoder.decode(value,{stream:true});
      let boundary;
      while((boundary=buffer.indexOf('\n\n'))>=0){
        const frame=buffer.slice(0,boundary);buffer=buffer.slice(boundary+2);
        const data=frame.split('\n').find(line=>line.startsWith('data:'));
        if(!data)continue;
        try{
          const event=JSON.parse(data.slice(5).trim());
          if(['run.succeeded','run.failed','run.cancelled'].includes(event.type))eventStreamTerminal=true;
          if(event.seq>liveAfter){
            const records=new Map(liveEventRecords.map(item=>[item.seq,item]));records.set(event.seq,event);
            liveEventRecords=[...records.values()].sort((a,b)=>a.seq-b.seq).slice(-50);liveAfter=event.seq;renderLiveEvents();
          }
        }catch{}
      }
    }
  }catch(err){if(err.name!=='AbortError'&&!eventStreamUnsupported)connectionError(err);}
  finally{
    if(eventStreamController===controller){eventStreamController=null;eventStreamRun=null;
      if(!eventStreamUnsupported&&!eventStreamTerminal&&currentRoute().split('?')[0]===`/runs/${encodeURIComponent(id)}`)eventStreamRetry=setTimeout(()=>startEventStream(id),1000);
    }
  }
}

const _renderWorkbench=renderWorkbench;
// @ts-expect-error TS2630: classic scripts intentionally replace this shared view hook.
renderWorkbench=function(r,g,events){
  _renderWorkbench(r,g,events);
  const actions=document.querySelector<UIElement>('.top-actions');
  if(!actions)return;
  let controls=(document.getElementById('runControls') as UIElement);
  if(!controls){controls=document.createElement('span') as UIElement;controls.id='runControls';controls.style.cssText='display:flex;gap:7px';actions.prepend(controls);}
  const created=r.state==='created',active=['running','waiting'].includes(r.state),paused=r.state==='paused',retryable=['failed','cancelled'].includes(r.state);
  controls.innerHTML=`${created?renderViewTemplate("workspace/controls-inner-html-btn"):''}${active?renderViewTemplate("workspace/controls-inner-html-btn-2"):''}${paused?renderViewTemplate("workspace/controls-inner-html-btn-3"):''}${retryable?renderViewTemplate("workspace/controls-inner-html-btn-4"):''}${active||paused?renderViewTemplate("workspace/controls-inner-html-btn-5"):''}`;
  controls.querySelectorAll<UIElement>('[data-run-action]').forEach(button=>button.onclick=async()=>{
    const action=button.dataset.runAction;
    if(action==='cancel'){if(!await openCancelRunModal())return;}
    if(action==='retry'){await retryRun(r.id,button);return;}
    button.disabled=true;
    try{await api(`/v1/runs/${encodeURIComponent(r.id)}/${action}`,{method:'POST'});await openRun(r.id);}
    catch(err){message(err.message,true);button.disabled=false;}
  });
};

let retryModalResolve=null;

function openRetryRunModal(){
  if(retryModalResolve)return Promise.resolve(false);
  return new Promise(resolve=>{
    const modal=(document.getElementById('retryRunModal') as UIElement);
    if(!modal){resolve(false);return;}
    retryModalResolve=resolve;
    modal.hidden=false;
    modal.style.display='';
  });
}

function closeRetryRunModal(confirmed){
  const modal=(document.getElementById('retryRunModal') as UIElement);
  if(modal)modal.hidden=true;
  const resolve=retryModalResolve;
  retryModalResolve=null;
  if(resolve)resolve(confirmed);
}

async function retryRun(runId,triggerBtn){
  if(!await openRetryRunModal())return;
  if(triggerBtn)triggerBtn.disabled=true;
  try{
    await api(`/v1/runs/${encodeURIComponent(runId)}/retry`,{method:'POST'});
    await openRun(runId);
  }catch(err){
    message(err.message,true);
    if(triggerBtn)triggerBtn.disabled=false;
  }
}

(document.getElementById('retryRunModalCancel') as UIElement)?.addEventListener('click',()=>closeRetryRunModal(false));
(document.getElementById('retryRunModalConfirm') as UIElement)?.addEventListener('click',()=>closeRetryRunModal(true));
(document.getElementById('retryRunModal') as UIElement)?.addEventListener('click',e=>{
  if((e.target as UIElement)===e.currentTarget)closeRetryRunModal(false);
});
document.addEventListener('keydown',e=>{
  if(e.key!=='Escape')return;
  const modal=(document.getElementById('retryRunModal') as UIElement);
  if(!modal||modal.hidden||!retryModalResolve)return;
  e.preventDefault();
  closeRetryRunModal(false);
});

let cancelModalResolve=null;

function openCancelRunModal(){
  if(cancelModalResolve)return Promise.resolve(false);
  return new Promise(resolve=>{
    const modal=(document.getElementById('cancelRunModal') as UIElement);
    if(!modal){resolve(false);return;}
    cancelModalResolve=resolve;
    modal.hidden=false;
    modal.style.display='';
  });
}

function closeCancelRunModal(confirmed){
  const modal=(document.getElementById('cancelRunModal') as UIElement);
  if(modal)modal.hidden=true;
  const resolve=cancelModalResolve;
  cancelModalResolve=null;
  if(resolve)resolve(confirmed);
}

(document.getElementById('cancelRunModalCancel') as UIElement)?.addEventListener('click',()=>closeCancelRunModal(false));
(document.getElementById('cancelRunModalConfirm') as UIElement)?.addEventListener('click',()=>closeCancelRunModal(true));
(document.getElementById('cancelRunModal') as UIElement)?.addEventListener('click',e=>{
  if((e.target as UIElement)===e.currentTarget)closeCancelRunModal(false);
});
document.addEventListener('keydown',e=>{
  if(e.key!=='Escape')return;
  const modal=(document.getElementById('cancelRunModal') as UIElement);
  if(!modal||modal.hidden||!cancelModalResolve)return;
  e.preventDefault();
  closeCancelRunModal(false);
});

// ── wire #inspRerun — retries the run from the last checkpoint ────────────
document.addEventListener('click', async function(e) {
  const btn = (e.target as UIElement)?.closest?.('#inspRerun');
  if (!btn || !current || !['failed','cancelled'].includes(wbCurrentRunState)) return;
  await retryRun(current, btn);
});

// ── openRun: wraps workbench openRun to add attempts + event merge ────────
const _baseOpenRun=openRun;
// @ts-expect-error TS2630: classic scripts intentionally replace this shared view hook.
openRun=async function(id){
  const epoch=refreshEpoch,seq=++detailRequest,same=renderedRun===id;
  if(!same){selectedActivation=null;selectedActivationDetail=null;after=0;eventRecords=[];outputOffset=0;eventCursors=[0];eventPageIndex=0;liveEventRecords=[];runDataLoaded=false;runAttempts=[];wbActivityAttempts=[];wbInputRequestSummary=[];
 for(const key of ['runInputInspector','runOutputInspector']){const el=(document.getElementById(key) as UIElement);if(el)el.textContent='Open the Data tab to load run inputs and outputs.';}}

  try{
    const r=await api(`/v1/runs/${encodeURIComponent(id)}?summary=true`);
    if(epoch!==refreshEpoch||seq!==detailRequest||current!==id)return;
    if(!r.overview)throw Error('Coordinator update required: this UI uses paged run views. Upgrade the coordinator before opening run histories.');
    const [g,e,o,l,attemptPage]=await Promise.all([
      api(`/v1/runs/${encodeURIComponent(id)}/graph`),
      api(`/v1/runs/${encodeURIComponent(id)}/events?order=desc&before=${eventCursors[eventPageIndex]||0}&limit=${RUN_DETAIL_PAGE_SIZE}`),
      api(`/v1/runs/${encodeURIComponent(id)}/outputs?offset=${outputOffset}&limit=${RUN_DETAIL_PAGE_SIZE}`),
      api(`/v1/runs/${encodeURIComponent(id)}/events?tail=true&limit=50`),
      api(`/v1/runs/${encodeURIComponent(id)}/attempts?offset=0&limit=50&details=false`)
    ]);
    if(epoch!==refreshEpoch||seq!==detailRequest||current!==id)return;
    wbActivityAttempts=attemptPage.attempts||[];
    if(!same)liveAfter=r.overview?.latestEventSeq||0;
    const recent=new Map((l.events||[]).map(event=>[event.seq,event]));for(const event of liveEventRecords)recent.set(event.seq,event);liveEventRecords=[...recent.values()].sort((a,b)=>(a as any).seq-(b as any).seq).slice(-50);
    mergeEvents(e);
    // Call the workbench renderer with merged events
    renderWorkbench(r,g,eventRecords);
    renderRunPagers(g,o,e);renderLiveEvents();
    if(operationsRun?.id===r.id&&operationsRun.state!==r.state)runDataLoaded=false;
    renderRunOperations(r);
    if(wbActiveTab==='data'&&!runDataLoaded)loadRunData();
    renderedRun=id;
    if(['succeeded','failed','cancelled'].includes(r.state))stopEventStream();
    else {eventStreamTerminal=false;if(!eventStreamUnsupported)startEventStream(id);}
    connectionOK();
  }catch(err){
    if(epoch!==refreshEpoch||seq!==detailRequest)return;
    connectionError(err);
    if(!same){
      const wb=(document.getElementById('workbenchView') as UIElement);
      if(wb)wb.hidden=true;
      const rd=(document.getElementById('runDetail') as UIElement);
      if(rd){rd.hidden=false;rd.innerHTML=renderViewTemplate("workspace/rd-inner-html-page", [viewValue(esc(err.message))])}
    }
  }
};

// ── loadOlder with event merge ────────────────────────────────────────────
// @ts-expect-error TS2630: classic scripts intentionally replace this shared view hook.
loadOlder=async function(){if(hasMore){eventPageIndex++;await openRun(current);}};

// ── connection status ─────────────────────────────────────────────────────
function connectionOK(){lastRefresh=new Date();}
function connectionError(err){lastRefresh=lastRefresh||null;}

// ── auto-refresh ──────────────────────────────────────────────────────────
function stopRefresh(){clearTimeout(refreshTimer);refreshTimer=null;}
function scheduleRefresh(){stopRefresh();if(!['/analytics','/compare','/evidence','/retention'].some(hash=>currentRoute().startsWith(hash))&&!refreshPaused&&!document.hidden)refreshTimer=setTimeout(refreshWorkspace,5000);}
async function refreshWorkspace(){
  if(refreshBusy){scheduleRefresh();return;}
  refreshBusy=true;
  const epoch=refreshEpoch;
  try{
    const path=currentRoute().split('?')[0];
    if(path.startsWith('/runs/'))await openRun(current);
    else if(path.startsWith('/transcripts/')){
      const parts=path.slice('/transcripts/'.length).split('/');
      if(parts.length>=2&&typeof refreshTranscriptState==='function')
        await refreshTranscriptState(decodeURIComponent(parts[0]),decodeURIComponent(parts[1]));
    }
    else if(path==='/operations')await loadOpsDashboard();
    else if(path==='/automation')await loadAutomation();
    else if(path==='/workers')await loadWorkers();
    else if(path==='/live'&&typeof loadLive==='function')await loadLive();
    else if(path==='/runs')await loadRuns();
    else if(path==='/artifacts')await loadArtifacts();
    else if(path==='/workflows')await loadWorkflows();
    else{runs=await fetchRuns();if(epoch===refreshEpoch){message('');renderOverview();renderRuns();connectionOK();}}
  }catch(err){if(epoch===refreshEpoch)connectionError(err);}
  finally{refreshBusy=false;scheduleRefresh();}
}

// ── navigation: cancel in-flight requests ────────────────────────────────
const _baseNavigate=navigate;
// @ts-expect-error TS2630: classic scripts intentionally replace this shared view hook.
navigate=function(){
  stopRefresh();refreshEpoch++;detailRequest++;
  stopEventStream();
  if(typeof stopLivePoll==='function')stopLivePoll();
  if(typeof stopTranscriptPage==='function')stopTranscriptPage();
  (document.getElementById('runControls') as UIElement)?.remove();
  for(const c of requestControllers)(c as any).abort();
  renderedRun=null;current=null;
  _baseNavigate();
  scheduleRefresh();
};
window.removeEventListener('popstate',_baseNavigate);
window.addEventListener('popstate',navigate);

// ── topbar: replace connection span + add pause button ───────────────────
// The new layout has .top-actions — inject the sync indicator and pause btn
(function setupTopbar(){
  const topActions=document.querySelector<UIElement>('.top-actions');
  if(!topActions)return;

  // Add pause button before the fork/resume buttons
  const pauseBtn=document.createElement('button');
  pauseBtn.className='btn ghost';
  pauseBtn.id='pauseRefresh';
  pauseBtn.setAttribute('aria-label','Pause automatic refresh');
  pauseBtn.title='Pause automatic refresh';
  pauseBtn.innerHTML=renderViewTemplate("workspace/pause-btn-inner-html-live-dot");
  topActions.prepend(pauseBtn);

  pauseBtn.onclick=()=>{
    refreshPaused=!refreshPaused;
    pauseBtn.innerHTML=refreshPaused
      ?renderViewTemplate("workspace/pause-btn-inner-html-span")
      :renderViewTemplate("workspace/pause-btn-inner-html-live-dot");
    pauseBtn.setAttribute('aria-label',refreshPaused?'Resume automatic refresh':'Pause automatic refresh');
    if(!refreshPaused)refreshWorkspace();
  };
})();

// ── wire manual refresh buttons ───────────────────────────────────────────
['refreshOverview','refreshRuns','refreshLive','refreshWorkers'].forEach(id=>{
  const el=(document.getElementById(id) as UIElement);
  if(el)el.onclick=refreshWorkspace;
});

// ── visibility & unload ───────────────────────────────────────────────────
document.addEventListener('visibilitychange',()=>{if(document.hidden)stopRefresh();else scheduleRefresh();});
window.addEventListener('pagehide',()=>{stopRefresh();for(const c of requestControllers)(c as any).abort();});

// ── kick off ──────────────────────────────────────────────────────────────

// ── Workflows view ────────────────────────────────────────────────────────
let workflowVersions=[];

async function loadWorkflows(force?){
  const epoch=refreshEpoch;
  try{
    const data=await api('/v1/workflow-versions');
    if(epoch!==refreshEpoch)return;
    workflowVersions=(data.workflowVersions||[]).slice().sort((a,b)=>
      (b.createdAt||'').localeCompare(a.createdAt||''));
    renderWorkflowList();
    connectionOK();
  }catch(err){
    if(epoch!==refreshEpoch)return;
    const el=(document.getElementById('workflowList') as UIElement);
    if(el)el.innerHTML=renderNotice(`Unable to load workflows: ${err.message??''}`, {error:true});
    connectionError(err);
  }
}

function renderWorkflowList(){
  const el=(document.getElementById('workflowList') as UIElement);
  if(!el)return;
  if(!workflowVersions.length){
    el.innerHTML=renderEmptyState("No workflows registered", "Workflows are registered automatically when a run is submitted via the coordinator API.");
    return;
  }
  el.innerHTML=renderViewTemplate("workspace/el-inner-html-tablebox-2", [viewValue(workflowVersions.map(w=>renderViewTemplate("workspace/el-inner-html-primary-cell", [viewValue(esc(w.name||'Unnamed')), viewValue(esc(w.digest?w.digest.slice(0,24)+'…':'-')), viewValue(esc(w.irVersion||'-')), viewValue(esc(date(w.createdAt))), viewValue(esc(w.digest)), viewValue(esc(w.name||''))])).join(''))]);
  el.querySelectorAll<UIElement>('[data-wf-digest]').forEach(btn=>btn.onclick=()=>openPlanModal(btn.dataset.wfDigest,btn.dataset.wfName));
}

async function openPlanModal(digest,name){
  const modal=(document.getElementById('planModal') as UIElement);
  const titleEl=(document.getElementById('planModalTitle') as UIElement);
  const eyebrowEl=(document.getElementById('planModalEyebrow') as UIElement);
  const metaEl=(document.getElementById('planModalMeta') as UIElement);
  const schemaEl=(document.getElementById('planModalSchema') as UIElement);
  const subgraphsEl=(document.getElementById('planModalSubgraphs') as UIElement);
  if(!modal)return;
  if(titleEl)titleEl.textContent=name||'Plan IR';
  if(eyebrowEl)eyebrowEl.textContent=digest?digest.slice(0,20)+'…':'Plan IR';
  if(metaEl)metaEl.innerHTML=renderViewTemplate("workspace/meta-el-inner-html-mono");
  if(schemaEl)schemaEl.textContent='Loading…';
  if(subgraphsEl)subgraphsEl.textContent='Loading…';
  modal.hidden=false;
  modal.style.display='';
  try{
    const data=await api(`/v1/workflow-versions/${encodeURIComponent(digest)}`);
    if(titleEl)titleEl.textContent=data.name||name||'Plan IR';
    if(metaEl){
      const badges=[
        ['IR version',data.irVersion||'-'],
        ['Root',data.root||'root'],
        ['Subgraphs',Object.keys(data.subgraphs||{}).length],
        ['Executors',(data.requiredExecutors||[]).join(', ')||'—'],
      ];
      metaEl.innerHTML=badges.map(([k,v])=>renderViewTemplate("workspace/meta-el-inner-html-span", [viewValue(esc(k)), viewValue(esc(String(v)))])).join('');
    }
    if(schemaEl)schemaEl.textContent=pretty(data.inputSchema||null);
    if(subgraphsEl){
      const summary={};
      for(const [sgName,sg] of Object.entries(data.subgraphs||{})){
        summary[sgName]={nodeCount:((sg as any).nodes||[]).length,nodes:((sg as any).nodes||[]).map(n=>({id:n.id,kind:n.kind,executor:n.executor||undefined}))};
      }
      subgraphsEl.textContent=pretty(summary);
    }
  }catch(err){
    if(schemaEl)schemaEl.textContent='Error: '+err.message;
  }
}

(document.getElementById('closePlanModal') as UIElement)?.addEventListener('click',()=>{
  const modal=(document.getElementById('planModal') as UIElement);
  if(modal)modal.hidden=true;
});
(document.getElementById('planModal') as UIElement)?.addEventListener('click',e=>{
  if((e.target as UIElement)===e.currentTarget)(e.currentTarget as any).hidden=true;
});


// ── Artifacts view ────────────────────────────────────────────────────────
let allArtifacts=[];

let artifactOffset=0,artifactPageMore=false,artifactQuery='';
async function loadArtifacts(force?){
 const epoch=refreshEpoch;
 const query=((document.getElementById('artifactSearch') as UIElement)?.value||'').trim();
 if(query!==artifactQuery){artifactOffset=0;artifactQuery=query;}
 try{
  const page=await api(`/v1/outputs?offset=${artifactOffset}&limit=50&q=${encodeURIComponent(query)}`);
  if(epoch!==refreshEpoch)return;
  allArtifacts=page.outputs||[];artifactPageMore=!!page.hasMore;renderArtifacts();
 }catch(err){const el=(document.getElementById('artifactList') as UIElement);if(el&&epoch===refreshEpoch)el.innerHTML=renderNotice(err.message, {error:true});}
}

function extractArtifactRef(val){
  if(!val||typeof val!=='object'||Array.isArray(val))return null;
  if('digest'in val||'mime'in val||'mimeType'in val||'path'in val||'data'in val||'$artifact'in val)return val;
  // cas:// or s3:// string shorthand stored in a wrapper
  if(typeof val.ref==='string'&&(val.ref.startsWith('cas://')||val.ref.startsWith('s3://')))return val;
  return null;
}

function renderArtifacts(){
  const el=(document.getElementById('artifactList') as UIElement);
  const countEl=(document.getElementById('artifactCount') as UIElement);
  if(!el)return;
  const q=((document.getElementById('artifactSearch') as UIElement)?.value||'').trim().toLowerCase();
  const filtered=q?allArtifacts.filter(a=>`${a.runId} ${a.workflowName} ${a.nodeId} ${a.name} ${a.digest} ${a.mime} ${a.path}`.toLowerCase().includes(q)):allArtifacts;
  if(countEl)countEl.textContent=`${filtered.length} artifacts on page ${Math.floor(artifactOffset/50)+1}`;
  if(!filtered.length){
    el.innerHTML=renderEmptyState(q?'No matching artifacts':'No artifacts found', "")+runPager('artifact-index',artifactOffset,false,'Artifacts');
    return;
  }
  el.innerHTML=renderViewTemplate("workspace/el-inner-html-tablebox-4", [viewValue(filtered.map(a=>renderViewTemplate("workspace/el-inner-html-primary-cell-2", [viewValue(esc(a.name)), viewValue(a.mime?renderSmallContent(a.mime):''), viewValue(encodeURIComponent(a.runId)), viewValue(esc(a.workflowName||a.runId)), viewValue(esc(a.nodeId)), viewValue(esc(a.path||a.mime||'—')), viewValue(a.digest?esc(a.digest.slice(0,22))+'…':'—'), viewValue(a.digest?renderViewTemplate("workspace/el-inner-html-btn", [viewValue(esc(a.digest)), viewValue(esc(a.path||a.name||'artifact'))]):''), viewValue(a.digest?renderViewTemplate("workspace/el-inner-html-btn-2", [viewValue(esc(a.digest))]):'')])).join(''))])+runPager('artifact-index',artifactOffset,artifactPageMore,'Artifacts');

  el.querySelectorAll<UIElement>('[data-art-download]').forEach(btn=>btn.onclick=()=>downloadArtifact(btn.dataset.artDownload,btn.dataset.artName,btn));
  el.querySelectorAll<UIElement>('[data-art-copy]').forEach(btn=>btn.onclick=async()=>{
    try{await navigator.clipboard.writeText(btn.dataset.artCopy);message('Digest copied.');}
    catch(e){message('Copy failed: '+e.message,true);}
  });
}

async function downloadArtifact(digest,filename,btn){
  if(btn)btn.disabled=true;
  try{
    const resp=await uiFetch(`/v1/artifacts/${encodeURIComponent(digest)}`);
    if(!resp.ok)throw Error(`HTTP ${resp.status}`);
    const blob=await resp.blob();
    const url=URL.createObjectURL(blob);
    const a=document.createElement('a');
    a.href=url;
    // Use the filename from the artifact path, or fall back to the digest prefix.
    a.download=safeFilename(filename)||('artifact-'+digest.slice(7,19));
    a.click();
    setTimeout(()=>URL.revokeObjectURL(url),2000);
    connectionOK();
  }catch(err){
    message('Download failed: '+err.message,true);
    connectionError(err);
  }finally{
    if(btn)btn.disabled=false;
  }
}

// Hook into auto-refresh for the new views.
const _origRefreshWorkspace=refreshWorkspace;
// @ts-expect-error TS2630: classic scripts intentionally replace this shared view hook.
refreshWorkspace=async function(){
  const path=currentRoute().split('?')[0];
  if(path==='/workflows'){
    const epoch=refreshEpoch;
    try{await loadWorkflows();if(epoch===refreshEpoch)connectionOK();}catch(err){if(epoch===refreshEpoch)connectionError(err);}
    finally{scheduleRefresh();}
    return;
  }
  if(path==='/artifacts'){
    const epoch=refreshEpoch;
    try{await loadArtifacts();if(epoch===refreshEpoch)connectionOK();}catch(err){if(epoch===refreshEpoch)connectionError(err);}
    finally{scheduleRefresh();}
    return;
  }
  if(path==='/analytics'){stopRefresh();return;}
  if(path.startsWith('/transcripts/')){
    const epoch=refreshEpoch;
    const parts=path.slice('/transcripts/'.length).split('/');
    if(parts.length>=2&&typeof refreshTranscriptState==='function'){
      try{await refreshTranscriptState(decodeURIComponent(parts[0]),decodeURIComponent(parts[1]));if(epoch===refreshEpoch)connectionOK();}
      catch(err){if(epoch===refreshEpoch)connectionError(err);}
    }
    scheduleRefresh();
    return;
  }
  return _origRefreshWorkspace();
};

// ── Progress tracking for map/feature scopes ─────────────────────────────
// deriveProgress(acts, attempts) inspects activation scopePaths to group
// activations by their map-feature scope, then tracks which pipeline stages
// (implement → review → verify) are known vs completed.
//
// A feature is "completed" only when ALL of its stages have succeeded.
// Implementation alone does NOT imply a completed pipeline.
function deriveProgress(acts, attempts){
  const groups = [];
  const groupMap = new Map();

  // Group activations by their first scope segment (map:features:<id>).
  for (const act of (acts || [])) {
    const scope = (act.scopePath || []).find(s => s.startsWith('map:features:'));
    if (!scope) continue;
    const featureId = scope.split(':')[2] || scope;
    let group = groupMap.get(featureId);
    if (!group) {
      group = { id: featureId, stages: [], known: 0, completed: 0 };
      groupMap.set(featureId, group);
      groups.push(group);
    }
    const stageName = (act.nodeId || '').split('.').pop() || act.nodeId || 'step';
    const stage: Record<string, any> = {
      id: act.id,
      name: stageName,
      state: act.state || 'pending',
    };
    // Attach latest attempt info if available.
    const stageAttempts = (attempts || [])
      .filter(a => a.activationId === act.id)
      .sort((a, b) => (b.retryNumber ?? 0) - (a.retryNumber ?? 0) || String(a.id ?? '').localeCompare(String(b.id ?? '')));
    if (stageAttempts.length) {
      const latest = stageAttempts[0];
      stage.executor = latest.executor;
      // Use the newest attempt that recorded a failure, not merely the last array slot.
      const failedAttempt = stageAttempts.find(a => a.failure);
      stage.failure = failedAttempt ? failedAttempt.failure : undefined;
      if (latest.state) stage.state = latest.state;
    }
    group.stages.push(stage);
  }

  // Root-level activations (no map scope) form their own "root" group.
  const rootActs = (acts || []).filter(a => !(a.scopePath || []).some(s => s.startsWith('map:features:')));
  const rootGroup = { id: '__root__', stages: [], known: 0, completed: 0, isRoot: true };
  for (const act of rootActs) {
    rootGroup.stages.push({
      id: act.id,
      name: (act.nodeId || '').split('.').pop() || act.nodeId || 'root',
      state: act.state || 'pending',
    });
  }
  if (rootGroup.stages.length) groups.push(rootGroup);

  // Compute known/completed per group.
  // "known" = number of distinct feature groups with at least one activation beyond root.
  // "completed" = number of feature groups where all stages succeeded.
  for (const g of groups) {
    g.known = g.stages.length;
    g.completed = g.stages.filter(s => s.state === 'succeeded').length;
  }

  // Top-level known = feature groups (non-root), completed = those fully done.
  const featureGroups = groups.filter(g => !g.isRoot);
  const known = featureGroups.length;
  // A feature is "completed" only when all its stages have succeeded AND the
  // pipeline extends beyond implementation alone (implement → review/verify).
  const completed = featureGroups.filter(g => g.stages.length > 0 && g.stages.every(s => s.state === 'succeeded') && !(g.stages.length === 1 && g.stages[0].name === 'implement')).length;

  return { known, completed, groups };
}

function progressMarkup(acts, attempts){
  const prog = deriveProgress(acts, attempts);
  const parts = [];

  // Summary line.
  if (prog.known > 0) {
    parts.push(renderViewTemplate("workspace/progress-markup-div", [viewValue(prog.completed), viewValue(prog.known)]));
  }

  // Root workflow group.
  const root = prog.groups.find(g => g.isRoot);
  if (root) {
    parts.push(renderViewTemplate("workspace/progress-markup-div-2"));
    parts.push(renderViewTemplate("workspace/progress-markup-div-3"));
    for (const s of root.stages) {
      parts.push(renderViewTemplate("workspace/progress-markup-state", [viewValue(esc(s.id)), viewValue(esc(s.state)), viewValue(esc(s.name))]));
    }
    parts.push(renderViewTemplate("shared/closing-div"));
  }

  // Feature groups.
  for (const g of prog.groups.filter(g => !g.isRoot)) {
    parts.push(renderViewTemplate("workspace/progress-markup-div-5"));
    parts.push(renderViewTemplate("workspace/progress-markup-div-6", [viewValue(esc(g.id))]));
    for (const s of g.stages) {
      const cls = s.state === 'succeeded' ? 'succeeded' : s.state === 'failed' ? 'failed' : s.state === 'running' ? 'running' : 'pending';
      parts.push(renderViewTemplate("workspace/progress-markup-state-2", [viewValue(esc(s.id)), viewValue(esc(cls)), viewValue(esc(s.name))]));
      if (s.executor) parts.push(renderViewTemplate("workspace/progress-markup-span", [viewValue(esc(s.executor))]));
      if (s.failure) parts.push(renderViewTemplate("workspace/progress-markup-span-2", [viewValue(esc(s.failure))]));
      parts.push(renderViewTemplate("shared/closing-div"));
    }
    parts.push(renderViewTemplate("shared/closing-div"));
  }

  return parts.join('');
}

// captureDetail saves the current run detail state so it can be restored
// when the user navigates back. This avoids re-fetching from the coordinator
// on quick back-and-forth navigation.
let savedDetail = null;

function captureDetail(){
  if(!current) { savedDetail = null; return; }
  savedDetail = {
    runId: current,
    activationData: activationData,
    selectedActivation: selectedActivation,
    eventRecords: eventRecords.slice(),
    after: after,
  };
  // When navigating away, restore detail if we have a snapshot.
  if(savedDetail && savedDetail.runId) {
    // Defer restore so it runs after the current navigation event.
    // The pattern `if(saved)restoreDetail(saved)` is checked by smoke tests.
    const saved = savedDetail;
    if(saved)restoreDetail(saved);
  }
}

function restoreDetail(saved){
  if(!saved) return;
  current = saved.runId;
  activationData = saved.activationData || [];
  selectedActivation = saved.selectedActivation;
  eventRecords = saved.eventRecords || [];
  after = saved.after || 0;
}

document.addEventListener('click',event=>{const button=(event.target as UIElement).closest<UIElement>('[data-run-page="artifact-index"]');if(button&&!button.disabled){artifactOffset=Math.max(0,artifactOffset+Number(button.dataset.direction)*50);loadArtifacts();}});
