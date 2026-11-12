/* ── inspector render ─────────────────────────── */
const TRANSCRIPT_POLL_MS=1200;
const SNAPSHOT_POLL_MS=5000;
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
function activationIsStreamingWorkbench(act){
  if(!act)return false;
  if(['succeeded','failed','cancelled'].includes(act.state))return false;
  return act.state==='running';
}
function scheduleTranscriptPoll(act,runId){
  stopTranscriptPoll();
  const token=transcriptPollToken,actId=act.id;
  let statePollCount=0;
  let trackedAct=act;
  async function tick(){
    if(token!==transcriptPollToken||selectedActivation!==actId||current!==runId)return;
    try{
      const d=await api(`/v1/runs/${encodeURIComponent(runId)}/logs?activationId=${encodeURIComponent(actId)}`);
      if(token!==transcriptPollToken||selectedActivation!==actId||current!==runId)return;
      const live=activationData.find(a=>a.id===actId)||trackedAct;
      updateTranscriptPanel(mergeActivationLogs(actId,d.logs||[]),activationIsStreamingWorkbench(live));
    }catch{}
    if(token!==transcriptPollToken||selectedActivation!==actId||current!==runId)return;
    statePollCount++;
    if(statePollCount%5===0){
      try{
        const fresh=await api(`/v1/runs/${encodeURIComponent(runId)}/activations/${encodeURIComponent(actId)}`);
        if(token!==transcriptPollToken||selectedActivation!==actId||current!==runId)return;
        if(fresh){
          const wasStreaming=activationIsStreamingWorkbench(trackedAct);
          trackedAct=fresh;
          const idx=activationData.findIndex(a=>a.id===actId);
          if(idx>=0)activationData[idx]=fresh;
          if(wasStreaming&&!activationIsStreamingWorkbench(fresh)){
            updateTranscriptPanel(activationTranscripts.get(actId)?.agent||'',false);
            const section=(document.getElementById('inspTranscriptSection') as UIElement);
            if(section)section.remove();
            return;
          }
        }
      }catch{}
    }
    if(token!==transcriptPollToken||selectedActivation!==actId||current!==runId)return;
    const live=activationData.find(a=>a.id===actId)||trackedAct;
    if(activationIsStreamingWorkbench(live))transcriptPollTimer=setTimeout(tick,TRANSCRIPT_POLL_MS);
    else updateTranscriptPanel(activationTranscripts.get(actId)?.agent||'',false);
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
  const transcriptUrl=`/transcripts/${encodeURIComponent(runId||current||'')}/${encodeURIComponent(act.id)}`;
  const rawText=cached?.agent||'';
  const isWaiting=!cached?.agent&&act.state==='running';
  const bodyContent=isWaiting
    ? 'Waiting for agent output…'
    : rawText
      ? renderMarkdown(rawText)
      : 'No transcript recorded.';
  const bodyInner=isWaiting?renderViewTemplate("shared/inline-text", [viewValue(esc(bodyContent))]):bodyContent;
  return renderViewTemplate("inspector/transcript-section-markup-insp-section", [viewValue(act.state==='running'?' · streaming':''), viewValue(transcriptUrl), viewValue(isWaiting?' waiting':''), viewValue(bodyInner)]);
}

function resolveInspectorAgent(act){
  // Prefer the latest attempt that has actually started. Queued retries are not
  // the executor currently working on the activation yet.
  const attempts=typeof runAttempts!=='undefined'?runAttempts:[];
  const actAttempts=attempts.filter(a=>a.activationId===act.id&&a.state!=='queued').sort((a,b)=>(b.retryNumber||0)-(a.retryNumber||0));
  return actAttempts[0]?.executor||act.executorHint||act.executor||'—';
}

// Each attempt retains its own disjoint accounting scopes and provenance.
function buildAttemptUsageMarkup(act){
 const attempts=typeof runAttempts!=='undefined'?runAttempts:[];
 const rows=attempts.filter(a=>a.activationId===act.id).sort((a,b)=>(a.retryNumber||0)-(b.retryNumber||0));
 if(!rows.length)return '';
 return renderViewTemplate("inspector/build-attempt-usage-markup-insp-section", [viewValue(rows.map((a,i)=>renderViewTemplate("inspector/build-attempt-usage-markup-section", [viewValue(esc(attemptOffset+i+1)), viewValue(buildAttemptAccountingMarkup(a.accounting))])).join(''))]);
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
  const transcriptUrl=`/transcripts/${encodeURIComponent(current||'')}/${encodeURIComponent(act.id)}`;
  const hasTranscript=activationHasTranscript(act);
  replaceHTMLPreservingDetails(body,renderViewTemplate("inspector/render-inspector-wb-insp-section", [viewValue(stateClass(act.state)||''), viewValue(esc(act.state)), viewValue(act.occurrence?`· iteration ${act.occurrence}`:'')]).trimEnd()+renderViewTemplate("inspector/render-inspector-wb-dd")+
        renderViewTemplate("inspector/render-inspector-wb-insp-value-id", [viewValue(act.queueReason?renderViewTemplate("inspector/render-inspector-wb-dt", [viewValue(esc(act.queueReason))]):""), viewValue(esc(agent)), viewValue(act.skill?renderViewTemplate("inspector/render-inspector-wb-dt-2", [viewValue(esc(skill))]):""), viewValue(esc(inRef)), viewValue(esc(inRef)), viewValue(esc(outRef)), viewValue(esc(outRef)), viewValue(hasTranscript?renderViewTemplate("inspector/render-inspector-wb-insp-detail-actions", [viewValue(transcriptUrl)]):''), viewValue(evidences.length?renderViewTemplate("inspector/render-inspector-wb-insp-section-2", [viewValue(evidences.map(ev=>renderViewTemplate("inspector/render-inspector-wb-evidence-item", [viewValue(esc(ev))])).join('')), viewValue(route?renderViewTemplate("inspector/render-inspector-wb-evidence-route", [viewValue(esc(route)), viewValue(esc(route))]):'')]):''), viewValue(act.outputs?renderViewTemplate("inspector/render-inspector-wb-insp-section-3", [viewValue(outputMarkup(act.outputs))]):''), viewValue(act.error?renderViewTemplate("inspector/render-inspector-wb-insp-section-4", [viewValue(esc(act.error))]):''), viewValue(buildAttemptUsageMarkup(act)), viewValue(typeof attemptMetadataMarkup==='function'?attemptMetadataMarkup(inspectorAttempts.filter(a=>a.activationId===act.id)):''), viewValue(session?renderViewTemplate("inspector/render-inspector-wb-insp-section-5"):''), viewValue(Math.floor(attemptOffset/50)+1), viewValue(attemptOffset===0?' disabled':''), viewValue(!attemptPageMore?' disabled':''), viewValue(transcriptSectionMarkup(act,current))]));
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
  const markup=renderViewTemplate("inspector/markup-insp-output-title")+snapshots.map(event=>{
   const ref=event.payload.snapshot;
   return renderViewTemplate("inspector/markup-artifact", [viewValue(esc(ref.name)), viewValue(esc(event.seq)), viewValue(esc(event.subjectId)), viewValue(esc(event.subjectId)), viewValue(esc(date(event.timestamp))), viewValue(esc(ref.digest)), viewValue(esc(ref.digest)), viewValue(esc(event.seq))]);
  }).join('')+(!snapshots.length?renderViewTemplate("inspector/markup-subtle"):'')+
  renderViewTemplate("inspector/markup-pager", [viewValue(snapshotPageIndex+1), viewValue(snapshotPageIndex===0?' disabled':''), viewValue(!data.hasMore?' disabled':'')]);
  if(el._snapshotMarkup!==markup){el._snapshotMarkup=markup;if(typeof replaceHTMLPreservingDetails==='function')replaceHTMLPreservingDetails(el,markup);else el.innerHTML=markup;}
  for(const button of el.querySelectorAll<UIElement>('[data-snapshot-seq]'))button.onclick=()=>{
   const event=snapshots.find(e=>String(e.seq)===button.dataset.snapshotSeq);
   if(event)showArtifactOutput(event.payload.snapshot,event.payload.snapshot.name);
  };
  for(const button of el.querySelectorAll<UIElement>('[data-snapshot-page]'))button.onclick=()=>{snapshotPageIndex+=Number(button.dataset.snapshotPage);loadInspectorSnapshots(act,runId);};
 }catch{const el=$('#inspSnapshots');if(el&&token===snapshotPollToken)el.textContent='Unable to load intermediate snapshots.';}
 const liveAct=(typeof activationData!=='undefined'?activationData:[]).find(item=>item.id===act.id);
 if(token===snapshotPollToken&&selectedActivation===act.id&&current===runId&&liveAct?.state==='running')snapshotPollTimer=setTimeout(()=>loadInspectorSnapshots(liveAct,runId),SNAPSHOT_POLL_MS);
}

