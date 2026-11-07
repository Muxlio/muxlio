// Operational screens consume coordinator contracts; executor names are opaque.
const hubState={operationOffset:0,automation:'schedules',retentionOffset:0,reportOffset:0,report:null,comparison:null,comparisonOffset:0};
const hubPages=new Map(),hubRequests=new Map();
const revisionViews=new Map(),revisionAvailability=new Map();
const hubID=value=>encodeURIComponent(value);
const hubRun=id=>id?`<a href="#run/${hubID(id)}">${esc(id)}</a>`:'Unavailable';
const hubButton=(action,label,attributes='')=>`<button type="button" class="btn ghost" data-ops-action="${action}" ${attributes}>${label}</button>`;
function hubSet(id,html){const el=document.getElementById(id);if(el)replaceHTMLPreservingDetails(el,html)}
function hubTable(headers,rows,empty='No records'){return rows.length?`<div class="tablebox ops-table"><table><thead><tr>${headers.map(h=>`<th>${esc(h)}</th>`).join('')}</tr></thead><tbody>${rows.join('')}</tbody></table></div>`:`<div class="tablebox empty"><strong>${esc(empty)}</strong></div>`}
function hubMetric(label,value,note=''){return `<div class="ops-metric"><span>${esc(label)}</span><strong>${esc(value)}</strong>${note?`<small>${esc(note)}</small>`:''}</div>`}
function hubPager(key,offset,more){return `<nav class="pager" aria-label="${esc(key)} pages"><span>Page ${Math.floor(offset/50)+1}</span><div class="pager-controls"><button type="button" class="btn ghost" data-hub-page="${esc(key)}" data-offset="${Math.max(0,offset-50)}"${offset===0?' disabled':''}>Previous</button><button type="button" class="btn ghost" data-hub-page="${esc(key)}" data-offset="${offset+50}"${!more?' disabled':''}>Next</button></div></nav>`}
function hubClientPage(key,items,render,offset=0){hubPages.set(key,{items,render,offset});return `<div data-hub-container="${esc(key)}">${render(items.slice(offset,offset+50))+hubPager(key,offset,offset+50<items.length)}</div>`}
function hubStatus(value){return `<span class="state ${esc(stateClass(value)||'queued')}">${esc(value||'Unavailable')}</span>`}
async function hubPost(path,body){return api(path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)})}
async function hubRead(id,work){const n=(hubRequests.get(id)||0)+1;hubRequests.set(id,n);const isCurrent=()=>hubRequests.get(id)===n;try{const html=await work(isCurrent);if(isCurrent())hubSet(id,html)}catch(err){if(isCurrent()&&err.name!=='AbortError')hubSet(id,`<div class="notice error" role="alert">${esc(err.message)}</div>`)}}
function hubJSON(value,label){try{return JSON.parse(value)}catch{throw Error(`${label} must be valid JSON.`)}}
function hubField(label,name,type='text',value='',extra=''){return `<label>${esc(label)}<input name="${name}" type="${type}" value="${esc(value)}" ${extra}></label>`}
function hubText(label,name,value='',extra=''){return `<label>${esc(label)}<textarea name="${name}" rows="5" ${extra}>${esc(value)}</textarea></label>`}
function hubDialog(title,fields,save,submit='Save'){
 const dialog=document.createElement('dialog');dialog.className='ops-dialog';dialog.setAttribute('aria-label',title);
 dialog.innerHTML=`<form class="ops-form"><div class="ops-dialog-head"><h2>${esc(title)}</h2><button type="button" class="btn ghost" data-close-dialog aria-label="Close dialog">Close</button></div>${fields}<p role="status" aria-live="polite"></p><div class="ops-dialog-actions"><button type="button" class="btn ghost" data-close-dialog>Cancel</button><button type="submit" class="btn primary">${submit}</button></div></form>`;
 let busy=false;document.body.append(dialog);dialog.addEventListener('close',()=>dialog.remove(),{once:true});dialog.addEventListener('cancel',event=>{if(busy)event.preventDefault()});dialog.querySelectorAll('[data-close-dialog]').forEach(b=>b.onclick=()=>{if(!busy)dialog.close()});
 const form=dialog.querySelector('form');form.onsubmit=async event=>{event.preventDefault();if(busy)return;const button=form.querySelector('[type="submit"]'),status=form.querySelector('[role="status"]');busy=true;button.disabled=true;dialog.querySelectorAll('[data-close-dialog]').forEach(b=>b.disabled=true);status.textContent='Saving…';try{await save(form);dialog.close()}catch(err){status.textContent=err.message}finally{busy=false;button.disabled=false;dialog.querySelectorAll('[data-close-dialog]').forEach(b=>b.disabled=false)}};
 dialog.showModal();return form;
}

async function loadOpsDashboard(){
 const kind=document.getElementById('operationKind')?.value||'attention',offset=hubState.operationOffset;
 return hubRead('operationsList',async()=>{
  const page=await api(`/v1/operations?kind=${hubID(kind)}&offset=${offset}&limit=50`),c=page.counts||{};
  hubSet('operationsSummary',[['Needs reconciliation',c.attention??0],['Queued work',c.queue??0],['Detached',c.detached??0],['Recovery records',c.recovery??0]].map(([label,n])=>hubMetric(label,n)).join(''));
  return hubTable(['Run / activation','State','Recorded reason','Recorded at',''],(page.items||[]).map(item=>`<tr><td>${hubRun(item.runId)}${item.activationId?`<small class="ops-id">${esc(item.activationId)}</small>`:''}</td><td>${hubStatus(item.state)}</td><td>${esc(item.reason||'No additional reason recorded')}</td><td>${esc(date(item.at))}</td><td>${item.kind==='attention'?hubButton('resolve','Resolve',`data-run="${esc(item.runId)}" data-attempt="${esc(item.attemptId)}"`):''}${hubButton('view-run','View run',`data-run="${esc(item.runId)}"`)}</td></tr>`),'No records in this category')+hubPager('operations',offset,page.hasMore);
 });
}

