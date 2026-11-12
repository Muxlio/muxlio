/* ── workbench ─────────────────────────────────── */
let activationDisplayNumbers = new Map();
// each() receives a compiler ID such as each__0; the suffix is not a task name.
function nodeDisplayName(nodeId){return /^each__\d+$/.test(nodeId||'')?'each':nodeId}
function stateClass(s){return{succeeded:'succeeded',failed:'failed',running:'running',waiting:'waiting',paused:'paused',queued:'queued',cancelled:'cancelled'}[s]||''}
function checkIcon(s){if(s==='succeeded')return renderViewTemplate("workbench/check-icon-node-card-check");if(s==='failed')return renderViewTemplate("workbench/check-icon-node-card-check-2");if(s==='running')return renderViewTemplate("workbench/check-icon-node-card-check-3");return ''}

// Prefer worker measurements; older attempts retain coordinator timestamps.
// Sum attempt durations individually so parallel work and retries are included.
function sumExecutionTime(attempts){
  if(!attempts?.length)return null;
  let total=0,known=false;
  for(const a of attempts){
    if(a.state==='queued')continue;
    let ms=a.executionDurationMs;
    if(typeof ms!=='number'||!Number.isFinite(ms)||ms<0){
      if(a.state==='running')continue;
      const start=Date.parse(a.startedAt),finish=Date.parse(a.finishedAt);
      if(!Number.isFinite(start)||!Number.isFinite(finish)||start<=0||finish<start)return null;
      ms=finish-start;
    }
    known=true;total+=ms;
  }
  return known?total:null;
}

