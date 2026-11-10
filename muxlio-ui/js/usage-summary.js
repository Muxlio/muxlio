// Shared UsageSummary presentation for workbench header, operations, and comparisons.
// Uses the coordinator contract only; never infers tokens from credits or context.

function usageRunStatesActive(state){
  return ['running','queued','waiting','paused'].includes(state);
}

function tokenDimensionCoverageNote(dimensions,dimension,attempts,runState){
  if(!dimensions)return '';
  const row=dimensions[dimension]||{};
  const reported=row.reporting??0;
  const pending=dimensions.coverage?.pending??0;
  const accountable=(dimensions.coverage?.attempts??attempts)-pending;
  if(reported<=0){
    if(pending>0&&usageRunStatesActive(runState))return ` · pending (${pending} ${pending===1?'attempt':'attempts'})`;
    return '';
  }
  const coverage=accountable>0&&reported<accountable?` · ${reported}/${accountable} attempts`:'';
  const pendingNote=pending>0&&usageRunStatesActive(runState)?` · ${pending} pending`:'';
  return coverage+pendingNote;
}

function tokenDimensionRows(dimensions,fallback){
  const note=(row,dimension)=>{
    if(!dimensions||!row||(row.reporting??0)<=0)return '';
    const accountable=(dimensions.coverage?.attempts??0)-(dimensions.coverage?.pending??0);
    return accountable>0&&row.reporting<accountable?` (${row.reporting}/${accountable} attempts)`:'';
  };
  const value=(dim)=>{
    const fromApi=dimensions?.[dim]?.observed;
    if(fromApi!=null)return fromApi;
    return fallback?.[dim]??null;
  };
  return [
    ['Input subtotal',value('input'),note(dimensions?.input,'input')],
    ['Output subtotal',value('output'),note(dimensions?.output,'output')],
    ['Cached subtotal',value('cached'),note(dimensions?.cached,'cached')],
  ].map(([name,total,suffix])=>[`${name}${suffix}`,total]);
}

function usageTotalCoverageNote(total,dimensions){
  if(!total&&!dimensions)return '';
  const pending=total?.pending??dimensions?.coverage?.pending??0;
  const reporting=total?.reporting??dimensions?.coverage?.reporting??0;
  const attempts=dimensions?.coverage?.attempts??0;
  const accountable=attempts-pending;
  const parts=[];
  if(Number.isInteger(total?.unknown)&&total.unknown>0)parts.push(`${total.unknown} unreported`);
  if(pending>0)parts.push(`${pending} pending`);
  if(reporting>0&&accountable>0&&reporting<accountable)parts.push(`${reporting}/${accountable} reporting`);
  return parts.length?` · ${parts.join(' · ')}`:'';
}

function creditCoverageNote(row,attemptCount){
  if(!row)return '';
  const unknown=row.unknown;
  if(Number.isInteger(unknown)&&unknown>0&&attemptCount>0)return ` · ${unknown} unreported`;
  return '';
}

function fallbackTokenSubtotal(fallback){
  if(!fallback)return null;
  const input=fallback.input,output=fallback.output;
  if(input!=null&&output!=null)return input+output;
  if(input!=null||output!=null)return null;
  return null;
}

function dimensionHasAnyReported(dimensions,fallback){
  const dims=['input','output','cached'];
  if(dimensions&&dims.some(name=>dimensions[name]?.observed!=null))return true;
  return !!fallback&&dims.some(name=>fallback[name]!=null);
}

function creditHasObserved(row){
  return row&&row.observed!=null;
}

function costHasObserved(cost){return cost?.observed!=null}
function costHasEstimated(cost){return cost?.estimated!=null}