async function resolveOperationalAttempt(run,id){
 const attempt=await api(`/v1/runs/${hubID(run)}/attempts/${hubID(id)}`);
 const form=hubDialog('Resolve uncertain execution',`<p>${esc(attempt.awaitReconciliationReason||'Verify the external outcome before resolving this attempt.')}</p><label>Verified outcome<select name="outcome"><option value="not-applied">Not applied — the old operation cannot commit later</option><option value="completed">Completed — record the verified result</option></select></label>${hubText('Evidence and reason','reason','','required')}${hubText('Verified executor result JSON (for completed outcomes)','result','','data-completed-result')}<label class="ops-check"><input name="verified" type="checkbox" required>I verified the external outcome and understand this records an operational decision.</label>`,async f=>{
  const outcome=f.elements.outcome.value,body={outcome,reason:f.elements.reason.value.trim()};if(!body.reason)throw Error('Record the evidence used for this decision.');
  if(outcome==='completed'){body.result=hubJSON(f.elements.result.value,'Verified result');if(body.result?.executionSucceeded!==true)throw Error('A completed outcome requires a verified successful executor result.');}
  await hubPost(`/v1/runs/${hubID(run)}/attempts/${hubID(id)}/resolve`,body);if(location.hash==='#operations')await loadOpsDashboard();else if(current===run)await openRun(run);
 },'Record resolution');
 const update=()=>{const field=form.querySelector('[data-completed-result]');field.hidden=form.elements.outcome.value!=='completed';form.elements.result.required=!field.hidden};form.elements.outcome.onchange=update;update();
}

// Include stale workers so lost heartbeats are visible rather than disappearing.
loadWorkers=async function(){
 return hubRead('workerRoster',async()=>{
  const data=await api('/v1/workers?includeStale=true'),workers=data.registeredWorkers||[],assignments=data.workers||[],now=Date.parse(data.serverTime)||Date.now(),windowMs=(data.heartbeatWindowSeconds||90)*1000;
  const live=w=>now-Date.parse(w.lastSeen)<=windowMs;
  const active=assignments.filter(a=>Date.parse(a.leaseUntil)>now);
  const slots=workers.filter(w=>live(w)&&!w.draining).reduce((n,w)=>n+w.capacity,0),occupied=active.filter(a=>workers.some(w=>w.workerId===a.workerId&&live(w)&&!w.draining)).length;
  hubSet('workerCapacitySummary',hubMetric('Live workers',workers.filter(live).length)+hubMetric('Occupied / available capacity',`${occupied} / ${slots}`,`${Math.max(0,slots-occupied)} unoccupied slots on accepting workers`)+hubMetric('Stale workers',workers.filter(w=>!live(w)).length)+hubMetric('Expired active leases',assignments.length-active.length));
  hubSet('workerList',hubTable(['Worker','Run','Activation','Lease','Generation','State'],assignments.map(a=>`<tr><td><a href="#workers" data-worker-focus="${esc(a.workerId)}">${esc(a.workerId)}</a></td><td>${hubRun(a.runId)}</td><td class="ops-id">${esc(a.activationId)}</td><td>${esc(date(a.leaseUntil))}<small>${Date.parse(a.leaseUntil)>now?`${esc(fmtDuration(Date.parse(a.leaseUntil)-now))} remaining`:'Expired'}</small></td><td>${esc(a.generation)}</td><td>${hubStatus(a.state)}</td></tr>`),'No active assignments'));
  return hubTable(['Worker','Health','Slots','Readiness and compatibility','Last heartbeat'],workers.map(w=>{
   const count=active.filter(a=>a.workerId===w.workerId).length,status=!live(w)?'stale':w.draining?'draining':count?'busy':'idle';
   return `<tr id="worker-roster-${hubID(w.workerId)}"><td><strong>${esc(w.workerId)}</strong></td><td>${hubStatus(status)}</td><td>${esc(count)} / ${esc(w.capacity)}${live(w)?`<meter min="0" max="${esc(Math.max(1,w.capacity))}" value="${esc(count)}" aria-label="Occupied worker slots"></meter>`:'<small>Availability unavailable</small>'}</td><td class="worker-capabilities-cell"><div class="worker-capability-list">${(w.capabilities||[]).map(cap=>workerCapabilityMarkup(cap,w.workerId)).join('')||'Capabilities unavailable'}</div></td><td>${esc(date(w.lastSeen))}<small>${esc(fmtDuration(Math.max(0,now-Date.parse(w.lastSeen))))} ago</small></td></tr>`;
  }),'No registered workers');
 });
};

async function loadWorkerCapabilityPage(workerID,name,version=''){
 hubSet('workerCapabilityContent','<p class="subtle">Loading capability…</p>');
 return hubRead('workerCapabilityContent',async()=>{
  const data=await api('/v1/workers?includeStale=true');
  const worker=(data.registeredWorkers||[]).find(w=>w.workerId===workerID);
  if(!worker)throw Error('This worker is no longer registered.');
  const cap=(worker.capabilities||[]).find(c=>(typeof c==='string'?c:c.name)===name&&(typeof c==='string'?'':c.version||'')===version);
  if(!cap)throw Error('This capability is no longer reported by the worker.');
  return `<p class="subtle">Worker <strong>${esc(workerID)}</strong></p>`+workerCapabilityDetailMarkup(cap,workerID).replace('<details ','<details open ');
 });
}

