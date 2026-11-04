/* ── workbench ─────────────────────────────────── */
let activationDisplayNumbers = new Map();
// each() receives a compiler ID such as each__0; the suffix is not a task name.
function nodeDisplayName(nodeId){return /^each__\d+$/.test(nodeId||'')?'each':nodeId}
function stateClass(s){return{succeeded:'succeeded',failed:'failed',running:'running',waiting:'waiting',paused:'paused',queued:'queued',cancelled:'cancelled'}[s]||''}
function checkIcon(s){if(s==='succeeded')return `<span class="node-card-check succeeded">✓</span>`;if(s==='failed')return `<span class="node-card-check failed">✕</span>`;if(s==='running')return `<span class="node-card-check running">⟳</span>`;return ''}

// sumRunTokenUsage aggregates token counts across all attempts (including
// retries) in the run. Returns null when no attempt has provider-reported
// usage — callers must render that as "Unavailable", never as zero.
function sumRunTokenUsage(attempts){
  if(!attempts||!attempts.length)return null;
  let anyReported=false,input=0,output=0,cached=0;
  for(const a of attempts){
    if(a.usage==null)continue; // provider did not report
    anyReported=true;
    input+=a.usage.input||0;
    output+=a.usage.output||0;
    cached+=a.usage.cached||0;
  }
  return anyReported?{input,output,cached}:null;
}

// fmtTokens formats a token count with a K suffix for readability.
function fmtTokens(n){if(n>=1000)return `${(n/1000).toFixed(1)}K`;return String(n)}

function sumExecutionTime(attempts){if(!attempts?.length)return null;let total=0,known=false;for(const a of attempts){if(a.executionDurationMs==null){if(a.state!=='running')return null;continue;}known=true;total+=a.executionDurationMs}return known?total:null}
function formatElapsed(start,end){if(!start||!end)return '—';const ms=Math.max(0,new Date(end)-new Date(start));if(ms<1000)return `${ms} ms`;if(ms<60000)return `${(ms/1000).toFixed(1)} s`;return `${Math.floor(ms/60000)}m ${Math.floor(ms%60000/1000)}s`}

function buildWbStats(r,acts){
  const total=acts.length,done=acts.filter(a=>a.state==='succeeded').length;
  const failed=acts.filter(a=>a.state==='failed').length;
  const running=acts.filter(a=>a.state==='running').length;
  const attempts=typeof runAttempts!=='undefined'?runAttempts:[];
  const usage=sumRunTokenUsage(attempts);
  const executionTime=sumExecutionTime(attempts);
  const elapsedEnd=!['succeeded','failed','cancelled'].includes(r.state)?new Date().toISOString():r.updatedAt;
  const tokIn =attempts.length?usage!=null?fmtTokens(usage.input):'Unavailable':'—';
  const tokOut=attempts.length?usage!=null?fmtTokens(usage.output):'Unavailable':'—';
  const tokCached=attempts.length?usage!=null?fmtTokens(usage.cached):'Unavailable':'—';
  const tokenBlock=`<div class="stat"><div class="stat-token-rows" aria-label="Token usage">`+
    `<div class="stat-token-row"><span class="stat-token-label">In</span><span class="stat-token-val">${esc(tokIn)}</span></div>`+
    `<div class="stat-token-row"><span class="stat-token-label">Out</span><span class="stat-token-val">${esc(tokOut)}</span></div>`+
    `<div class="stat-token-row"><span class="stat-token-label">Cached</span><span class="stat-token-val">${esc(tokCached)}</span></div>`+
    `</div></div>`;
  const stats=[
    {val:`${done}/${total}`,label:'Node executions',cls:''},
    {val:failed>0?String(failed):'—',label:'Failed',cls:failed?'red':''},
    {val:running>0?String(running):'—',label:'Running',cls:running?'amber':''},
    {val:r.state?(r.state[0].toUpperCase()+r.state.slice(1)):'—',label:'Run state',cls:r.state==='succeeded'?'green':r.state==='failed'?'red':r.state==='running'?'amber':''},
    {val:formatElapsed(r.createdAt,elapsedEnd),label:'Elapsed time',cls:''},
    {val:executionTime==null?'Unavailable':formatElapsed(new Date(0),new Date(executionTime)),label:'Total execution time',cls:''},
  ];
  return stats.map(s=>`<div class="stat"><div class="stat-val ${s.cls}">${esc(s.val)}</div><div class="stat-label">${esc(s.label)}</div></div>`).join('')+tokenBlock;
}

