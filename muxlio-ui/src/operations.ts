// Run operations use the shared executor contract. States and capabilities are
// shown as reported, independently of the adapter providing them.
let operationsOffset=0,operationsRun=null,operationsRequest=0;
function attemptRowMarkup(a){
 const validation=a.result?.validationFailure,outcome=a.awaitReconciliationReason||validation?.message||a.failure||(a.result?.executionSucceeded?'Executor completed':'');
 const activation=typeof activationData!=='undefined'?(activationData||[]).find(item=>item.id===a.activationId):null;
 const step=activation?.nodeId||'Step unavailable';
 return renderViewTemplate("operations/attempt-row-markup-attempt-card", [viewValue(esc(step)), viewValue(esc(a.executor||'Executor unavailable')), viewValue(esc(a.retryNumber??'—')), viewValue(esc(date(a.startedAt))), viewValue(esc(a.id)), viewValue(esc(a.id)), viewValue(a.awaitReconciliationReason?renderStatusBadge('awaiting_reconciliation','Needs reconciliation'):''), viewValue(esc(stateClass(a.state))), viewValue(esc(a.state)), viewValue(esc(a.effect||'Effect policy unavailable')), viewValue(buildAttemptAccountingMarkup(a.accounting)), viewValue(esc(a.executionDurationMs==null?'Unavailable':fmtDuration(a.executionDurationMs))), viewValue(esc(a.activationId)), viewValue(esc(a.id)), viewValue(outcome||a.operationRef?renderViewTemplate("operations/attempt-row-markup-attempt-outcome", [viewValue(validation?renderViewTemplate("operations/attempt-row-markup-attempt-validation"):''), viewValue(outcome?renderViewTemplate("shared/paragraph", [viewValue(esc(outcome))]):''), viewValue(a.operationRef?renderViewTemplate("operations/attempt-row-markup-subtle", [viewValue(esc(a.operationRef.id)), viewValue(a.operationRef.pollAt?` · next poll ${esc(date(a.operationRef.pollAt))}`:'')]):'')]):'')]);
}
function renderRunOperations(run){
 if(operationsRun?.id!==run.id)operationsOffset=0;
 operationsRun=run;
 const overview=run.overview||{},states=overview.attemptStates||{};
 const attention=(document.getElementById('runAttention') as UIElement);if(attention){attention.hidden=true;}
 if(typeof renderWbActivity==='function')renderWbActivity(run,typeof activationData!=='undefined'?activationData:[]);
 const panel=(document.getElementById('runContract') as UIElement);if(!panel)return;
 const limits=run.limits||{};
 const rows=[['Plan version',run.planDigest||'Unavailable'],['End-to-end deadline',run.deadlineSeconds?date(new Date(Date.parse(run.createdAt)+run.deadlineSeconds*1000)):'No deadline'],['Attempts',`${overview.attempts??0}${limits.maxAttempts!=null?' / '+limits.maxAttempts:''}`],['Activations',`${overview.activations??0}${limits.maxActivations!=null?' / '+limits.maxActivations:''}`],['Active attempt limit',limits.maxActiveAttempts??'Unlimited'],['Accounting limits',(limits.accounting||[]).map(l=>`${l.kind} ${l.maximum} ${l.provider||l.currency||''} ${l.unit||l.basis||''} · ${l.onMissing||'fail-closed'}`).join('; ')||'Unlimited'],['Retention',run.pinned?'Pinned':run.retainedUntil?`Retained until ${date(run.retainedUntil)}`:'Service retention policy']];
 const markup=buildOperationsUsageSectionMarkup(overview.accounting,overview.attempts??0,states);
 if(typeof replaceHTMLPreservingDetails==="function")replaceHTMLPreservingDetails(panel,markup);else panel.innerHTML=markup;
 const settings=(document.getElementById('runSettings') as UIElement);if(settings){const html=renderViewTemplate("operations/html-execution-contract", [viewValue(rows.map(([label,value])=>renderDefinitionRow(label, value)).join('')), viewValue(run.fork?renderViewTemplate("operations/html-p", [viewValue(encodeURIComponent(run.fork.sourceRunId)), viewValue(esc(run.fork.sourceRunId)), viewValue(run.fork.reuseActivations?.length||0)]):'')]);if(typeof replaceHTMLPreservingDetails==='function')replaceHTMLPreservingDetails(settings,html);else settings.innerHTML=html;}
 if(typeof wbActiveTab!=='undefined'&&wbActiveTab==='operations')loadRunOperations();
}
async function loadRunOperations(){
 if(!operationsRun)return;
 const id=operationsRun.id,offset=operationsOffset,request=++operationsRequest;
 const panel=(document.getElementById('runAttemptsPanel') as UIElement);if(!panel)return;
 try{
  const page=await api(`/v1/runs/${encodeURIComponent(id)}/attempts?offset=${offset}&limit=50&details=false`);
  if(id!==current||request!==operationsRequest)return;
  const attempts=page.attempts||[];
  panel.innerHTML=runPager('attempts',offset,page.hasMore,'Attempts')+(attempts.length?renderViewTemplate("operations/panel-inner-html-attempt-list", [viewValue(attempts.map(attemptRowMarkup).join(''))]):renderViewTemplate("operations/panel-inner-html-tablebox"));
 }catch(err){if(id===current&&request===operationsRequest)panel.innerHTML=renderNotice(err.message, {error:true});}
}
document.addEventListener('click',event=>{
 if((event.target as UIElement).closest<UIElement>('[data-open-operations]'))showWbTab('operations');
 const button=(event.target as UIElement).closest<UIElement>('[data-inspect-operation]');
 if(button){showWbTab('graph');loadActivation(button.dataset.inspectOperation);}
 const pager=(event.target as UIElement).closest<UIElement>('[data-run-page="attempts"]');
 if(pager&&!pager.disabled){operationsOffset=Math.max(0,operationsOffset+Number(pager.dataset.direction)*50);loadRunOperations();}
});