const automationNames={schedules:'Schedules',triggers:'Webhooks',subscriptions:'Subscriptions',deadletters:'Dead letters'};
async function loadAutomation(){
 const kind=hubState.automation;
 const title=document.getElementById('automationTitle'),create=document.getElementById('createAutomation');if(title)title.textContent=automationNames[kind];if(create){create.hidden=kind==='deadletters';create.textContent=`Create ${kind==='schedules'?'schedule':kind==='triggers'?'webhook':'subscription'}`;}
 document.querySelectorAll('[data-automation-tab]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.automationTab===kind)));
 return hubRead('automationList',async isCurrent=>{
  const offset=hubState.automationOffset||0,page=await api(kind==='deadletters'?`/v1/automation/history?kind=deadletters&offset=${offset}&limit=50`:`/v1/${kind}`),items=page[kind==='deadletters'?'records':kind]||[];
  if(!isCurrent())return '';
  const render=rows=>kind==='deadletters'?hubTable(['Subscription','Run','Attempts','Last failure',''],rows.map(d=>`<tr><td>${esc(d.subscriptionId)}</td><td>${hubRun(d.runId)}</td><td>${esc(d.attempts)} / ${esc(d.maxRetries)}</td><td>${esc(d.lastError||'Unavailable')}</td><td>${hubButton('requeue','Review redelivery',`data-delivery="${esc(d.id)}"`)}</td></tr>`),'No dead letters'):hubTable(['Name','Status',kind==='schedules'?'Next occurrence':'Target','Configuration',''],rows.map(item=>{
   const enabled=kind==='schedules'?!item.paused:item.enabled;
   const config=kind==='schedules'?`${item.kind} · ${item.kind==='interval'?MuxlioFormat.duration(item.intervalSeconds,'seconds'):item.cron||date(item.startAt)} · ${item.overlap}`:kind==='triggers'?`Signature: ${item.signatureScheme} · deduplication ${MuxlioFormat.duration(item.deduplicationSeconds,'seconds')}`:(item.eventTypes||[]).join(', ');
   return `<tr><td><strong>${esc(item.id)}</strong>${item.workflowDigest?`<small class="ops-id" title="${esc(item.workflowDigest)}">${esc(item.workflowDigest)}</small>`:''}</td><td>${hubStatus(kind==='schedules'&&enabled&&!item.nextFire?'finished':enabled?'enabled':'paused')}</td><td>${kind==='schedules'?esc(item.nextFire?date(item.nextFire):'No further occurrences'):kind==='triggers'?`<code>/webhooks/${esc(item.id)}</code>`:esc(item.target)}</td><td>${esc(config)}</td><td>${hubButton('automation-history','History',`data-kind="${kind}" data-id="${esc(item.id)}"`)}${hubButton('automation-toggle',enabled?'Pause':'Enable',`data-kind="${kind}" data-id="${esc(item.id)}" data-enabled="${!enabled}"`)}</td></tr>`;
  }),`No ${automationNames[kind].toLowerCase()} configured`);
  return kind==='deadletters'?render(items)+hubPager('deadletters',offset,page.hasMore):hubClientPage(`automation-list-${kind}`,items,render);
 });
}

async function showAutomationHistory(kind,id,offset=0){
 hubState.history={kind,id,offset};
 hubSet('automationHistory','<p class="subtle">Loading history…</p>');
 return hubRead('automationHistory',async()=>{
  const data=await api(`/v1/automation/history?kind=${kind}&id=${hubID(id)}&offset=${offset}&limit=50`),rows=data.records||[];
  return `<div class="section-head"><h2>${esc(id)} · history</h2></div>`+hubTable(['Recorded at','State','Run','Delivery / attempts','Details'],rows.map(row=>`<tr><td>${esc(date(row.timestamp||row.createdAt||row.dueAt))}</td><td>${hubStatus(row.state)}</td><td>${hubRun(row.runId)}</td><td>${esc(row.id||row.idempotencyKey||row.eventType||'—')}${row.attempts!=null?`<small>${esc(row.attempts)} attempts</small>`:''}</td><td>${esc(row.lastError||row.reason||'—')}${row.nextAttemptAt?`<small>Next attempt ${esc(date(row.nextAttemptAt))}</small>`:''}</td></tr>`),'No delivery records')+hubPager('automation-history',offset,data.hasMore);
 });
}

async function createOperationalAutomation(){
 const kind=hubState.automation;if(kind==='deadletters')return;
 const versions=(await api('/v1/workflow-versions')).workflowVersions||[];
 const choices=versions.map(v=>`<option value="${esc(v.digest)}">${esc(v.name||v.digest)} · ${esc(v.digest.slice(0,20))}</option>`).join('');
 if(kind!=='subscriptions'&&!versions.length)throw Error('Register a workflow version before creating automation.');
 const workflow=`<label>Pinned workflow version<select name="workflowDigest" required>${choices}</select></label>`;
 let fields=hubField('Name','id','text','','required maxlength="128" pattern="[A-Za-z0-9_-]+"');
 if(kind==='schedules')fields+=workflow+hubText('Run input JSON','input','{}','required')+`<label>Schedule<select name="kind"><option value="once">One time</option><option value="interval">Recurring interval</option><option value="cron">Cron</option></select></label>`+hubField('Start at','startAt','datetime-local','','required')+`<div data-interval-fields>${hubField('Repeat every','interval','number','1','min="1" step="1"')}<label>Unit<select name="unit"><option value="minutes">Minutes</option><option value="hours">Hours</option><option value="days">Days</option><option value="seconds">Seconds</option></select></label></div><div data-cron-fields>${hubField('Cron expression','cron','text','0 * * * *')}${hubField('Timezone','timezone','text',Intl.DateTimeFormat().resolvedOptions().timeZone||'UTC')}</div><label>When executions overlap<select name="overlap"><option value="skip">Skip overlapping execution</option><option value="queue">Queue it</option><option value="allow">Allow overlap</option></select></label>`+hubField('Maximum catch-up executions','maxCatchUp','number','1','required min="1" max="1000"')+'<p class="subtle">Creating the schedule enables execution at its start time.</p>';
 if(kind==='triggers')fields+=workflow+hubField('Signing secret','secret','password','','required minlength="16" autocomplete="new-password"')+hubText('Input mapping JSON (optional; empty uses the event payload)','mapping')+hubField('Deduplication window, seconds','dedup','number','86400','required min="1" max="2592000"')+'<p class="subtle">Incoming payloads must use the configured HMAC signature. Secrets are write-only.</p>';
 if(kind==='subscriptions')fields+=hubField('Destination URL','target','url','','required placeholder="https://example.com/events"')+hubField('Signing secret','secret','password','','required minlength="16" autocomplete="new-password"')+hubField('Event types, comma separated','events','text','run.succeeded,run.failed','required')+hubField('Maximum retries','retries','number','5','required min="0" max="50"')+hubField('Payload fields to redact, comma separated','redact')+'<p class="subtle">Creating a subscription enables signed deliveries to this destination.</p>';
 const form=hubDialog(`Create ${kind==='schedules'?'schedule':kind==='triggers'?'webhook':'subscription'}`,fields,async f=>{
  const v=f.elements,body={id:v.id.value};
  if(kind==='schedules'){Object.assign(body,{workflowDigest:v.workflowDigest.value,input:hubJSON(v.input.value,'Run input'),kind:v.kind.value,startAt:new Date(v.startAt.value).toISOString(),overlap:v.overlap.value,maxCatchUp:Number(v.maxCatchUp.value)});if(body.kind==='interval')body.intervalSeconds=Number(v.interval.value)*{seconds:1,minutes:60,hours:3600,days:86400}[v.unit.value];if(body.kind==='cron')Object.assign(body,{cron:v.cron.value,timezone:v.timezone.value});}
  if(kind==='triggers'){Object.assign(body,{workflowDigest:v.workflowDigest.value,secret:v.secret.value,deduplicationSeconds:Number(v.dedup.value),enabled:true});if(v.mapping.value.trim())body.inputMapping=hubJSON(v.mapping.value,'Input mapping');}
  if(kind==='subscriptions')Object.assign(body,{target:v.target.value,secret:v.secret.value,eventTypes:v.events.value.split(',').map(s=>s.trim()).filter(Boolean),redactFields:v.redact.value.split(',').map(s=>s.trim()).filter(Boolean),maxRetries:Number(v.retries.value),enabled:true});
  await hubPost(`/v1/${kind}`,body);if(v.secret)v.secret.value='';await loadAutomation();
 },'Create');
 if(kind==='schedules'){
  const local=new Date(Date.now()+60000);local.setMinutes(local.getMinutes()-local.getTimezoneOffset());form.elements.startAt.value=local.toISOString().slice(0,16);
  const update=()=>{form.querySelector('[data-interval-fields]').hidden=form.elements.kind.value!=='interval';form.querySelector('[data-cron-fields]').hidden=form.elements.kind.value!=='cron'};form.elements.kind.onchange=update;update();
 }
}

async function loadRunComparison(){
 const form=document.getElementById('compareRunsForm');if(!form)return;
 const params=new URLSearchParams(location.hash.split('?')[1]||'');
 if(params.has('before'))form.elements.before.value=params.get('before');if(params.has('after'))form.elements.after.value=params.get('after');
 const recent=await api('/v1/runs?limit=200');hubSet('comparisonRunChoices',(recent.runs||[]).map(r=>`<option value="${esc(r.id)}">${esc(r.workflowName||'Workflow')} · ${esc(r.state)}</option>`).join(''));
 if(params.get('before')&&params.get('after'))await compareOperationalRuns(params.get('before'),params.get('after'));
}
async function compareOperationalRuns(before,after){
 hubSet('comparisonResult','<p class="subtle">Comparing recorded histories…</p>');
 return hubRead('comparisonResult',async isCurrent=>{
  const data=await api(`/v1/runs/${hubID(before)}/compare?other=${hubID(after)}`);if(!isCurrent())return '';hubState.comparison=data;hubState.comparisonOffset=0;return comparisonMarkup(data);
 });
}
function comparisonMarkup(data){
 const rows=(data.activations||[]).filter(a=>!document.getElementById('compareChangedOnly')?.checked||a.changed),offset=hubState.comparisonOffset;
 return `<div class="ops-comparison-summary">${hubMetric('Pinned plan',data.planChanged?'Changed':'Same')}${hubMetric('Inputs',data.inputsChanged?'Changed':'Same')}${hubMetric('Results',data.resultsChanged?'Changed':'Same')}${hubMetric('Reused outputs',data.reusedOutputs?.length??0)}</div><div class="ops-toolbar">${hubButton('compare-inputs','Inspect inputs and plans')}${hubButton('fork-source','Fork baseline',`data-run="${esc(data.before.id)}"`)}<label class="ops-check"><input id="compareChangedOnly" type="checkbox"${document.getElementById('compareChangedOnly')?.checked?' checked':''}>Changed nodes only</label></div><div class="ops-comparison-columns">${[data.before,data.after].map((run,i)=>`<section class="ops-comparison-side"><h2>${i?'Comparison':'Baseline'} · ${hubRun(run.id)}</h2><p>${hubStatus(run.state)} · ${esc(MuxlioFormat.elapsed(run.createdAt,run.updatedAt))} elapsed</p><div class="usage-grid">${usageCardMarkup('Tokens',i?data.afterUsage?.tokens:data.beforeUsage?.tokens,fmtTokens)}${usageCardMarkup('Cost',i?data.afterUsage?.costUsd:data.beforeUsage?.costUsd,n=>'$'+Number(n).toFixed(4))}</div></section>`).join('')}</div><div class="section-head"><h2>Node outcomes · ${rows.length} matching</h2></div>`+hubTable(['Node / scope','Baseline','Comparison','Change',''],rows.slice(offset,offset+50).map(a=>`<tr><td><strong>${esc(a.nodeId)}</strong><small>${esc(a.scopePath?.join(' / ')||'root')}</small></td><td>${hubStatus(a.before?.state)}</td><td>${hubStatus(a.after?.state)}</td><td>${a.changed?'Changed':'Same'}</td><td>${hubButton('compare-node','Inspect outputs',`data-node-index="${data.activations.indexOf(a)}"`)}</td></tr>`),'No matching node outcomes')+hubPager('comparison',offset,offset+50<rows.length);
}
async function forkOperationalRun(id){
 const [run,graph,registry]=await Promise.all([api(`/v1/runs/${hubID(id)}`),api(`/v1/runs/${hubID(id)}/graph`),api('/v1/workflow-versions')]);
 const versions=registry.workflowVersions||[],candidates=(graph.activations||[]).filter(a=>a.state==='succeeded');
 const fields=`<p>Create a separate run from ${hubRun(id)}. The original history remains intact.</p><label>Pinned workflow<select name="planDigest"><option value="">Original pinned plan</option>${versions.map(v=>`<option value="${esc(v.digest)}">${esc(v.name||v.digest)} · ${esc(v.digest.slice(0,20))}</option>`).join('')}</select></label>${hubText('Fork input JSON','input',JSON.stringify(run.input??null,null,2),'required')}<details><summary>Reuse outputs (optional)</summary><p class="subtle">The coordinator validates that selected outputs are pure, compatible, and reusable.</p><div class="ops-reuse-list">${candidates.map(a=>`<label class="ops-check"><input type="checkbox" name="reuse" value="${esc(a.id)}">${esc(a.nodeId)} · ${esc(a.scopePath?.join(' / ')||'root')}</label>`).join('')||'<p>No completed nodes.</p>'}</div></details><label class="ops-check"><input type="checkbox" name="start">Start the fork after creating it</label>`;
 hubDialog('Fork run',fields,async f=>{
  const body={input:hubJSON(f.elements.input.value,'Fork input'),reuseActivations:[...f.querySelectorAll('[name="reuse"]:checked')].map(x=>x.value),start:f.elements.start.checked};
  if(f.elements.planDigest.value)body.planDigest=f.elements.planDigest.value;
  const result=await hubPost(`/v1/runs/${hubID(id)}/fork`,body);location.hash=`#run/${hubID(result.runId)}`;
 },'Create fork');
}

async function loadRetention(){
 const offset=hubState.retentionOffset;
 return hubRead('retentionPins',async()=>{
  const page=await api(`/v1/retention?offset=${offset}&limit=50`),policy=page.policy||{};
  hubSet('retentionPolicy',[['Events',policy.eventHistory],['Worker output',policy.stdoutStderr],['Artifacts',policy.artifacts],['Plan cache',policy.caches],['Run workspaces',policy.workspaces]].map(([label,n])=>hubMetric(label,n===0?'No automatic expiry':n==null?'Unavailable':fmtDuration(n/1e6),'Default retention after run completion')).join(''));
  return `<p class="subtle">${esc(page.total??0)} explicit pins. Automatic cleanup checks every 15 minutes when the runtime is idle. Active, pinned, and uncertain work stays protected.</p>`+hubTable(['Kind','Pinned identity',''],(page.pins||[]).map(p=>`<tr><td>${esc(p.kind)}</td><td class="ops-id">${p.kind==='run'?hubRun(p.id):esc(p.id)}</td><td>${hubButton('unpin','Review unpin',`data-kind="${esc(p.kind)}" data-id="${esc(p.id)}"`)}</td></tr>`),'No explicit pins')+hubPager('retention',offset,page.hasMore);
 });
}
function addRetentionPin(kind='run',id=''){
 hubDialog('Pin data',`<label>Data kind<select name="kind">${['run','plan','artifact'].map(k=>`<option${k===kind?' selected':''}>${k}</option>`).join('')}</select></label>${hubField('Run ID or content digest','id','text',id,'required')}<p class="subtle">Pins protect the selected data and reachable references from cleanup.</p>`,async f=>{await hubPost('/v1/retention',{kind:f.elements.kind.value,id:f.elements.id.value.trim(),pinned:true});await refreshRetentionContext()},'Pin');
}
function setRetentionDeadline(id=''){
 const form=hubDialog('Set run retention deadline',`${hubField('Run ID','id','text',id,'required')}${hubField('Retain until','until','datetime-local')}<p class="subtle">Leave the deadline empty to use the default retention policy. A pinned run remains protected.</p><div data-retention-current></div>`,async f=>{await hubPost('/v1/retention',{kind:'run',id:f.elements.id.value.trim(),retainedUntil:f.elements.until.value?new Date(f.elements.until.value).toISOString():null});await refreshRetentionContext()},'Set deadline');
 const preview=async()=>{const key=form.elements.id.value.trim();if(!key)return;try{const run=await api(`/v1/runs/${hubID(key)}?summary=true`);if(form.elements.id.value.trim()!==key)return;form.querySelector('[data-retention-current]').textContent=`Current state: ${run.state}. ${run.pinned?'Pinned. ':''}${run.retainedUntil?'Current deadline: '+date(run.retainedUntil):'Default retention policy.'}`;if(run.retainedUntil){const d=new Date(run.retainedUntil);d.setMinutes(d.getMinutes()-d.getTimezoneOffset());form.elements.until.value=d.toISOString().slice(0,16)}}catch(err){form.querySelector('[data-retention-current]').textContent=err.message}};
 form.elements.id.onchange=preview;if(id)preview();
}
async function refreshRetentionContext(){if(location.hash==='#retention')await loadRetention();else if(current)await openRun(current)}

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
  return hubTable(['Report','Type','Imported',''],(page.reports||[]).map(r=>`<tr><td><strong>${esc(r.name)}</strong><small class="ops-id">${esc(r.id)}</small></td><td>${esc(r.kind)}</td><td>${esc(date(r.importedAt))}</td><td>${hubButton('view-report','Inspect',`data-report="${esc(r.id)}"`)}${hubButton('compare-report','Compare with open report',`data-report="${esc(r.id)}"`)}</td></tr>`),'No imported evidence')+hubPager('reports',offset,page.hasMore);
 });
}
function reportRows(report){
 const doc=report.document;
 if(report.kind==='evaluation')return (doc.results||[]).map(row=>({group:row.versionId,case:row.caseId,correct:row.execution?.success===true,expected:1,collected:1,duration:row.durationMs,state:row.execution?.state,runId:row.execution?.runId,scores:row.scores,usage:row.execution?.usage,error:row.error||row.outputError||row.execution?.error,output:row.execution?.output}));
 if(Array.isArray(doc.observations))return doc.observations.map(row=>({group:doc.manifest?.revision||'Recorded configuration',case:`${row.case} / repetition ${row.repetition}`,correct:row.correct,expected:1,collected:1,duration:row.wallTimeMs,state:row.correct?'correct':'failed',runId:row.run?.id,usage:row.usage,error:row.error,queue:row.queue,raw:row}));
 return (doc.configurations||[]).flatMap((config,i)=>(config.cases||[]).map(row=>({group:`Configuration ${i+1}`,case:row.case,correct:row.correct,expected:row.planned,collected:row.collected,duration:row.correctWallMedianMs,state:`${row.correct} correct / ${row.collected} collected`,usage:{costUsd:{observed:row.costUsd?.observedSubtotal,estimated:row.costUsd?.estimatedSubtotal,unknown:row.costUsd?.unknownAttempts}},raw:row,manifest:config.manifest})));
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
 return facts.length?`<details><summary>Recorded checks &amp; timing</summary><dl class="contract-fields">${facts.map(([label,value])=>`<div><dt>${esc(label)}</dt><dd>${esc(value)}</dd></div>`).join('')}</dl></details>`:'';
}
function reportProvenance(report){
 const doc=report.document,manifests=doc.manifest?[doc.manifest]:(doc.configurations||[]).map(c=>c.manifest);
 return manifests.map((m,i)=>`<details class="execution-contract"><summary>${manifests.length>1?`Configuration ${i+1} · `:''}Provenance</summary><dl class="contract-fields">${[['Dataset',m.datasetId],['Dataset digest',m.datasetDigest],['Suite',m.suite],['Source revision',m.revision||m.gitRevision||m.git?.revision],['Concurrency',m.concurrency],['Worker capacity',m.capacity],['Runner',m.runnerId],['Scorers',m.scorerIds?.join(', ')],['Pricing basis',m.costBasis],['Configuration digest',m.configurationDigest],['Map concurrency',m.mapConcurrency],['Platform',[m.os,m.arch].filter(Boolean).join(' / ')||null],['Models',m.metadata?.models],['Versions',m.metadata?.versions],['Machine',m.metadata?.machine],['Pricing basis',m.metadata?.pricingBasis],['Source hashes',m.sourceHashes]].filter(([,v])=>v!=null).map(([k,v])=>`<div><dt>${esc(k)}</dt><dd>${esc(typeof v==='object'?JSON.stringify(v):v)}</dd></div>`).join('')}</dl>${m.versions?`<ul>${m.versions.map(v=>`<li>${esc(v.id)} · <code>${esc(v.planDigest)}</code></li>`).join('')}</ul>`:''}</details>`).join('');
}
function evidenceMarkup(report){
 const rows=reportRows(report),groups=new Map();
 if(report.kind==='evaluation')for(const version of report.document.manifest.versions||[])groups.set(version.id,{collected:0,correct:0,expected:0,durations:[]});
 for(const row of rows){if(!groups.has(row.group))groups.set(row.group,{collected:0,correct:0,expected:0,durations:[]});const g=groups.get(row.group);g.collected+=row.collected??0;if(row.expected==null)g.expectedKnown=false;g.expected+=row.expected??0;g.correct+=typeof row.correct==='boolean'?Number(row.correct):row.correct??0;if(typeof row.duration==='number'&&Number.isFinite(row.duration)&&row.duration>=0)g.durations.push(row.duration);}
 if(report.document.observations){const m=report.document.manifest,group=m.revision||'Recorded configuration';if(!groups.has(group))groups.set(group,{collected:0,correct:0,expected:0,durations:[]});groups.get(group).expected=Array.isArray(m.cases)&&Number.isInteger(m.repetitions)?m.cases.length*m.repetitions:null;}
 const summary=[...groups].map(([name,g])=>`<section class="ops-evidence-group"><h3>${esc(name)}</h3><div class="ops-metrics">${hubMetric('Correct / collected',`${g.correct} / ${g.collected}`)}${hubMetric('Missing cases',report.kind==='evaluation'||g.expectedKnown===false||g.expected==null?'Unavailable':Math.max(0,g.expected-g.collected),report.kind==='evaluation'?'Expected case identities are not included in this manifest':'')}${hubMetric(report.kind==='evaluation'?'Mean duration (all outcomes)':report.document.configurations?'Mean of case medians':'Mean duration (all outcomes)',g.durations.length?fmtDuration(g.durations.reduce((a,b)=>a+b,0)/g.durations.length):'Unavailable',`${g.durations.length} duration measurements`)}</div></section>`).join('');
 const render=items=>hubTable(['Version / configuration','Case','Outcome','Duration','Scores / usage',''],items.map(row=>{
  const scores=Object.entries(row.scores||{}).map(([id,s])=>`${id}: ${typeof s?.value!=='number'||!Number.isFinite(s.value)?'Unavailable':s.value}${s?.error?' (scorer failed)':''}`).join(' · ');
  return `<tr><td>${esc(row.group)}</td><td>${esc(row.case)}</td><td>${hubStatus(row.state)}${row.error?`<small>${esc(row.error)}</small>`:''}${row.runId?`<small>${hubRun(row.runId)}</small>`:''}</td><td>${esc(fmtDuration(row.duration))}${benchmarkFacts(row)}</td><td>${esc(scores||'No scores reported')}${row.usage?`<small>Tokens: ${esc(hubUsage(row.usage.tokens))}</small><small>Cost: ${esc(hubUsage(row.usage.costUsd,true))}</small>`:''}</td><td>${hubButton('inspect-evidence-row','Inspect',`data-evidence-row="${rows.indexOf(row)}"`)}</td></tr>`;
 }), 'No result rows');
 return `<div class="section-head"><h2>${esc(report.name)}</h2>${hubButton('export-report','Download original result JSON')}</div><p class="subtle">Recorded evidence only. Different datasets, machines, models, or configurations do not establish a speedup. Failed and missing measurements remain visible.</p>${reportProvenance(report)}${summary}<div id="evidenceRows">${hubClientPage('evidence-rows',rows,render)}</div>`;
}
function hubUsage(value,money=false){if(!value)return 'Unavailable';const f=n=>typeof n!=='number'||!Number.isFinite(n)||n<0?'Unavailable':money?'$'+n.toFixed(4):fmtTokens(n);return `${f(value.observed)} observed · ${f(value.estimated)} estimated · ${Number.isInteger(value.unknown)&&value.unknown>=0?value.unknown:'Unavailable'} unreported`}
async function openEvidence(id){
 return hubRead('evidenceDetail',async isCurrent=>{const report=await api(`/v1/reports/${hubID(id)}`);if(!isCurrent())return '';hubState.report=report;return evidenceMarkup(report)});
}
async function compareEvidence(id){
 if(!hubState.report)throw Error('Open a baseline report first.');
 const baseline=hubState.report,other=await api(`/v1/reports/${hubID(id)}`);if(baseline.kind!==other.kind)throw Error('Compare reports of the same type.');
 if(Boolean(baseline.document.configurations)!==Boolean(other.document.configurations))throw Error('Compare reports at the same aggregation level.');
 const left=reportRows(baseline),right=reportRows(other),leftGroups=[...new Set(left.map(r=>r.group))],rightGroups=[...new Set(right.map(r=>r.group))];
 const form=hubDialog('Compare recorded evidence',`<p>${esc(baseline.name)} → ${esc(other.name)}</p><p class="subtle">Choose recorded versions or configurations. Cases align by identity; missing counterparts stay unavailable. Dataset and environment differences remain part of the evidence.</p><div class="ops-comparison-columns"><label>Baseline version<select name="baseline">${leftGroups.map(g=>`<option>${esc(g)}</option>`).join('')}</select></label><label>Comparison version<select name="candidate">${rightGroups.map(g=>`<option>${esc(g)}</option>`).join('')}</select></label></div><details><summary>Both provenance records</summary><h3>Baseline</h3>${reportProvenance(baseline)}<h3>Comparison</h3>${reportProvenance(other)}</details><div data-evidence-comparison></div>`,async()=>{},'Done');
 const render=()=>{
  const leftMap=new Map(left.filter(r=>r.group===form.elements.baseline.value).map(r=>[r.case,r])),rightMap=new Map(right.filter(r=>r.group===form.elements.candidate.value).map(r=>[r.case,r]));
  const keys=[...new Set([...leftMap.keys(),...rightMap.keys()])],aligned=keys.map(k=>({left:leftMap.get(k),right:rightMap.get(k)}));
  form.querySelector('[data-evidence-comparison]').innerHTML=hubClientPage('evidence-comparison',aligned,items=>hubTable(['Case','Baseline outcome','Comparison outcome','Baseline duration','Comparison duration'],items.map(({left:a,right:b})=>`<tr><td>${esc((a||b).case)}</td><td>${esc(a?.state??'Unavailable')}</td><td>${esc(b?.state??'Unavailable')}</td><td>${esc(fmtDuration(a?.duration))}</td><td>${esc(fmtDuration(b?.duration))}</td></tr>`)));
 };form.elements.baseline.onchange=render;form.elements.candidate.onchange=render;render();
}