// buildUsagePresentation turns a UsageSummary (plus optional attempt fallback) into
// primary headline measures and expandable detail rows.
function buildUsagePresentation(usage,options={}){
  const attemptCount=options.attemptCount??0;
  const runState=options.runState??'';
  const runError=options.runError??null;
  const fallback=options.fallbackTokens??null;
  const dimensions=usage?.tokenDimensions;
  const credits=usage?.credits||[];
  const primary=[];
  const details=[];
  const tokens=usage?.tokens;
  const tokenObserved=tokens?.observed??fallbackTokenSubtotal(fallback);
  const hasTokenMeasures=tokenObserved!=null||dimensionHasAnyReported(dimensions,fallback);
  if(hasTokenMeasures){
    primary.push({
      key:'tokens',
      label:'Token subtotal',
      value:tokenObserved,
      format:fmtTokens,
      coverage:usageTotalCoverageNote(tokens,dimensions),
    });
    for(const [name,total] of tokenDimensionRows(dimensions,fallback)){
      details.push({key:`token-${name}`,label:name,value:total,format:fmtTokens});
    }
  }else if(tokens?.estimated!=null){
    primary.push({key:'tokens-estimated',label:'Tokens (estimated)',value:tokens.estimated,format:fmtTokens,coverage:usageTotalCoverageNote(tokens,dimensions)});
  }
  for(const row of credits){
    if(creditHasObserved(row)){
      primary.push({
        key:`credit:${row.unit||'unknown'}`,
        label:`Credits · ${row.unit||'unit unavailable'}`,
        value:row.observed,
        format:fmtCredits,
        coverage:creditCoverageNote(row,attemptCount),
      });
    }
  }
  const cost=usage?.costUsd;
  if(costHasObserved(cost)){
    primary.push({key:'cost-observed',label:'Cost · observed',value:cost.observed,format:fmtCostUsd,coverage:usageTotalCoverageNote(cost,null)});
  }
  if(costHasEstimated(cost)){
    primary.push({key:'cost-estimated',label:'Cost · estimated',value:cost.estimated,format:fmtCostUsd,coverage:''});
  }
  if(cost&&(cost.observed==null&&cost.estimated==null)&&Number.isInteger(cost.unknown)&&cost.unknown>0){
    details.push({key:'cost-unknown',label:'Cost coverage',value:`${cost.unknown} attempts without cost`,format:x=>x});
  }
  let status='ready';
  if(runError&&primary.length===0)status='error';
  else if(primary.length===0){
    if(!attemptCount)status='none';
    else if(usageRunStatesActive(runState)&&(dimensions?.coverage?.pending??tokens?.pending??0)>0)status='pending';
    else status='empty';
  }
  const creditsOnly=primary.length>0&&primary.every(item=>item.key.startsWith('credit:'));
  return {status,primary,details,attemptCount,runState,creditsOnly,hasAttempts:attemptCount>0,usage,dimensions,fallback};
}

function usageMeasureValue(measure){
  if(measure.value==null)return 'Unavailable';
  return measure.format(measure.value);
}

function usageCardMarkup(label,value,format,extraRows=[]){
  const rows=[['Observed',value?.observed],['Estimated',value?.estimated],...extraRows];
  const pending=value?.pending>0?`<div class="usage-coverage"><dt>Pending attempts</dt><dd>${esc(value.pending)}</dd></div>`:'';
  const reporting=Number.isInteger(value?.reporting)&&value.reporting>=0?`<div class="usage-coverage"><dt>Reporting attempts</dt><dd>${esc(value.reporting)}</dd></div>`:'';
  return `<section class="usage-card"><h3>${esc(label)}</h3><dl>${rows.map(([name,total])=>`<div><dt>${name}</dt><dd>${esc(total==null?'Unavailable':format(total))}</dd></div>`).join('')}<div class="usage-coverage"><dt>Unreported attempts</dt><dd>${esc(Number.isInteger(value?.unknown)&&value.unknown>=0?value.unknown:'Unavailable')}</dd></div>${pending}${reporting}</dl></section>`;
}

function buildUsageGridMarkup(usage,attemptCount){
  const creditCards=(usage?.credits||[]).map(row=>usageCardMarkup(`Credits (${row.unit||'unit unavailable'})`,row,fmtCredits)).join('');
  const tokenExtras=tokenDimensionRows(usage?.tokenDimensions).map(([name,total])=>[name,total]);
  return `${usageCardMarkup('Tokens',usage?.tokens,fmtTokens,tokenExtras)}${usageCardMarkup('Cost',usage?.costUsd,fmtCostUsd)}${creditCards}`;
}

