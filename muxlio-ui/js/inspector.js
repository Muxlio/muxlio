/* ── inspector render ─────────────────────────── */
const TRANSCRIPT_POLL_MS=1200;
const activationTranscripts=new Map();
let transcriptPollTimer=null,transcriptPollToken=0;
let selectedActivationDetail=null,attemptOffset=0,attemptPageMore=false;

function decodeLogData(data){
  if(typeof data!=='string')return '';
  try{return new TextDecoder().decode(Uint8Array.from(atob(data),c=>c.charCodeAt(0)))}catch{return String(data)}
}
function mergeActivationLogs(actId,logs){
  let state=activationTranscripts.get(actId);
  if(!state){state={agent:'',seen:new Set()};activationTranscripts.set(actId,state);}
  const list=(logs||[]).slice().sort((a,b)=>(a.seq||0)-(b.seq||0));
  const hasAgent=list.some(x=>x.stream==='agent');
  const relevant=hasAgent?list.filter(x=>x.stream==='agent'):list;
  for(const chunk of relevant){
    const key=`${chunk.stream||'log'}:${chunk.seq}`;
    if(state.seen.has(key))continue;
    state.seen.add(key);
    state.agent=(state.agent+decodeLogData(chunk.data)).slice(-65536);
  }
  return state.agent;
}
function stopTranscriptPoll(){if(transcriptPollTimer){clearTimeout(transcriptPollTimer);transcriptPollTimer=null}transcriptPollToken++}
function updateTranscriptPanel(text,live){
  const el=$('#inspTranscriptBody');if(!el)return;
  const atBottom=el.scrollHeight-el.scrollTop-el.clientHeight<40;
  if(!text){
    if(live){
      el.textContent='Waiting for agent output…';
      el.classList.add('waiting');
    }else{
      el.textContent='No transcript recorded.';
      el.classList.remove('waiting');
    }
  }else{
    el.innerHTML=renderMarkdown(text);
    el.classList.remove('waiting');
  }
  if(live&&atBottom)el.scrollTop=el.scrollHeight;
}
function scheduleTranscriptPoll(act,runId){
  stopTranscriptPoll();
  const token=transcriptPollToken,actId=act.id;
  async function tick(){
    if(token!==transcriptPollToken||selectedActivation!==actId||current!==runId)return;
    try{
      const d=await api(`/v1/runs/${encodeURIComponent(runId)}/logs?activationId=${encodeURIComponent(actId)}&offset=0&limit=20`);
      if(token!==transcriptPollToken||selectedActivation!==actId||current!==runId)return;
      updateTranscriptPanel(mergeActivationLogs(actId,d.logs||[]),act.state==='running');
    }catch{}
    if(token!==transcriptPollToken||selectedActivation!==actId||current!==runId)return;
    const live=activationData.find(a=>a.id===actId);
    if(live&&live.state==='running')transcriptPollTimer=setTimeout(tick,TRANSCRIPT_POLL_MS);
  }
  tick();
}
function activationHasTranscript(act){
  if(!act)return false;
  const attempts=typeof runAttempts!=='undefined'?runAttempts:[];
  const hasAttempt=attempts.some(a=>a.activationId===act.id);
  const hasCached=Boolean(activationTranscripts.get(act.id)?.agent);
  const hasExecutor=Boolean(act.executorHint||act.executor||hasAttempt);
  if(!hasExecutor&&!hasCached)return false;
  return act.state==='running'||hasCached||['succeeded','failed','cancelled'].includes(act.state);
}
function transcriptSectionMarkup(act,runId){
  if(act.state!=='running')return '';
  if(!activationHasTranscript(act))return '';
  const cached=activationTranscripts.get(act.id);
  const transcriptUrl=`#transcript/${encodeURIComponent(runId||current||'')}/${encodeURIComponent(act.id)}`;
  const rawText=cached?.agent||'';
  const isWaiting=!cached?.agent&&act.state==='running';
  const bodyContent=isWaiting
    ? 'Waiting for agent output…'
    : rawText
      ? renderMarkdown(rawText)
      : 'No transcript recorded.';
  const bodyInner=isWaiting?`<span>${esc(bodyContent)}</span>`:bodyContent;
  return `<div class="insp-section" id="inspTranscriptSection"><div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:6px"><span style="font-size:10px;font-weight:650;color:var(--muted)">Live transcript${act.state==='running'?' · streaming':''}</span><a href="${transcriptUrl}" class="insp-transcript-link" aria-label="Open transcript page">Open ↗</a></div><div class="insp-transcript markdown-body${isWaiting?' waiting':''}" id="inspTranscriptBody">${bodyInner}</div></div>`;
}

function resolveInspectorAgent(act){
  // Prefer the latest attempt that has actually started. Queued retries are not
  // the executor currently working on the activation yet.
  const attempts=typeof runAttempts!=='undefined'?runAttempts:[];
  const actAttempts=attempts.filter(a=>a.activationId===act.id&&a.state!=='queued').sort((a,b)=>(b.retryNumber||0)-(a.retryNumber||0));
  return actAttempts[0]?.executor||act.executorHint||act.executor||'—';
}

