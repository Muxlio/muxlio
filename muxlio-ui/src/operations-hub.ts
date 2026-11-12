// Operational screens consume coordinator contracts; executor names are opaque.
const hubState: Record<string, any>={operationOffset:0,automation:'schedules',retentionOffset:0,reportOffset:0,report:null,comparison:null,comparisonOffset:0};
const hubPages=new Map(),hubRequests=new Map();
const revisionViews=new Map(),revisionAvailability=new Map();
const hubID=value=>encodeURIComponent(value);
const hubRun=id=>id?renderViewTemplate("operations-hub/hub-run-a", [viewValue(hubID(id)), viewValue(esc(id))]):'Unavailable';
const hubButton=(action,label,attributes='')=>renderViewTemplate("operations-hub/hub-button-btn", [viewValue(action), viewValue(attributes), viewValue(label)]);
function hubSet(id,html){const el=(document.getElementById(id) as UIElement);if(el)replaceHTMLPreservingDetails(el,html)}
function hubTable(headers,rows,empty='No records'){return renderDataTable(headers,rows.map(componentBody),{operational:true,emptyTitle:empty})}
function hubMetric(label,value,note=''){return renderMetricCard(label,value,note)}
function hubPager(key,offset,more){return renderPagination({label:key+' pages',summary:`Page ${Math.floor(offset/50)+1}`,previous:{attributes:{'data-hub-page':key,'data-offset':Math.max(0,offset-50)},disabled:offset===0},next:{attributes:{'data-hub-page':key,'data-offset':offset+50},disabled:!more}})}
function hubClientPage(key,items,render,offset=0){hubPages.set(key,{items,render,offset});return renderViewTemplate("operations-hub/hub-client-page-div", [viewValue(esc(key)), viewValue(render(items.slice(offset,offset+50))+hubPager(key,offset,offset+50<items.length))])}
function hubStatus(value){return renderStatusBadge(stateClass(value)||'queued', value||'Unavailable')}
async function hubPost(path,body){return api(path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)})}
async function hubRead(id,work){const n=(hubRequests.get(id)||0)+1;hubRequests.set(id,n);const isCurrent=()=>hubRequests.get(id)===n;try{const html=await work(isCurrent);if(isCurrent())hubSet(id,html)}catch(err){if(isCurrent()&&err.name!=='AbortError')hubSet(id,renderNotice(err.message, {error:true}))}}
function hubJSON(value,label){try{return JSON.parse(value)}catch{throw Error(`${label} must be valid JSON.`)}}
function hubField(label,name,type='text',value='',extra=''){return renderInputField(label,name,type,value,componentBody(extra))}
function hubText(label,name,value='',extra=''){return renderTextField(label,name,value,componentBody(extra))}
function hubDialog(title,fields,save,submit='Save'){return openFormDialog({title,fields:componentBody(fields),onSubmit:save,submit}) as HTMLFormElement & {elements: HTMLFormControlsCollection & Record<string, HTMLInputElement>}}

async function loadOpsDashboard(){
 const kind=(document.getElementById('operationKind') as UIElement)?.value||'attention',offset=hubState.operationOffset;
 return hubRead('operationsList',async()=>{
  const page=await api(`/v1/operations?kind=${hubID(kind)}&offset=${offset}&limit=50`),c=page.counts||{};
  hubSet('operationsSummary',[['Needs reconciliation',c.attention??0],['Queued work',c.queue??0],['Detached',c.detached??0],['Recovery records',c.recovery??0]].map(([label,n])=>hubMetric(label,n)).join(''));
  return hubTable(['Run / activation','State','Recorded reason','Recorded at',''],(page.items||[]).map(item=>renderViewTemplate("operations-hub/load-ops-dashboard-tr", [viewValue(hubRun(item.runId)), viewValue(item.activationId?renderViewTemplate("operations-hub/load-ops-dashboard-ops-id", [viewValue(esc(item.activationId))]):''), viewValue(hubStatus(item.state)), viewValue(esc(item.reason||'No additional reason recorded')), viewValue(esc(date(item.at))), viewValue(item.kind==='attention'?hubButton('resolve','Resolve',renderViewTemplate("operations-hub/load-ops-dashboard-fragment", [viewValue(esc(item.runId)), viewValue(esc(item.attemptId))])):''), viewValue(hubButton('view-run','View run',renderViewTemplate("operations-hub/load-ops-dashboard-fragment-2", [viewValue(esc(item.runId))])))])),'No records in this category')+hubPager('operations',offset,page.hasMore);
 });
}

async function resolveOperationalAttempt(run,id){
 const attempt=await api(`/v1/runs/${hubID(run)}/attempts/${hubID(id)}`);
 const form=hubDialog('Resolve uncertain execution',renderViewTemplate("operations-hub/form-ops-check", [viewValue(esc(attempt.awaitReconciliationReason||'Verify the external outcome before resolving this attempt.')), viewValue(hubText('Evidence and reason','reason','','required')), viewValue(hubText('Verified executor result JSON (for completed outcomes)','result','','data-completed-result'))]),async f=>{
  const outcome=f.elements.outcome.value,body: Record<string, any>={outcome,reason:f.elements.reason.value.trim()};if(!body.reason)throw Error('Record the evidence used for this decision.');
  if(outcome==='completed'){body.result=hubJSON(f.elements.result.value,'Verified result');if(body.result?.executionSucceeded!==true)throw Error('A completed outcome requires a verified successful executor result.');}
  await hubPost(`/v1/runs/${hubID(run)}/attempts/${hubID(id)}/resolve`,body);if(currentRoute().split('?')[0]==='/operations')await loadOpsDashboard();else if(current===run)await openRun(run);
 },'Record resolution');
 const update=()=>{const field=form.querySelector<UIElement>('[data-completed-result]');field.hidden=form.elements.outcome.value!=='completed';form.elements.result.required=!field.hidden};form.elements.outcome.onchange=update;update();
}