async function openAttemptDetails(id){
 const run=current;
 try{const attempt=await api(`/v1/runs/${encodeURIComponent(run)}/attempts/${encodeURIComponent(id)}`);if(current===run)showArtifactOutput(null,`Attempt ${id}`,attempt);}
 catch(err){if(current===run)message(err.message,true);}
}
function attemptMetadataMarkup(attempts){
 return (attempts||[]).map(a=>renderViewTemplate("operations/attempt-metadata-markup-attempt-contract", [viewValue(esc(a.executor)), viewValue(esc(a.state)), viewValue(esc(a.id)), viewValue(esc(a.id)), viewValue(esc(a.effect||'Unspecified')), viewValue(esc(a.effect||'Unspecified')), viewValue(a.operationRef?renderViewTemplate("operations/attempt-metadata-markup-insp-value-id", [viewValue(esc(a.operationRef.id)), viewValue(esc(a.operationRef.id))]):''), viewValue(a.awaitReconciliationReason?renderViewTemplate("operations/attempt-metadata-markup-dt", [viewValue(esc(a.awaitReconciliationReason)), viewValue(esc(a.awaitReconciliationReason))]):''), viewValue(a.result?.validationFailure?renderPlaceholder(`${a.result.validationFailure.code??''} · ${a.result.validationFailure.path??''} · ${a.result.validationFailure.message??''}`, "insp-error"):''), viewValue(a.result?.diagnostic?renderViewTemplate("operations/attempt-metadata-markup-p", [viewValue(esc(a.result.diagnostic.code)), viewValue(esc(a.result.diagnostic.message)), viewValue(esc(a.result.diagnostic.fix||''))]):''), viewValue(esc(a.id)), viewValue(esc((a.retryNumber??0)+1)), viewValue(esc(stateClass(a.state)))])).join('');
}
document.addEventListener('click',event=>{const button=(event.target as UIElement).closest<UIElement>('[data-attempt-details]');if(button)openAttemptDetails(button.dataset.attemptDetails);});
async function loadSessionControls(runId,handle){
 const panel=(document.getElementById('sessionControls') as UIElement);if(!panel)return;
 try{
  const queue=await api(`/v1/runs/${encodeURIComponent(runId)}/sessions/${encodeURIComponent(handle.id)}/inputs`);
  if(current!==runId||(document.getElementById('sessionControls') as UIElement)!==panel)return;
  const policies=[];
  if(queue.boundary)policies.push(['next-boundary','Next turn']);
  if(queue.live)policies.push(['live-only','Live delivery']);
  if(queue.boundary&&queue.live)policies.push(['live-or-next-boundary','Live or next turn']);
  panel.dataset.sessionId=handle.id;
  panel.innerHTML=renderViewTemplate("operations/panel-inner-html-insp-kv", [viewValue(esc(handle.id)), viewValue(esc(handle.id)), viewValue(esc(handle.ownerWorker||'Unavailable')), viewValue(esc(handle.ownerWorker||'Unavailable')), viewValue(esc(date(handle.expiresAt))), viewValue(esc(date(handle.expiresAt))), viewValue(policies.length?renderViewTemplate("operations/panel-inner-html-btn", [viewValue(policies.map(([value,label])=>renderViewTemplate("shared/select-option", [viewValue(value), viewValue(label)])).join(''))]):renderViewTemplate("operations/panel-inner-html-p")), viewValue(queue.inputs?.length||0), viewValue((queue.inputs||[]).slice(-20).map(input=>renderViewTemplate("operations/panel-inner-html-p-2", [viewValue(esc(input.requestId)), viewValue(esc(input.status)), viewValue(esc(input.delivery))])).join(''))]);
  const form=panel.querySelector<UIElement>('form');if(!form)return;
  let requestId=crypto.randomUUID(),submitted=null;
  form.onsubmit=async event=>{
   event.preventDefault();const status=form.querySelector<UIElement>('[role="status"]'),button=form.querySelector<UIElement>('button');
   const payload={version:queue.version,requestId,message:form.elements.message.value,policy:form.elements.policy.value};
   if(submitted&&(submitted.message!==payload.message||submitted.policy!==payload.policy)){requestId=crypto.randomUUID();payload.requestId=requestId;}
   submitted=payload;button.disabled=true;
   try{await api(`/v1/runs/${encodeURIComponent(runId)}/sessions/${encodeURIComponent(handle.id)}/inputs`,{method:'POST',headers:{'Content-Type':'application/json','Idempotency-Key':requestId},body:JSON.stringify(payload)});status.textContent='Input accepted. Awaiting delivery acknowledgement.';form.reset();requestId=crypto.randomUUID();submitted=null;}
   catch(err){status.textContent=err.message;}finally{button.disabled=false;}
  };
 }catch(err){if(current===runId&&(document.getElementById('sessionControls') as UIElement)===panel)panel.textContent=`Session inputs unavailable: ${err.message}`;}
}