// buildAttemptUsageMarkup renders a compact token-usage section for each
// attempt of an activation. Attempts without provider-reported usage are
// labelled "Unavailable"; the section is suppressed entirely when the
// activation has no attempts at all (older runs with no records).
function buildAttemptUsageMarkup(act){
  const attempts=typeof runAttempts!=='undefined'?runAttempts:[];
  const actAttempts=attempts.filter(a=>a.activationId===act.id).sort((a,b)=>(a.retryNumber||0)-(b.retryNumber||0));
  if(!actAttempts.length)return '';
  const formatTokens=n=>fmtTokens(n||0);
  const rows=actAttempts.map((a,i)=>{
    const label=String(attemptOffset+i+1);
    if(a.usage==null){
      return `<tr><td>${esc(label)}</td><td colspan="3" class="num" style="color:var(--muted)">—</td></tr>`;
    }
    return `<tr><td>${esc(label)}</td><td class="num">${esc(formatTokens(a.usage.input))}</td><td class="num">${esc(formatTokens(a.usage.output))}</td><td class="num">${esc(formatTokens(a.usage.cached))}</td></tr>`;
  });
  return `<div class="insp-section">
    <div style="font-size:10px;font-weight:650;color:var(--muted);margin-bottom:6px">Token usage</div>
    <div class="tablebox" style="font-size:11px"><table>
      <thead><tr><th>#</th><th>In</th><th>Out</th><th>Cached</th></tr></thead>
      <tbody>${rows.join('')}</tbody>
    </table></div>
  </div>`;
}

function renderInspectorWb(act){
  const labelEl=$('#inspNodeLabel'),nameEl=$('#inspNodeName'),descEl=$('#inspNodeDesc');
  const parts=(act.nodeId||'').split('.');
  const nodeType=parts.length>1?parts[0]:'node';
  const nodeName=nodeDisplayName(parts.slice(-1)[0]||act.nodeId);
  // hide placeholder
  const identity=$('#inspIdentity');
  if(identity)identity.hidden=false;
  const displayNum=(typeof activationDisplayNumbers!=='undefined'&&activationDisplayNumbers.get(act.id))||(Math.max(0,activationData.findIndex(a=>a.id===act.id))+1);
  if(labelEl)labelEl.textContent=`${String(displayNum).padStart(2,'0')} · ${nodeType.toUpperCase()}`;
  if(nameEl)nameEl.textContent=nodeName;
  if(descEl){descEl.textContent=act.description||'';descEl.hidden=!act.description}
  const body=$('#inspBody');
  if(!body)return;
  const oldSession=$('#sessionControls');
  const inspectorAttempts=typeof runAttempts!=='undefined'?runAttempts:[];
  const session=inspectorAttempts.filter(a=>a.activationId===act.id).map(a=>a.result?.session).filter(Boolean).at(-1);
  const agent=resolveInspectorAgent(act);
  const skill=act.skill||'—';
  const inRef=act.inputRef||act.inputArtifact||'—';
  const outRef=act.outputRef||act.outputArtifact||(act.outputs&&Object.keys(act.outputs)[0]?Object.keys(act.outputs)[0]+'.json':'—');
  const evidences=act.evidence||act.criteria||[];
  const route=act.route||'';
  const transcriptUrl=`#transcript/${encodeURIComponent(current||'')}/${encodeURIComponent(act.id)}`;
  const hasTranscript=activationHasTranscript(act);
  replaceHTMLPreservingDetails(body,`
    <div class="insp-section">
      <dl class="insp-kv">
        <dt>Status</dt><dd class="${stateClass(act.state)||''}">${esc(act.state)} ${act.occurrence?`· iteration ${act.occurrence}`:''}`.trimEnd()+'</dd>'+
        `${act.queueReason?`<dt>Queue reason</dt><dd>${esc(act.queueReason)}</dd>`:""}<dt>Executor</dt><dd>${esc(agent)}</dd>
        ${act.skill?`<dt>Skill</dt><dd>${esc(skill)}</dd>`:""}
        <dt>Inputs</dt><dd>${esc(inRef)}</dd>
        <dt>Output</dt><dd>${esc(outRef)}</dd>
      </dl>
      ${hasTranscript?`<div class="insp-detail-actions"><a href="${transcriptUrl}" class="btn ghost"><svg viewBox="0 0 20 20" aria-hidden="true"><path d="M5 3h7l3 3v11H5zM12 3v4h3M8 10h4M8 13h4"/></svg>View transcript<svg viewBox="0 0 20 20" aria-hidden="true"><path d="m8 5 5 5-5 5"/></svg></a></div>`:''}
    </div>
    ${evidences.length?`
    <div class="insp-section">
      <div style="font-size:10px;font-weight:650;color:var(--muted);margin-bottom:6px">Evidence and decision</div>
      <div class="evidence-list">${evidences.map(ev=>`<div class="evidence-item">${esc(ev)}</div>`).join('')}</div>
      ${route?`<div class="evidence-route">route: <code>${esc(route)}</code></div>`:''}
    </div>`:''}
    ${act.outputs?`
    <div class="insp-section">
      <h3 class="insp-output-title">Outputs</h3>
      ${outputMarkup(act.outputs)}
    </div>`:''}
    ${act.error?`<div class="insp-section"><div class="insp-error">${esc(act.error)}</div></div>`:''}
    ${buildAttemptUsageMarkup(act)}
    ${typeof attemptMetadataMarkup==='function'?attemptMetadataMarkup(inspectorAttempts.filter(a=>a.activationId===act.id)):''}
    ${session?'<section class="insp-section" id="sessionControls"></section>':''}
    <nav class="pager" aria-label="Attempt pages"><span>Attempts · page ${Math.floor(attemptOffset/50)+1}</span><button class="btn ghost" data-attempt-page="-1"${attemptOffset===0?' disabled':''}>Previous</button><button class="btn ghost" data-attempt-page="1"${!attemptPageMore?' disabled':''}>Next</button></nav>
    <div class="insp-section" id="inspSnapshots"><h3 class="insp-output-title">Intermediate snapshots</h3><div class="subtle">Loading snapshots…</div></div>
    ${transcriptSectionMarkup(act,current)}
  `);
  if(activationHasTranscript(act)&&(act.state==='running'||activationTranscripts.get(act.id)?.agent))scheduleTranscriptPoll(act,current);
  loadInspectorSnapshots(act,current);
  if(session&&typeof loadSessionControls==='function'){
   const panel=$('#sessionControls');
   if(oldSession?.dataset.sessionId===session.id&&panel)panel.replaceWith(oldSession);else loadSessionControls(current,session);
  }
  const actionsEl=$('#inspActions');
  if(actionsEl)actionsEl.hidden=!['failed','cancelled'].includes(wbCurrentRunState);
}

