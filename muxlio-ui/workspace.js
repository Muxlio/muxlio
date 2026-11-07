// workspace.js — enhancements over the base index.html
// Adds: auto-refresh, connection status, pause/resume, improved event rendering, attempt cards.

let refreshTimer=null,refreshPaused=false,refreshBusy=false,refreshEpoch=0,lastRefresh=null;
let runAttempts=[],requestControllers=new Set(),renderedRun=null,detailRequest=0,eventRecords=[];
const RUN_DETAIL_PAGE_SIZE=50;
let outputOffset=0,eventCursors=[0],eventPageIndex=0,liveAfter=0,liveEventRecords=[],runDataLoaded=false;
function runPager(key,offset,more,label){return `<nav class="pager" aria-label="${esc(label)} pages"><span>${esc(label)} · page ${Math.floor(offset/RUN_DETAIL_PAGE_SIZE)+1}</span><div class="pager-controls"><button class="btn ghost" data-run-page="${key}" data-direction="-1"${offset===0?' disabled':''}>Previous</button><button class="btn ghost" data-run-page="${key}" data-direction="1"${!more?' disabled':''}>Next</button></div></nav>`}
function renderRunPagers(graph,outputs,events){
 const outputsPager=document.getElementById('outputsPager');if(outputsPager)outputsPager.innerHTML=runPager('outputs',outputOffset,outputs.hasMore,'Artifacts and outputs');
 const eventsPager=document.getElementById('eventsPager');if(eventsPager)eventsPager.innerHTML=runPager('events',eventPageIndex*RUN_DETAIL_PAGE_SIZE,events.hasMore,'Event history');
 const list=document.getElementById('wbArtifactList');if(list)list.innerHTML=(outputs.outputs||[]).map(item=>`<div class="artifact-row"><div class="artifact-icon">📄</div><div><div class="artifact-name">${esc(item.name)}</div><div class="artifact-meta">${esc(item.nodeId)}</div></div><button class="artifact-view" data-open-output="${esc(item.activationId)}" data-output-name="${esc(item.name)}">View</button></div>`).join('')||'<p class="subtle">No outputs on this page.</p>';
}
document.addEventListener('click',async event=>{
 if(event.target.closest('#focusCurrent')){const node=document.querySelector('.node-card.running,.node-card.waiting,.node-card.queued,.node-card.failed');if(node){for(let parent=node.parentElement;parent;parent=parent.parentElement)if(parent.tagName==='DETAILS')parent.open=true;node.scrollIntoView({block:'center',inline:'center',behavior:'smooth'});node.focus({preventScroll:true});}return;}
 const output=event.target.closest('[data-open-output]');
 if(output){const act=await loadActivation(output.dataset.openOutput);if(act&&current===act.runId){const value=act.outputs?.[output.dataset.outputName];showArtifactOutput(artifactData(value),output.dataset.outputName,value);}return;}
 const button=event.target.closest('[data-run-page]');if(!button||button.disabled)return;
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
 replaceHTMLPreservingDetails(document.getElementById('runInputInspector'),inputMarkup(data.input));
 replaceHTMLPreservingDetails(document.getElementById('runOutputInspector'),outputMarkup(data.output,'run-output'));
}
let eventStreamController=null,eventStreamRun=null,eventStreamUnsupported=false,eventStreamRetry=null,eventStreamTerminal=false;

// ── event renderer ────────────────────────────────────────────────────────
eventHtml=function(e){
  const label=eventLabel(e.type),
        tone=String(e.type||'').includes('fail')?'err':String(e.type||'').includes('succeed')?'ok':'',
        time=MuxlioFormat.timestamp(e.timestamp,true);
  return `<li class="ev${tone?' ev-'+tone:''}"><span class="ev-time">${esc(time)}</span><span class="ev-dot" aria-hidden="true"></span><span class="ev-body"><strong class="ev-label">${esc(label)}</strong>${e.subjectId?`<span class="ev-sub">${esc(e.subjectId)}</span>`:''}</span><span class="ev-seq">#${esc(e.seq)}</span>${e.payload?`<details class="ev-data" data-preserve-key="event:${esc(e.seq)}"><summary>data</summary><pre>${esc(pretty(e.payload))}</pre></details>`:''}</li>`;
};

// ── guarded api with abort + timeout ─────────────────────────────────────
api=async function(path,opts={}){
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
  const timeline=document.getElementById('wbTimeline');
  // Keep historical pages fixed while the first page follows new events.
  if(!timeline||eventPageIndex!==0)return;
  const merged=new Map(eventRecords.map(event=>[event.seq,event]));
  for(const event of liveEventRecords)merged.set(event.seq,event);
  eventRecords=[...merged.values()].sort((a,b)=>b.seq-a.seq).slice(0,RUN_DETAIL_PAGE_SIZE);
  if(eventRecords.length)eventCursors[1]=eventRecords[eventRecords.length-1].seq;
  replaceHTMLPreservingDetails(timeline,eventRecords.length?eventRecords.map(eventHtml).join(''):'<li class="subtle">No events recorded.</li>');
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
      if(!eventStreamUnsupported&&!eventStreamTerminal&&location.hash===`#run/${encodeURIComponent(id)}`)eventStreamRetry=setTimeout(()=>startEventStream(id),1000);
    }
  }
}

