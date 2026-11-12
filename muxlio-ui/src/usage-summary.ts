// Render server-computed canonical accounting. Decimal strings stay exact.
function usageRunStatesActive(state){return ['running','queued','waiting','paused'].includes(state)}
function accountingStatus(row: AccountingMeasureSummary | null){return row?.completeness||'unknown'}
function accountingExact(value){return value==null?'Unavailable':String(value)}
function accountingCollectedLabel(row: AccountingMeasureSummary | null){const f=row?.evidence?.collected||[];return f.includes('provider-estimate')?(f.length===1?'Collected (provider estimate)':'Collected (includes provider estimates)'):'Collected'}
function buildAttemptAccountingMarkup(accounting){
 const rows=[];
 for(const model of accounting?.models||[])rows.push(renderViewTemplate("usage-summary/build-attempt-accounting-markup-div", [viewValue(esc(model.source)), viewValue(esc(model.role)), viewValue(esc(model.id))]));
 const reading=(label,measure)=>{for(const origin of ['collected','estimated']){const value=measure?.[origin];if(!value)continue;const category=origin==='collected'?(value.fidelity==='provider-estimate'?'collected (provider estimate)':value.fidelity==='proxy'?'collected (proxy)':'collected'):'estimated (Muxlio)';const alternative=origin==='estimated'&&measure.collected?' · alternative calculation':'';const provenance=[value.source,value.fidelity,value.method,value.version,value.catalogId].filter(Boolean).join(' · ');rows.push(renderViewTemplate("usage-summary/reading-div", [viewValue(esc(provenance)), viewValue(esc(label+' · '+category+alternative)), viewValue(esc(value.value))]))}};
 for(const entry of accounting?.entries||[]){for(const [key,label] of [['total','Tokens'],['input','Input'],['output','Output'],['cacheRead','Cache read'],['cacheWrite','Cache write'],['reasoning','Reasoning']])reading(entry.model?`${entry.model} · ${label}`:label,entry.tokens?.[key]);for(const credit of entry.credits||[])reading(`${credit.provider} · ${credit.unit}`,credit.amount);for(const cost of entry.costs||[])reading(`${cost.currency} · ${cost.basis}`,cost.amount)}
 return rows.join('')||renderViewTemplate("usage-summary/build-attempt-accounting-markup-div-2");
}
function accountingMeasures(summary: AccountingSummary | null){
 const rows=[];
 if(summary?.tokens)rows.push({key:'tokens',label:'Tokens',row:summary.tokens});
 for(const c of summary?.credits||[])rows.push({key:`credit:${c.provider}:${c.unit}`,label:`Credits · ${c.provider} · ${c.unit}`,row:c.value});
 for(const c of summary?.costs||[])rows.push({key:`cost:${c.currency}:${c.basis}`,label:`${c.currency} · ${c.basis==='api-equivalent'?'API-equivalent estimate':c.basis==='plan-debit'?'Plan debit':'Cost (unspecified basis)'}`,row:c.value});
 return rows;
}
function buildUsagePresentation(accounting: AccountingSummary | null,options: UsagePresentationOptions={}){
 const primary=[],details=[];
 for(const item of accountingMeasures(accounting)){
  const row=item.row,value=row.effectiveTotal??row.knownSubtotal;
  if(value!=null)primary.push({key:item.key,label:item.label,value,format:accountingExact,coverage:row.effectiveTotal==null?' · incomplete':row.estimatedFallbackSubtotal!=null?' · includes estimates':''});
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
 const row=value||{};
 const rows=[['Known subtotal',row.knownSubtotal],[accountingCollectedLabel(row),row.collectedSubtotal],['Estimated fallback',row.estimatedFallbackSubtotal],['Complete total',row.effectiveTotal]];
 const readings=rows.map(([name,total])=>{const display=total==null?'—':kind==='tokens'?fmtTokens(total):accountingExact(total);return {label:String(name),value:display,title:total==null?'Not reported':display!==String(total)?String(total):''}});
 return renderAccountingCard(label,readings,accountingStatus(row));
}
function buildUsageGridMarkup(accounting){
 const measures=accountingMeasures(accounting);
 if(!measures.some(x=>x.key==='tokens'))measures.unshift({key:'tokens',label:'Tokens'});
 if(!accounting?.credits?.length)measures.push({key:'credit:unavailable',label:'Credits'});
 if(!accounting?.costs?.length)measures.push({key:'cost:unavailable',label:'Cost'});
 return ['tokens','credit:','cost:'].map(key=>renderViewTemplate("usage-summary/build-usage-grid-markup-usage-column", [viewValue(measures.filter(x=>key==='tokens'?x.key===key:x.key.startsWith(key)).map(x=>usageCardMarkup(x.label,x.row,x.key)).join(''))])).join('');
}
function buildOperationsUsageSectionMarkup(accounting,attemptCount,attemptStates){return renderViewTemplate("usage-summary/build-operations-usage-section-markup-operations-summary", [viewValue(Object.entries(attemptStates||{}).map(([state,count])=>renderStatusBadge(stateClass(state), `${state} · ${count}`)).join('')), viewValue(buildUsageGridMarkup(accounting))])}

const WB_USAGE_HEADLINE_KEYS=['tokens','tokens-estimated','cost-observed','cost-estimated'];

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
  const label=headline.key==='tokens'?'Accounting':headline.label;
  const accessibleLabel=`${headline.label}${headline.coverage||''}`;
  return renderViewTemplate("usage-summary/build-workbench-usage-markup-stat-4", [viewValue(esc(status)), viewValue(esc(accessibleLabel)), viewValue(esc(headlineVal)), viewValue(esc(label))]);
}