let snapshotPollTimer=null,snapshotPollToken=0;
let snapshotActivation=null,snapshotCursors=[0],snapshotPageIndex=0;
async function loadInspectorSnapshots(act,runId){
 if(snapshotPollTimer)clearTimeout(snapshotPollTimer);
 if(snapshotActivation!==act.id){snapshotActivation=act.id;snapshotCursors=[0];snapshotPageIndex=0;}
 const token=++snapshotPollToken;
 try{
  const data=await api(`/v1/runs/${encodeURIComponent(runId)}/snapshots?activationId=${encodeURIComponent(act.id)}&after=${snapshotCursors[snapshotPageIndex]||0}&limit=20`);
  if(token!==snapshotPollToken||selectedActivation!==act.id||current!==runId)return;
  const snapshots=data.snapshots||[];
  snapshotCursors[snapshotPageIndex+1]=data.nextAfter||0;
  const el=$('#inspSnapshots');if(!el)return;
  el.innerHTML='<h3 class="insp-output-title">Intermediate snapshots</h3><p class="subtle">Observations from attempts; not committed final outputs.</p>'+snapshots.map(event=>{
   const ref=event.payload.snapshot;
   return `<section class="artifact"><strong>${esc(ref.name)}</strong> <span class="subtle">seq ${esc(event.seq)}</span><div class="subtle">${esc(event.subjectId)} · ${esc(date(event.timestamp))}</div><code>${esc(ref.digest)}</code><button class="btn ghost" data-snapshot-seq="${esc(event.seq)}">View snapshot</button></section>`;
  }).join('')+(!snapshots.length?'<div class="subtle">No snapshots recorded.</div>':'')+
  `<nav class="pager" aria-label="Snapshot pages"><span>Page ${snapshotPageIndex+1}</span><button class="btn ghost" data-snapshot-page="-1"${snapshotPageIndex===0?' disabled':''}>Previous</button><button class="btn ghost" data-snapshot-page="1"${!data.hasMore?' disabled':''}>Next</button></nav>`;
  for(const button of el.querySelectorAll('[data-snapshot-seq]'))button.onclick=()=>{
   const event=snapshots.find(e=>String(e.seq)===button.dataset.snapshotSeq);
   if(event)showArtifactOutput(event.payload.snapshot,event.payload.snapshot.name);
  };
  for(const button of el.querySelectorAll('[data-snapshot-page]'))button.onclick=()=>{snapshotPageIndex+=Number(button.dataset.snapshotPage);loadInspectorSnapshots(act,runId);};
 }catch{const el=$('#inspSnapshots');if(el&&token===snapshotPollToken)el.textContent='Unable to load intermediate snapshots.';}
 if(token===snapshotPollToken&&selectedActivation===act.id&&current===runId&&act.state==='running')snapshotPollTimer=setTimeout(()=>loadInspectorSnapshots(act,runId),TRANSCRIPT_POLL_MS);
}