const baseOperationalRun=renderRunOperations;
renderRunOperations=function(run){
 baseOperationalRun(run);
 const fork=document.getElementById('forkRunBtn');if(fork)fork.hidden=false;
 const deadline=run.deadlineSeconds?Date.parse(run.createdAt)+run.deadlineSeconds*1000:null,remaining=deadline==null?'No deadline':fmtDuration(Math.max(0,deadline-Date.now()));
 const host=document.getElementById('runOperationalControls');
 if(host)host.innerHTML=`<div class="ops-metrics">${hubMetric('Deadline remaining',stateIsTerminal(run.state)?'Run finished':remaining,deadline?date(deadline):'')}${hubMetric('Attempts / limit',`${run.overview?.attempts??0} / ${run.limits?.maxAttempts??'Unlimited'}`)}${hubMetric('Activations / limit',`${run.overview?.activations??0} / ${run.limits?.maxActivations??'Unlimited'}`)}${hubMetric('Retention',run.pinned?'Pinned':run.retainedUntil?date(run.retainedUntil):'Default policy')}</div><div class="ops-toolbar">${hubButton('pin-run',run.pinned?'Review unpin':'Pin run',`data-run="${esc(run.id)}" data-pinned="${run.pinned?'true':'false'}"`)}${hubButton('run-deadline','Retention deadline',`data-run="${esc(run.id)}"`)}</div>`;
};
function stateIsTerminal(state){return ['succeeded','failed','cancelled'].includes(state)}