function graphMarkup(acts,scopes){
  activationDisplayNumbers.clear();
  function frameGraph(content,complete=false){
    const started=!!acts?.length;
    const edge=pending=>`<div class="graph-connector${pending?' pending':''}" aria-hidden="true"><div class="connector-line"></div></div>`;
    return `<div class="graph-col"><div class="node-start-gate${started?'':' pending'}">Start</div></div>${edge(!started)}${content}${edge(!complete)}<div class="graph-col"><div class="node-done-gate${complete?'':' pending'}" aria-label="Done${complete?'':' (pending)'}">Done</div></div>`;
  }
  if(!scopes)scopes=[];
  if(!acts||!acts.length){
    return frameGraph(`<div class="graph-scope-empty">No activations materialized yet.</div>`);
  }
  // Rebuild the recursive execution scopes from the API's scope paths. Each
  // path segment belongs beneath the control node that created that scope.
  const root={path:[],children:[],activations:[]},byPath=new Map([[JSON.stringify([]),root]]);
  const allPaths=[...(scopes||[]).map(s=>s.path||[]),...acts.map(a=>a.scopePath||[])];
  allPaths.flatMap(path=>path.map((_,index)=>path.slice(0,index+1))).sort((a,b)=>a.length-b.length).forEach(path=>{
    const key=JSON.stringify(path);if(byPath.has(key))return;
    const parentPath=path.slice(0,-1),parent=byPath.get(JSON.stringify(parentPath));
    if(!parent)return;
    const segment=path[path.length-1],parts=segment.split(':'),kind=parts[0],owner=parts[1]||'';
    let label=kind==='map'?(owner==='features'?'Feature scope':'Map item scope'):kind==='branch'?`Branch · ${owner}`:kind==='loop'?`Loop · ${owner}`:`Scope · ${owner||segment}`;
    let detail=kind==='map'?parts.slice(2).join(':'):kind==='branch'?(parts[2]==='true'?'condition true':'condition false'):kind==='loop'?`iteration ${parts.slice(2).join(':')}`:'';
    const child={path,children:[],activations:[],owner,label,detail,kind};parent.children.push(child);byPath.set(key,child);
  });
  acts.forEach(a=>{const scope=byPath.get(JSON.stringify(a.scopePath||[]))||root;scope.activations.push(a)});
  for(const scope of byPath.values())scope.activations.sort((a,b)=>(a.occurrence||0)-(b.occurrence||0));
  const counter={value:0};
  const knownKinds=new Map();
  for(const scope of byPath.values())scope.children.forEach(child=>knownKinds.set(child.owner,child.kind));
  function scopeStats(scope){
    const nestedActs=[];
    function collect(child){nestedActs.push(...child.activations);child.children.forEach(collect)}
    collect(scope);
    const counts={};nestedActs.forEach(a=>counts[a.state||'queued']=(counts[a.state||'queued']||0)+1);
    const state=['failed','running','paused','waiting','queued','cancelled'].find(key=>counts[key])||(nestedActs.length?'succeeded':'waiting');
    return {count:nestedActs.length,state,counts};
  }
  function statusBadge(state,label=state){
    return `<span class="graph-status ${stateClass(state)}">${esc(label)}</span>`;
  }
  function statusSummary(stats){
    return `<span class="graph-status-summary">${['failed','running','succeeded','paused','waiting','queued','cancelled'].filter(state=>stats.counts[state]).map(state=>statusBadge(state,`${stats.counts[state]} ${state}`)).join('')||statusBadge('waiting')}</span>`;
  }
  function renderBranch(group){
    const stats=scopeStats({activations:[],children:group});
    return `<details class="graph-branch-set" data-preserve-key="branch:${esc(JSON.stringify(group[0].path.slice(0,-1)))}:${esc(group[0].owner)}"><summary class="graph-control-heading"><strong>Branch · ${esc(group[0].owner)}</strong>${statusSummary(stats)}<span class="graph-disclosure-hint">Paths</span></summary><div class="graph-branch-routes">${['true','false'].map(condition=>{
      const selected=group.filter(child=>child.detail===`condition ${condition}`);
      return `<div class="graph-branch-route ${selected.length?'taken':'untaken'}"><div class="graph-branch-path"><div class="graph-route-heading"><span class="graph-route-condition">${condition.toUpperCase()}</span><span>${selected.length?'Taken':'Not taken'}</span></div>${selected.length?selected.map(child=>renderScope(child,false,true)).join(''):'<div class="graph-route-skip">Bypass</div>'}</div></div>`;
    }).join('')}</div><div class="graph-control-footer">Paths rejoin before continuing</div></details>`;
  }
  function renderNestedScopes(children){
    if(!children.length)return '';
    const groups=new Map();
    children.forEach(child=>{const key=JSON.stringify([child.kind,child.owner]);const group=groups.get(key)||[];group.push(child);groups.set(key,group)});
    return `<div class="graph-nested-scopes">${[...groups.values()].map(group=>{
      const first=group[0];
      if(first.kind==='branch')return renderBranch(group);
      if(first.kind!=='map'&&first.kind!=='loop')return group.map(child=>renderScope(child)).join('');
      const loop=first.kind==='loop';
      if(loop)group.sort((a,b)=>a.detail.localeCompare(b.detail,undefined,{numeric:true}));
      const stats=scopeStats({activations:[],children:group});
      const title=`${loop?'Loop':'Map'} · ${nodeDisplayName(first.owner)}`;
      return `<details class="graph-map-set ${loop?'graph-loop-set':''}" data-preserve-key="control:${esc(JSON.stringify(first.path.slice(0,-1)))}:${esc(first.kind)}:${esc(first.owner)}"><summary class="graph-control-heading"><strong>${esc(title)}</strong><span class="graph-control-count">${group.length} ${loop?(group.length===1?'iteration':'iterations'):(group.length===1?'item':'items')}</span>${statusSummary(stats)}</summary><div class="graph-map-grid">${group.map((child,index)=>{
        const childStats=scopeStats(child);
        const label=loop?child.detail:`Item · ${child.detail||'item'}`;
        const hasNext=index<group.length-1;
        const result=[...child.activations].reverse().find(a=>typeof a.outputs?.passed==='boolean')?.outputs.passed;
        const outcome=loop?(hasNext?`Repeat: ${group[index+1].detail}`:result===true?'Condition met · exit loop':result===false?'Condition not met':'Latest iteration'):'';
        return `<details class="graph-map-item" data-preserve-key="${loop?'loop':'map'}:${esc(JSON.stringify(child.path))}"><summary><span class="graph-item-label">${esc(label)}</span>${statusSummary(childStats)}${outcome?`<span class="graph-iteration-outcome ${hasNext?'repeat':result===true?'exit':''}">${esc(outcome)}</span>`:''}</summary><div class="graph-map-item-content">${renderScope(child,false,true)}</div>${loop?`<div class="graph-iteration-transition ${hasNext?'repeat':''}">${esc(outcome)}</div>`:''}</details>`;
      }).join('')}</div>${loop?'<div class="graph-control-footer">Repeat while condition is unmet <span>Condition met: continue</span></div>':''}</details>`;
    }).join('')}</div>`;
  }
  function renderScope(scope,isRoot=false,omitHeader=false){
    const childrenByOwner=new Map();
    scope.children.forEach(child=>{const list=childrenByOwner.get(child.owner)||[];list.push(child);childrenByOwner.set(child.owner,list)});
    const renderedOwners=new Set();let html='';
    scope.activations.forEach((a,i)=>{
      const number=++counter.value,sc=stateClass(a.state);
      activationDisplayNumbers.set(a.id, number);
      const nested=childrenByOwner.get(a.nodeId)||[];
      const nodeTypeParts=(a.nodeId||'').split('.');
      let nodeType=nodeTypeParts.length>1?nodeTypeParts[0]:(nested.length&&nested[0].kind?nested[0].kind:knownKinds.get(a.nodeId)||'node');
      const nodeName=nodeDisplayName(nodeTypeParts.slice(-1)[0]||a.nodeId);
      const model=a.executorHint||a.executor||'';
      const skill=a.skill||'';
      html+=`<div class="graph-col${nested.length?' graph-control-stage':''}">
        <button class="node-card ${sc} ${selectedActivation===a.id?'selected':''}" data-act="${esc(a.id)}" aria-pressed="${selectedActivation===a.id}">
          ${checkIcon(a.state)}
          <div class="node-type"><span class="node-type-num">${String(number).padStart(2,'0')}${nodeType?' · '+nodeType.toUpperCase():''}</span></div>
          <div class="node-name">${esc(nodeName)}</div>
          <div class="node-execution-state">${statusBadge(a.state||'queued')}</div>
          <div class="node-meta">${model?`<span>${esc(model)}</span>`:''} ${skill?`<span>skill: ${esc(skill)}</span>`:''}</div>
        </button>
        `;
      if(nested.length){renderedOwners.add(a.nodeId);html+=renderNestedScopes(nested)}
      html+='</div>';
      if(i<scope.activations.length-1){
        html+=`<div class="graph-connector"><div class="connector-line"></div>${a.state==='succeeded'?`<span class="connector-label pass">PASS</span>`:''}${a.state==='failed'?`<span class="connector-label fail">FAIL →</span>`:''}</div>`;
      }
    });
    const orphaned=scope.children.filter(child=>!renderedOwners.has(child.owner));
    if(orphaned.length)html+=renderNestedScopes(orphaned);
    if(!html)html='<div class="graph-scope-empty">No activations in this scope.</div>';
    if(isRoot){
      const allDone=acts.every(a=>a.state==='succeeded');
      return frameGraph(html,allDone);
    }
    if(omitHeader)return `<div class="graph-scope-flow">${html}</div>`;
    return `<details class="graph-scope" data-preserve-key="scope:${esc(JSON.stringify(scope.path))}"><summary class="graph-scope-heading">${esc(scope.label)}${scope.detail?`<small>${esc(scope.detail)}</small>`:''}</summary><div class="graph-scope-flow">${html}</div></details>`;
  }
  return renderScope(root,true);
}