/* ── shared helpers (kept from original) ─────── */
const PREVIEW_LIMIT=4096;
function valueType(v){if(v===null)return 'null';if(Array.isArray(v))return 'array';if(typeof v==='object')return 'object';return typeof v}
function treeMarkup(value,key='Value',depth=0,statePath='tree'){const type=valueType(value),label=esc(key),count=type==='array'?value.length:type==='object'?Object.keys(value).length:0;if(type==='object'||type==='array'){if(!count)return `<div class="data-row"><span class="data-key">${label}</span><span class="data-type">${type} · empty</span></div>`;const children=type==='array'?value.map((v,i)=>[String(i),v]):Object.entries(value);return `<div class="data-row"><details data-preserve-key="${esc(`${statePath}/${key}`)}"><summary><span class="data-key">${label}</span> <span class="data-type">${type} · ${count}</span></summary><div class="data-children data-tree">${children.map(([k,v])=>treeMarkup(v,k,depth+1,`${statePath}/${key}`)).join('')}</div></details></div>`}if(type==='null')return `<div class="data-row"><span class="data-key">${label}</span><span class="data-type">null</span></div>`;return `<div class="data-row"><span class="data-key">${label}</span><span class="data-value ${esc(type)}">${esc(type==='string'?JSON.stringify(value):String(value))}</span><span class="data-type">${esc(type)}</span></div>`}
function inputMarkup(value){if(value===undefined||value===null)return `<div class="data-empty">${value===null?'Input is null.':'No input recorded.'}</div>`;return `<div class="data-tools"><button class="btn ghost" data-copy="run-input">Copy input</button></div><div class="data-tree">${treeMarkup(value,'Input',0,'run:input')}</div>`}
function artifactData(v){if(v&&typeof v==='object'&&!Array.isArray(v)&&('mime'in v||'mimeType'in v||'path'in v||'data'in v||'digest'in v||'$artifact'in v))return v;return null}
function outputItems(value){if(value===undefined||value===null)return [];if(Array.isArray(value))return value.map((v,i)=>[String(i),v]);if(typeof value==='object'){const entries=Object.entries(value);return entries.length?entries:[['Output',value]]}return [['Output',value]]}
function safePreview(value,mime){let raw;if(typeof value==='string')raw=value;else if(value===undefined||value===null)return null;else raw=JSON.stringify(value,null,2);if(!raw)return null;if(mime&&!/(text\/|json|xml|javascript|yaml|csv|markdown)/i.test(mime))return null;return {text:raw.slice(0,PREVIEW_LIMIT),truncated:raw.length>PREVIEW_LIMIT}}
function outputMarkup(value){if(value===undefined||value===null)return `<div class="data-empty">${value===null?'Output is null.':'No output recorded.'}</div>`;const items=outputItems(value);return `${items.map(([name,item])=>{const ref=artifactData(item);if(!ref)return `<section class="artifact"><div class="artifact-head"><strong>${esc(name)}</strong><span class="data-type">${esc(valueType(item))}</span></div>${typeof item==='string'?`<div class="markdown-body">${renderMarkdown(item)}</div>`:`<div class="data-tree">${treeMarkup(item,'Value')}</div>`}</section>`;const mime=typeof (ref.mime||ref.mimeType)==='string'?(ref.mime||ref.mimeType):'',path=typeof ref.path==='string'?ref.path:'',data=ref.data;let size=typeof data==='string'?new TextEncoder().encode(data).length:null;const meta=[['MIME',mime],['Path',path],['Digest',ref.digest||''],['Size',ref.size??(size===null?'':`${size} B`)]].filter(x=>x[1]!=='');return `<section class="artifact"><div class="artifact-head"><div class="artifact-title"><span class="artifact-file-icon" aria-hidden="true">▤</span><div><strong>${esc(path||name)}</strong><div class="artifact-subtitle">${esc(name)}</div></div></div></div>${meta.length?`<dl class="artifact-meta">${meta.map(([k,v])=>`<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`).join('')}</dl>`:''}<div class="artifact-actions"><button class="btn ghost" data-view-artifact="${esc(name)}">View</button><button class="btn ghost" data-download="artifact" data-name="${esc(name)}" data-copy="run-output">Download</button></div></section>`}).join('')}`}
function safeFilename(name){return String(name||'artifact').replace(/[^a-z0-9._-]+/gi,'_').replace(/^\.+/,'').slice(0,100)||'artifact'}
async function loadArtifactBlob(ref){
  const mime=ref.mime||ref.mimeType||'application/octet-stream';
  if(ref.data!=null)return new Blob([typeof ref.data==='string'?ref.data:JSON.stringify(ref.data,null,2)],{type:mime});
  if(!ref.digest)throw Error('No artifact content or digest recorded.');
  const response=await uiFetch(`/v1/artifacts/${encodeURIComponent(ref.digest)}`);
  if(response.status===410)throw Error('This artifact has expired under the retention policy.');
  if(!response.ok)throw Error(`Unable to load artifact (HTTP ${response.status}).`);
  return new Blob([await response.arrayBuffer()],{type:mime});
}
let outputModalOpener,outputModalRequest=0;
function closeArtifactOutput(){outputModalRequest++;$('#outputModal').hidden=true;outputModalOpener?.focus();}
async function showArtifactOutput(ref,name,value){
  const modal=$('#outputModal'),body=$('#outputModalBody'),request=++outputModalRequest;
  outputModalOpener=document.activeElement;
  $('#outputModalTitle').textContent=ref?.path||name||'Output';
  body.textContent='Loading output…';modal.hidden=false;$('#closeOutputModal').focus();
  try{
    const blob=ref?await loadArtifactBlob(ref):new Blob([typeof value==='string'?value:JSON.stringify(value,null,2)],{type:'text/plain'});
    const text=await blob.text();
    if(request!==outputModalRequest)return;
    const markdown=/^text\/markdown(?:;|$)/i.test(ref?.mime||ref?.mimeType||'')||/\.md$/i.test(ref?.path||'');
    body.innerHTML=markdown?`<article class="markdown-body">${renderMarkdown(text)}</article>`:`<pre class="output-raw">${esc(text)}</pre>`;
  }catch(err){if(request===outputModalRequest)body.innerHTML=`<div class="notice error" role="alert">${esc(err.message)}</div>`;}
}
document.getElementById('closeOutputModal')?.addEventListener('click',closeArtifactOutput);
document.getElementById('outputModal')?.addEventListener('click',e=>{if(e.target.id==='outputModal')closeArtifactOutput();});
document.addEventListener('keydown',e=>{
  const modal=$('#outputModal');if(!modal||modal.hidden)return;
  if(e.key==='Escape'){e.preventDefault();closeArtifactOutput();}
  if(e.key==='Tab'){
    const items=[...modal.querySelectorAll('button,a[href],[tabindex="0"]')];
    const first=items[0],last=items[items.length-1];
    if(e.shiftKey&&document.activeElement===first){e.preventDefault();last?.focus();}
    else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first?.focus();}
  }
});
async function inspectorAction(e){
  const copyLogs=e.target.closest('[data-copylogs]');
  if(copyLogs){const out=$('#inspectorContent'),text=out&&out._copyLogsText;if(!text)return;try{await navigator.clipboard.writeText(text);message('Logs copied.')}catch(err){message('Copy failed: '+err.message,true)}return}
  const view=e.target.closest('[data-view-artifact]');
  const copy=e.target.closest('[data-copy]'),download=e.target.closest('[data-download]');
  if(!copy&&!download&&!view)return;
  const kind=view?'run-output':(copy||download).dataset.copy||(copy||download).dataset.download;
  let value=kind==='run-input'?currentRunInput:e.target.closest('#inspectorContent')||e.target.closest('#inspBody')?activationData.find(a=>a.id===selectedActivation)?.outputs:currentRunOutput;
  const name=view?view.dataset.viewArtifact:(copy||download).dataset.name;
  if(name&&value&&typeof value==='object')value=value[name];
  const ref=artifactData(value);
  if(view){await showArtifactOutput(ref,name,value);return;}
  let content=ref?(ref.data??ref):value;
  if(download&&ref){try{content=await loadArtifactBlob(ref)}catch(err){message('Download failed: '+err.message,true);return;}}
  if(copy&&!download){const text=JSON.stringify(content??null,null,2);try{if(!navigator.clipboard)throw Error('Clipboard unavailable');await navigator.clipboard.writeText(text);message('Copied.')}catch(err){message('Copy failed: '+err.message,true)}return}
  const blob=content instanceof Blob?content:new Blob([typeof content==='string'?content:JSON.stringify(content??null,null,2)],{type:ref?.mime||'application/json'}),url=URL.createObjectURL(blob),a=document.createElement('a');
  a.href=url;a.download=safeFilename(ref?.path?.split('/').pop()||name||'output.json');a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);
}
let currentRunInput,currentRunOutput;
const EVENT_LABELS={'run.created':'Run created','run.started':'Run started','run.paused':'Run paused','run.succeeded':'Run succeeded','run.failed':'Run failed','run.cancelled':'Run cancelled','activation.created':'Activation created','activation.started':'Activation started','activation.succeeded':'Activation succeeded','activation.failed':'Activation failed','attempt.started':'Attempt started','attempt.succeeded':'Attempt succeeded','attempt.failed':'Attempt failed','signal.received':'Signal received','timer.fired':'Timer fired'};
function eventLabel(type){return EVENT_LABELS[type]||type.replace(/[._]/g,' ').replace(/\b\w/g,c=>c.toUpperCase())}
function eventHtml(e){const label=eventLabel(e.type);return `<li><time style="font-size:9px;color:var(--muted)">${esc(date(e.timestamp))} <span style="color:var(--muted2)">· seq ${esc(e.seq)}</span></time><strong style="display:block;font-size:11px;color:var(--ink)">${esc(label)}</strong>${e.subjectId?`<span style="color:var(--muted);font-size:10px">${esc(e.subjectId)}</span>`:''}${e.payload?`<details data-preserve-key="event:${esc(e.seq)}"><summary style="color:var(--muted);font-size:10px;cursor:pointer">Event data</summary><pre style="font-size:10px">${esc(pretty(e.payload))}</pre></details>`:''}</li>`}

