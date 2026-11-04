/* ── inspector render ─────────────────────────── */
const TRANSCRIPT_POLL_MS=1200;
const activationTranscripts=new Map();
let transcriptPollTimer=null,transcriptPollToken=0;

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
    state.agent+=decodeLogData(chunk.data);
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
    el.textContent=text;
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
      const d=await api(`/v1/runs/${encodeURIComponent(runId)}/logs?activationId=${encodeURIComponent(actId)}`);
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
  if(!activationHasTranscript(act))return '';
  const cached=activationTranscripts.get(act.id);
  const transcriptUrl=`#transcript/${encodeURIComponent(runId||current||'')}/${encodeURIComponent(act.id)}`;
  const text=cached?.agent||(act.state==='running'?'Waiting for agent output…':'No transcript recorded.');
  const isWaiting=!cached?.agent&&act.state==='running';
  return `<div class="insp-section" id="inspTranscriptSection"><div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:6px"><span style="font-size:10px;font-weight:650;color:var(--muted)">Live transcript${act.state==='running'?' · streaming':''}</span><a href="${transcriptUrl}" class="insp-transcript-link" aria-label="Open transcript page">Open ↗</a></div><pre class="insp-transcript${isWaiting?' waiting':''}" id="inspTranscriptBody">${esc(text)}</pre></div>`;
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
  const rows=actAttempts.map(a=>{
    const retry=a.retryNumber||0;
    const label=retry===0?'Attempt 1':`Retry ${retry} (attempt ${retry+1})`;
    if(a.usage==null){
      return `<tr><td>${esc(label)}</td><td colspan="3" style="color:var(--muted)">Unavailable — no token counts recorded</td></tr>`;
    }
    const fmtTokens=n=>n>=1000?`${(n/1000).toFixed(1)}K`:String(n||0);
    return `<tr><td>${esc(label)}</td><td class="num">${esc(fmtTokens(a.usage.input))}</td><td class="num">${esc(fmtTokens(a.usage.output))}</td><td class="num">${esc(fmtTokens(a.usage.cached))}</td></tr>`;
  });
  return `<div class="insp-section">
    <div style="font-size:10px;font-weight:650;color:var(--muted);margin-bottom:6px">Token usage</div>
    <div class="tablebox" style="font-size:11px"><table>
      <thead><tr><th></th><th>In</th><th>Out</th><th>Cached</th></tr></thead>
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
        `<dt>Agent</dt><dd>${esc(agent)}</dd>
        <dt>Skill</dt><dd>${esc(skill)}</dd>
        <dt>Inputs</dt><dd>${esc(inRef)}</dd>
        <dt>Output</dt><dd>${esc(outRef)}</dd>
      </dl>
      ${hasTranscript?`<div style="margin-top:10px"><a href="${transcriptUrl}" class="insp-transcript-link">View transcript ↗</a></div>`:''}
    </div>
    ${evidences.length?`
    <div class="insp-section">
      <div style="font-size:10px;font-weight:650;color:var(--muted);margin-bottom:6px">Evidence and decision</div>
      <div class="evidence-list">${evidences.map(ev=>`<div class="evidence-item">${esc(ev)}</div>`).join('')}</div>
      ${route?`<div class="evidence-route">route: <code>${esc(route)}</code></div>`:''}
    </div>`:''}
    ${act.outputs?`
    <div class="insp-section">
      <div style="font-size:10px;font-weight:650;color:var(--muted);margin-bottom:6px">Outputs</div>
      <div class="data-tree">${treeMarkup(act.outputs,'outputs',0,`run:${current}:activation:${act.id}:outputs`)}</div>
    </div>`:''}
    ${act.error?`<div class="insp-section"><div class="insp-error">${esc(act.error)}</div></div>`:''}
    ${buildAttemptUsageMarkup(act)}
    ${transcriptSectionMarkup(act,current)}
  `);
  if(activationHasTranscript(act)&&(act.state==='running'||activationTranscripts.get(act.id)?.agent))scheduleTranscriptPoll(act,current);
  const actionsEl=$('#inspActions');
  if(actionsEl)actionsEl.hidden=!['failed','cancelled'].includes(wbCurrentRunState);
}

/* ── shared helpers (kept from original) ─────── */
const PREVIEW_LIMIT=4096;
function valueType(v){if(v===null)return 'null';if(Array.isArray(v))return 'array';if(typeof v==='object')return 'object';return typeof v}
function treeMarkup(value,key='Value',depth=0,statePath='tree'){const type=valueType(value),label=esc(key),count=type==='array'?value.length:type==='object'?Object.keys(value).length:0;if(type==='object'||type==='array'){if(!count)return `<div class="data-row"><span class="data-key">${label}</span><span class="data-type">${type} · empty</span></div>`;const children=type==='array'?value.map((v,i)=>[String(i),v]):Object.entries(value);return `<div class="data-row"><details data-preserve-key="${esc(`${statePath}/${key}`)}"><summary><span class="data-key">${label}</span> <span class="data-type">${type} · ${count}</span></summary><div class="data-children data-tree">${children.map(([k,v])=>treeMarkup(v,k,depth+1,`${statePath}/${key}`)).join('')}</div></details></div>`}if(type==='null')return `<div class="data-row"><span class="data-key">${label}</span><span class="data-type">null</span></div>`;return `<div class="data-row"><span class="data-key">${label}</span><span class="data-value ${esc(type)}">${esc(type==='string'?JSON.stringify(value):String(value))}</span><span class="data-type">${esc(type)}</span></div>`}
function inputMarkup(value){if(value===undefined||value===null)return `<div class="data-empty">${value===null?'Input is null.':'No input recorded.'}</div>`;return `<div class="data-tools"><button class="btn ghost" data-copy="run-input">Copy input</button></div><div class="data-tree">${treeMarkup(value,'Input',0,'run:input')}</div>`}
function artifactData(v){if(v&&typeof v==='object'&&!Array.isArray(v)&&('mime'in v||'mimeType'in v||'path'in v||'data'in v||'digest'in v||'$artifact'in v))return v;return null}
function outputItems(value){if(value===undefined||value===null)return [];if(Array.isArray(value))return value.map((v,i)=>[String(i),v]);if(typeof value==='object'){const entries=Object.entries(value);return entries.length?entries:[['Output',value]]}return [['Output',value]]}
function safePreview(value,mime){let raw;if(typeof value==='string')raw=value;else if(value===undefined||value===null)return null;else raw=JSON.stringify(value,null,2);if(!raw)return null;if(mime&&!/(text\/|json|xml|javascript|yaml|csv|markdown)/i.test(mime))return null;return {text:raw.slice(0,PREVIEW_LIMIT),truncated:raw.length>PREVIEW_LIMIT}}
function outputMarkup(value){if(value===undefined||value===null)return `<div class="data-empty">${value===null?'Output is null.':'No output recorded.'}</div>`;const items=outputItems(value);return `<div class="data-tools"><button class="btn ghost" data-copy="run-output">Copy output</button></div>${items.map(([name,item])=>{const ref=artifactData(item);if(!ref)return `<section class="artifact"><div class="artifact-head"><strong>${esc(name)}</strong><span class="data-type">${esc(valueType(item))}</span></div><div class="data-tree">${treeMarkup(item,'Value')}</div></section>`;const mime=typeof (ref.mime||ref.mimeType)==='string'?(ref.mime||ref.mimeType):'',path=typeof ref.path==='string'?ref.path:'',data=ref.data,preview=safePreview(data,mime);let size=typeof data==='string'?new TextEncoder().encode(data).length:null;const meta=[['MIME',mime],['Path',path],['Digest',ref.digest||''],['Size',ref.size??(size===null?'':`${size} B`)]].filter(x=>x[1]!=='');return `<section class="artifact"><div class="artifact-head"><strong>${esc(name)}</strong><span class="data-type">Artifact</span></div>${meta.length?`<dl class="artifact-meta">${meta.map(([k,v])=>`<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`).join('')}</dl>`:''}<div class="data-tools"><button class="btn ghost" data-download="artifact" data-name="${esc(name)}" data-copy="run-output">Download</button></div>${preview?`<details><summary class="data-type">${preview.truncated?'Preview (4 KB)':'Preview'}</summary><pre class="preview">${esc(preview.text)}</pre></details>`:`<div class="data-note" style="color:var(--muted);font-size:10px">Preview unavailable</div>`}</section>`}).join('')}`}
function safeFilename(name){return String(name||'artifact').replace(/[^a-z0-9._-]+/gi,'_').replace(/^\.+/,'').slice(0,100)||'artifact'}
async function inspectorAction(e){
  const copyLogs=e.target.closest('[data-copylogs]');
  if(copyLogs){const out=$('#inspectorContent'),text=out&&out._copyLogsText;if(!text)return;try{await navigator.clipboard.writeText(text);message('Logs copied.')}catch(err){message('Copy failed: '+err.message,true)}return}
  const copy=e.target.closest('[data-copy]'),download=e.target.closest('[data-download]');
  if(!copy&&!download)return;
  const kind=(copy||download).dataset.copy||(copy||download).dataset.download;
  let value=kind==='run-input'?currentRunInput:e.target.closest('#inspectorContent')||e.target.closest('#inspBody')?activationData.find(a=>a.id===selectedActivation)?.outputs:currentRunOutput;
  const name=(copy||download).dataset.name;
  if(name&&value&&typeof value==='object')value=value[name];
  const ref=artifactData(value),content=ref?(ref.data??ref):value;
  if(copy){const text=JSON.stringify(content??null,null,2);try{if(!navigator.clipboard)throw Error('Clipboard unavailable');await navigator.clipboard.writeText(text);message('Copied.')}catch(err){message('Copy failed: '+err.message,true)}return}
  const blob=new Blob([typeof content==='string'?content:JSON.stringify(content??null,null,2)],{type:ref?.mime||'application/json'}),url=URL.createObjectURL(blob),a=document.createElement('a');
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
  if(inspectorTab==='outputs'){out.innerHTML=`<dl class="kv"><dt>Scope</dt><dd>${esc((act.scopePath||[]).join(' / ')||'root')}</dd><dt>Occurrence</dt><dd>${esc(act.occurrence)}</dd><dt>State</dt><dd>${esc(act.state)}</dd></dl><h3>Outputs</h3>${outputMarkup(act.outputs)}`;return}
  out.innerHTML='<p class="inspector-placeholder">Loading…</p>';
  const id=selectedActivation,tab=inspectorTab,run=current,request=++inspectorRequest;
  Promise.all([inspectorTab==='logs'?api(`/v1/runs/${encodeURIComponent(current)}/logs?activationId=${encodeURIComponent(id)}`):Promise.resolve(null),inspectorTab==='attempts'?api(`/v1/runs/${encodeURIComponent(current)}/attempts?activationId=${encodeURIComponent(id)}`):Promise.resolve(null)]).then(([l,a])=>{if(request!==inspectorRequest||id!==selectedActivation||tab!==inspectorTab||run!==current||!out.isConnected)return;if(inspectorTab==='logs'){const logs=l.logs||[];const decoded=logs.length?logs.map(x=>{let data=x.data;try{data=new TextDecoder().decode(Uint8Array.from(atob(data),c=>c.charCodeAt(0)))}catch{}return {ts:date(x.timestamp),stream:x.stream,text:data}}).filter(x=>x):[];if(!decoded.length){out.innerHTML='<p class="inspector-placeholder">No logs.</p>';}else{let allText=decoded.map(x=>x.text).join('\n');out.innerHTML='<div class="data-tools"><button class="btn ghost" data-copylogs>Copy logs</button></div>'+decoded.map(x=>`<h3>${esc(x.ts)} · ${esc(x.stream)}</h3><pre>${esc(x.text)}</pre>`).join('');out._copyLogsText=allText}}else{const attempts=a.attempts||[];out.innerHTML=attempts.length?`<div class="tablebox"><table><thead><tr><th>Retry</th><th>Executor</th><th>State</th><th>Started</th><th>Finished</th></tr></thead><tbody>${attempts.map(x=>`<tr><td>${esc(x.retryNumber)}</td><td>${esc(x.executor)}</td><td>${esc(x.state)}</td><td>${esc(date(x.startedAt))}</td><td>${esc(date(x.finishedAt))}</td></tr>`).join('')}</tbody></table></div>`:'<p class="inspector-placeholder">No attempts.</p>'}}).catch(e=>{if(request!==inspectorRequest||id!==selectedActivation||run!==current)return;if(out.isConnected)out.innerHTML=`<p style="color:var(--red)">${esc(e.message)}</p>`})
}
function loadActivation(id){
  stopTranscriptPoll();
  selectedActivation=id;
  document.querySelectorAll('[data-act]').forEach(b=>{const on=b.dataset.act===id;b.classList.toggle('selected',on);b.setAttribute('aria-pressed',String(on))});
  renderInspector();
}
async function loadOlder(){const id=current;try{const d=await api(`/v1/runs/${encodeURIComponent(current)}/events?after=${after}&limit=50`);if(id!==current)return;after=d.nextAfter||after;hasMore=!!d.hasMore;const list=$('#timeline');if(d.events?.length)list.insertAdjacentHTML('beforeend',d.events.map(eventHtml).join(''));if($('#moreEvents'))$('#moreEvents').disabled=!hasMore}catch(e){message(e.message,true)}}
async function sendSignal(){const name=$('#signalName')?.value.trim(),status=$('#signalStatus');if(!name){if(status){status.className='inline-status error';status.textContent='Enter a signal name.'}return}let payload;try{payload=JSON.parse($('#signalPayload')?.value||'{}');if(!payload||typeof payload!=='object'||Array.isArray(payload))throw Error()}catch{if(status){status.className='inline-status error';status.textContent='Payload must be a JSON object.'}return}try{await api(`/v1/runs/${encodeURIComponent(current)}/signals/${encodeURIComponent(name)}`,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)});if(status){status.textContent=`Signal "${name}" sent.`;status.className='inline-status ok'}}catch(e){if(status){status.className='inline-status error';status.textContent=e.message}}}
function formatWorkerCapability(cap){
  if(cap==null)return '';
  if(typeof cap==='string')return cap;
  if(typeof cap==='object'&&cap.name)return cap.version?`${cap.name}@${cap.version}`:cap.name;
  return String(cap);
}
async function loadWorkers(){
  try{
    const d=await api('/v1/workers');
    const roster=d.registeredWorkers||[];
    const assignments=d.workers||[];
    const busy=new Set(assignments.filter(x=>x.state==='active').map(x=>x.workerId));
    const rosterEl=$('#workerRoster');
    if(rosterEl){
      rosterEl.innerHTML=roster.length?`<div class="tablebox"><table><thead><tr><th>Worker</th><th>Status</th><th>Capacity</th><th>Capabilities</th><th>Last seen</th></tr></thead><tbody>${roster.map(x=>{const anchor=encodeURIComponent(x.workerId),id=esc(x.workerId),status=busy.has(x.workerId)?'busy':'idle',caps=(x.capabilities||[]).map(c=>esc(formatWorkerCapability(c))).filter(Boolean).join(', ')||'—';return `<tr id="worker-roster-${anchor}"><td class="primary-cell"><strong>${id}</strong></td><td><span class="state ${status==='busy'?'running':'queued'}">${status}</span></td><td class="num">${esc(x.capacity)}</td><td class="mono">${caps}</td><td class="num">${esc(date(x.lastSeen))}</td></tr>`}).join('')}</tbody></table></div>`:'<div class="tablebox empty"><strong>No workers available</strong>No workers have heartbeated within the coordinator window. Start a worker and register it with the coordinator.</div>';
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