let wbActivityAttempts=[],wbInputRequestSummary=[],wbActivitySignature='';
function latestAttemptForActivation(attempts,activationId){
  return (attempts||[]).filter(a=>a.activationId===activationId&&a.state!=='queued').sort((a,b)=>(b.retryNumber||0)-(a.retryNumber||0))[0]||null;
}
function activationActivityElapsed(act,attempts,nowIso){
  const attempt=latestAttemptForActivation(attempts,act.id);
  if(attempt?.startedAt){
    const stillActive=['running','waiting','paused'].includes(attempt.state);
    const end=stillActive?nowIso:(attempt.finishedAt||null);
    if(end)return formatElapsed(attempt.startedAt,end);
  }
  return null;
}
function materializedStateCounts(acts){
  const counts={};for(const act of acts||[])counts[act.state||'queued']=(counts[act.state||'queued']||0)+1;return counts;
}
function wbActivityStripSummary(items){
  let running=0,waiting=0,paused=0,attention=0;
  const runStatus=(items||[]).find(item=>item.scope==='run')?.title;
  for(const item of items||[]){
    if(item.scope==='run')continue;
    if(item.tone==='attention')attention++;
    else if(item.tone==='progress')running++;
    else if(item.tone==='paused')paused++;
    else if(item.tone==='waiting'||item.tone==='queued')waiting++;
  }
  const parts=runStatus?[runStatus]:[];
  if(running)parts.push(`${running} running`);
  if(waiting)parts.push(`${waiting} waiting`);
  if(paused)parts.push(`${paused} paused`);
  if(attention)parts.push(`${attention} ${attention===1?'needs':'need'} attention`);
  if(parts.length)return parts.join(' · ');
  return 'No actionable activity';
}
function wbActivityButton(label,attrs){return renderViewTemplate("workbench/wb-activity-button-btn", [viewValue(attrs), viewValue(esc(label))])}
function buildWbActivityMarkup(run,acts,attempts,inputRequests,nowIso=new Date().toISOString()){
  const overview=run.overview||{},states=overview.attemptStates||{},actsList=acts||[],attemptList=attempts||[],requests=inputRequests||[];
  const items=[];
  const runWait=['waiting','paused','created'].includes(run.state);
  if(runWait){
    const since=run.updatedAt||run.createdAt,elapsed=since?formatElapsed(since,nowIso):null;
  const runDetail={created:'Run created and waiting to start.',waiting:'Run is waiting for admission or capacity.',paused:'Run is paused.'}[run.state]||'Waiting for coordinator admission or worker capacity.';
    items.push({scope:'run',tone:run.state==='paused'?'paused':'waiting',title:`Run ${run.state}`,detail:runDetail,elapsed,timing:elapsed?`${run.state==='paused'?'Paused':'Waiting'} ${elapsed}`:'Timing unavailable',actions:`${run.state==='paused'?wbActivityButton('Resume run',renderViewTemplate("workbench/build-wb-activity-markup-fragment")):''}`});
  }
  if(run.error&&!runWait)items.push({tone:'attention',title:'Run failed',detail:run.error,actions:wbActivityButton('Inspect operations','data-open-operations')});
  const held=attemptList.filter(a=>a.awaitReconciliationReason);
  for(const attempt of held){
    const act=actsList.find(a=>a.id===attempt.activationId);
    const step=act?nodeDisplayName((act.nodeId||'').split('.').slice(-1)[0]||act.nodeId):'Step unavailable';
    const elapsed=attempt.startedAt?formatElapsed(attempt.startedAt,attempt.finishedAt||nowIso):null;
    items.push({tone:'attention',title:step,detail:attempt.awaitReconciliationReason||'External outcome uncertain.',elapsed,timing:elapsed?`Held ${elapsed}`:'Timing unavailable',actions:`${wbActivityButton('View node',renderViewTemplate("workbench/build-wb-activity-markup-fragment-2", [viewValue(esc(attempt.activationId))]))}${wbActivityButton('Resolve uncertain execution',renderViewTemplate("workbench/build-wb-activity-markup-fragment-3", [viewValue(esc(run.id)), viewValue(esc(attempt.id))]))}${wbActivityButton('Operations','data-open-operations')}`});
  }
  const heldCount=overview.heldAttempts||0;
  if(heldCount>held.length)items.push({tone:'attention',title:'Held uncertain effects',detail:`${heldCount-held.length} more attempts need reconciliation on other pages.`,actions:wbActivityButton('Inspect operations','data-open-operations')});
  const detached=states.detached||0;
  if(detached)items.push({tone:'attention',title:'Detached remote operations',detail:`${detached} remote operations are detached. Waiting does not occupy a worker slot.`,actions:wbActivityButton('Inspect operations','data-open-operations')});
  for(const req of requests.filter(r=>r.state==='pending')){
    items.push({tone:'attention',title:req.name||'Input request',detail:`Activation ${req.activationId||'unavailable'}${req.expiresAt?` · expires ${req.expiresAt}`:''}`,timing:'Awaiting response',actions:`${wbActivityButton('Respond',renderViewTemplate("workbench/build-wb-activity-markup-fragment-4", [viewValue(esc(req.requestId))]))}${req.activationId?wbActivityButton('View node',renderViewTemplate("workbench/build-wb-activity-markup-fragment-2", [viewValue(esc(req.activationId))])):''}`});
  }
  for(const act of actsList.filter(a=>a.state==='failed')){
    const elapsed=activationActivityElapsed(act,attemptList,nowIso);
    items.push({tone:'attention',title:nodeDisplayName((act.nodeId||'').split('.').slice(-1)[0]||act.nodeId),detail:act.error||act.queueReason||'Execution failed.',elapsed,timing:elapsed?`Failed after ${elapsed}`:'Timing unavailable',actions:`${wbActivityButton('View node',renderViewTemplate("workbench/build-wb-activity-markup-fragment-2", [viewValue(esc(act.id))]))}${wbActivityButton('Live transcript',renderViewTemplate("workbench/build-wb-activity-markup-fragment-5", [viewValue(esc(act.id))]))}`});
  }
  const inFlight=actsList.filter(a=>['running','waiting','paused'].includes(a.state));
  const inFlightIds=new Set(inFlight.map(a=>a.id));
  for(const act of inFlight){
    const elapsed=activationActivityElapsed(act,attemptList,nowIso);
    const detail=act.queueReason||(act.state==='running'?'Executing on a worker.':'Waiting to start.');
    items.push({tone:act.state==='running'?'progress':act.state==='paused'?'paused':'waiting',title:nodeDisplayName((act.nodeId||'').split('.').slice(-1)[0]||act.nodeId),detail,elapsed,timing:elapsed?`${act.state==='running'?'Running':act.state==='paused'?'Paused':'Waiting'} ${elapsed}`:'Timing unavailable',actions:`${wbActivityButton('View node',renderViewTemplate("workbench/build-wb-activity-markup-fragment-2", [viewValue(esc(act.id))]))}${act.state==='running'?wbActivityButton('Live transcript',renderViewTemplate("workbench/build-wb-activity-markup-fragment-5", [viewValue(esc(act.id))])):''}`});
  }
  for(const act of actsList.filter(a=>a.state==='queued'&&!inFlightIds.has(a.id))){
    items.push({tone:'queued',title:nodeDisplayName((act.nodeId||'').split('.').slice(-1)[0]||act.nodeId),detail:act.queueReason||'Queued for dispatch.',timing:act.queueReason?'Admission wait':'Timing unavailable',actions:wbActivityButton('View node',renderViewTemplate("workbench/build-wb-activity-markup-fragment-2", [viewValue(esc(act.id))]))});
  }
  const stripSummary=wbActivityStripSummary(items);
  const signature=JSON.stringify({run:run.state,error:run.error||'',strip:stripSummary,held:held.map(a=>a.id),detached,requests:requests.map(r=>[r.requestId,r.state]),items:items.map(i=>[i.title,i.detail,i.tone])});
  const terminal=['succeeded','failed','cancelled'].includes(run.state);
  const idle=terminal&&!items.length;
  const itemHtml=items.length?renderViewTemplate("workbench/item-html-wb-activity-list", [viewValue(items.map(item=>renderViewTemplate("workbench/item-html-wb-activity-item", [viewValue(esc(item.tone)), viewValue(esc(stateClass(item.tone==='attention'?'failed':item.tone==='progress'?'running':item.tone==='paused'?'paused':item.tone==='waiting'?'waiting':item.tone==='queued'?'queued':'waiting'))), viewValue(esc(item.tone==='attention'?'Needs attention':item.tone==='progress'?'Running':item.tone==='paused'?'Paused':item.tone==='waiting'?'Waiting':item.tone==='queued'?'Queued':'Waiting')), viewValue(esc(item.title)), viewValue(esc(item.detail)), viewValue(item.timing?renderViewTemplate("workbench/item-html-wb-activity-timing", [viewValue(esc(item.timing))]):''), viewValue(item.actions||'')])).join(''))]):renderViewTemplate("workbench/item-html-wb-activity-empty", [viewValue(idle?' is-complete':''), viewValue(idle?'✓':'·'), viewValue(idle?'No activations need attention.':'No activations materialized yet.'), viewValue(idle?renderViewTemplate("workbench/item-html-small"):'')]);
  const html=renderViewTemplate("workbench/html-wb-activity-details", [viewValue(esc(stripSummary)), viewValue(itemHtml)]);
  const announce=items.length?`Current activity. ${stripSummary.split(' · ').join('. ')}.`:(idle?`Run ${run.state}. ${stripSummary}.`:'Current activity. No actionable activity.');
  return {html,signature,announce,visible:!idle||!terminal||actsList.length>0};
}
function renderWbActivity(run,acts){
  const panel=(document.getElementById('wbActivity') as UIElement);if(!panel)return;

  if(!panel.dataset)(panel as {dataset: DOMStringMap}).dataset={};
  if(panel.dataset.runId!==run.id){panel.dataset.runId=run.id;panel._activitySignature='';wbActivitySignature='';}
  const attempts=typeof wbActivityAttempts!=='undefined'?wbActivityAttempts:[];
  const requests=typeof wbInputRequestSummary!=='undefined'?wbInputRequestSummary:[];
  const {html,signature,announce,visible}=buildWbActivityMarkup(run,acts,attempts,requests);
  panel.hidden=!visible;
  if(panel._activitySignature===signature)return;
  panel._activitySignature=signature;
  const announcer=(document.getElementById('wbActivityAnnounce') as UIElement);
  if(announce&&announcer&&announce!==wbActivitySignature){announcer.textContent=announce;wbActivitySignature=announce;}
  if(typeof replaceHTMLPreservingDetails==='function')replaceHTMLPreservingDetails(panel,html);else panel.innerHTML=html;
}
document.addEventListener('click',event=>{
  const transcript=(event.target as UIElement).closest<UIElement>('[data-wb-transcript]');
  if(transcript&&current){navigateTo(`/transcripts/${encodeURIComponent(current)}/${encodeURIComponent(transcript.dataset.wbTranscript)}`);return;}
  const focusAct=(event.target as UIElement).closest<UIElement>('[data-wb-focus-act]');
  if(focusAct){showWbTab('graph');loadActivation(focusAct.dataset.wbFocusAct);return;}
  const focusInput=(event.target as UIElement).closest<UIElement>('[data-wb-focus-input]');
  if(focusInput){
    const panel=(document.getElementById('inputRequestsPanel') as UIElement);panel?.scrollIntoView({block:'nearest'});
    const form=[...(document.querySelectorAll<UIElement>('form[data-input-request]')||[])].find(item=>item.dataset.inputRequest===focusInput.dataset.wbFocusInput);
    const field=form?.querySelector<UIElement>('input,select,textarea');
    field?.focus({preventScroll:true});
    return;
  }
  const runAction=(event.target as UIElement).closest<UIElement>('#wbActivity [data-run-action]');
  if(runAction){document.querySelector<UIElement>(`#runControls [data-run-action="${runAction.dataset.runAction}"]`)?.click();}
});
function buildWbStats(r,acts){
  const total=r.overview?.activations??acts.length,done=r.overview?.completed??acts.filter(a=>a.state==='succeeded').length;
  const attempts=typeof runAttempts!=='undefined'?runAttempts:[];
  const usage=r.overview?.accounting??r.accounting;
  const dimensions=usage?.tokenDimensions;
  const attemptCount=r.overview?.attempts??dimensions?.coverage?.attempts??attempts.length;
  const executionTime=r.overview?r.overview.executionDurationMs:sumExecutionTime(attempts);
  const elapsedEnd=!['succeeded','failed','cancelled'].includes(r.state)?new Date().toISOString():r.updatedAt;
  const stats=[
    {val:`${done}/${total}`,label:'Node executions',cls:''},
    {val:r.state?(r.state[0].toUpperCase()+r.state.slice(1)):'—',label:'Run state',cls:r.state==='succeeded'?'green':r.state==='failed'?'red':r.state==='running'?'amber':''},
    {val:formatElapsed(r.createdAt,elapsedEnd),label:'Elapsed time',cls:''},
    {val:executionTime==null?'Unavailable':formatElapsed(new Date(0),new Date(executionTime)),label:'Total execution time',cls:'',title:'Sum across execution attempts, including retries and parallel tasks. Uses worker durations when recorded; older attempts use coordinator start and finish times, which can include dispatch overhead.'},
  ];
  const progress=stats.map(s=>renderViewTemplate("workbench/progress-stat", [viewValue(s.title?renderViewTemplate("workbench/progress-fragment", [viewValue(esc(s.title))]):''), viewValue(s.cls), viewValue(esc(s.val)), viewValue(esc(s.label))])).join('');
  const presentation=buildUsagePresentation(usage||{},{
    attemptCount,
    runState:r.state,
    runError:r.error,
  });
  return progress+buildWorkbenchUsageMarkup(presentation);
}