// Include stale workers so lost heartbeats are visible rather than disappearing.
// @ts-expect-error TS2630: classic scripts intentionally replace this shared view hook.
loadWorkers=async function(){
 return hubRead('workerRoster',async()=>{
  const data=await api('/v1/workers?includeStale=true'),workers=data.registeredWorkers||[],assignments=data.workers||[],now=Date.parse(data.serverTime)||Date.now(),windowMs=(data.heartbeatWindowSeconds||90)*1000;
  const live=w=>now-Date.parse(w.lastSeen)<=windowMs;
  const active=assignments.filter(a=>Date.parse(a.leaseUntil)>now);
  const slots=workers.filter(w=>live(w)&&!w.draining).reduce((n,w)=>n+w.capacity,0),occupied=active.filter(a=>workers.some(w=>w.workerId===a.workerId&&live(w)&&!w.draining)).length;
  hubSet('workerCapacitySummary',hubMetric('Live workers',workers.filter(live).length)+hubMetric('Occupied / available capacity',`${occupied} / ${slots}`,`${Math.max(0,slots-occupied)} unoccupied slots on accepting workers`)+hubMetric('Stale workers',workers.filter(w=>!live(w)).length)+hubMetric('Expired active leases',assignments.length-active.length));
  hubSet('workerList',hubTable(['Worker','Run','Activation','Lease','Generation','State'],assignments.map(a=>renderViewTemplate("operations-hub/load-workers-ops-id", [viewValue(esc(a.workerId)), viewValue(esc(a.workerId)), viewValue(hubRun(a.runId)), viewValue(esc(a.activationId)), viewValue(esc(date(a.leaseUntil))), viewValue(Date.parse(a.leaseUntil)>now?`${esc(fmtDuration(Date.parse(a.leaseUntil)-now))} remaining`:'Expired'), viewValue(esc(a.generation)), viewValue(hubStatus(a.state))])),'No active assignments'));
  return hubTable(['Worker','Health','Slots','Readiness and compatibility','Last heartbeat'],workers.map(w=>{
   const count=active.filter(a=>a.workerId===w.workerId).length,status=!live(w)?'stale':w.draining?'draining':count?'busy':'idle';
   return renderViewTemplate("operations-hub/load-workers-worker-capabilities-cell", [viewValue(hubID(w.workerId)), viewValue(esc(w.workerId)), viewValue(hubStatus(status)), viewValue(esc(count)), viewValue(esc(w.capacity)), viewValue(live(w)?renderViewTemplate("operations-hub/load-workers-meter", [viewValue(esc(Math.max(1,w.capacity))), viewValue(esc(count))]):renderViewTemplate("operations-hub/load-workers-small")), viewValue((w.capabilities||[]).map(cap=>workerCapabilityMarkup(cap,w.workerId)).join('')||'Capabilities unavailable'), viewValue(esc(date(w.lastSeen))), viewValue(esc(fmtDuration(Math.max(0,now-Date.parse(w.lastSeen)))))]);
  }),'No registered workers');
 });
};

async function loadWorkerCapabilityPage(workerID,name,version=''){
 hubSet('workerCapabilityContent',renderPlaceholder("Loading capability…", "subtle"));
 return hubRead('workerCapabilityContent',async()=>{
  const data=await api('/v1/workers?includeStale=true');
  const worker=(data.registeredWorkers||[]).find(w=>w.workerId===workerID);
  if(!worker)throw Error('This worker is no longer registered.');
  const cap=(worker.capabilities||[]).find(c=>(typeof c==='string'?c:c.name)===name&&(typeof c==='string'?'':c.version||'')===version);
  if(!cap)throw Error('This capability is no longer reported by the worker.');
  return renderViewTemplate("operations-hub/load-worker-capability-page-subtle-2", [viewValue(esc(workerID))])+workerCapabilityDetailMarkup(cap,workerID).replace(renderViewTemplate("operations-hub/load-worker-capability-page-details", [viewValue(" ")]),renderViewTemplate("operations-hub/load-worker-capability-page-details-2", [viewValue(" ")]));
 });
}