function buildOperationsUsageSectionMarkup(usage,attemptCount,attemptStates){
  const states=attemptStates||{};
  return `<div class="operations-summary"><div class="operations-heading"><div><h2>Usage</h2><p class="subtle">Across ${esc(attemptCount??0)} attempts. Observed values were reported by executors; estimates were supplied by executors. Provider credits use executor-reported units and are not converted into tokens or cost. Unreported attempts have neither value. Pending attempts are excluded from coverage. Totals can be incomplete.</p></div><div class="operation-states">${Object.entries(states).map(([state,count])=>`<span class="state ${esc(stateClass(state))}">${esc(state)} · ${esc(count)}</span>`).join('')}</div></div><div class="usage-grid">${buildUsageGridMarkup(usage,attemptCount)}</div></div>`;
}

const WB_USAGE_HEADLINE_KEYS=['tokens','tokens-estimated','cost-observed','cost-estimated'];
const WB_USAGE_DRAWER_CAP=6;

function workbenchUsageHeadlineMeasure(primary){
  if(!primary?.length)return null;
  for(const key of WB_USAGE_HEADLINE_KEYS){
    const match=primary.find(item=>item.key===key);
    if(match)return match;
  }
  return primary[0];
}

function workbenchUsageDetailLabel(measure){
  if(!measure)return '';
  return `${measure.label}${measure.coverage||''}`;
}

function buildWorkbenchUsageMarkup(presentation,options={}){
  const {status,primary,details,attemptCount,hasAttempts,usage}=presentation;
  const link=options.linkAttempts?`<button type="button" class="btn ghost wb-usage-link" data-open-operations>Attempt-level usage</button>`:'';
  if(status==='none'&&!hasAttempts){
    return `<div class="stat wb-usage wb-usage--idle" aria-label="Usage not available yet"><div class="stat-val">—</div><div class="stat-label">Usage</div></div>`;
  }
  if(status==='error'){
    return `<div class="stat wb-usage wb-usage--error" role="status"><div class="stat-val subtle">Unavailable</div><div class="stat-label">Usage</div>${link}</div>`;
  }
  if((status==='empty'||status==='pending')&&!primary.length){
    const pendingNote=status==='pending'?'Usage pending':'No usage reported';
    return `<div class="stat wb-usage wb-usage--${status==='pending'?'pending':'empty'}" role="status"><div class="stat-val subtle">${esc(pendingNote)}</div><div class="stat-label">Usage</div>${link}</div>`;
  }
  const headline=workbenchUsageHeadlineMeasure(primary);
  const headlineVal=usageMeasureValue(headline);
  const headlineTitle=workbenchUsageDetailLabel(headline);
  const secondary=primary.filter(item=>item!==headline);
  const detailRows=[...secondary,...details];
  const detailCap=WB_USAGE_DRAWER_CAP;
  const detailVisible=detailRows.slice(0,detailCap);
  const detailOverflow=detailRows.slice(detailCap);
  const usageDetailRow=row=>{
    const val=row.value==null?'Unavailable':(typeof row.format==='function'?row.format(row.value):row.value);
    return `<div class="wb-usage-detail-row"><dt>${esc(workbenchUsageDetailLabel(row))}</dt><dd>${esc(val)}</dd></div>`;
  };
  const detailHtml=detailVisible.map(usageDetailRow).join('');
  const overflowHtml=detailOverflow.length?`<details class="wb-usage-overflow"><summary>Show ${detailOverflow.length} more usage details</summary><dl class="wb-usage-detail-list">${detailOverflow.map(usageDetailRow).join('')}</dl></details>`:'';
  const coverage=usage?.tokenDimensions?.coverage;
  const coverageHtml=coverage?`<p class="wb-usage-coverage subtle">${esc(attemptCount)} attempts · ${esc(coverage.reporting??0)} reporting · ${esc(coverage.pending??0)} pending</p>`:'';
  const drawer=detailRows.length||link?`<div class="wb-usage-drawer"><dl class="wb-usage-detail-list">${detailHtml}</dl>${coverageHtml}${overflowHtml}${link}</div>`:'';
  const titleAttr=headlineTitle?` title="${esc(headlineTitle)}"`:'';
  const expandNote=detailRows.length||link?' expand for usage breakdown':'';
  return `<div class="stat wb-usage" data-usage-state="${esc(status)}"${titleAttr}><details class="wb-usage-details"><summary class="wb-usage-head" aria-label="Usage${esc(expandNote)}"><div class="stat-val">${esc(headlineVal)}</div><div class="stat-label">Usage</div></summary>${drawer}</details></div>`;
}