function buildWbEvents(events){
  if(!events||!events.length)return '<div style="color:var(--muted);font-size:11px;padding:4px 0">No events recorded.</div>';
  // Cap at 100 most-recent events to prevent DOM bloat on long-running workflows.
  return events.slice().reverse().slice(0,100).map(e=>{
    const t=e.timestamp?new Date(e.timestamp).toLocaleTimeString('en',{hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false}):'—';
    const label=eventLabel(e.type);
    return `<div class="event-row"><span class="event-time">${esc(t)}</span><span class="event-text"><strong>${esc(label)}</strong>${e.subjectId?` <span class="subtle">${esc(e.subjectId)}</span>`:''}</span></div>`;
  }).join('');
}

function buildWbArtifacts(acts){
  const withOutputs=acts.filter(a=>a.outputs&&typeof a.outputs==='object'&&Object.keys(a.outputs).length>0);
  if(!withOutputs.length)return '<div style="color:var(--muted);font-size:11px;padding:4px 0">No artifacts recorded.</div>';
  return withOutputs.flatMap(a=>
    Object.entries(a.outputs).map(([name,val])=>{
      const isArtifact=val&&typeof val==='object'&&('mime'in val||'path'in val||'data'in val||'digest'in val);
      const meta=isArtifact?`${val.mime||''} · ${val.path||'artifact'}`:valueType(val);
      return `<div class="artifact-row">
        <div class="artifact-icon">${isArtifact?'📄':'{ }'}</div>
        <div><div class="artifact-name">${esc(name)}</div><div class="artifact-meta">${esc(a.nodeId)} · ${esc(meta)}</div></div>
        <button class="artifact-view" data-act="${esc(a.id)}" data-artname="${esc(name)}">View</button>
      </div>`;
    })
  ).join('');
}