const automationNames={schedules:'Schedules',triggers:'Webhooks',subscriptions:'Subscriptions',deadletters:'Dead letters'};
async function loadAutomation(){
 const kind=hubState.automation;
 const title=(document.getElementById('automationTitle') as UIElement),create=(document.getElementById('createAutomation') as UIElement);if(title)title.textContent=automationNames[kind];if(create){create.hidden=kind==='deadletters';create.textContent=`Create ${kind==='schedules'?'schedule':kind==='triggers'?'webhook':'subscription'}`;}
 document.querySelectorAll<UIElement>('[data-automation-tab]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.automationTab===kind)));
 return hubRead('automationList',async isCurrent=>{
  const offset=hubState.automationOffset||0,page=await api(kind==='deadletters'?`/v1/automation/history?kind=deadletters&offset=${offset}&limit=50`:`/v1/${kind}`),items=page[kind==='deadletters'?'records':kind]||[];
  if(!isCurrent())return '';
  const render=rows=>kind==='deadletters'?hubTable(['Subscription','Run','Attempts','Last failure',''],rows.map(d=>renderViewTemplate("operations-hub/render-tr", [viewValue(esc(d.subscriptionId)), viewValue(hubRun(d.runId)), viewValue(esc(d.attempts)), viewValue(esc(d.maxRetries)), viewValue(esc(d.lastError||'Unavailable')), viewValue(hubButton('requeue','Review redelivery',renderViewTemplate("operations-hub/render-fragment", [viewValue(esc(d.id))])))])),'No dead letters'):hubTable(['Name','Status',kind==='schedules'?'Next occurrence':'Target','Configuration',''],rows.map(item=>{
   const enabled=kind==='schedules'?!item.paused:item.enabled;
   const config=kind==='schedules'?`${item.kind} · ${item.kind==='interval'?MuxlioFormat.duration(item.intervalSeconds,'seconds'):item.cron||date(item.startAt)} · ${item.overlap}`:kind==='triggers'?`Signature: ${item.signatureScheme} · deduplication ${MuxlioFormat.duration(item.deduplicationSeconds,'seconds')}`:(item.eventTypes||[]).join(', ');
   return renderViewTemplate("operations-hub/render-tr-2", [viewValue(esc(item.id)), viewValue(item.workflowDigest?renderViewTemplate("operations-hub/render-ops-id", [viewValue(esc(item.workflowDigest)), viewValue(esc(item.workflowDigest))]):''), viewValue(hubStatus(kind==='schedules'&&enabled&&!item.nextFire?'finished':enabled?'enabled':'paused')), viewValue(kind==='schedules'?esc(item.nextFire?date(item.nextFire):'No further occurrences'):kind==='triggers'?renderViewTemplate("operations-hub/render-code", [viewValue(esc(item.id))]):esc(item.target)), viewValue(esc(config)), viewValue(hubButton('automation-history','History',renderViewTemplate("operations-hub/render-fragment-2", [viewValue(kind), viewValue(esc(item.id))]))), viewValue(hubButton('automation-toggle',enabled?'Pause':'Enable',renderViewTemplate("operations-hub/render-fragment-3", [viewValue(kind), viewValue(esc(item.id)), viewValue(!enabled)])))]);
  }),`No ${automationNames[kind].toLowerCase()} configured`);
  return kind==='deadletters'?render(items)+hubPager('deadletters',offset,page.hasMore):hubClientPage(`automation-list-${kind}`,items,render);
 });
}

async function showAutomationHistory(kind,id,offset=0){
 hubState.history={kind,id,offset};
 hubSet('automationHistory',renderPlaceholder("Loading history…", "subtle"));
 return hubRead('automationHistory',async()=>{
  const data=await api(`/v1/automation/history?kind=${kind}&id=${hubID(id)}&offset=${offset}&limit=50`),rows=data.records||[];
  return renderViewTemplate("operations-hub/show-automation-history-section-head", [viewValue(esc(id))])+hubTable(['Recorded at','State','Run','Delivery / attempts','Details'],rows.map(row=>renderViewTemplate("operations-hub/show-automation-history-tr", [viewValue(esc(date(row.timestamp||row.createdAt||row.dueAt))), viewValue(hubStatus(row.state)), viewValue(hubRun(row.runId)), viewValue(esc(row.id||row.idempotencyKey||row.eventType||'—')), viewValue(row.attempts!=null?renderViewTemplate("operations-hub/show-automation-history-small", [viewValue(esc(row.attempts))]):''), viewValue(esc(row.lastError||row.reason||'—')), viewValue(row.nextAttemptAt?renderViewTemplate("operations-hub/show-automation-history-small-2", [viewValue(esc(date(row.nextAttemptAt)))]):'')])),'No delivery records')+hubPager('automation-history',offset,data.hasMore);
 });
}

async function createOperationalAutomation(){
 const kind=hubState.automation;if(kind==='deadletters')return;
 const versions=(await api('/v1/workflow-versions')).workflowVersions||[];
 const choices=versions.map(v=>renderViewTemplate("operations-hub/choices-option", [viewValue(esc(v.digest)), viewValue(esc(v.name||v.digest)), viewValue(esc(v.digest.slice(0,20)))])).join('');
 if(kind!=='subscriptions'&&!versions.length)throw Error('Register a workflow version before creating automation.');
 const workflow=renderViewTemplate("operations-hub/workflow-label", [viewValue(choices)]);
 let fields=hubField('Name','id','text','',renderViewTemplate("operations-hub/fields-fragment"));
 if(kind==='schedules')fields+=workflow+hubText('Run input JSON','input','{}','required')+renderViewTemplate("operations-hub/create-operational-automation-label")+hubField('Start at','startAt','datetime-local','','required')+renderViewTemplate("operations-hub/create-operational-automation-div", [viewValue(hubField('Repeat every','interval','number','1',renderViewTemplate("operations-hub/create-operational-automation-fragment"))), viewValue(hubField('Cron expression','cron','text','0 * * * *')), viewValue(hubField('Timezone','timezone','text',Intl.DateTimeFormat().resolvedOptions().timeZone||'UTC'))])+hubField('Maximum catch-up executions','maxCatchUp','number','1',renderViewTemplate("operations-hub/create-operational-automation-fragment-2"))+renderPlaceholder("Creating the schedule enables execution at its start time.", "subtle");
 if(kind==='triggers')fields+=workflow+hubField('Signing secret','secret','password','',renderViewTemplate("operations-hub/create-operational-automation-fragment-3"))+hubText('Input mapping JSON (optional; empty uses the event payload)','mapping')+hubField('Deduplication window, seconds','dedup','number','86400',renderViewTemplate("operations-hub/create-operational-automation-fragment-4"))+renderPlaceholder("Incoming payloads must use the configured HMAC signature. Secrets are write-only.", "subtle");
 if(kind==='subscriptions')fields+=hubField('Destination URL','target','url','',renderViewTemplate("operations-hub/create-operational-automation-fragment-5"))+hubField('Signing secret','secret','password','',renderViewTemplate("operations-hub/create-operational-automation-fragment-3"))+hubField('Event types, comma separated','events','text','run.succeeded,run.failed','required')+hubField('Maximum retries','retries','number','5',renderViewTemplate("operations-hub/create-operational-automation-fragment-6"))+hubField('Payload fields to redact, comma separated','redact')+renderPlaceholder("Creating a subscription enables signed deliveries to this destination.", "subtle");
 const form=hubDialog(`Create ${kind==='schedules'?'schedule':kind==='triggers'?'webhook':'subscription'}`,fields,async f=>{
  const v=f.elements,body: Record<string, any>={id:v.id.value};
  if(kind==='schedules'){Object.assign(body,{workflowDigest:v.workflowDigest.value,input:hubJSON(v.input.value,'Run input'),kind:v.kind.value,startAt:new Date(v.startAt.value).toISOString(),overlap:v.overlap.value,maxCatchUp:Number(v.maxCatchUp.value)});if(body.kind==='interval')body.intervalSeconds=Number(v.interval.value)*{seconds:1,minutes:60,hours:3600,days:86400}[v.unit.value];if(body.kind==='cron')Object.assign(body,{cron:v.cron.value,timezone:v.timezone.value});}
  if(kind==='triggers'){Object.assign(body,{workflowDigest:v.workflowDigest.value,secret:v.secret.value,deduplicationSeconds:Number(v.dedup.value),enabled:true});if(v.mapping.value.trim())body.inputMapping=hubJSON(v.mapping.value,'Input mapping');}
  if(kind==='subscriptions')Object.assign(body,{target:v.target.value,secret:v.secret.value,eventTypes:v.events.value.split(',').map(s=>s.trim()).filter(Boolean),redactFields:v.redact.value.split(',').map(s=>s.trim()).filter(Boolean),maxRetries:Number(v.retries.value),enabled:true});
  await hubPost(`/v1/${kind}`,body);if(v.secret)v.secret.value='';await loadAutomation();
 },'Create');
 if(kind==='schedules'){
  const local=new Date(Date.now()+60000);local.setMinutes(local.getMinutes()-local.getTimezoneOffset());form.elements.startAt.value=local.toISOString().slice(0,16);
  const update=()=>{form.querySelector<UIElement>('[data-interval-fields]').hidden=form.elements.kind.value!=='interval';form.querySelector<UIElement>('[data-cron-fields]').hidden=form.elements.kind.value!=='cron'};form.elements.kind.onchange=update;update();
 }
}

async function loadRunComparison(){
 const form=(document.getElementById('compareRunsForm') as UIElement);if(!form)return;
 const params=new URLSearchParams(currentRoute().split('?')[1]||'');
 if(params.has('before'))form.elements.before.value=params.get('before');if(params.has('after'))form.elements.after.value=params.get('after');
 const recent=await api('/v1/runs?limit=200');hubSet('comparisonRunChoices',(recent.runs||[]).map(r=>renderViewTemplate("operations-hub/choices-option", [viewValue(esc(r.id)), viewValue(esc(r.workflowName||'Workflow')), viewValue(esc(r.state))])).join(''));
 if(params.get('before')&&params.get('after'))await compareOperationalRuns(params.get('before'),params.get('after'));
}
async function compareOperationalRuns(before,after){
 hubSet('comparisonResult',renderPlaceholder("Comparing recorded histories…", "subtle"));
 return hubRead('comparisonResult',async isCurrent=>{
  const data=await api(`/v1/runs/${hubID(before)}/compare?other=${hubID(after)}`);if(!isCurrent())return '';hubState.comparison=data;hubState.comparisonOffset=0;return comparisonMarkup(data);
 });
}
function comparisonMarkup(data){
 const rows=(data.activations||[]).filter(a=>!(document.getElementById('compareChangedOnly') as UIElement)?.checked||a.changed),offset=hubState.comparisonOffset;
 return renderViewTemplate("operations-hub/comparison-markup-ops-comparison-summary", [viewValue(hubMetric('Pinned plan',data.planChanged?'Changed':'Same')), viewValue(hubMetric('Inputs',data.inputsChanged?'Changed':'Same')), viewValue(hubMetric('Results',data.resultsChanged?'Changed':'Same')), viewValue(hubMetric('Reused outputs',data.reusedOutputs?.length??0)), viewValue(hubButton('compare-inputs','Inspect inputs and plans')), viewValue(hubButton('fork-source','Fork baseline',renderViewTemplate("operations-hub/load-ops-dashboard-fragment-2", [viewValue(esc(data.before.id))]))), viewValue((document.getElementById('compareChangedOnly') as UIElement)?.checked?' checked':''), viewValue([data.before,data.after].map((run,i)=>renderViewTemplate("operations-hub/comparison-markup-ops-comparison-side", [viewValue(i?'Comparison':'Baseline'), viewValue(hubRun(run.id)), viewValue(hubStatus(run.state)), viewValue(esc(MuxlioFormat.elapsed(run.createdAt,run.updatedAt))), viewValue(buildUsageGridMarkup(i?data.afterAccounting:data.beforeAccounting))])).join('')), viewValue(rows.length)])+hubTable(['Node / scope','Baseline','Comparison','Change',''],rows.slice(offset,offset+50).map(a=>renderViewTemplate("operations-hub/comparison-markup-tr", [viewValue(esc(a.nodeId)), viewValue(esc(a.scopePath?.join(' / ')||'root')), viewValue(hubStatus(a.before?.state)), viewValue(hubStatus(a.after?.state)), viewValue(a.changed?'Changed':'Same'), viewValue(hubButton('compare-node','Inspect outputs',renderViewTemplate("operations-hub/comparison-markup-fragment", [viewValue(data.activations.indexOf(a))])))])),'No matching node outcomes')+hubPager('comparison',offset,offset+50<rows.length);
}
async function forkOperationalRun(id){
 const [run,graph,registry]=await Promise.all([api(`/v1/runs/${hubID(id)}`),api(`/v1/runs/${hubID(id)}/graph`),api('/v1/workflow-versions')]);
 const versions=registry.workflowVersions||[],candidates=(graph.activations||[]).filter(a=>a.state==='succeeded');
 const fields=renderViewTemplate("operations-hub/fields-subtle", [viewValue(hubRun(id)), viewValue(versions.map(v=>renderViewTemplate("operations-hub/choices-option", [viewValue(esc(v.digest)), viewValue(esc(v.name||v.digest)), viewValue(esc(v.digest.slice(0,20)))])).join('')), viewValue(hubText('Fork input JSON','input',JSON.stringify(run.input??null,null,2),'required')), viewValue(candidates.map(a=>renderViewTemplate("operations-hub/fields-ops-check", [viewValue(esc(a.id)), viewValue(esc(a.nodeId)), viewValue(esc(a.scopePath?.join(' / ')||'root'))])).join('')||renderViewTemplate("operations-hub/fields-p"))]);
 hubDialog('Fork run',fields,async f=>{
  const body: Record<string, any>={input:hubJSON(f.elements.input.value,'Fork input'),reuseActivations:[...f.querySelectorAll('[name="reuse"]:checked')].map(x=>x.value),start:f.elements.start.checked};
  if(f.elements.planDigest.value)body.planDigest=f.elements.planDigest.value;
  const result=await hubPost(`/v1/runs/${hubID(id)}/fork`,body);navigateTo(`/runs/${hubID(result.runId)}`);
 },'Create fork');
}

async function loadRetention(){
 const offset=hubState.retentionOffset;
 return hubRead('retentionPins',async()=>{
  const page=await api(`/v1/retention?offset=${offset}&limit=50`),policy=page.policy||{};
  hubSet('retentionPolicy',[['Events',policy.eventHistory],['Worker output',policy.stdoutStderr],['Artifacts',policy.artifacts],['Plan cache',policy.caches],['Run workspaces',policy.workspaces]].map(([label,n])=>hubMetric(label,n===0?'No automatic expiry':n==null?'Unavailable':fmtDuration(n/1e6),'Default retention after run completion')).join(''));
  return renderPlaceholder(`${page.total??0} explicit pins. Automatic cleanup checks every 15 minutes when the runtime is idle. Active, pinned, and uncertain work stays protected.`, "subtle")+hubTable(['Kind','Pinned identity',''],(page.pins||[]).map(p=>renderViewTemplate("operations-hub/load-retention-ops-id", [viewValue(esc(p.kind)), viewValue(p.kind==='run'?hubRun(p.id):esc(p.id)), viewValue(hubButton('unpin','Review unpin',renderViewTemplate("operations-hub/render-fragment-2", [viewValue(esc(p.kind)), viewValue(esc(p.id))])))])),'No explicit pins')+hubPager('retention',offset,page.hasMore);
 });
}
function addRetentionPin(kind='run',id=''){
 hubDialog('Pin data',renderViewTemplate("operations-hub/add-retention-pin-subtle", [viewValue(['run','plan','artifact'].map(k=>renderViewTemplate("operations-hub/add-retention-pin-option", [viewValue(k===kind?' selected':''), viewValue(k)])).join('')), viewValue(hubField('Run ID or content digest','id','text',id,'required'))]),async f=>{await hubPost('/v1/retention',{kind:f.elements.kind.value,id:f.elements.id.value.trim(),pinned:true});await refreshRetentionContext()},'Pin');
}
function setRetentionDeadline(id=''){
 const form=hubDialog('Set run retention deadline',renderViewTemplate("operations-hub/form-subtle", [viewValue(hubField('Run ID','id','text',id,'required')), viewValue(hubField('Retain until','until','datetime-local'))]),async f=>{await hubPost('/v1/retention',{kind:'run',id:f.elements.id.value.trim(),retainedUntil:f.elements.until.value?new Date(f.elements.until.value).toISOString():null});await refreshRetentionContext()},'Set deadline');
 const preview=async()=>{const key=form.elements.id.value.trim();if(!key)return;try{const run=await api(`/v1/runs/${hubID(key)}?summary=true`);if(form.elements.id.value.trim()!==key)return;form.querySelector<UIElement>('[data-retention-current]').textContent=`Current state: ${run.state}. ${run.pinned?'Pinned. ':''}${run.retainedUntil?'Current deadline: '+date(run.retainedUntil):'Default retention policy.'}`;if(run.retainedUntil){const d=new Date(run.retainedUntil);d.setMinutes(d.getMinutes()-d.getTimezoneOffset());form.elements.until.value=d.toISOString().slice(0,16)}}catch(err){form.querySelector<UIElement>('[data-retention-current]').textContent=err.message}};
 form.elements.id.onchange=preview;if(id)preview();
}
async function refreshRetentionContext(){if(currentRoute().split('?')[0]==='/retention')await loadRetention();else if(current)await openRun(current)}

function evidenceDocument(documents){
 if(documents.length===1){const doc=documents[0];if(doc?.manifest?.datasetId&&Array.isArray(doc.results))return {kind:'evaluation',document:doc};if(Array.isArray(doc?.configurations)||doc?.manifest?.suite&&Array.isArray(doc.observations))return {kind:'benchmark',document:doc};}
 const manifest=documents.find(doc=>doc?.suite==='muxlio-reference/v1'),observations=documents.filter(doc=>doc?.schemaVersion===1&&typeof doc.case==='string'&&typeof doc.correct==='boolean');
 if(manifest&&observations.length)return {kind:'benchmark',document:{manifest,observations}};
 throw Error('Choose evaluation results, a benchmark comparison, or a benchmark manifest with observation files.');
}
async function loadEvidence(){
 const offset=hubState.reportOffset;
 return hubRead('evidenceList',async()=>{
  const page=await api(`/v1/reports?offset=${offset}&limit=50`);
  return hubTable(['Report','Type','Imported',''],(page.reports||[]).map(r=>renderViewTemplate("operations-hub/load-evidence-ops-id", [viewValue(esc(r.name)), viewValue(esc(r.id)), viewValue(esc(r.kind)), viewValue(esc(date(r.importedAt))), viewValue(hubButton('view-report','Inspect',renderViewTemplate("operations-hub/load-evidence-fragment", [viewValue(esc(r.id))]))), viewValue(hubButton('compare-report','Compare with open report',renderViewTemplate("operations-hub/load-evidence-fragment", [viewValue(esc(r.id))])))])),'No imported evidence')+hubPager('reports',offset,page.hasMore);
 });
}
function reportRows(report){
 const doc=report.document;
 if(report.kind==='evaluation')return (doc.results||[]).map(row=>({group:row.versionId,case:row.caseId,correct:row.execution?.success===true,expected:1,collected:1,duration:row.durationMs,state:row.execution?.state,runId:row.execution?.runId,scores:row.scores,accounting:row.execution?.accounting,error:row.error||row.outputError||row.execution?.error,output:row.execution?.output}));
 if(Array.isArray(doc.observations))return doc.observations.map(row=>({group:doc.manifest?.revision||'Recorded configuration',case:`${row.case} / repetition ${row.repetition}`,correct:row.correct,expected:1,collected:1,duration:row.wallTimeMs,state:row.correct?'correct':'failed',runId:row.run?.id,accounting:row.accounting,error:row.error,queue:row.queue,raw:row}));
 return (doc.configurations||[]).flatMap((config,i)=>(config.cases||[]).map(row=>({group:`Configuration ${i+1}`,case:row.case,correct:row.correct,expected:row.planned,collected:row.collected,duration:row.correctWallMedianMs,state:`${row.correct} correct / ${row.collected} collected`,accounting:row.accounting,raw:row,manifest:config.manifest})));
}
function benchmarkFacts(row){
 const sample=row.raw;if(!sample)return '';
 const verdict=value=>value===true?'Passed':value===false?'Failed':'Unavailable';
 const outcomes=value=>Array.isArray(value)?`${value.filter(v=>v===true).length} passed · ${value.filter(v=>v===false).length} failed · ${value.filter(v=>v==null).length} unavailable`:verdict(value);
 const facts=[];
 if('correctWallP95Ms'in sample)facts.push(['P95 correct duration',fmtDuration(sample.correctWallP95Ms)]);
 if('queueMedianMs'in sample)facts.push(['Queue median',fmtDuration(sample.queueMedianMs)],['Queue P95',fmtDuration(sample.queueP95Ms)]);
 if('recoveryCorrect'in sample||'recovery'in sample)facts.push(['Recovery verification',outcomes(sample.recovery??sample.recoveryCorrect)]);
 if('cancellationCorrect'in sample||'cancellation'in sample)facts.push(['Cancellation verification',outcomes(sample.cancellation??sample.cancellationCorrect)]);
 if('manualInterventions'in sample)facts.push(['Manual interventions',sample.manualInterventions]);
 if(sample.run?.state)facts.push(['Execution state',sample.run.state]);
 return facts.length?renderViewTemplate("operations-hub/benchmark-facts-contract-fields", [viewValue(facts.map(([label,value])=>renderDefinitionRow(label, value)).join(''))]):'';
}
function reportProvenance(report){
 const doc=report.document,manifests=doc.manifest?[doc.manifest]:(doc.configurations||[]).map(c=>c.manifest);
 return manifests.map((m,i)=>renderViewTemplate("operations-hub/report-provenance-execution-contract", [viewValue(manifests.length>1?`Configuration ${i+1} · `:''), viewValue([['Dataset',m.datasetId],['Dataset digest',m.datasetDigest],['Suite',m.suite],['Source revision',m.revision||m.gitRevision||m.git?.revision],['Concurrency',m.concurrency],['Worker capacity',m.capacity],['Runner',m.runnerId],['Scorers',m.scorerIds?.join(', ')],['Pricing basis',m.costBasis],['Configuration digest',m.configurationDigest],['Map concurrency',m.mapConcurrency],['Platform',[m.os,m.arch].filter(Boolean).join(' / ')||null],['Models',m.metadata?.models],['Versions',m.metadata?.versions],['Machine',m.metadata?.machine],['Pricing basis',m.metadata?.pricingBasis],['Source hashes',m.sourceHashes]].filter(([,v])=>v!=null).map(([k,v])=>renderDefinitionRow(k, typeof v==='object'?JSON.stringify(v):v)).join('')), viewValue(m.versions?renderViewTemplate("operations-hub/report-provenance-ul", [viewValue(m.versions.map(v=>renderViewTemplate("operations-hub/report-provenance-li", [viewValue(esc(v.id)), viewValue(esc(v.planDigest))])).join(''))]):'')])).join('');
}
function evidenceMarkup(report){
 const rows=reportRows(report),groups=new Map();
 if(report.kind==='evaluation')for(const version of report.document.manifest.versions||[])groups.set(version.id,{collected:0,correct:0,expected:0,durations:[]});
 for(const row of rows){if(!groups.has(row.group))groups.set(row.group,{collected:0,correct:0,expected:0,durations:[]});const g=groups.get(row.group);g.collected+=row.collected??0;if(row.expected==null)g.expectedKnown=false;g.expected+=row.expected??0;g.correct+=typeof row.correct==='boolean'?Number(row.correct):row.correct??0;if(typeof row.duration==='number'&&Number.isFinite(row.duration)&&row.duration>=0)g.durations.push(row.duration);}
 if(report.document.observations){const m=report.document.manifest,group=m.revision||'Recorded configuration';if(!groups.has(group))groups.set(group,{collected:0,correct:0,expected:0,durations:[]});groups.get(group).expected=Array.isArray(m.cases)&&Number.isInteger(m.repetitions)?m.cases.length*m.repetitions:null;}
 const summary=[...groups].map(([name,g])=>renderViewTemplate("operations-hub/summary-ops-evidence-group", [viewValue(esc(name)), viewValue(hubMetric('Correct / collected',`${g.correct} / ${g.collected}`)), viewValue(hubMetric('Missing cases',report.kind==='evaluation'||g.expectedKnown===false||g.expected==null?'Unavailable':Math.max(0,g.expected-g.collected),report.kind==='evaluation'?'Expected case identities are not included in this manifest':'')), viewValue(hubMetric(report.kind==='evaluation'?'Mean duration (all outcomes)':report.document.configurations?'Mean of case medians':'Mean duration (all outcomes)',g.durations.length?fmtDuration(g.durations.reduce((a,b)=>Number(a)+Number(b),0)/g.durations.length):'Unavailable',`${g.durations.length} duration measurements`))])).join('');
 const render=items=>hubTable(['Version / configuration','Case','Outcome','Duration','Scores / usage',''],items.map(row=>{
  const scores=Object.entries(row.scores||{}).map(([id,s])=>`${id}: ${typeof (s as any)?.value!=='number'||!Number.isFinite((s as any).value)?'Unavailable':(s as any).value}${(s as any)?.error?' (scorer failed)':''}`).join(' · ');
  return renderViewTemplate("operations-hub/render-tr-3", [viewValue(esc(row.group)), viewValue(esc(row.case)), viewValue(hubStatus(row.state)), viewValue(row.error?renderSmallContent(row.error):''), viewValue(row.runId?renderSmallContent(componentBody(hubRun(row.runId))):''), viewValue(esc(fmtDuration(row.duration))), viewValue(benchmarkFacts(row)), viewValue(esc(scores||'No scores reported')), viewValue(hubAccounting(row.accounting)), viewValue(hubButton('inspect-evidence-row','Inspect',renderViewTemplate("operations-hub/render-fragment-4", [viewValue(rows.indexOf(row))])))]);
 }), 'No result rows');
 return renderViewTemplate("operations-hub/evidence-markup-section-head", [viewValue(esc(report.name)), viewValue(hubButton('export-report','Download original result JSON')), viewValue(reportProvenance(report)), viewValue(summary), viewValue(hubClientPage('evidence-rows',rows,render))]);
}
function hubAccounting(summary){if(!summary)return '';const rows=[...(summary.referenceValue?[['Estimated reference value (USD)',summary.referenceValue]]:[]),['Tokens',summary.tokens],...(summary.credits||[]).map(c=>[`Credits · ${c.provider} · ${c.unit}`,c.value]),...(summary.costs||[]).map(c=>[`${c.currency} · ${c.basis==='provider-metered'?'Provider-metered usage':c.basis==='model-reference'?'Provider model reference':c.basis==='credit-replacement'?'Add-on replacement equivalent':c.basis==='api-equivalent'?'API-equivalent estimate':c.basis==='plan-debit'?'Plan debit':'Cost (unspecified basis)'}`,c.value])];return rows.map(([label,row])=>renderViewTemplate("operations-hub/hub-accounting-small", [viewValue(esc(label)), viewValue(esc(row?.knownSubtotal??'Unavailable')), viewValue(esc(row?.completeness||'unknown'))])).join('')}

async function openEvidence(id){
 return hubRead('evidenceDetail',async isCurrent=>{const report=await api(`/v1/reports/${hubID(id)}`);if(!isCurrent())return '';hubState.report=report;return evidenceMarkup(report)});
}
async function compareEvidence(id){
 if(!hubState.report)throw Error('Open a baseline report first.');
 const baseline=hubState.report,other=await api(`/v1/reports/${hubID(id)}`);if(baseline.kind!==other.kind)throw Error('Compare reports of the same type.');
 if(Boolean(baseline.document.configurations)!==Boolean(other.document.configurations))throw Error('Compare reports at the same aggregation level.');
 const left=reportRows(baseline),right=reportRows(other),leftGroups=[...new Set(left.map(r=>r.group))],rightGroups=[...new Set(right.map(r=>r.group))];
 const form=hubDialog('Compare recorded evidence',renderViewTemplate("operations-hub/form-subtle-2", [viewValue(esc(baseline.name)), viewValue(esc(other.name)), viewValue(leftGroups.map(g=>renderViewTemplate("operations-hub/form-option", [viewValue(esc(g))])).join('')), viewValue(rightGroups.map(g=>renderViewTemplate("operations-hub/form-option", [viewValue(esc(g))])).join('')), viewValue(reportProvenance(baseline)), viewValue(reportProvenance(other))]),async()=>{},'Done');
 const render=()=>{
  const leftMap=new Map(left.filter(r=>r.group===form.elements.baseline.value).map(r=>[r.case,r])),rightMap=new Map(right.filter(r=>r.group===form.elements.candidate.value).map(r=>[r.case,r]));
  const keys=[...new Set([...leftMap.keys(),...rightMap.keys()])],aligned=keys.map(k=>({left:leftMap.get(k),right:rightMap.get(k)}));
  form.querySelector<UIElement>('[data-evidence-comparison]').innerHTML=hubClientPage('evidence-comparison',aligned,items=>hubTable(['Case','Baseline outcome','Comparison outcome','Baseline duration','Comparison duration'],items.map(({left:a,right:b})=>renderViewTemplate("shared/table-row-five", [viewValue(esc((a||b).case)), viewValue(esc(a?.state??'Unavailable')), viewValue(esc(b?.state??'Unavailable')), viewValue(esc(fmtDuration(a?.duration))), viewValue(esc(fmtDuration(b?.duration)))]))));
 };form.elements.baseline.onchange=render;form.elements.candidate.onchange=render;render();
}

const baseOperationalRun=renderRunOperations;
// @ts-expect-error TS2630: classic scripts intentionally replace this shared view hook.
renderRunOperations=function(run){
 baseOperationalRun(run);
 const fork=(document.getElementById('forkRunBtn') as UIElement);if(fork)fork.hidden=false;
 const deadline=run.deadlineSeconds?Date.parse(run.createdAt)+run.deadlineSeconds*1000:null,remaining=deadline==null?'No deadline':fmtDuration(Math.max(0,deadline-Date.now()));
 const host=(document.getElementById('runOperationalControls') as UIElement);
 if(host)host.innerHTML=renderViewTemplate("operations-hub/host-inner-html-ops-metrics", [viewValue(hubMetric('Deadline remaining',stateIsTerminal(run.state)?'Run finished':remaining,deadline?date(deadline):'')), viewValue(hubMetric('Attempts / limit',`${run.overview?.attempts??0} / ${run.limits?.maxAttempts??'Unlimited'}`)), viewValue(hubMetric('Activations / limit',`${run.overview?.activations??0} / ${run.limits?.maxActivations??'Unlimited'}`)), viewValue(hubMetric('Retention',run.pinned?'Pinned':run.retainedUntil?date(run.retainedUntil):'Default policy')), viewValue(hubButton('pin-run',run.pinned?'Review unpin':'Pin run',renderViewTemplate("operations-hub/host-inner-html-fragment", [viewValue(esc(run.id)), viewValue(run.pinned?'true':'false')]))), viewValue(hubButton('run-deadline','Retention deadline',renderViewTemplate("operations-hub/load-ops-dashboard-fragment-2", [viewValue(esc(run.id))])))]);
};
function stateIsTerminal(state){return ['succeeded','failed','cancelled'].includes(state)}

async function loadArtifactRevisions(run,target='runRevisionsPanel',pageIndex=null){
 let view=revisionViews.get(target);if(!view||view.run!==run){view={run,page:0,cursors:[0],items:[]};revisionViews.set(target,view)}if(pageIndex!=null)view.page=pageIndex;
 return hubRead(target,async()=>{
  const data=await api(`/v1/revisions?runId=${hubID(run)}&before=${view.cursors[view.page]||0}&limit=50`);view.items=data.revisions||[];view.cursors[view.page+1]=data.nextBefore;
  return renderPlaceholder("Intermediate observations are immutable evidence, separate from committed final outputs.", "subtle")+hubTable(['Revision','Node / attempt','Recorded at','Availability',''],view.items.map(event=>{
   const ref=event.payload?.snapshot||{};
   return renderViewTemplate("operations-hub/load-artifact-revisions-ops-id", [viewValue(esc(ref.name||'Snapshot')), viewValue(esc(ref.digest||'Unavailable')), viewValue(esc(event.payload?.activationId||'Unavailable')), viewValue(esc(event.subjectId)), viewValue(esc(date(event.timestamp))), viewValue(esc(revisionAvailability.get(ref.digest)||'Not checked')), viewValue(hubButton('view-revision','View',renderViewTemplate("operations-hub/load-artifact-revisions-fragment", [viewValue(target), viewValue(event.seq)]))), viewValue(ref.digest?hubButton('pin-revision','Pin',renderViewTemplate("operations-hub/load-artifact-revisions-fragment-2", [viewValue(esc(ref.digest))]))+hubButton('check-artifact','Check availability',renderViewTemplate("operations-hub/load-artifact-revisions-fragment-2", [viewValue(esc(ref.digest))])):'')]);
  }),'No intermediate revisions recorded')+hubPager(`revisions-${target}`,view.page*50,data.hasMore);
 });
}
const baseOperationalTab=showWbTab;
// @ts-expect-error TS2630: classic scripts intentionally replace this shared view hook.
showWbTab=function(tab){baseOperationalTab(tab);if(tab==='artifacts'&&current)loadArtifactRevisions(current)};
const baseAttemptRowMarkup=attemptRowMarkup;
// @ts-expect-error TS2630: classic scripts intentionally replace this shared view hook.
attemptRowMarkup=function(attempt){const markup=baseAttemptRowMarkup(attempt);return attempt.awaitReconciliationReason?markup.replace(renderViewTemplate("operations-hub/attempt-row-markup-article"),renderViewTemplate("operations-hub/attempt-row-markup-attempt-outcome", [viewValue(hubButton('resolve','Resolve uncertain execution',renderViewTemplate("operations-hub/load-ops-dashboard-fragment", [viewValue(esc(attempt.runId||current)), viewValue(esc(attempt.id))])))])):markup};

document.addEventListener('change',event=>{
 if((event.target as UIElement).id==='operationKind'){hubState.operationOffset=0;loadOpsDashboard()}
 if((event.target as UIElement).id==='compareChangedOnly'&&hubState.comparison){hubState.comparisonOffset=0;hubSet('comparisonResult',comparisonMarkup(hubState.comparison))}
});
document.addEventListener('submit',async event=>{
 const form=(event.target as UIElement);
 if(form.id==='artifactRevisionsForm'){event.preventDefault();await loadArtifactRevisions(form.elements.run.value.trim(),'globalRevisions',0);return;}
 if(form.id==='compareRunsForm'){event.preventDefault();const before=form.elements.before.value.trim(),after=form.elements.after.value.trim();if(before&&after){const hash=`/compare?before=${hubID(before)}&after=${hubID(after)}`;if(currentRoute()===hash)await compareOperationalRuns(before,after);else navigateTo(hash);}return;}
 if(form.id!=='importReportForm')return;
 event.preventDefault();const status=form.querySelector<UIElement>('[role="status"]'),button=form.querySelector<UIElement>('[type="submit"]');button.disabled=true;status.textContent='Importing recorded results…';
 try{const files=[...form.elements.files.files];if(files.reduce((n,f)=>n+f.size,0)>7*1024*1024)throw Error('Choose result files totaling at most 7 MiB.');const docs=await Promise.all(files.map(async f=>hubJSON(await f.text(),f.name))),body=evidenceDocument(docs);(body as any).name=form.elements.name.value.trim();const result=await hubPost('/v1/reports',body);status.textContent='Imported immutable result evidence.';form.reset();await loadEvidence();await openEvidence(result.id);}catch(err){status.textContent=err.message}finally{button.disabled=false}
});
document.addEventListener('click',async event=>{
 const worker=(event.target as UIElement).closest<UIElement>('[data-worker-focus]');if(worker){event.preventDefault();(document.getElementById('worker-roster-'+hubID(worker.dataset.workerFocus)) as UIElement)?.scrollIntoView({block:'center'});return;}
 const tab=(event.target as UIElement).closest<UIElement>('[data-automation-tab]');if(tab){hubState.automation=tab.dataset.automationTab;hubState.automationOffset=0;hubSet('automationHistory','');await loadAutomation();return;}
 const pager=(event.target as UIElement).closest<UIElement>('[data-hub-page]');if(pager&&!pager.disabled){
  const key=pager.dataset.hubPage,offset=Number(pager.dataset.offset);
  if(key==='operations'){hubState.operationOffset=offset;await loadOpsDashboard()}
  else if(key==='retention'){hubState.retentionOffset=offset;await loadRetention()}
  else if(key==='reports'){hubState.reportOffset=offset;await loadEvidence()}
  else if(key==='comparison'){hubState.comparisonOffset=offset;hubSet('comparisonResult',comparisonMarkup(hubState.comparison))}
  else if(key==='deadletters'){hubState.automationOffset=offset;await loadAutomation()}
  else if(key==='automation-history'&&hubState.history){await showAutomationHistory(hubState.history.kind,hubState.history.id,offset)}
  else if(key.startsWith('revisions-')){const target=key.slice('revisions-'.length),view=revisionViews.get(target);if(view)await loadArtifactRevisions(view.run,target,offset/50)}
  else{const page=hubPages.get(key),container=pager.closest<UIElement>('[data-hub-container]');if(page&&container){page.offset=offset;container.innerHTML=page.render(page.items.slice(offset,offset+50))+hubPager(key,offset,offset+50<page.items.length);}}
  return;
 }
 const fork=(event.target as UIElement).closest<UIElement>('#forkRunBtn');if(fork){try{await forkOperationalRun(current)}catch(err){message(err.message,true)}return;}
 const button=(event.target as UIElement).closest<UIElement>('[data-ops-action]');if(!button||button.disabled)return;
 const d=button.dataset;button.disabled=true;
 try{
  switch(d.opsAction){
   case 'refresh-operations':await loadOpsDashboard();break;
   case 'refresh-automation':await loadAutomation();break;
   case 'refresh-retention':await loadRetention();break;
   case 'refresh-evidence':await loadEvidence();break;
   case 'view-revision':{const item=revisionViews.get(d.target)?.items.find(e=>String(e.seq)===d.seq);if(item)showArtifactOutput(item.payload.snapshot,item.payload.snapshot.name);break;}
   case 'pin-revision':addRetentionPin('artifact',d.digest);break;
   case 'check-artifact':{const response=await uiFetch(`/v1/artifacts/${hubID(d.digest)}`,{method:'HEAD'}),label=response.status===200?'Available':response.status===410?'Expired by retention':response.status===404?'Unavailable':'Check failed';revisionAvailability.set(d.digest,label);const field=button.closest<UIElement>('tr')?.querySelector<UIElement>('[data-artifact-availability]');if(field)field.textContent=label;break;}
   case 'view-run':navigateTo(`/runs/${hubID(d.run)}`);break;
   case 'resolve':await resolveOperationalAttempt(d.run||current,d.attempt);break;
   case 'create-automation':await createOperationalAutomation();break;
   case 'automation-history':await showAutomationHistory(d.kind,d.id);break;
   case 'automation-toggle':await hubPost('/v1/automation',{kind:d.kind,id:d.id,enabled:d.enabled==='true'});await loadAutomation();break;
   case 'requeue':hubDialog('Requeue delivery',renderViewTemplate("operations-hub/operations-hub-ops-check"),async()=>{await hubPost(`/v1/subscriptions/deliveries/${hubID(d.delivery)}/requeue`,{});await loadAutomation()},'Requeue delivery');break;
   case 'add-pin':addRetentionPin();break;
   case 'set-retention':setRetentionDeadline();break;
   case 'run-deadline':setRetentionDeadline(d.run);break;
   case 'pin-run':if(d.pinned!=='true'){await hubPost('/v1/retention',{kind:'run',id:d.run,pinned:true});await refreshRetentionContext();break;}d.kind='run';d.id=d.run;
   case 'unpin':hubDialog('Remove retention pin',renderViewTemplate("operations-hub/operations-hub-p", [viewValue(esc(d.id))]),async()=>{await hubPost('/v1/retention',{kind:d.kind,id:d.id,pinned:false});await refreshRetentionContext()},'Remove pin');break;
   case 'compare-current':navigateTo(`/compare?before=${hubID(current)}`);break;
   case 'fork-source':await forkOperationalRun(d.run);break;
   case 'compare-inputs':{const c=hubState.comparison;showArtifactOutput(null,'Inputs and pinned plans',{baseline:{input:c.before.input,plan:c.beforePlan},comparison:{input:c.after.input,plan:c.afterPlan}});break;}
   case 'compare-node':{const row=hubState.comparison.activations[Number(d.nodeIndex)];showArtifactOutput(null,`${row.nodeId} · output comparison`,{baseline:row.before?.outputs,comparison:row.after?.outputs});break;}
   case 'view-report':await openEvidence(d.report);break;
   case 'compare-report':await compareEvidence(d.report);break;
   case 'inspect-evidence-row':{const row=reportRows(hubState.report)[Number(d.evidenceRow)];showArtifactOutput(null,`${row.case} · recorded evidence`,row.raw||row);break;}
   case 'export-report':{const report=hubState.report,blob=new Blob([JSON.stringify(report.document,null,2)],{type:'application/json'}),url=URL.createObjectURL(blob),link=document.createElement('a');link.href=url;link.download=`${report.kind}-${report.id.slice(0,12)}.json`;link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);break;}
  }
 }catch(err){if(err.name!=='AbortError')message(err.message,true)}finally{button.disabled=false}
});