function graphMarkup(acts,scopes,runComplete){
  activationDisplayNumbers.clear();
  function frameGraph(content,complete=false){
    const started=!!acts?.length;
    const edge=pending=>renderViewTemplate("workbench/edge-graph-connector", [viewValue(pending?' pending':'')]);
    return renderViewTemplate("workbench/frame-graph-graph-col", [viewValue(started?'':' pending'), viewValue(edge(!started)), viewValue(content), viewValue(edge(!complete)), viewValue(complete?'':' pending'), viewValue(complete?'':' (pending)')]);
  }
  if(!scopes)scopes=[];
  if(!acts||!acts.length){
    return frameGraph(renderViewTemplate("workbench/graph-markup-graph-scope-empty"));
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
    return renderViewTemplate("workbench/status-badge-graph-status", [viewValue(stateClass(state)), viewValue(esc(label))]);
  }
  function statusSummary(stats){
    return renderViewTemplate("workbench/status-summary-graph-status-summary", [viewValue(['failed','running','succeeded','paused','waiting','queued','cancelled'].filter(state=>stats.counts[state]).map(state=>statusBadge(state,`${stats.counts[state]} ${state}`)).join('')||statusBadge('waiting'))]);
  }
  function renderBranch(group){
    const stats=scopeStats({activations:[],children:group});
    return renderViewTemplate("workbench/render-branch-graph-branch-set", [viewValue(esc(JSON.stringify(group[0].path.slice(0,-1)))), viewValue(esc(group[0].owner)), viewValue(esc(group[0].owner)), viewValue(statusSummary(stats)), viewValue(['true','false'].map(condition=>{
      const selected=group.filter(child=>child.detail===`condition ${condition}`);
      return renderViewTemplate("workbench/render-branch-graph-branch-route", [viewValue(selected.length?'taken':'untaken'), viewValue(condition.toUpperCase()), viewValue(selected.length?'Taken':'Not taken'), viewValue(selected.length?selected.map(child=>renderScope(child,false,true)).join(''):renderViewTemplate("workbench/render-branch-graph-route-skip"))]);
    }).join(''))]);
  }
  function renderNestedScopes(children){
    if(!children.length)return '';
    const groups=new Map();
    children.forEach(child=>{const key=JSON.stringify([child.kind,child.owner]);const group=groups.get(key)||[];group.push(child);groups.set(key,group)});
    return renderViewTemplate("workbench/render-nested-scopes-graph-nested-scopes", [viewValue([...groups.values()].map(group=>{
      const first=group[0];
      if(first.kind==='branch')return renderBranch(group);
      if(first.kind!=='map'&&first.kind!=='loop')return group.map(child=>renderScope(child)).join('');
      const loop=first.kind==='loop';
      if(loop)group.sort((a,b)=>a.detail.localeCompare(b.detail,undefined,{numeric:true}));
      const stats=scopeStats({activations:[],children:group});
      const title=`${loop?'Loop':'Map'} · ${nodeDisplayName(first.owner)}`;
      return renderViewTemplate("workbench/render-nested-scopes-graph-map-set", [viewValue(loop?'graph-loop-set':''), viewValue(esc(JSON.stringify(first.path.slice(0,-1)))), viewValue(esc(first.kind)), viewValue(esc(first.owner)), viewValue(esc(title)), viewValue(group.length), viewValue(loop?(group.length===1?'iteration':'iterations'):(group.length===1?'item':'items')), viewValue(statusSummary(stats)), viewValue(group.map((child,index)=>{
        const childStats=scopeStats(child);
        const label=loop?child.detail:`Item · ${child.detail||'item'}`;
        const hasNext=index<group.length-1;
        const result=[...child.activations].reverse().find(a=>typeof a.outputs?.passed==='boolean')?.outputs.passed;
        const outcome=loop?(hasNext?`Repeat: ${group[index+1].detail}`:result===true?'Condition met · exit loop':result===false?'Condition not met':'Latest iteration'):'';
        return renderViewTemplate("workbench/render-nested-scopes-graph-map-item", [viewValue(loop?'loop':'map'), viewValue(esc(JSON.stringify(child.path))), viewValue(esc(label)), viewValue(statusSummary(childStats)), viewValue(outcome?renderViewTemplate("workbench/render-nested-scopes-graph-iteration-outcome", [viewValue(hasNext?'repeat':result===true?'exit':''), viewValue(esc(outcome))]):''), viewValue(renderScope(child,false,true)), viewValue(loop?renderViewTemplate("workbench/render-nested-scopes-graph-iteration-transition", [viewValue(hasNext?'repeat':''), viewValue(esc(outcome))]):'')]);
      }).join('')), viewValue(loop?renderViewTemplate("workbench/render-nested-scopes-graph-control-footer"):'')]);
    }).join(''))]);
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
      html+=renderViewTemplate("workbench/render-scope-graph-col", [viewValue(nested.length?' graph-control-stage':''), viewValue(sc), viewValue(selectedActivation===a.id?'selected':''), viewValue(esc(a.id)), viewValue(selectedActivation===a.id), viewValue(checkIcon(a.state)), viewValue(String(number).padStart(2,'0')), viewValue(nodeType?' · '+nodeType.toUpperCase():''), viewValue(esc(nodeName)), viewValue(statusBadge(a.state||'queued')), viewValue(model?renderViewTemplate("shared/inline-text", [viewValue(esc(model))]):''), viewValue(skill?renderViewTemplate("workbench/render-scope-span-2", [viewValue(esc(skill))]):'')]);
      if(nested.length){renderedOwners.add(a.nodeId);html+=renderNestedScopes(nested)}
      html+=renderViewTemplate("shared/closing-div");
      if(i<scope.activations.length-1){
        html+=renderViewTemplate("workbench/render-scope-graph-connector", [viewValue(a.state==='succeeded'?renderViewTemplate("workbench/render-scope-connector-label"):''), viewValue(a.state==='failed'?renderViewTemplate("workbench/render-scope-connector-label-2"):'')]);
      }
    });
    const orphaned=scope.children.filter(child=>!renderedOwners.has(child.owner));
    if(orphaned.length)html+=renderNestedScopes(orphaned);
    if(!html)html=renderViewTemplate("workbench/html-graph-scope-empty");
    if(isRoot){
      const allDone=runComplete??acts.every(a=>a.state==='succeeded');
      return frameGraph(html,allDone);
    }
    if(omitHeader)return renderViewTemplate("workbench/render-scope-graph-scope-flow", [viewValue(html)]);
    return renderViewTemplate("workbench/render-scope-graph-scope", [viewValue(esc(JSON.stringify(scope.path))), viewValue(esc(scope.label)), viewValue(scope.detail?renderSmallContent(scope.detail):''), viewValue(html)]);
  }
  return renderScope(root,true);
}