async function loadArtifactRevisions(run,target='runRevisionsPanel',pageIndex=null){
 let view=revisionViews.get(target);if(!view||view.run!==run){view={run,page:0,cursors:[0],items:[]};revisionViews.set(target,view)}if(pageIndex!=null)view.page=pageIndex;
 return hubRead(target,async()=>{
  const data=await api(`/v1/revisions?runId=${hubID(run)}&before=${view.cursors[view.page]||0}&limit=50`);view.items=data.revisions||[];view.cursors[view.page+1]=data.nextBefore;
  return '<p class="subtle">Intermediate observations are immutable evidence, separate from committed final outputs.</p>'+hubTable(['Revision','Node / attempt','Recorded at','Availability',''],view.items.map(event=>{
   const ref=event.payload?.snapshot||{};
   return `<tr><td><strong>${esc(ref.name||'Snapshot')}</strong><small class="ops-id">${esc(ref.digest||'Unavailable')}</small></td><td>${esc(event.payload?.activationId||'Unavailable')}<small>${esc(event.subjectId)}</small></td><td>${esc(date(event.timestamp))}</td><td><span data-artifact-availability>${esc(revisionAvailability.get(ref.digest)||'Not checked')}</span></td><td>${hubButton('view-revision','View',`data-target="${target}" data-seq="${event.seq}"`)}${ref.digest?hubButton('pin-revision','Pin',`data-digest="${esc(ref.digest)}"`)+hubButton('check-artifact','Check availability',`data-digest="${esc(ref.digest)}"`):''}</td></tr>`;
  }),'No intermediate revisions recorded')+hubPager(`revisions-${target}`,view.page*50,data.hasMore);
 });
}
const baseOperationalTab=showWbTab;
showWbTab=function(tab){baseOperationalTab(tab);if(tab==='artifacts'&&current)loadArtifactRevisions(current)};
const baseAttemptRowMarkup=attemptRowMarkup;
attemptRowMarkup=function(attempt){const markup=baseAttemptRowMarkup(attempt);return attempt.state==='awaiting_reconciliation'?markup.replace('</article>',`<div class="attempt-outcome">${hubButton('resolve','Resolve uncertain execution',`data-run="${esc(attempt.runId||current)}" data-attempt="${esc(attempt.id)}"`)}</div></article>`):markup};