/* ── shared helpers (kept from original) ─────── */
const PREVIEW_LIMIT=MuxlioJson?.PREVIEW_BYTES??4096;
const jsonCopyStores=new Map();
function valueType(v){return MuxlioJson.jsonValueType(v)}
function treeMarkup(value,key='Value',depth=0,statePath='tree'){return MuxlioJson.jsonTreeMarkup(value,key,depth,statePath,esc)}
function rememberJsonCopy(target,text){if(target&&text!=null)jsonCopyStores.set(target,String(text))}
function resolveJsonCopy(target,mode){const full=jsonCopyStores.get(target)||'';if(mode==='preview'){const preview=full.slice(0,PREVIEW_LIMIT);return {text:preview,label:full.length>preview.length?'Preview copied.':'JSON copied.'}}return {text:full,label:'JSON copied.'}}
function inputMarkup(value){if(value===undefined||value===null)return renderViewTemplate("inspector/input-markup-data-empty", [viewValue(value===null?'Input is null.':'No input recorded.')]);const panel=MuxlioJson.jsonPanelFromValue(value,esc,{title:'Input',statePath:'run:input',copyLabel:'Copy input'});rememberJsonCopy('run:input',panel.raw);return panel.html}
function artifactData(v){if(v&&typeof v==='object'&&!Array.isArray(v)&&('mime'in v||'mimeType'in v||'path'in v||'data'in v||'digest'in v||'$artifact'in v))return v;return null}
function outputItems(value){if(value===undefined||value===null)return [];if(Array.isArray(value))return value.map((v,i)=>[String(i),v]);if(typeof value==='object'){const entries=Object.entries(value);return entries.length?entries:[['Output',value]]}return [['Output',value]]}
function safePreview(value,mime){let raw;if(typeof value==='string')raw=value;else if(value===undefined||value===null)return null;else raw=JSON.stringify(value,null,2);if(!raw)return null;if(mime&&!/(text\/|json|xml|javascript|yaml|csv|markdown)/i.test(mime))return null;return {text:raw.slice(0,PREVIEW_LIMIT),truncated:raw.length>PREVIEW_LIMIT,raw}}
function outputMarkup(value, ..._extra: unknown[]){if(value===undefined||value===null)return renderViewTemplate("inspector/input-markup-data-empty", [viewValue(value===null?'Output is null.':'No output recorded.')]);const items=outputItems(value);return `${items.map(([name,item])=>{const ref=artifactData(item);if(!ref){const panel=MuxlioJson.jsonPanelFromValue(item,esc,{title:name,statePath:`output:${name}`,copyLabel:'Copy JSON'});rememberJsonCopy(`output:${name}`,panel.raw);return renderViewTemplate("inspector/output-markup-artifact", [viewValue(esc(name)), viewValue(esc(valueType(item))), viewValue(typeof item==='string'?renderViewTemplate("inspector/output-markup-markdown-body", [viewValue(renderMarkdown(item))]):panel.html)])}const mime=typeof (ref.mime||ref.mimeType)==='string'?(ref.mime||ref.mimeType):'',path=typeof ref.path==='string'?ref.path:'',data=ref.data;let size=typeof data==='string'?new TextEncoder().encode(data).length:null;const meta=[['MIME',mime],['Path',path],['Digest',ref.digest||''],['Size',ref.size??(size===null?'':`${size} B`)]].filter(x=>x[1]!=='');const jsonPreview=MuxlioJson.jsonArtifactPreview(ref,esc);const previewPayload=jsonPreview?MuxlioJson.jsonPreviewPayload(ref):null;if(previewPayload)rememberJsonCopy(`artifact-preview:${path||name}`,previewPayload.raw);return renderViewTemplate("inspector/output-markup-artifact-2", [viewValue(esc(path||name)), viewValue(esc(name)), viewValue(meta.length?renderViewTemplate("inspector/output-markup-artifact-meta", [viewValue(meta.map(([k,v])=>renderViewTemplate("inspector/output-markup-dt", [viewValue(esc(k)), viewValue(k==='Digest'?'':esc(v)), viewValue(k==='Digest'?renderViewTemplate("inspector/output-markup-digest-details", [viewValue(esc(v)), viewValue(esc(v))]):esc(v))])).join(''))]):''), viewValue(jsonPreview||''), viewValue(esc(name)), viewValue(esc(name))])}).join('')}`}
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
async function showArtifactOutput(ref,name,value?){
  const modal=$('#outputModal'),body=$('#outputModalBody'),request=++outputModalRequest;
  outputModalOpener=document.activeElement;
  $('#outputModalTitle').textContent=ref?.path||name||'Output';
  body.textContent='Loading output…';body.classList.add('json-loading');modal.hidden=false;$('#closeOutputModal').focus();
  try{
    const blob=ref?await loadArtifactBlob(ref):new Blob([typeof value==='string'?value:JSON.stringify(value,null,2)],{type:'text/plain'});
    const text=await blob.text();
    if(request!==outputModalRequest)return;
    const mime=ref?.mime||ref?.mimeType||'';
    const markdown=/^text\/markdown(?:;|$)/i.test(mime)||/\.md$/i.test(ref?.path||'');
    const json=MuxlioJson.isJsonMime(mime)||MuxlioJson.jsonPathLooksJson(ref?.path)||(!markdown&&!ref&&typeof value!=='string');
    body.classList.remove('json-loading');
    if(markdown){
      body.innerHTML=renderViewTemplate("inspector/body-inner-html-markdown-body", [viewValue(renderMarkdown(text))]);
      body._jsonRaw='';
      return;
    }
    if(json){
      const target=`modal:${ref?.path||name||'output'}`;
      const panel=MuxlioJson.jsonPanelFromText(text,esc,{title:'Value',statePath:target,copyLabel:'Copy JSON'});
      rememberJsonCopy(target,text);
      body.innerHTML=panel.html;
      body._jsonRaw=text;
      body.dataset.jsonTarget=target;
      return;
    }
    body.innerHTML=renderViewTemplate("inspector/body-inner-html-output-raw", [viewValue(esc(text))]);
    body._jsonRaw=text;
  }catch(err){if(request===outputModalRequest){body.classList.remove('json-loading');body.innerHTML=renderNotice(err.message, {error:true});}}
}
(document.getElementById('closeOutputModal') as UIElement)?.addEventListener('click',closeArtifactOutput);
document.addEventListener('click',e=>{if((e.target as UIElement).closest<UIElement>('[data-copy-json]'))inspectorAction(e)});
(document.getElementById('outputModal') as UIElement)?.addEventListener('click',e=>{if((e.target as UIElement).id==='outputModal')closeArtifactOutput();});
document.addEventListener('keydown',e=>{
  const modal=$('#outputModal');if(!modal||modal.hidden)return;
  if(e.key==='Escape'){e.preventDefault();closeArtifactOutput();}
  if(e.key==='Tab'){
    const items=[...modal.querySelectorAll<UIElement>('button,a[href],[tabindex="0"]')];
    const first=items[0],last=items[items.length-1];
    if(e.shiftKey&&document.activeElement===first){e.preventDefault();last?.focus();}
    else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first?.focus();}
  }
});
async function inspectorAction(e){
  const copyJson=(e.target as UIElement).closest<UIElement>('[data-copy-json]');
  if(copyJson){const mode=copyJson.dataset.copyJson||'full',target=copyJson.dataset.copyTarget||$('#outputModalBody')?.dataset.jsonTarget;const {text,label}=resolveJsonCopy(target,mode);try{if(!text)throw Error('Nothing to copy');if(!navigator.clipboard)throw Error('Clipboard unavailable');await navigator.clipboard.writeText(text);message(label)}catch(err){message('Copy failed: '+err.message,true)}return}
  const copyValue=(e.target as UIElement).closest<UIElement>('[data-copy-value]');
  if(copyValue){try{if(!navigator.clipboard)throw Error('Clipboard unavailable');await navigator.clipboard.writeText(copyValue.dataset.copyValue);message('Copied.')}catch(err){message('Copy failed: '+err.message,true)}return}
  const copyLogs=(e.target as UIElement).closest<UIElement>('[data-copylogs]');
  if(copyLogs){const out=$('#inspectorContent'),text=out&&out._copyLogsText;if(!text)return;try{await navigator.clipboard.writeText(text);message('Logs copied.')}catch(err){message('Copy failed: '+err.message,true)}return}
  const view=(e.target as UIElement).closest<UIElement>('[data-view-artifact]');
  const copy=(e.target as UIElement).closest<UIElement>('[data-copy]'),download=(e.target as UIElement).closest<UIElement>('[data-download]');
  if(!copy&&!download&&!view)return;
  const kind=view?'run-output':(copy||download).dataset.copy||(copy||download).dataset.download;
  let value=kind==='run-input'?currentRunInput:(e.target as UIElement).closest<UIElement>('#inspectorContent')||(e.target as UIElement).closest<UIElement>('#inspBody')?activationData.find(a=>a.id===selectedActivation)?.outputs:currentRunOutput;
  const name=view?view.dataset.viewArtifact:(copy||download).dataset.name;
  if(name&&value&&typeof value==='object')value=value[name];
  const ref=artifactData(value);
  if(view){await showArtifactOutput(ref,name,value);return;}
  let content=ref?(ref.data??ref):value;
  if(download&&ref){try{content=await loadArtifactBlob(ref)}catch(err){message('Download failed: '+err.message,true);return;}}
  if(copy&&!download){let text;if(content instanceof Blob)text=await content.text();else if(typeof content==='string')text=content;else text=MuxlioJson.stringifyForCopy(content);try{if(!navigator.clipboard)throw Error('Clipboard unavailable');await navigator.clipboard.writeText(text??'');message('Copied.')}catch(err){message('Copy failed: '+err.message,true)}return}
  const blob=content instanceof Blob?content:new Blob([typeof content==='string'?content:JSON.stringify(content??null,null,2)],{type:ref?.mime||'application/json'}),url=URL.createObjectURL(blob),a=document.createElement('a');
  a.href=url;a.download=safeFilename(ref?.path?.split('/').pop()||name||'output.json');a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);
}
let currentRunInput,currentRunOutput;
const EVENT_LABELS={'run.created':'Run created','run.started':'Run started','run.paused':'Run paused','run.succeeded':'Run succeeded','run.failed':'Run failed','run.cancelled':'Run cancelled','activation.created':'Activation created','activation.started':'Activation started','activation.succeeded':'Activation succeeded','activation.failed':'Activation failed','attempt.started':'Attempt started','attempt.succeeded':'Attempt succeeded','attempt.failed':'Attempt failed','signal.received':'Signal received','timer.fired':'Timer fired'};
function eventLabel(type){return EVENT_LABELS[type]||type.replace(/[._]/g,' ').replace(/\b\w/g,c=>c.toUpperCase())}
function eventHtml(e){const label=eventLabel(e.type);return renderViewTemplate("inspector/event-html-li", [viewValue(esc(date(e.timestamp))), viewValue(esc(e.seq)), viewValue(esc(label)), viewValue(e.subjectId?renderViewTemplate("inspector/event-html-span", [viewValue(esc(e.subjectId))]):''), viewValue(e.payload?renderViewTemplate("inspector/event-html-details", [viewValue(esc(e.seq)), viewValue(esc(pretty(e.payload)))]):'')])}

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
  if(inspectorTab==='outputs'){out.innerHTML=renderViewTemplate("inspector/out-inner-html-kv", [viewValue(esc((act.scopePath||[]).join(' / ')||'root')), viewValue(esc(act.occurrence)), viewValue(esc(act.state)), viewValue(act.queueReason?renderViewTemplate("inspector/render-inspector-wb-dt", [viewValue(esc(act.queueReason))]):""), viewValue(outputMarkup(act.outputs))]);return}
  out.innerHTML=renderPlaceholder("Loading…", "inspector-placeholder");
  const id=selectedActivation,tab=inspectorTab,run=current,request=++inspectorRequest;
  Promise.all([inspectorTab==='logs'?api(`/v1/runs/${encodeURIComponent(current)}/logs?activationId=${encodeURIComponent(id)}`):Promise.resolve(null),inspectorTab==='attempts'?api(`/v1/runs/${encodeURIComponent(current)}/attempts?activationId=${encodeURIComponent(id)}`):Promise.resolve(null)]).then(([l,a])=>{if(request!==inspectorRequest||id!==selectedActivation||tab!==inspectorTab||run!==current||!out.isConnected)return;if(inspectorTab==='logs'){const logs=l.logs||[];const decoded=logs.length?logs.map(x=>{let data=x.data;try{data=new TextDecoder().decode(Uint8Array.from(atob(data),c=>c.charCodeAt(0)))}catch{}return {ts:date(x.timestamp),stream:x.stream,text:data}}).filter(x=>x):[];if(!decoded.length){out.innerHTML=renderPlaceholder("No logs.", "inspector-placeholder");}else{let allText=decoded.map(x=>x.text).join('\n');out.innerHTML=renderViewTemplate("inspector/out-inner-html-data-tools")+decoded.map(x=>renderViewTemplate("inspector/out-inner-html-h3", [viewValue(esc(x.ts)), viewValue(esc(x.stream)), viewValue(esc(x.text))])).join('');out._copyLogsText=allText}}else{const attempts=a.attempts||[];out.innerHTML=attempts.length?renderViewTemplate("inspector/out-inner-html-tablebox", [viewValue(attempts.map(x=>renderViewTemplate("shared/table-row-five", [viewValue(esc(x.retryNumber)), viewValue(esc(x.executor)), viewValue(esc(x.state)), viewValue(esc(date(x.startedAt))), viewValue(esc(date(x.finishedAt)))])).join(''))]):renderPlaceholder("No attempts.", "inspector-placeholder")}}).catch(e=>{if(request!==inspectorRequest||id!==selectedActivation||run!==current)return;if(out.isConnected)out.innerHTML=renderViewTemplate("inspector/out-inner-html-p", [viewValue(esc(e.message))])})
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
  document.querySelectorAll<UIElement>('[data-act]').forEach(b=>{const on=b.dataset.act===id;b.classList.toggle('selected',on);b.setAttribute('aria-pressed',String(on))});
  renderInspector();return act;
 }catch(err){if(current===run&&selectedActivation===id)message(err.message,true);return null;}
}
document.addEventListener('click',event=>{const button=(event.target as UIElement).closest<UIElement>('[data-attempt-page]');if(button&&!button.disabled&&selectedActivation)loadActivation(selectedActivation,Math.max(0,attemptOffset+Number(button.dataset.attemptPage)*50));});
async function loadOlder(){const id=current;try{const d=await api(`/v1/runs/${encodeURIComponent(current)}/events?after=${after}&limit=50`);if(id!==current)return;after=d.nextAfter||after;hasMore=!!d.hasMore;const list=$('#timeline');if(d.events?.length)list.insertAdjacentHTML('beforeend',d.events.map(eventHtml).join(''));if($('#moreEvents'))$('#moreEvents').disabled=!hasMore}catch(e){message(e.message,true)}}
async function sendSignal(){const name=$('#signalName')?.value.trim(),status=$('#signalStatus');if(!name){if(status){status.className='inline-status error';status.textContent='Enter a signal name.'}return}let payload;try{payload=JSON.parse($('#signalPayload')?.value||'{}');if(!payload||typeof payload!=='object'||Array.isArray(payload))throw Error()}catch{if(status){status.className='inline-status error';status.textContent='Payload must be a JSON object.'}return}try{await api(`/v1/runs/${encodeURIComponent(current)}/signals/${encodeURIComponent(name)}`,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)});if(status){status.textContent=`Signal "${name}" sent.`;status.className='inline-status ok'}}catch(e){if(status){status.className='inline-status error';status.textContent=e.message}}}