// wbCurrentRunState tracks the state of the run currently open in the
// workbench. Read by inspector.js to decide whether to show the retry
// actions panel; must be set before renderWorkbench calls renderInspectorWb.
let wbCurrentRunState='';
function renderWorkbench(r,g,events){
  const acts=g.activations||[];
  activationData=acts;
  wbCurrentRun=r.id;
  wbCurrentRunState=r.state;

  $('#workbenchView').hidden=false;
  $('#runDetail').hidden=true;
  document.querySelector('.content')?.classList.add('content--wb');

  $('#wbTitle').textContent=r.workflowName||'Workflow run';
  $('#wbSubtitle').textContent=`${r.id}  ·  ${r.createdAt?`Created ${date(r.createdAt)}`:''}`;

  const sb=$('#wbStatusBadge');
  sb.className=`status-badge ${stateClass(r.state)||''}`;
  sb.textContent=({succeeded:'✓ Succeeded',failed:'✗ Failed',running:'⟳ Running',waiting:'Waiting',paused:'Ⅱ Paused',cancelled:'Cancelled'}[r.state]||r.state||'—');

  $('#wbStats').innerHTML=buildWbStats(r,acts);
  replaceHTMLPreservingDetails($('#graphFlow'),graphMarkup(acts,g.scopes||[]));
  replaceHTMLPreservingDetails($('#wbEventList'),buildWbEvents(events));
  $('#wbArtifactList').innerHTML=buildWbArtifacts(acts);
  $('#inspActivationCount').textContent=`${acts.length} nodes`;

  // timeline
  const tlEl=$('#wbTimeline');
  replaceHTMLPreservingDetails(tlEl,events.length?events.map(eventHtml).join(''):'<li class="subtle" style="font-size:11px;color:var(--muted)">No events.</li>');
  wbAfter=events[events.length-1]?.seq||0;
  const moreBtn=$('#moreEventsWb');
  if(moreBtn)moreBtn.disabled=!wbHasMore;

  // run data
  currentRunInput=r.input;currentRunOutput=r.output;
  replaceHTMLPreservingDetails($('#runInputInspector'),inputMarkup(r.input));
  replaceHTMLPreservingDetails($('#runOutputInspector'),outputMarkup(r.output,'run-output'));

  // node click handlers
  document.querySelectorAll('[data-act]').forEach(b=>b.onclick=()=>loadActivation(b.dataset.act));

  // re-attach inspector click
  $('#runDetail').onclick=null;
  $('#nodeInspector').onclick=inspectorAction;

  if(selectedActivation)renderInspector();
}