document.addEventListener('change',event=>{
 if(event.target.id==='operationKind'){hubState.operationOffset=0;loadOpsDashboard()}
 if(event.target.id==='compareChangedOnly'&&hubState.comparison){hubState.comparisonOffset=0;hubSet('comparisonResult',comparisonMarkup(hubState.comparison))}
});
document.addEventListener('submit',async event=>{
 const form=event.target;
 if(form.id==='artifactRevisionsForm'){event.preventDefault();await loadArtifactRevisions(form.elements.run.value.trim(),'globalRevisions',0);return;}
 if(form.id==='compareRunsForm'){event.preventDefault();const before=form.elements.before.value.trim(),after=form.elements.after.value.trim();if(before&&after){const hash=`#compare?before=${hubID(before)}&after=${hubID(after)}`;if(location.hash===hash)await compareOperationalRuns(before,after);else location.hash=hash;}return;}
 if(form.id!=='importReportForm')return;
 event.preventDefault();const status=form.querySelector('[role="status"]'),button=form.querySelector('[type="submit"]');button.disabled=true;status.textContent='Importing recorded results…';
 try{const files=[...form.elements.files.files];if(files.reduce((n,f)=>n+f.size,0)>7*1024*1024)throw Error('Choose result files totaling at most 7 MiB.');const docs=await Promise.all(files.map(async f=>hubJSON(await f.text(),f.name))),body=evidenceDocument(docs);body.name=form.elements.name.value.trim();const result=await hubPost('/v1/reports',body);status.textContent='Imported immutable result evidence.';form.reset();await loadEvidence();await openEvidence(result.id);}catch(err){status.textContent=err.message}finally{button.disabled=false}
});
document.addEventListener('click',async event=>{
 const worker=event.target.closest('[data-worker-focus]');if(worker){event.preventDefault();document.getElementById('worker-roster-'+hubID(worker.dataset.workerFocus))?.scrollIntoView({block:'center'});return;}
 const tab=event.target.closest('[data-automation-tab]');if(tab){hubState.automation=tab.dataset.automationTab;hubState.automationOffset=0;hubSet('automationHistory','');await loadAutomation();return;}
 const pager=event.target.closest('[data-hub-page]');if(pager&&!pager.disabled){
  const key=pager.dataset.hubPage,offset=Number(pager.dataset.offset);
  if(key==='operations'){hubState.operationOffset=offset;await loadOpsDashboard()}
  else if(key==='retention'){hubState.retentionOffset=offset;await loadRetention()}
  else if(key==='reports'){hubState.reportOffset=offset;await loadEvidence()}
  else if(key==='comparison'){hubState.comparisonOffset=offset;hubSet('comparisonResult',comparisonMarkup(hubState.comparison))}
  else if(key==='deadletters'){hubState.automationOffset=offset;await loadAutomation()}
  else if(key==='automation-history'&&hubState.history){await showAutomationHistory(hubState.history.kind,hubState.history.id,offset)}
  else if(key.startsWith('revisions-')){const target=key.slice('revisions-'.length),view=revisionViews.get(target);if(view)await loadArtifactRevisions(view.run,target,offset/50)}
  else{const page=hubPages.get(key),container=pager.closest('[data-hub-container]');if(page&&container){page.offset=offset;container.innerHTML=page.render(page.items.slice(offset,offset+50))+hubPager(key,offset,offset+50<page.items.length);}}
  return;
 }
 const fork=event.target.closest('#forkRunBtn');if(fork){try{await forkOperationalRun(current)}catch(err){message(err.message,true)}return;}
 const button=event.target.closest('[data-ops-action]');if(!button||button.disabled)return;
 const d=button.dataset;button.disabled=true;
 try{
  switch(d.opsAction){
   case 'refresh-operations':await loadOpsDashboard();break;
   case 'refresh-automation':await loadAutomation();break;
   case 'refresh-retention':await loadRetention();break;
   case 'refresh-evidence':await loadEvidence();break;
   case 'view-revision':{const item=revisionViews.get(d.target)?.items.find(e=>String(e.seq)===d.seq);if(item)showArtifactOutput(item.payload.snapshot,item.payload.snapshot.name);break;}
   case 'pin-revision':addRetentionPin('artifact',d.digest);break;
   case 'check-artifact':{const response=await uiFetch(`/v1/artifacts/${hubID(d.digest)}`,{method:'HEAD'}),label=response.status===200?'Available':response.status===410?'Expired by retention':response.status===404?'Unavailable':'Check failed';revisionAvailability.set(d.digest,label);const field=button.closest('tr')?.querySelector('[data-artifact-availability]');if(field)field.textContent=label;break;}
   case 'view-run':location.hash=`#run/${hubID(d.run)}`;break;
   case 'resolve':await resolveOperationalAttempt(d.run||current,d.attempt);break;
   case 'create-automation':await createOperationalAutomation();break;
   case 'automation-history':await showAutomationHistory(d.kind,d.id);break;
   case 'automation-toggle':await hubPost('/v1/automation',{kind:d.kind,id:d.id,enabled:d.enabled==='true'});await loadAutomation();break;
   case 'requeue':hubDialog('Requeue delivery','<p>This will attempt delivery again to the subscription’s configured destination.</p><label class="ops-check"><input type="checkbox" required>I want this delivery sent again.</label>',async()=>{await hubPost(`/v1/subscriptions/deliveries/${hubID(d.delivery)}/requeue`,{});await loadAutomation()},'Requeue delivery');break;
   case 'add-pin':addRetentionPin();break;
   case 'set-retention':setRetentionDeadline();break;
   case 'run-deadline':setRetentionDeadline(d.run);break;
   case 'pin-run':if(d.pinned!=='true'){await hubPost('/v1/retention',{kind:'run',id:d.run,pinned:true});await refreshRetentionContext();break;}d.kind='run';d.id=d.run;
   case 'unpin':hubDialog('Remove retention pin',`<p>Remove the pin on <code>${esc(d.id)}</code>? The data becomes eligible for the existing retention policy; this action does not delete it immediately.</p>`,async()=>{await hubPost('/v1/retention',{kind:d.kind,id:d.id,pinned:false});await refreshRetentionContext()},'Remove pin');break;
   case 'compare-current':location.hash=`#compare?before=${hubID(current)}`;break;
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