// Simple JSON Schema contracts get typed controls. Complex contracts retain a
// JSON editor and show their full schema; the coordinator validates all input.
function requestFields(schema, path=[], required=true) {
  if(schema?.oneOf || schema?.anyOf || schema?.allOf || schema?.$ref)schema={title:schema.title,description:schema.description};
  // JSON Schema requires a property's presence, not a nonempty string.
  const attrs=renderViewTemplate("inspector/attrs-fragment", [viewValue(esc(JSON.stringify(path))), viewValue(required), viewValue(required && (schema?.type!=='string' || Array.isArray(schema?.enum))?'required':'')]);
  const label=esc(schema?.title||path[path.length-1]||'Response');
  const description=schema?.description?renderSmallContent(schema.description):'';
  if(schema?.type==='object' && schema.properties && required && !Array.isArray(schema.enum)){
    return renderViewTemplate("inspector/request-fields-fieldset", [viewValue(label), viewValue(Object.entries(schema.properties).map(([key,prop])=>requestFields(prop,[...path,key],(schema.required||[]).includes(key))).join(''))]);
  }
  let control;
  if(Array.isArray(schema?.enum))control=renderViewTemplate("inspector/control-select", [viewValue(attrs), viewValue(schema.enum.map(value=>renderViewTemplate("shared/select-option", [viewValue(esc(JSON.stringify(value))), viewValue(esc(JSON.stringify(value)))])).join(''))]);
  else if(schema?.type==='boolean')control=renderViewTemplate("inspector/control-select-2", [viewValue(attrs)]);
  else if(schema?.type==='string')control=renderViewTemplate("inspector/control-input", [viewValue(attrs), viewValue(schema.minLength!==undefined?renderViewTemplate("inspector/control-fragment", [viewValue(esc(schema.minLength))]):''), viewValue(schema.maxLength!==undefined?renderViewTemplate("inspector/control-fragment-2", [viewValue(esc(schema.maxLength))]):'')]);
  else if(['number','integer'].includes(schema?.type))control=renderViewTemplate("inspector/control-input-2", [viewValue(attrs), viewValue(schema.type==='integer'?'1':'any'), viewValue(schema.minimum!==undefined?renderViewTemplate("inspector/control-fragment-3", [viewValue(esc(schema.minimum))]):''), viewValue(schema.maximum!==undefined?renderViewTemplate("inspector/control-fragment-4", [viewValue(esc(schema.maximum))]):'')]);
  else control=renderViewTemplate("inspector/control-textarea", [viewValue(attrs)]);
  return renderViewTemplate("inspector/request-fields-input-request-field", [viewValue(label), viewValue(required?' *':''), viewValue(control), viewValue(description)]);
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
    wbInputRequestSummary=requests;
    if(typeof renderWbActivity==='function'&&typeof wbCurrentRunSummary!=='undefined'&&wbCurrentRun===runId&&typeof activationData!=='undefined')renderWbActivity(wbCurrentRunSummary,activationData);
    if(container._requestSignature===signature)return;
    const existingForms=new Map([...container.querySelectorAll<UIElement>('form[data-input-request]')].map(form=>[form.dataset.inputRequest,form]));
    container._requestSignature=signature;
    panel.hidden=!requests.length;
    container.innerHTML=requests.map(req=>renderViewTemplate("inspector/container-inner-html-subtle", [viewValue(esc(req.name)), viewValue(esc(req.state)), viewValue(esc(req.requestId)), viewValue(esc(req.activationId)), viewValue(req.expiresAt?` · Expires ${esc(req.expiresAt)}`:''), viewValue(req.permissions?.length?` · Requires ${esc(req.permissions.join(', '))}`:''), viewValue(req.state==='pending'?renderViewTemplate("inspector/container-inner-html-btn", [viewValue(esc(req.requestId)), viewValue(requestFields(req.schema)), viewValue(esc(JSON.stringify(req.schema??{},null,2)))]):'')])).join('');
    container.querySelectorAll<UIElement>('form[data-input-request]').forEach(form=>{
      const definition=JSON.stringify([runId,requests.find(req=>req.requestId===form.dataset.inputRequest)]);
      const existing=existingForms.get(form.dataset.inputRequest);
      if(existing?._requestDefinition===definition){form.replaceWith(existing);return}
      form._requestDefinition=definition;
      const key=crypto.randomUUID();
      form.onsubmit=async event=>{
        event.preventDefault();
        const status=form.querySelector<UIElement>('[role="status"]'),button=form.querySelector<UIElement>('button');
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
 return renderViewTemplate("inspector/worker-capability-markup-worker-capability-chip", [viewValue(encodeURIComponent(workerID)), viewValue(encodeURIComponent(cap.name||'')), viewValue(encodeURIComponent(cap.version||'')), viewValue(readiness), viewValue(esc(cap.name||'Unnamed capability')), viewValue(label), viewValue(esc(cap.name||'unnamed capability')), viewValue(label), viewValue(esc(cap.name||'Unnamed capability'))]);
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
 const features=Object.entries(contract.capabilities||{}).filter(([,enabled])=>enabled===true).map(([name])=>renderViewTemplate("inspector/features-worker-feature", [viewValue(esc(featureLabels[name]||title(name)))])).join('');
 return renderViewTemplate("inspector/worker-capability-detail-markup-worker-capability", [viewValue(esc(workerID)), viewValue(esc(cap.name||'unknown')), viewValue(esc(cap.version||'')), viewValue(esc(cap.name||'Unnamed capability')), viewValue(cap.version?`Version ${esc(cap.version)}`:'Version unavailable'), viewValue(failed?` · ${failed} ${failed===1?'check needs':'checks need'} attention`:''), viewValue(readiness), viewValue(label), viewValue(features||renderViewTemplate("inspector/worker-capability-detail-markup-subtle")), viewValue(esc(contract.contractVersion||'Unavailable')), viewValue(contract.environment?.digest?renderViewTemplate("inspector/worker-capability-detail-markup-div", [viewValue(esc(contract.environment.digest)), viewValue(esc(contract.environment.digest))]):''), viewValue(checks.length?renderViewTemplate("inspector/worker-capability-detail-markup-worker-readiness-checks", [viewValue(checks.map(check=>{const state=check.passed===true?'passed':check.passed===false?'failed':'unknown';return renderViewTemplate("inspector/worker-capability-detail-markup-worker-readiness-check", [viewValue(state), viewValue(state==='passed'?'Passed':state==='failed'?'Failed':'Not reported'), viewValue(state==='passed'?'✓':state==='failed'?'!':'—'), viewValue(esc(title(check.name||'Check'))), viewValue(esc(check.message||'No details reported.'))])}).join(''))]):renderPlaceholder("No readiness check details reported.", "subtle worker-check-empty"))]);
}
async function loadWorkers(){
  try{
    const d=await api('/v1/workers');
    const roster=d.registeredWorkers||[];
    const assignments=d.workers||[];
    const busy=new Set(assignments.filter(x=>x.state==='active').map(x=>x.workerId));
    const rosterEl=$('#workerRoster');
    if(rosterEl){
      rosterEl.innerHTML=roster.length?renderViewTemplate("inspector/roster-el-inner-html-tablebox", [viewValue(roster.map(x=>{const anchor=encodeURIComponent(x.workerId),id=esc(x.workerId),status=x.draining?'draining':busy.has(x.workerId)?'busy':'idle',caps=(x.capabilities||[]).map(cap=>workerCapabilityMarkup(cap,x.workerId)).filter(Boolean).join(', ')||'—';return renderViewTemplate("inspector/roster-el-inner-html-primary-cell", [viewValue(anchor), viewValue(id), viewValue(Object.keys(x.labels||{}).length?renderViewTemplate("inspector/roster-el-inner-html-details", [viewValue(esc(pretty(x.labels)))]):''), viewValue(status==='busy'?'running':'queued'), viewValue(status), viewValue(esc(x.capacity)), viewValue(caps), viewValue(esc(date(x.lastSeen)))])}).join(''))]):renderEmptyState("No workers available", "No workers have heartbeated within the coordinator window. Start a worker and register it with the coordinator.");
    }
    const workerLink=id=>renderViewTemplate("inspector/worker-link-a", [viewValue(encodeURIComponent(id)), viewValue(esc(id))]);
    $('#workerList').innerHTML=assignments.length?renderViewTemplate("inspector/worker-list-inner-html-tablebox", [viewValue(assignments.map(x=>renderViewTemplate("inspector/worker-list-inner-html-primary-cell", [viewValue(workerLink(x.workerId)), viewValue(encodeURIComponent(x.runId)), viewValue(esc(x.runId)), viewValue(esc(x.activationId)), viewValue(esc(x.generation)), viewValue(esc(date(x.leaseUntil))), viewValue(esc(x.state)), viewValue(esc(x.state))])).join(''))]):renderEmptyState("No active assignments", "The coordinator is not reporting any active worker assignments right now.");
  }catch(e){
    if(e.name==='AbortError')return;
    const err=renderNotice(`Unable to load workers: ${e.message??''}`, {error:true});
    if($('#workerRoster'))$('#workerRoster').innerHTML=err;
    $('#workerList').innerHTML=err;
  }
}