/* detailed legacy detail fallback */
function renderDetail(r,g,events){
  currentRunInput=r.input;currentRunOutput=r.output;
  renderWorkbench(r,g,events);
}

function renderInspector(){
  const act=activationData.find(a=>a.id===selectedActivation);
  if(!act)return;
  // workbench inspector
  const inspBody=$('#inspBody');
  if(inspBody&&typeof renderInspectorWb==='function'){renderInspectorWb(act);return}
  // legacy
  const out=$('#inspectorContent');
  if(!out)return;
  $('#inspectorTitle').textContent=`Activation · ${act.nodeId}`;
  $('#inspectorHint').textContent=`${act.state} · ${act.id}`;
  if(inspectorTab==='outputs'){out.innerHTML=`<dl class="kv"><dt>Scope</dt><dd>${esc((act.scopePath||[]).join(' / ')||'root')}</dd><dt>Occurrence</dt><dd>${esc(act.occurrence)}</dd><dt>State</dt><dd>${esc(act.state)}</dd>${act.queueReason?`<dt>Queue reason</dt><dd>${esc(act.queueReason)}</dd>`:""}</dl><h3 class="insp-output-title">Outputs</h3>${outputMarkup(act.outputs)}`;return}
  out.innerHTML='<p class="inspector-placeholder">Loading…</p>';
  const id=selectedActivation,tab=inspectorTab,run=current,request=++inspectorRequest;
  Promise.all([inspectorTab==='logs'?api(`/v1/runs/${encodeURIComponent(current)}/logs?activationId=${encodeURIComponent(id)}`):Promise.resolve(null),inspectorTab==='attempts'?api(`/v1/runs/${encodeURIComponent(current)}/attempts?activationId=${encodeURIComponent(id)}`):Promise.resolve(null)]).then(([l,a])=>{if(request!==inspectorRequest||id!==selectedActivation||tab!==inspectorTab||run!==current||!out.isConnected)return;if(inspectorTab==='logs'){const logs=l.logs||[];const decoded=logs.length?logs.map(x=>{let data=x.data;try{data=new TextDecoder().decode(Uint8Array.from(atob(data),c=>c.charCodeAt(0)))}catch{}return {ts:date(x.timestamp),stream:x.stream,text:data}}).filter(x=>x):[];if(!decoded.length){out.innerHTML='<p class="inspector-placeholder">No logs.</p>';}else{let allText=decoded.map(x=>x.text).join('\n');out.innerHTML='<div class="data-tools"><button class="btn ghost" data-copylogs>Copy logs</button></div>'+decoded.map(x=>`<h3>${esc(x.ts)} · ${esc(x.stream)}</h3><pre>${esc(x.text)}</pre>`).join('');out._copyLogsText=allText}}else{const attempts=a.attempts||[];out.innerHTML=attempts.length?`<div class="tablebox"><table><thead><tr><th>Retry</th><th>Executor</th><th>State</th><th>Started</th><th>Finished</th></tr></thead><tbody>${attempts.map(x=>`<tr><td>${esc(x.retryNumber)}</td><td>${esc(x.executor)}</td><td>${esc(x.state)}</td><td>${esc(date(x.startedAt))}</td><td>${esc(date(x.finishedAt))}</td></tr>`).join('')}</tbody></table></div>`:'<p class="inspector-placeholder">No attempts.</p>'}}).catch(e=>{if(request!==inspectorRequest||id!==selectedActivation||run!==current)return;if(out.isConnected)out.innerHTML=`<p style="color:var(--red)">${esc(e.message)}</p>`})
}
async function loadActivation(id,offset=0){
 stopTranscriptPoll();
 const run=current,request=++inspectorRequest;
 selectedActivation=id;
 if(selectedActivationDetail?.id!==id){selectedActivationDetail=null;activationTranscripts.clear();}
 attemptOffset=offset;
 try{
  const [act,page]=await Promise.all([
   api(`/v1/runs/${encodeURIComponent(run)}/activations/${encodeURIComponent(id)}`),
   api(`/v1/runs/${encodeURIComponent(run)}/attempts?activationId=${encodeURIComponent(id)}&offset=${offset}&limit=50&details=false`)
  ]);
  if(current!==run||selectedActivation!==id||request!==inspectorRequest)return null;
  selectedActivationDetail=act;runAttempts=page.attempts||[];attemptPageMore=!!page.hasMore;
  const index=activationData.findIndex(a=>a.id===id);
  if(index>=0)activationData[index]=act;else activationData.push(act);
  document.querySelectorAll('[data-act]').forEach(b=>{const on=b.dataset.act===id;b.classList.toggle('selected',on);b.setAttribute('aria-pressed',String(on))});
  renderInspector();return act;
 }catch(err){if(current===run&&selectedActivation===id)message(err.message,true);return null;}
}
document.addEventListener('click',event=>{const button=event.target.closest('[data-attempt-page]');if(button&&!button.disabled&&selectedActivation)loadActivation(selectedActivation,Math.max(0,attemptOffset+Number(button.dataset.attemptPage)*50));});
async function loadOlder(){const id=current;try{const d=await api(`/v1/runs/${encodeURIComponent(current)}/events?after=${after}&limit=50`);if(id!==current)return;after=d.nextAfter||after;hasMore=!!d.hasMore;const list=$('#timeline');if(d.events?.length)list.insertAdjacentHTML('beforeend',d.events.map(eventHtml).join(''));if($('#moreEvents'))$('#moreEvents').disabled=!hasMore}catch(e){message(e.message,true)}}
async function sendSignal(){const name=$('#signalName')?.value.trim(),status=$('#signalStatus');if(!name){if(status){status.className='inline-status error';status.textContent='Enter a signal name.'}return}let payload;try{payload=JSON.parse($('#signalPayload')?.value||'{}');if(!payload||typeof payload!=='object'||Array.isArray(payload))throw Error()}catch{if(status){status.className='inline-status error';status.textContent='Payload must be a JSON object.'}return}try{await api(`/v1/runs/${encodeURIComponent(current)}/signals/${encodeURIComponent(name)}`,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)});if(status){status.textContent=`Signal "${name}" sent.`;status.className='inline-status ok'}}catch(e){if(status){status.className='inline-status error';status.textContent=e.message}}}