const _renderWorkbench=renderWorkbench;
renderWorkbench=function(r,g,events){
  _renderWorkbench(r,g,events);
  const actions=document.querySelector('.top-actions');
  if(!actions)return;
  let controls=document.getElementById('runControls');
  if(!controls){controls=document.createElement('span');controls.id='runControls';controls.style.cssText='display:flex;gap:7px';actions.prepend(controls);}
  const created=r.state==='created',active=['running','waiting'].includes(r.state),paused=r.state==='paused',retryable=['failed','cancelled'].includes(r.state);
  controls.innerHTML=`${created?'<button class="btn primary" data-run-action="start">Start run</button>':''}${active?'<button class="btn ghost" data-run-action="pause">Pause run</button>':''}${paused?'<button class="btn primary" data-run-action="resume">Resume run</button>':''}${retryable?'<button class="btn primary" data-run-action="retry">Retry run</button>':''}${active||paused?'<button class="btn ghost" data-run-action="cancel" style="color:var(--red)">Cancel run</button>':''}`;
  controls.querySelectorAll('[data-run-action]').forEach(button=>button.onclick=async()=>{
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
    const modal=document.getElementById('retryRunModal');
    if(!modal){resolve(false);return;}
    retryModalResolve=resolve;
    modal.hidden=false;
    modal.style.display='';
  });
}

