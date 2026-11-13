// Render server-computed canonical accounting. Decimal strings stay exact.
function usageRunStatesActive(state){return ['running','queued','waiting','paused'].includes(state)}
function accountingStatus(row: AccountingMeasureSummary | null){return row?.completeness||'unknown'}
function accountingReferenceMethods(methods){return ((methods||[]).length>1?'Mixed rates · ':'')+(methods||[]).map(m=>({'provider-quote':'Provider quote','provider-metered':'Provider-metered usage','published-token-rates':'Published token rates','credit-replacement':'Credit replacement rate'}[m]||m)).join(' · ')}
function accountingExact(value){return value==null?'Unavailable':String(value)}
function accountingCollectedLabel(row: AccountingMeasureSummary | null){const f=row?.evidence?.collected||[];return f.includes('provider-estimate')?(f.length===1?'Collected (provider estimate)':'Collected (includes provider estimates)'):'Collected'}
function buildAttemptAccountingMarkup(accounting){
 const rows=[];
 for(const session of accounting?.sessions||[])rows.push(renderViewTemplate("usage-summary/reading-div",[viewValue(esc([session.source,session.version,session.startedAt,session.finishedAt].filter(Boolean).join(' · '))),viewValue(esc('Provider session · '+session.provider)),viewValue(esc(session.id))]));
 for(const model of accounting?.models||[])rows.push(renderViewTemplate("usage-summary/build-attempt-accounting-markup-div", [viewValue(esc(model.source)), viewValue(esc(model.role)), viewValue(esc(model.id))]));
 const reading=(label,measure)=>{for(const origin of ['collected','estimated']){const value=measure?.[origin];if(!value)continue;const category=origin==='collected'?(value.fidelity==='provider-estimate'?'collected (provider estimate)':value.fidelity==='proxy'?'collected (proxy)':'collected'):'estimated (Muxlio)';const alternative=origin==='estimated'&&measure.collected?' · alternative calculation':'';const provenance=[value.source,value.fidelity,value.method,value.version,value.catalogId,...Object.entries(value.attributes||{}).map(([key,value])=>key+'='+value)].filter(Boolean).join(' · ');rows.push(renderViewTemplate("usage-summary/reading-div", [viewValue(esc(provenance)), viewValue(esc(label+' · '+category+alternative)), viewValue(esc(value.value))]))}};
 for(const entry of accounting?.entries||[]){for(const [key,label] of [['total','Tokens'],['input','Input'],['output','Output'],['cacheRead','Cache read'],['cacheWrite','Cache write'],['reasoning','Reasoning']])reading(entry.model?`${entry.model} · ${label}`:label,entry.tokens?.[key]);for(const credit of entry.credits||[])reading(`${credit.provider} · ${credit.unit}`,credit.amount);for(const cost of entry.costs||[])reading(`${cost.currency} · ${cost.basis}`,cost.amount)}
 for(const coverage of accounting?.coverage||[])rows.push(renderViewTemplate("usage-summary/reading-div", [viewValue(esc(coverage.reason||'')), viewValue(esc([coverage.key?.kind,coverage.key?.currency,coverage.key?.basis,coverage.key?.provider,coverage.key?.unit].filter(Boolean).join(' · ')+' coverage')), viewValue(esc(coverage.state+(coverage.reason?' · '+coverage.reason:'')))]));
 return rows.join('')||renderViewTemplate("usage-summary/build-attempt-accounting-markup-div-2");
}
// Keep the narrow inspector concise; full readings and provenance stay in details.
function buildAttemptAccountingPreviewMarkup(accounting){
 const rows=[];
 const reading=(label,measure,tokens=false)=>{const value=measure?.collected??measure?.estimated;if(!value)return;const origin=measure.collected?(value.fidelity==='provider-estimate'?' (provider estimate)':value.fidelity==='proxy'?' (proxy)':''):' (estimated)';rows.push(renderViewTemplate("usage-summary/reading-div",[viewValue(esc(value.value)),viewValue(esc(label+origin)),viewValue(esc(tokens?fmtTokens(value.value):value.value))]))};
 for(const entry of accounting?.entries||[]){
  const prefix=entry.model?entry.model+' · ':'';
  if(entry.tokens?.total?.collected||entry.tokens?.total?.estimated)reading(prefix+'Tokens',entry.tokens.total,true);
  else{reading(prefix+'Input',entry.tokens?.input,true);reading(prefix+'Output',entry.tokens?.output,true)}
  for(const credit of entry.credits||[])reading(`Credits · ${credit.provider} · ${credit.unit}`,credit.amount);
  for(const cost of entry.costs||[])reading(`${cost.currency} · ${cost.basis==='provider-metered'?'Provider-metered usage':cost.basis==='model-reference'?'Provider model reference':cost.basis==='credit-replacement'?'Add-on replacement equivalent':cost.basis==='api-equivalent'?'API-equivalent estimate':cost.basis==='plan-debit'?'Plan debit':'Cost (unspecified basis)'}`,cost.amount);
 }
 return rows.join('')||renderViewTemplate("usage-summary/build-attempt-accounting-markup-div-2");
}
function accountingMeasures(summary: AccountingSummary | null){
 const rows=[];
 if(summary?.referenceValue)rows.push({key:'reference-value',label:'Estimated reference value (USD)',row:summary.referenceValue});
 if(summary?.tokens)rows.push({key:'tokens',label:'Tokens',row:summary.tokens});
 for(const c of summary?.credits||[])rows.push({key:`credit:${c.provider}:${c.unit}`,label:`Credits · ${c.provider} · ${c.unit}`,row:c.value});
 for(const c of summary?.costs||[])rows.push({key:`cost:${c.currency}:${c.basis}`,label:`${c.currency} · ${c.basis==='provider-metered'?'Provider-metered usage':c.basis==='model-reference'?'Provider model reference':c.basis==='credit-replacement'?'Add-on replacement equivalent':c.basis==='api-equivalent'?'API-equivalent estimate':c.basis==='plan-debit'?'Plan debit':'Cost (unspecified basis)'}`,row:c.value});
 return rows;
}
function buildUsagePresentation(accounting: AccountingSummary | null,options: UsagePresentationOptions={}){
 const primary=[],details=[];
 for(const item of accountingMeasures(accounting)){
  const row=item.row,value=row.effectiveTotal??row.knownSubtotal;
  if(value!=null)primary.push({key:item.key,label:item.label,value,format:accountingExact,isSubtotal:row.effectiveTotal==null,coverage:row.effectiveTotal==null?' · incomplete':row.estimatedFallbackSubtotal!=null?' · includes estimates':''});
  if(row.collectedSubtotal!=null)details.push({key:item.key+':collected',label:item.label+' · '+accountingCollectedLabel(row).toLowerCase(),value:row.collectedSubtotal,format:accountingExact});
  if(row.estimatedFallbackSubtotal!=null)details.push({key:item.key+':estimated',label:item.label+' · estimated fallback',value:row.estimatedFallbackSubtotal,format:accountingExact});
  details.push({key:item.key+':coverage',label:item.label+' coverage',value:accountingStatus(row),format:accountingExact});
 }
 for(const [key,label] of [['input','Input'],['output','Output'],['cacheRead','Cache read'],['cacheWrite','Cache write'],['reasoning','Reasoning']]){const row=accounting?.tokenDimensions?.[key];if(row?.knownSubtotal!=null)details.push({key:'dimension:'+key,label:label+' subtotal',value:row.knownSubtotal,format:accountingExact})}
 for(const [key,label] of [['credits','Unknown credit unit'],['costs','Unknown cost identity']]){const row=accounting?.unattributed?.[key];if(row&&(row.unknownAttempts||row.pendingAttempts))details.push({key:'unknown:'+key,label,value:`${row.unknownAttempts||0} unknown · ${row.pendingAttempts||0} pending`,format:accountingExact})}
 const attemptCount=options.attemptCount??0,runState=options.runState||'';
 const status=primary.length?'ready':options.runError?'error':!attemptCount?'none':usageRunStatesActive(runState)?'pending':'empty';
 return {status,primary,details,attemptCount,runState,hasAttempts:attemptCount>0,creditsOnly:primary.length>0&&primary.every(x=>x.key.startsWith('credit:')),usage:accounting};
}
function usageMeasureValue(measure){return measure.value==null?'Unavailable':measure.format(measure.value)}
function usageCardMarkup(label,value,kind){
 const row=value||{},total=row.effectiveTotal??row.knownSubtotal;
 const collected=row.collectedSubtotal!=null,estimated=row.estimatedFallbackSubtotal!=null;
 const reading=(name,amount)=>{const display=amount==null?'Unavailable':kind==='tokens'?fmtTokens(amount):accountingExact(amount);return {label:name,value:display,title:amount!=null&&display!==String(amount)?String(amount):''}};
 const reference=kind==='reference-value';
 const name=reference?(row.effectiveTotal!=null?'Value':'Subtotal'):collected&&estimated?'Known usage':estimated?'Estimated':collected?accountingCollectedLabel(row):'Usage';
 const state=accountingStatus(row);
 let note=total==null?(state==='inapplicable'?'Not applicable':'No usage reported'):row.effectiveTotal!=null?'Complete':state==='unknown'?'Coverage unverified · total may be higher':'Incomplete · total may be higher';
 if(reference){const counts=row.attempts||{};const valued=(counts.complete||0)+(counts.partial||0);const unpriced=(counts.unknown||0)+(counts.pending||0);note=(row.effectiveTotal!=null?'Complete':valued+unpriced?`${valued} of ${valued+unpriced} attempts priced · incomplete`:'Incomplete · total may be higher')+(row.methods?.length?' · '+accountingReferenceMethods(row.methods):'')}
 const breakdown=collected&&estimated?[reading(accountingCollectedLabel(row),row.collectedSubtotal),reading('Estimated fallback',row.estimatedFallbackSubtotal)]:[];
 return renderAccountingCard(label,reading(name,total),note,breakdown);
}
function buildUsageGridMarkup(accounting){
 const measures=accountingMeasures(accounting).filter(x=>x.key!=='reference-value'||(x.row.effectiveTotal??x.row.knownSubtotal)!=null);
 if(!measures.some(x=>x.key==='tokens'))measures.unshift({key:'tokens',label:'Tokens'});
 const columns=['reference-value','tokens','credit:','cost:'].map(key=>measures.filter(x=>key==='tokens'||key==='reference-value'?x.key===key:x.key.startsWith(key))).filter(items=>items.length).map(items=>renderViewTemplate("usage-summary/build-usage-grid-markup-usage-column", [viewValue(items.map(x=>usageCardMarkup(x.label,x.row,x.key)).join(''))])).join('');
 return columns;
}
function buildOperationsUsageSectionMarkup(accounting,attemptCount,attemptStates){return renderViewTemplate("usage-summary/build-operations-usage-section-markup-operations-summary", [viewValue(Object.entries(attemptStates||{}).map(([state,count])=>renderStatusBadge(stateClass(state), `${state} · ${count}`)).join('')), viewValue(buildUsageGridMarkup(accounting))])}