// Simple JSON Schema contracts get typed controls. Complex contracts retain a
// JSON editor and show their full schema; the coordinator validates all input.
function requestFields(schema, path=[], required=true) {
  if(schema?.oneOf || schema?.anyOf || schema?.allOf || schema?.$ref)schema={title:schema.title,description:schema.description};
  // JSON Schema requires a property's presence, not a nonempty string.
  const attrs=`data-response-path="${esc(JSON.stringify(path))}" data-response-required="${required}" ${required && (schema?.type!=='string' || Array.isArray(schema?.enum))?'required':''}`;
  const label=esc(schema?.title||path[path.length-1]||'Response');
  const description=schema?.description?`<small>${esc(schema.description)}</small>`:'';
  if(schema?.type==='object' && schema.properties && required && !Array.isArray(schema.enum)){
    return `<fieldset><legend>${label}</legend>${Object.entries(schema.properties).map(([key,prop])=>requestFields(prop,[...path,key],(schema.required||[]).includes(key))).join('')}</fieldset>`;
  }
  let control;
  if(Array.isArray(schema?.enum))control=`<select ${attrs} data-json-value><option value="">Choose…</option>${schema.enum.map(value=>`<option value="${esc(JSON.stringify(value))}">${esc(JSON.stringify(value))}</option>`).join('')}</select>`;
  else if(schema?.type==='boolean')control=`<select ${attrs} data-json-value><option value="">Choose…</option><option value="true">True</option><option value="false">False</option></select>`;
  else if(schema?.type==='string')control=`<input ${attrs} type="text" ${schema.minLength!==undefined?`minlength="${esc(schema.minLength)}"`:''} ${schema.maxLength!==undefined?`maxlength="${esc(schema.maxLength)}"`:''}>`;
  else if(['number','integer'].includes(schema?.type))control=`<input ${attrs} data-json-value type="number" step="${schema.type==='integer'?'1':'any'}" ${schema.minimum!==undefined?`min="${esc(schema.minimum)}"`:''} ${schema.maximum!==undefined?`max="${esc(schema.maximum)}"`:''}>`;
  else control=`<textarea ${attrs} data-json-value aria-label="JSON response" placeholder="JSON value"></textarea>`;
  return `<label class="input-request-field">${label}${required?' *':''}${control}${description}</label>`;
}