function closeRetryRunModal(confirmed){
  const modal=document.getElementById('retryRunModal');
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

document.getElementById('retryRunModalCancel')?.addEventListener('click',()=>closeRetryRunModal(false));
document.getElementById('retryRunModalConfirm')?.addEventListener('click',()=>closeRetryRunModal(true));
document.getElementById('retryRunModal')?.addEventListener('click',e=>{
  if(e.target===e.currentTarget)closeRetryRunModal(false);
});
document.addEventListener('keydown',e=>{
  if(e.key!=='Escape')return;
  const modal=document.getElementById('retryRunModal');
  if(!modal||modal.hidden||!retryModalResolve)return;
  e.preventDefault();
  closeRetryRunModal(false);
});

let cancelModalResolve=null;

function openCancelRunModal(){
  if(cancelModalResolve)return Promise.resolve(false);
  return new Promise(resolve=>{
    const modal=document.getElementById('cancelRunModal');
    if(!modal){resolve(false);return;}
    cancelModalResolve=resolve;
    modal.hidden=false;
    modal.style.display='';
  });
}

function closeCancelRunModal(confirmed){
  const modal=document.getElementById('cancelRunModal');
  if(modal)modal.hidden=true;
  const resolve=cancelModalResolve;
  cancelModalResolve=null;
  if(resolve)resolve(confirmed);
}

document.getElementById('cancelRunModalCancel')?.addEventListener('click',()=>closeCancelRunModal(false));
document.getElementById('cancelRunModalConfirm')?.addEventListener('click',()=>closeCancelRunModal(true));
document.getElementById('cancelRunModal')?.addEventListener('click',e=>{
  if(e.target===e.currentTarget)closeCancelRunModal(false);
});
document.addEventListener('keydown',e=>{
  if(e.key!=='Escape')return;
  const modal=document.getElementById('cancelRunModal');
  if(!modal||modal.hidden||!cancelModalResolve)return;
  e.preventDefault();
  closeCancelRunModal(false);
});

// ── wire #inspRerun — retries the run from the last checkpoint ────────────
document.addEventListener('click', async function(e) {
  const btn = e.target?.closest?.('#inspRerun');
  if (!btn || !current || !['failed','cancelled'].includes(wbCurrentRunState)) return;
  await retryRun(current, btn);
});

// ── openRun: wraps workbench openRun to add attempts + event merge ────────
const _baseOpenRun=openRun;
openRun=async function(id){
  const epoch=refreshEpoch,seq=++detailRequest,same=renderedRun===id;
  if(!same){selectedActivation=null;selectedActivationDetail=null;after=0;eventRecords=[];outputOffset=0;eventCursors=[0];eventPageIndex=0;liveEventRecords=[];runDataLoaded=false;runAttempts=[];
 for(const key of ['runInputInspector','runOutputInspector']){const el=document.getElementById(key);if(el)el.textContent='Open the Data tab to load run inputs and outputs.';}}

  try{
    const r=await api(`/v1/runs/${encodeURIComponent(id)}?summary=true`);
    if(epoch!==refreshEpoch||seq!==detailRequest||current!==id)return;
    if(!r.overview)throw Error('Coordinator update required: this UI uses paged run views. Upgrade the coordinator before opening run histories.');
    const [g,e,o,l]=await Promise.all([
      api(`/v1/runs/${encodeURIComponent(id)}/graph`),
      api(`/v1/runs/${encodeURIComponent(id)}/events?order=desc&before=${eventCursors[eventPageIndex]||0}&limit=${RUN_DETAIL_PAGE_SIZE}`),
      api(`/v1/runs/${encodeURIComponent(id)}/outputs?offset=${outputOffset}&limit=${RUN_DETAIL_PAGE_SIZE}`),
      api(`/v1/runs/${encodeURIComponent(id)}/events?tail=true&limit=50`)
    ]);
    if(epoch!==refreshEpoch||seq!==detailRequest||current!==id)return;
    if(!same)liveAfter=r.overview?.latestEventSeq||0;
    const recent=new Map((l.events||[]).map(event=>[event.seq,event]));for(const event of liveEventRecords)recent.set(event.seq,event);liveEventRecords=[...recent.values()].sort((a,b)=>a.seq-b.seq).slice(-50);
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
      const wb=document.getElementById('workbenchView');
      if(wb)wb.hidden=true;
      const rd=document.getElementById('runDetail');
      if(rd){rd.hidden=false;rd.innerHTML=`<div class="page"><div class="notice error" role="alert">Unable to load run: ${esc(err.message)}. <a href="#runs" style="color:var(--teal)">← Back to runs</a></div></div>`}
    }
  }
};

// ── loadOlder with event merge ────────────────────────────────────────────
loadOlder=async function(){if(hasMore){eventPageIndex++;await openRun(current);}};

// ── connection status ─────────────────────────────────────────────────────
function connectionOK(){lastRefresh=new Date();}
function connectionError(err){lastRefresh=lastRefresh||null;}

// ── auto-refresh ──────────────────────────────────────────────────────────
function stopRefresh(){clearTimeout(refreshTimer);refreshTimer=null;}
function scheduleRefresh(){stopRefresh();if(!['#analytics','#compare','#evidence','#retention'].some(hash=>location.hash.startsWith(hash))&&!refreshPaused&&!document.hidden)refreshTimer=setTimeout(refreshWorkspace,5000);}
async function refreshWorkspace(){
  if(refreshBusy){scheduleRefresh();return;}
  refreshBusy=true;
  const epoch=refreshEpoch;
  try{
    if(location.hash.startsWith('#run/'))await openRun(current);
    else if(location.hash.startsWith('#transcript/')){
      const parts=location.hash.slice('#transcript/'.length).split('/');
      if(parts.length>=2&&typeof refreshTranscriptState==='function')
        await refreshTranscriptState(decodeURIComponent(parts[0]),decodeURIComponent(parts[1]));
    }
    else if(location.hash==='#operations')await loadOpsDashboard();
    else if(location.hash==='#automation')await loadAutomation();
    else if(location.hash==='#workers')await loadWorkers();
    else if(location.hash==='#live'&&typeof loadLive==='function')await loadLive();
    else if(location.hash==='#runs')await loadRuns();
    else if(location.hash==='#artifacts')await loadArtifacts();
    else if(location.hash==='#workflows')await loadWorkflows();
    else{runs=await fetchRuns();if(epoch===refreshEpoch){message('');renderOverview();renderRuns();connectionOK();}}
  }catch(err){if(epoch===refreshEpoch)connectionError(err);}
  finally{refreshBusy=false;scheduleRefresh();}
}