function showWbTab(tab){
  for(const t of ['graph','events','data']){
    const el=$(`#wbTab${t[0].toUpperCase()+t.slice(1)}`);
    if(el)el.hidden=t!==tab;
  }
  document.querySelectorAll('[data-wb-tab]').forEach(b=>{b.setAttribute('aria-selected',String(b.dataset.wbTab===tab))});
}

async function openRun(id){
  const wb=$('#workbenchView');
  if(wb)wb.hidden=true;
  $('#runDetail').hidden=false;
  document.querySelector('.content')?.classList.remove('content--wb');
  $('#runDetail').innerHTML='<div class="loading">Loading run…</div>';
  selectedActivation=null;
  try{
    const [r,g,e]=await Promise.all([
      api(`/v1/runs/${encodeURIComponent(id)}`),
      api(`/v1/runs/${encodeURIComponent(id)}/graph`),
      api(`/v1/runs/${encodeURIComponent(id)}/events?after=0&limit=50`)
    ]);
    if(current!==id)return;
    after=e.nextAfter||0;
    wbHasMore=!!e.hasMore;
    activationData=g.activations||[];
    // Use workbench layout for all runs
    renderWorkbench(r,g,e.events||[]);
  }catch(err){
    if(wb)wb.hidden=true;
    $('#runDetail').hidden=false;
    $('#runDetail').innerHTML=`<div class="page"><div class="notice error" role="alert">Unable to load run: ${esc(err.message)} <a href="#runs" style="color:var(--teal)">← Back to runs</a></div></div>`;
  }
}