function readRequestResponse(form) {
  let response={};
  for(const field of form.querySelectorAll('[data-response-path]')){
    if(field.value==='' && !field.required && field.dataset.responseRequired!=='true')continue;
    const path=JSON.parse(field.dataset.responsePath);
    const value=field.hasAttribute('data-json-value')?JSON.parse(field.value):field.value;
    if(!path.length){response=value;continue}
    let obj=response;
    for(const key of path.slice(0,-1)){
      if(!Object.hasOwn(obj,key))Object.defineProperty(obj,key,{value:{},enumerable:true,writable:true});
      obj=obj[key];
    }
    Object.defineProperty(obj,path[path.length-1],{value,enumerable:true,writable:true,configurable:true});
  }
  return response;
}

async function refreshInputRequests(runId) {
  const panel=$('#inputRequestsPanel'),container=$('#inputRequests');
  if(!panel||!container)return;
  try{
    const data=await api(`/v1/runs/${encodeURIComponent(runId)}/input-requests`);
    if(current!==runId)return;
    const requests=data.requests||[],signature=JSON.stringify([runId,requests]);
    if(container._requestSignature===signature)return;
    const existingForms=new Map([...container.querySelectorAll('form[data-input-request]')].map(form=>[form.dataset.inputRequest,form]));
    container._requestSignature=signature;
    panel.hidden=!requests.length;
    container.innerHTML=requests.map(req=>`<article><strong>${esc(req.name)}</strong> · ${esc(req.state)}<div class="subtle">Request ${esc(req.requestId)} · Activation ${esc(req.activationId)}${req.expiresAt?` · Expires ${esc(req.expiresAt)}`:''}${req.permissions?.length?` · Requires ${esc(req.permissions.join(', '))}`:''}</div>${req.state==='pending'?`<form data-input-request="${esc(req.requestId)}">${requestFields(req.schema)}<details><summary>Response schema</summary><pre>${esc(JSON.stringify(req.schema??{},null,2))}</pre></details><button class="btn" type="submit">Submit response</button><span role="status"></span></form>`:''}</article>`).join('');
    container.querySelectorAll('form[data-input-request]').forEach(form=>{
      const definition=JSON.stringify([runId,requests.find(req=>req.requestId===form.dataset.inputRequest)]);
      const existing=existingForms.get(form.dataset.inputRequest);
      if(existing?._requestDefinition===definition){form.replaceWith(existing);return}
      form._requestDefinition=definition;
      const key=crypto.randomUUID();
      form.onsubmit=async event=>{
        event.preventDefault();
        const status=form.querySelector('[role="status"]'),button=form.querySelector('button');
        button.disabled=true;
        try{
          const response=readRequestResponse(form);
          await api(`/v1/runs/${encodeURIComponent(runId)}/input-requests/${encodeURIComponent(form.dataset.inputRequest)}`,{method:'POST',headers:{'Content-Type':'application/json','Idempotency-Key':key},body:JSON.stringify(response)});
          status.textContent='Response accepted.';
          await refreshInputRequests(runId);
        }catch(err){status.textContent=err.message;button.disabled=false}
      };
    });
  }catch(err){if(current===runId){panel.hidden=false;container.textContent=`Unable to load input requests: ${err.message}`;container._requestSignature=null}}
}
function formatWorkerCapability(cap){
  if(cap==null)return '';
  if(typeof cap==='string')return cap;
  if(typeof cap==='object'&&cap.name)return cap.version?`${cap.name}@${cap.version}`:cap.name;
  return String(cap);
}
function workerCapabilityMarkup(cap,workerID=''){
 if(!cap)return '';
 if(typeof cap==='string')cap={name:cap};
 const readiness=cap.readiness?.ready===true?'ready':cap.readiness?.ready===false?'blocked':'unknown';
 const label={ready:'Ready',blocked:'Blocked',unknown:'Readiness unavailable'}[readiness];
 return `<a href="#worker-capability/${encodeURIComponent(workerID)}/${encodeURIComponent(cap.name||'')}/${encodeURIComponent(cap.version||'')}" class="worker-capability-chip worker-capability-chip--${readiness}" title="${esc(cap.name||'Unnamed capability')} · ${label}" aria-label="Inspect ${esc(cap.name||'unnamed capability')}: ${label}"><span class="worker-capability-dot" aria-hidden="true"></span><span>${esc(cap.name||'Unnamed capability')}</span></a>`;
}
function workerCapabilityDetailMarkup(cap,workerID=''){
 if(!cap)return '';
 if(typeof cap==='string')cap={name:cap};
 const report=cap.readiness,contract=cap.contract||{},checks=report?.checks||[];
 const readiness=report?.ready===true?'ready':report?.ready===false?'blocked':'unknown';
 const label={ready:'Ready',blocked:'Blocked',unknown:'Unavailable'}[readiness];
 const failed=checks.filter(check=>check.passed===false).length;
 const title=value=>String(value).replace(/([a-z])([A-Z])/g,'$1 $2').replace(/[-_]/g,' ').replace(/^./,c=>c.toUpperCase());
 const featureLabels={artifacts:'Artifacts',cancel:'Cancellation',progress:'Live progress',remoteTask:'Remote tasks',session:'Sessions',typedResult:'Typed results',usage:'Usage reporting'};
 const features=Object.entries(contract.capabilities||{}).filter(([,enabled])=>enabled===true).map(([name])=>`<span class="worker-feature">${esc(featureLabels[name]||title(name))}</span>`).join('');
 return `<details class="worker-capability" data-preserve-key="capability:${esc(workerID)}:${esc(cap.name||'unknown')}:${esc(cap.version||'')}"><summary><span class="worker-capability-identity"><strong>${esc(cap.name||'Unnamed capability')}</strong><span>${cap.version?`Version ${esc(cap.version)}`:'Version unavailable'}${failed?` · ${failed} ${failed===1?'check needs':'checks need'} attention`:''}</span></span><span class="worker-readiness worker-readiness--${readiness}"><span aria-hidden="true"></span>${label}</span><svg class="worker-capability-chevron" viewBox="0 0 16 16" aria-hidden="true"><path d="m6 3 5 5-5 5"/></svg></summary><div class="worker-capability-body"><div class="worker-features">${features||'<span class="subtle">Optional features unavailable</span>'}</div><dl class="worker-capability-meta"><div><dt>Contract</dt><dd>${esc(contract.contractVersion||'Unavailable')}</dd></div>${contract.environment?.digest?`<div><dt>Environment</dt><dd><code title="${esc(contract.environment.digest)}">${esc(contract.environment.digest)}</code></dd></div>`:''}</dl>${checks.length?`<div class="worker-readiness-checks">${checks.map(check=>{const state=check.passed===true?'passed':check.passed===false?'failed':'unknown';return `<div class="worker-readiness-check worker-readiness-check--${state}"><span class="worker-check-icon" aria-label="${state==='passed'?'Passed':state==='failed'?'Failed':'Not reported'}">${state==='passed'?'✓':state==='failed'?'!':'—'}</span><div><strong>${esc(title(check.name||'Check'))}</strong><p>${esc(check.message||'No details reported.')}</p></div></div>`}).join('')}</div>`:'<p class="subtle worker-check-empty">No readiness check details reported.</p>'}</div></details>`;
}
async function loadWorkers(){
  try{
    const d=await api('/v1/workers');
    const roster=d.registeredWorkers||[];
    const assignments=d.workers||[];
    const busy=new Set(assignments.filter(x=>x.state==='active').map(x=>x.workerId));
    const rosterEl=$('#workerRoster');
    if(rosterEl){
      rosterEl.innerHTML=roster.length?`<div class="tablebox"><table><thead><tr><th>Worker</th><th>Status</th><th>Capacity</th><th>Capabilities</th><th>Last seen</th></tr></thead><tbody>${roster.map(x=>{const anchor=encodeURIComponent(x.workerId),id=esc(x.workerId),status=x.draining?'draining':busy.has(x.workerId)?'busy':'idle',caps=(x.capabilities||[]).map(cap=>workerCapabilityMarkup(cap,x.workerId)).filter(Boolean).join(', ')||'—';return `<tr id="worker-roster-${anchor}"><td class="primary-cell"><strong>${id}</strong>${Object.keys(x.labels||{}).length?`<details><summary>Placement &amp; isolation labels</summary><pre>${esc(pretty(x.labels))}</pre></details>`:''}</td><td><span class="state ${status==='busy'?'running':'queued'}">${status}</span></td><td class="num">${esc(x.capacity)}</td><td class="mono">${caps}</td><td class="num">${esc(date(x.lastSeen))}</td></tr>`}).join('')}</tbody></table></div>`:'<div class="tablebox empty"><strong>No workers available</strong>No workers have heartbeated within the coordinator window. Start a worker and register it with the coordinator.</div>';
    }
    const workerLink=id=>`<a href="#worker-roster-${encodeURIComponent(id)}" style="color:var(--teal)"><strong>${esc(id)}</strong></a>`;
    $('#workerList').innerHTML=assignments.length?`<div class="tablebox"><table><thead><tr><th>Worker</th><th>Run</th><th>Activation</th><th>Generation</th><th>Lease until</th><th>State</th></tr></thead><tbody>${assignments.map(x=>`<tr><td class="primary-cell">${workerLink(x.workerId)}</td><td><a href="#run/${encodeURIComponent(x.runId)}" style="color:var(--teal)">${esc(x.runId)}</a></td><td class="mono">${esc(x.activationId)}</td><td>${esc(x.generation)}</td><td style="color:var(--muted)">${esc(date(x.leaseUntil))}</td><td><span class="state ${esc(x.state)}">${esc(x.state)}</span></td></tr>`).join('')}</tbody></table></div>`:'<div class="tablebox empty"><strong>No active assignments</strong>The coordinator is not reporting any active worker assignments right now.</div>';
  }catch(e){
    if(e.name==='AbortError')return;
    const err=`<div class="notice error" role="alert">Unable to load workers: ${esc(e.message)}</div>`;
    if($('#workerRoster'))$('#workerRoster').innerHTML=err;
    $('#workerList').innerHTML=err;
  }
}