// ── navigation: cancel in-flight requests ────────────────────────────────
const _baseNavigate=navigate;
navigate=function(){
  stopRefresh();refreshEpoch++;detailRequest++;
  stopEventStream();
  if(typeof stopLivePoll==='function')stopLivePoll();
  if(typeof stopTranscriptPage==='function')stopTranscriptPage();
  document.getElementById('runControls')?.remove();
  for(const c of requestControllers)c.abort();
  renderedRun=null;current=null;
  _baseNavigate();
  scheduleRefresh();
};
window.removeEventListener('hashchange',_baseNavigate);
window.addEventListener('hashchange',navigate);

// ── topbar: replace connection span + add pause button ───────────────────
// The new layout has .top-actions — inject the sync indicator and pause btn
(function setupTopbar(){
  const topActions=document.querySelector('.top-actions');
  if(!topActions)return;

  // Add pause button before the fork/resume buttons
  const pauseBtn=document.createElement('button');
  pauseBtn.className='btn ghost';
  pauseBtn.id='pauseRefresh';
  pauseBtn.setAttribute('aria-label','Pause automatic refresh');
  pauseBtn.title='Pause automatic refresh';
  pauseBtn.innerHTML='<span class="live-dot" aria-hidden="true"></span><span>Live</span>';
  topActions.prepend(pauseBtn);

  pauseBtn.onclick=()=>{
    refreshPaused=!refreshPaused;
    pauseBtn.innerHTML=refreshPaused
      ?'<span>Hold</span>'
      :'<span class="live-dot" aria-hidden="true"></span><span>Live</span>';
    pauseBtn.setAttribute('aria-label',refreshPaused?'Resume automatic refresh':'Pause automatic refresh');
    if(!refreshPaused)refreshWorkspace();
  };
})();

// ── wire manual refresh buttons ───────────────────────────────────────────
['refreshOverview','refreshRuns','refreshLive','refreshWorkers'].forEach(id=>{
  const el=document.getElementById(id);
  if(el)el.onclick=refreshWorkspace;
});

// ── visibility & unload ───────────────────────────────────────────────────
document.addEventListener('visibilitychange',()=>{if(document.hidden)stopRefresh();else scheduleRefresh();});
window.addEventListener('pagehide',()=>{stopRefresh();for(const c of requestControllers)c.abort();});

// ── kick off ──────────────────────────────────────────────────────────────
navigate();

// ── Workflows view ────────────────────────────────────────────────────────
let workflowVersions=[];

async function loadWorkflows(force){
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
    const el=document.getElementById('workflowList');
    if(el)el.innerHTML=`<div class="notice error" role="alert">Unable to load workflows: ${esc(err.message)}</div>`;
    connectionError(err);
  }
}

function renderWorkflowList(){
  const el=document.getElementById('workflowList');
  if(!el)return;
  if(!workflowVersions.length){
    el.innerHTML=`<div class="tablebox empty"><strong>No workflows registered</strong>Workflows are registered automatically when a run is submitted via the coordinator API.</div>`;
    return;
  }
  el.innerHTML=`<div class="tablebox"><table>
    <thead><tr><th>Name</th><th>Digest</th><th>IR Version</th><th>Registered</th><th style="width:80px"></th></tr></thead>
    <tbody>${workflowVersions.map(w=>`
      <tr>
        <td class="primary-cell"><strong>${esc(w.name||'Unnamed')}</strong></td>
        <td class="mono">${esc(w.digest?w.digest.slice(0,24)+'…':'-')}</td>
        <td style="color:var(--muted)">${esc(w.irVersion||'-')}</td>
        <td style="color:var(--muted)">${esc(date(w.createdAt))}</td>
        <td><button class="btn ghost" data-wf-digest="${esc(w.digest)}" data-wf-name="${esc(w.name||'')}">View plan</button></td>
      </tr>`).join('')}
    </tbody></table></div>`;
  el.querySelectorAll('[data-wf-digest]').forEach(btn=>btn.onclick=()=>openPlanModal(btn.dataset.wfDigest,btn.dataset.wfName));
}