const WB_USAGE_HEADLINE_KEYS=['reference-value','tokens','tokens-estimated','cost-observed','cost-estimated'];

function workbenchUsageHeadlineMeasure(primary){
  if(!primary?.length)return null;
  for(const key of WB_USAGE_HEADLINE_KEYS){
    const match=primary.find(item=>item.key===key);
    if(match)return match;
  }
  return primary[0];
}

function buildWorkbenchUsageMarkup(presentation){
  const {status,primary,hasAttempts}=presentation;
  if(status==='none'&&!hasAttempts){
    return renderViewTemplate("usage-summary/build-workbench-usage-markup-stat");
  }
  if(status==='error'){
    return renderViewTemplate("usage-summary/build-workbench-usage-markup-stat-2");
  }
  if((status==='empty'||status==='pending')&&!primary.length){
    const pendingNote=status==='pending'?'Accounting pending':'Accounting unavailable';
    return renderViewTemplate("usage-summary/build-workbench-usage-markup-stat-3", [viewValue(status==='pending'?'pending':'empty'), viewValue(esc(pendingNote))]);
  }
  const headline=workbenchUsageHeadlineMeasure(primary);
  const headlineVal=headline.key==='tokens'?`${fmtTokens(headline.value)} tokens`:usageMeasureValue(headline);
  const label=(headline.key==='tokens'?'Accounting':headline.label)+(headline.isSubtotal?' · subtotal':'');
  const accessibleLabel=`${headline.label}${headline.coverage||''}`;
  return renderViewTemplate("usage-summary/build-workbench-usage-markup-stat-4", [viewValue(esc(status)), viewValue(esc(accessibleLabel)), viewValue(esc(headlineVal)), viewValue(esc(label))]);
}
