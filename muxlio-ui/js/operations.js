// Run operations use the shared executor contract. States and capabilities are
// shown as reported, independently of the adapter providing them.
let operationsOffset=0,operationsRun=null,operationsRequest=0;
function usageCardMarkup(label,value,format){
 const rows=[['Observed',value?.observed],['Estimated',value?.estimated]];
 return `<section class="usage-card"><h3>${esc(label)}</h3><dl>${rows.map(([name,total])=>`<div><dt>${name}</dt><dd>${esc(total==null?'Unavailable':format(total))}</dd></div>`).join('')}<div class="usage-coverage"><dt>Unreported attempts</dt><dd>${esc(value?.unknown??'Unavailable')}</dd></div></dl></section>`;
}
function attemptRowMarkup(a){
 const validation=a.result?.validationFailure,outcome=a.awaitReconciliationReason||validation?.message||a.failure||(a.result?.executionSucceeded?'Executor completed':'');
 const token=(name,label)=>`<div><dt>${label}</dt><dd>${esc(a.usage?.[name]==null?'Unavailable':fmtTokens(a.usage[name]))}</dd></div>`;
 return `<article class="attempt-card"><div class="attempt-identity"><strong>${esc(a.executor||'Executor unavailable')}</strong><span class="attempt-id" title="${esc(a.id)}">${esc(a.id)}</span><span class="subtle">${esc(date(a.startedAt))} · attempt ${esc(a.retryNumber??'—')}</span></div><div class="attempt-status"><span class="state ${esc(stateClass(a.state))}">${esc(a.state)}</span><span class="subtle">${esc(a.effect||'Effect policy unavailable')}</span></div><dl class="attempt-metrics">${token('input','Input')}${token('output','Output')}${token('cached','Cached')}<div><dt>Duration</dt><dd>${esc(a.executionDurationMs==null?'Unavailable':fmtDuration(a.executionDurationMs))}</dd></div></dl><div class="attempt-actions"><button class="btn ghost" data-inspect-operation="${esc(a.activationId)}">View node</button><button class="btn ghost" data-attempt-details="${esc(a.id)}">Details</button></div>${outcome||a.operationRef?`<div class="attempt-outcome">${validation?'<span class="attempt-validation">Output validation failed</span>':''}${outcome?`<p>${esc(outcome)}</p>`:''}${a.operationRef?`<span class="subtle">Remote task <code>${esc(a.operationRef.id)}</code>${a.operationRef.pollAt?` · next poll ${esc(date(a.operationRef.pollAt))}`:''}</span>`:''}</div>`:''}</article>`;
}
function renderRunOperations(run){
 if(operationsRun?.id!==run.id)operationsOffset=0;
 operationsRun=run;
 const overview=run.overview||{},states=overview.attemptStates||{};
 const attention=document.getElementById('runAttention');
 if(attention){
  const held=overview.heldAttempts||0,detached=states.detached||0;
  attention.hidden=!held&&!detached&&!run.error;
  attention.innerHTML=`${held?`<p><strong>${held} attempts need reconciliation.</strong> Review their external outcome before retrying.</p>`:''}${detached?`<p>${detached} remote operations are detached. Waiting does not occupy a worker slot.</p>`:''}${run.error?`<p class="insp-error">${esc(run.error)}</p>`:''}<button class="btn ghost" data-open-operations>Inspect operations</button>`;
 }
 const panel=document.getElementById('runContract');if(!panel)return;
 const limits=run.limits||{};
 const rows=[['Plan version',run.planDigest||'Unavailable'],['End-to-end deadline',run.deadlineSeconds?date(new Date(Date.parse(run.createdAt)+run.deadlineSeconds*1000)):'No deadline'],['Attempts',`${overview.attempts??0}${limits.maxAttempts!=null?' / '+limits.maxAttempts:''}`],['Activations',`${overview.activations??0}${limits.maxActivations!=null?' / '+limits.maxActivations:''}`],['Active attempt limit',limits.maxActiveAttempts??'Unlimited'],['Token limit',limits.maxTokens!=null?fmtTokens(limits.maxTokens):'Unlimited'],['Cost limit',limits.maxCostUsd!=null?'$'+limits.maxCostUsd:'Unlimited'],['Missing usage policy',limits.missingUsage||'fail-closed'],['Retention',run.pinned?'Pinned':run.retainedUntil?`Retained until ${date(run.retainedUntil)}`:'Service retention policy']];
 const markup=`<div class="operations-summary"><div class="operations-heading"><div><h2>Usage</h2><p class="subtle">Totals across ${esc(overview.attempts??0)} attempts. Unreported usage is unavailable.</p></div><div class="operation-states">${Object.entries(states).map(([state,count])=>`<span class="state ${esc(stateClass(state))}">${esc(state)} · ${esc(count)}</span>`).join('')}</div></div><div class="usage-grid">${usageCardMarkup('Tokens',overview.usage?.tokens,fmtTokens)}${usageCardMarkup('Cost',overview.usage?.costUsd,n=>'$'+Number(n).toFixed(4))}</div><details class="execution-contract" data-preserve-key="execution-contract"><summary>Execution settings <span>Limits, retention and pinned plan</span></summary><dl class="contract-fields">${rows.map(([label,value])=>`<div><dt>${esc(label)}</dt><dd>${esc(value)}</dd></div>`).join('')}</dl>${run.fork?`<p>Forked from <a href="#run/${encodeURIComponent(run.fork.sourceRunId)}">${esc(run.fork.sourceRunId)}</a> · ${run.fork.reuseActivations?.length||0} selected reuse candidates</p>`:''}</details></div>`;
 if(typeof replaceHTMLPreservingDetails==="function")replaceHTMLPreservingDetails(panel,markup);else panel.innerHTML=markup;
 if(typeof wbActiveTab!=='undefined'&&wbActiveTab==='operations')loadRunOperations();
}
async function loadRunOperations(){
 if(!operationsRun)return;
 const id=operationsRun.id,offset=operationsOffset,request=++operationsRequest;
 const panel=document.getElementById('runAttemptsPanel');if(!panel)return;
 try{
  const page=await api(`/v1/runs/${encodeURIComponent(id)}/attempts?offset=${offset}&limit=50&details=false`);
  if(id!==current||request!==operationsRequest)return;
  const attempts=page.attempts||[];
  panel.innerHTML=runPager('attempts',offset,page.hasMore,'Attempts')+(attempts.length?`<div class="attempt-list">${attempts.map(attemptRowMarkup).join('')}</div>`:'<div class="tablebox empty"><strong>No execution attempts</strong><p>Attempts will appear here when execution starts.</p></div>');
 }catch(err){if(id===current&&request===operationsRequest)panel.innerHTML=`<p class="notice error">${esc(err.message)}</p>`;}
}
document.addEventListener('click',event=>{
 if(event.target.closest('[data-open-operations]'))showWbTab('operations');
 const button=event.target.closest('[data-inspect-operation]');
 if(button){showWbTab('graph');loadActivation(button.dataset.inspectOperation);}
 const pager=event.target.closest('[data-run-page="attempts"]');
 if(pager&&!pager.disabled){operationsOffset=Math.max(0,operationsOffset+Number(pager.dataset.direction)*50);loadRunOperations();}
});