async function openPlanModal(digest,name){
  const modal=document.getElementById('planModal');
  const titleEl=document.getElementById('planModalTitle');
  const eyebrowEl=document.getElementById('planModalEyebrow');
  const metaEl=document.getElementById('planModalMeta');
  const schemaEl=document.getElementById('planModalSchema');
  const subgraphsEl=document.getElementById('planModalSubgraphs');
  if(!modal)return;
  if(titleEl)titleEl.textContent=name||'Plan IR';
  if(eyebrowEl)eyebrowEl.textContent=digest?digest.slice(0,20)+'…':'Plan IR';
  if(metaEl)metaEl.innerHTML='<span class="mono">Loading…</span>';
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
      metaEl.innerHTML=badges.map(([k,v])=>`<span><em>${esc(k)}</em> <strong>${esc(String(v))}</strong></span>`).join('');
    }
    if(schemaEl)schemaEl.textContent=pretty(data.inputSchema||null);
    if(subgraphsEl){
      const summary={};
      for(const [sgName,sg] of Object.entries(data.subgraphs||{})){
        summary[sgName]={nodeCount:(sg.nodes||[]).length,nodes:(sg.nodes||[]).map(n=>({id:n.id,kind:n.kind,executor:n.executor||undefined}))};
      }
      subgraphsEl.textContent=pretty(summary);
    }
  }catch(err){
    if(schemaEl)schemaEl.textContent='Error: '+err.message;
  }
}

document.getElementById('closePlanModal')?.addEventListener('click',()=>{
  const modal=document.getElementById('planModal');
  if(modal)modal.hidden=true;
});
document.getElementById('planModal')?.addEventListener('click',e=>{
  if(e.target===e.currentTarget)e.currentTarget.hidden=true;
});


// ── Artifacts view ────────────────────────────────────────────────────────
let allArtifacts=[];

let artifactOffset=0,artifactPageMore=false,artifactQuery='';
async function loadArtifacts(force){
 const epoch=refreshEpoch;
 const query=(document.getElementById('artifactSearch')?.value||'').trim();
 if(query!==artifactQuery){artifactOffset=0;artifactQuery=query;}
 try{
  const page=await api(`/v1/outputs?offset=${artifactOffset}&limit=50&q=${encodeURIComponent(query)}`);
  if(epoch!==refreshEpoch)return;
  allArtifacts=page.outputs||[];artifactPageMore=!!page.hasMore;renderArtifacts();
 }catch(err){const el=document.getElementById('artifactList');if(el&&epoch===refreshEpoch)el.innerHTML=`<div class="notice error">${esc(err.message)}</div>`;}
}

function extractArtifactRef(val){
  if(!val||typeof val!=='object'||Array.isArray(val))return null;
  if('digest'in val||'mime'in val||'mimeType'in val||'path'in val||'data'in val||'$artifact'in val)return val;
  // cas:// or s3:// string shorthand stored in a wrapper
  if(typeof val.ref==='string'&&(val.ref.startsWith('cas://')||val.ref.startsWith('s3://')))return val;
  return null;
}