function buildWbEvents(events){
  if(!events||!events.length)return renderViewTemplate("workbench/build-wb-events-div");
  // Cap at 100 most-recent events to prevent DOM bloat on long-running workflows.
  return events.slice().sort((a,b)=>b.seq-a.seq).slice(0,100).map(e=>{
    const t=MuxlioFormat.timestamp(e.timestamp,true);
    const label=eventLabel(e.type);
    return renderViewTemplate("workbench/build-wb-events-event-row", [viewValue(esc(t)), viewValue(esc(label)), viewValue(e.subjectId?renderViewTemplate("workbench/build-wb-events-subtle", [viewValue(esc(e.subjectId))]):'')]);
  }).join('');
}

function buildWbArtifacts(acts){
  const withOutputs=acts.filter(a=>a.outputs&&typeof a.outputs==='object'&&Object.keys(a.outputs).length>0);
  if(!withOutputs.length)return renderViewTemplate("workbench/build-wb-artifacts-div");
  return withOutputs.flatMap(a=>
    Object.entries(a.outputs).map(([name,val])=>{
      const isArtifact=val&&typeof val==='object'&&('mime'in val||'path'in val||'data'in val||'digest'in val);
      const meta=isArtifact?`${(val as any).mime||''} · ${(val as any).path||'artifact'}`:valueType(val);
      return renderViewTemplate("workbench/build-wb-artifacts-artifact-row", [viewValue(isArtifact?'📄':'{ }'), viewValue(esc(name)), viewValue(esc(a.nodeId)), viewValue(esc(meta)), viewValue(esc(a.id)), viewValue(esc(name))]);
    })
  ).join('');
}