async function openAttemptDetails(id){
 const run=current;
 try{const attempt=await api(`/v1/runs/${encodeURIComponent(run)}/attempts/${encodeURIComponent(id)}`);if(current===run)showArtifactOutput(attempt,`Attempt ${id}`);}
 catch(err){if(current===run)message(err.message,true);}
}
function attemptMetadataMarkup(attempts){
 return (attempts||[]).map(a=>`<details class="attempt-contract"><summary>${esc(a.executor)} · ${esc(a.state)}</summary><dl class="insp-kv"><dt>Attempt</dt><dd>${esc(a.id)}</dd><dt>Effect policy</dt><dd>${esc(a.effect||'Unspecified')}</dd>${a.operationRef?`<dt>Remote task</dt><dd>${esc(a.operationRef.id)}</dd>`:''}${a.awaitReconciliationReason?`<dt>Reconciliation</dt><dd>${esc(a.awaitReconciliationReason)}</dd>`:''}</dl>${a.result?.validationFailure?`<p class="insp-error">${esc(a.result.validationFailure.code)} · ${esc(a.result.validationFailure.path)} · ${esc(a.result.validationFailure.message)}</p>`:''}${a.result?.diagnostic?`<p>${esc(a.result.diagnostic.code)} · ${esc(a.result.diagnostic.message)}</p><p>${esc(a.result.diagnostic.fix||'')}</p>`:''}<button class="btn ghost" data-attempt-details="${esc(a.id)}">View full attempt details</button></details>`).join('');
}
document.addEventListener('click',event=>{const button=event.target.closest('[data-attempt-details]');if(button)openAttemptDetails(button.dataset.attemptDetails);});
async function loadSessionControls(runId,handle){
 const panel=document.getElementById('sessionControls');if(!panel)return;
 try{
  const queue=await api(`/v1/runs/${encodeURIComponent(runId)}/sessions/${encodeURIComponent(handle.id)}/inputs`);
  if(current!==runId||document.getElementById('sessionControls')!==panel)return;
  const policies=[];
  if(queue.boundary)policies.push(['next-boundary','Next turn']);
  if(queue.live)policies.push(['live-only','Live delivery']);
  if(queue.boundary&&queue.live)policies.push(['live-or-next-boundary','Live or next turn']);
  panel.dataset.sessionId=handle.id;
  panel.innerHTML=`<h3>Persistent session</h3><dl class="insp-kv"><dt>Session</dt><dd>${esc(handle.id)}</dd><dt>Owner worker</dt><dd>${esc(handle.ownerWorker||'Unavailable')}</dd><dt>Expires</dt><dd>${esc(date(handle.expiresAt))}</dd></dl><p class="subtle">Accepted input remains queued until the executor acknowledges delivery.</p>${policies.length?`<form data-session-input><label>Message<textarea name="message" required maxlength="65536"></textarea></label><label>Delivery<select name="policy">${policies.map(([value,label])=>`<option value="${value}">${label}</option>`).join('')}</select></label><button class="btn" type="submit">Queue input</button><p role="status"></p></form>`:'<p>Input delivery is unavailable for this session.</p>'}<details><summary>Recent input receipts (${queue.inputs?.length||0})</summary>${(queue.inputs||[]).slice(-20).map(input=>`<p><code>${esc(input.requestId)}</code> · ${esc(input.status)} · ${esc(input.delivery)}</p>`).join('')}</details>`;
  const form=panel.querySelector('form');if(!form)return;
  let requestId=crypto.randomUUID(),submitted=null;
  form.onsubmit=async event=>{
   event.preventDefault();const status=form.querySelector('[role="status"]'),button=form.querySelector('button');
   const payload={version:queue.version,requestId,message:form.elements.message.value,policy:form.elements.policy.value};
   if(submitted&&(submitted.message!==payload.message||submitted.policy!==payload.policy)){requestId=crypto.randomUUID();payload.requestId=requestId;}
   submitted=payload;button.disabled=true;
   try{await api(`/v1/runs/${encodeURIComponent(runId)}/sessions/${encodeURIComponent(handle.id)}/inputs`,{method:'POST',headers:{'Content-Type':'application/json','Idempotency-Key':requestId},body:JSON.stringify(payload)});status.textContent='Input accepted. Awaiting delivery acknowledgement.';form.reset();requestId=crypto.randomUUID();submitted=null;}
   catch(err){status.textContent=err.message;}finally{button.disabled=false;}
  };
 }catch(err){if(current===runId&&document.getElementById('sessionControls')===panel)panel.textContent=`Session inputs unavailable: ${err.message}`;}
}