function renderArtifacts(){
  const el=document.getElementById('artifactList');
  const countEl=document.getElementById('artifactCount');
  if(!el)return;
  const q=(document.getElementById('artifactSearch')?.value||'').trim().toLowerCase();
  const filtered=q?allArtifacts.filter(a=>`${a.runId} ${a.workflowName} ${a.nodeId} ${a.name} ${a.digest} ${a.mime} ${a.path}`.toLowerCase().includes(q)):allArtifacts;
  if(countEl)countEl.textContent=`${filtered.length} artifacts on page ${Math.floor(artifactOffset/50)+1}`;
  if(!filtered.length){
    el.innerHTML=`<div class="tablebox empty"><strong>${q?'No matching artifacts':'No artifacts found'}</strong></div>`+runPager('artifact-index',artifactOffset,false,'Artifacts');
    return;
  }
  el.innerHTML=`<div class="tablebox"><table>
    <thead><tr><th>Name</th><th>Run / node</th><th>MIME / path</th><th>Digest</th><th style="width:90px"></th></tr></thead>
    <tbody>${filtered.map(a=>`
      <tr>
        <td class="primary-cell"><strong>${esc(a.name)}</strong>${a.mime?`<small>${esc(a.mime)}</small>`:''}</td>
        <td><a href="#run/${encodeURIComponent(a.runId)}" style="color:var(--teal)">${esc(a.workflowName||a.runId)}</a><small style="display:block;color:var(--muted)">${esc(a.nodeId)}</small></td>
        <td class="mono">${esc(a.path||a.mime||'—')}</td>
        <td class="mono">${a.digest?esc(a.digest.slice(0,22))+'…':'—'}</td>
        <td class="row-actions">
          ${a.digest?`<button class="btn ghost" data-art-download="${esc(a.digest)}" data-art-name="${esc(a.path||a.name||'artifact')}">Download</button>`:''}
          ${a.digest?`<button class="btn ghost" data-art-copy="${esc(a.digest)}">Copy digest</button>`:''}
        </td>
      </tr>`).join('')}
    </tbody></table></div>`+runPager('artifact-index',artifactOffset,artifactPageMore,'Artifacts');

  el.querySelectorAll('[data-art-download]').forEach(btn=>btn.onclick=()=>downloadArtifact(btn.dataset.artDownload,btn.dataset.artName,btn));
  el.querySelectorAll('[data-art-copy]').forEach(btn=>btn.onclick=async()=>{
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
refreshWorkspace=async function(){
  if(location.hash==='#workflows'){
    const epoch=refreshEpoch;
    try{await loadWorkflows();if(epoch===refreshEpoch)connectionOK();}catch(err){if(epoch===refreshEpoch)connectionError(err);}
    finally{scheduleRefresh();}
    return;
  }
  if(location.hash==='#artifacts'){
    const epoch=refreshEpoch;
    try{await loadArtifacts();if(epoch===refreshEpoch)connectionOK();}catch(err){if(epoch===refreshEpoch)connectionError(err);}
    finally{scheduleRefresh();}
    return;
  }
  if(location.hash==='#analytics'){stopRefresh();return;}
  if(location.hash.startsWith('#transcript/')){
    const epoch=refreshEpoch;
    const parts=location.hash.slice('#transcript/'.length).split('/');
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
    const stage = {
      id: act.id,
      name: stageName,
      state: act.state || 'pending',
    };
    // Attach latest attempt info if available.
    const stageAttempts = (attempts || []).filter(a => a.activationId === act.id);
    if (stageAttempts.length) {
      const latest = stageAttempts[stageAttempts.length - 1];
      stage.executor = latest.executor;
      // Use the most recent failure across all attempts, not just the latest.
      const failedAttempt = [...stageAttempts].reverse().find(a => a.failure);
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
    parts.push(`<div style="font-size:11px;font-weight:640;color:var(--ink);margin-bottom:10px">${prog.completed} completed / ${prog.known} known</div>`);
  }

  // Root workflow group.
  const root = prog.groups.find(g => g.isRoot);
  if (root) {
    parts.push(`<div style="margin-bottom:12px">`);
    parts.push(`<div style="font-size:10px;font-weight:700;color:var(--teal);margin-bottom:6px">Root workflow</div>`);
    for (const s of root.stages) {
      parts.push(`<div data-stage="${esc(s.id)}" style="display:flex;align-items:center;gap:6px;padding:3px 0;font-size:11px"><span class="state ${esc(s.state)}" style="font-size:10px"></span><span>${esc(s.name)}</span></div>`);
    }
    parts.push(`</div>`);
  }

  // Feature groups.
  for (const g of prog.groups.filter(g => !g.isRoot)) {
    parts.push(`<div style="margin-bottom:12px;padding:10px;border:1px solid var(--border2);border-radius:6px;background:var(--surface2)">`);
    parts.push(`<div style="font-size:10px;font-weight:700;color:var(--teal);margin-bottom:8px">Feature · ${esc(g.id)}</div>`);
    for (const s of g.stages) {
      const cls = s.state === 'succeeded' ? 'succeeded' : s.state === 'failed' ? 'failed' : s.state === 'running' ? 'running' : 'pending';
      parts.push(`<div data-stage="${esc(s.id)}" style="display:flex;align-items:center;gap:6px;padding:3px 0;font-size:11px"><span class="state ${esc(cls)}" style="font-size:10px"></span><span>${esc(s.name)}</span>`);
      if (s.executor) parts.push(`<span style="color:var(--muted);font-size:10px">· ${esc(s.executor)}</span>`);
      if (s.failure) parts.push(`<span style="color:var(--red);font-size:10px;display:block;padding:2px 0 0 10px">${esc(s.failure)}</span>`);
      parts.push(`</div>`);
    }
    parts.push(`</div>`);
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

document.addEventListener('click',event=>{const button=event.target.closest('[data-run-page="artifact-index"]');if(button&&!button.disabled){artifactOffset=Math.max(0,artifactOffset+Number(button.dataset.direction)*50);loadArtifacts();}});