// wbCurrentRunState tracks the state of the run currently open in the
// workbench. Read by inspector.js to decide whether to show the retry
// actions panel; must be set before renderWorkbench calls renderInspectorWb.
let wbCurrentRunState='',wbCurrentRunSummary=null;
let wbActiveTab='graph';
function renderWorkbench(r,g,events){
  const acts=g.activations||[];
  activationData=acts;
  wbCurrentRun=r.id;
  wbCurrentRunState=r.state;
  wbCurrentRunSummary=r;
  if(typeof refreshInputRequests==='function')refreshInputRequests(r.id);

  $('#workbenchView').hidden=false;
  $('#runDetail').hidden=true;
  document.querySelector<UIElement>('.content')?.classList.add('content--wb');

  $('#wbTitle').textContent=r.workflowName||'Workflow run';
  $('#wbSubtitle').textContent=`${r.id}  ·  ${r.createdAt?`Created ${date(r.createdAt)}`:''}`;

  const sb=$('#wbStatusBadge');
  sb.className=`status-badge ${stateClass(r.state)||''}`;
  sb.textContent=({succeeded:'✓ Succeeded',failed:'✗ Failed',running:'⟳ Running',waiting:'Waiting',paused:'Ⅱ Paused',cancelled:'Cancelled'}[r.state]||r.state||'—');

  $('#wbStats').innerHTML=buildWbStats(r,acts);
  renderWbActivity(r,acts);
  replaceHTMLPreservingDetails($('#graphFlow'),graphMarkup(acts,g.scopes||[],r.overview?r.overview.completed===r.overview.activations:undefined));
  if(!g.hasOwnProperty('offset'))$('#wbArtifactList').innerHTML=buildWbArtifacts(acts);
  $('#inspActivationCount').textContent=`${r.overview?.activations??acts.length} nodes`;

  // timeline
  const tlEl=$('#wbTimeline');
  replaceHTMLPreservingDetails(tlEl,events.length?events.map(eventHtml).join(''):renderViewTemplate("workbench/render-workbench-subtle"));
  wbAfter=events[events.length-1]?.seq||0;
  const moreBtn=$('#moreEventsWb');
  if(moreBtn)moreBtn.disabled=!wbHasMore;

  // run data
  if(!r.overview){currentRunInput=r.input;currentRunOutput=r.output;}
  if(!r.overview)replaceHTMLPreservingDetails($('#runInputInspector'),inputMarkup(r.input));
  if(!r.overview)replaceHTMLPreservingDetails($('#runOutputInspector'),outputMarkup(r.output,'run-output'));

  // node click handlers
  document.querySelectorAll<UIElement>('[data-act]').forEach(b=>b.onclick=()=>loadActivation(b.dataset.act));

  // re-attach inspector click
  $('#runDetail').onclick=null;
  $('#nodeInspector').onclick=inspectorAction;
  $('#runInputInspector').onclick=inspectorAction;
  $('#runOutputInspector').onclick=inspectorAction;

  if(selectedActivation&&typeof selectedActivationDetail!=='undefined'&&selectedActivationDetail?.id===selectedActivation){const index=activationData.findIndex(a=>a.id===selectedActivation);if(index>=0)activationData[index]={...selectedActivationDetail,...activationData[index]};else activationData.push(selectedActivationDetail);}
  if(selectedActivation)renderInspector();
  else {const body=$('#inspBody');if(body)body.innerHTML=renderPlaceholder("Select an execution to inspect its attempts and outputs.", "inspector-placeholder");const identity=$('#inspIdentity');if(identity)identity.hidden=true;const actions=$('#inspActions');if(actions)actions.hidden=true;}
}

function showWbTab(tab){
  wbActiveTab=tab;
  for(const t of ['graph','events','artifacts','data','operations']){
    const el=$(`#wbTab${t[0].toUpperCase()+t.slice(1)}`);
    if(el)el.hidden=t!==tab;
  }
  if(tab==='data'&&typeof loadRunData==='function')loadRunData();
  if(tab==='operations'&&typeof loadRunOperations==='function')loadRunOperations();
  document.querySelectorAll<UIElement>('[data-wb-tab]').forEach(b=>{b.setAttribute('aria-selected',String(b.dataset.wbTab===tab))});
}

async function openRun(id){
  const wb=$('#workbenchView');
  if(wb)wb.hidden=true;
  $('#runDetail').hidden=false;
  document.querySelector<UIElement>('.content')?.classList.remove('content--wb');
  $('#runDetail').innerHTML=renderViewTemplate("workbench/run-detail-inner-html-loading");
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
    $('#runDetail').innerHTML=renderViewTemplate("workbench/run-detail-inner-html-page", [viewValue(esc(err.message))]);
  }
}

