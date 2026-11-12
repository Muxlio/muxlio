// Accounting totals and partitions come from the server's shared reducer.
// Number conversion is reserved for approximate chart geometry.
function analyticsAttemptPending(state){return ['running','queued','uncertain','awaiting_reconciliation','waiting','suspended'].includes(state)}
function analyticsAttemptBucketTime(attempt, ..._extra: unknown[]){const ms=Date.parse(attempt.finishedAt);return Number.isFinite(ms)&&ms>0?{ms,provenance:'finishedAt'}:null}
function analyticsMeasureCatalog(summary){
 const out=[];const add=(id,label,kind,row,extra: Record<string, any>={})=>{if(row?.knownSubtotal!=null)out.push({id,label,kind,...extra})};
 add('reference-value',`Estimated reference value (USD)${summary?.referenceValue?.methods?.length>1?' · Mixed rates':''}`,'reference',summary?.referenceValue);
 add('tokens','Tokens','tokens',summary?.tokens);
 for(const [key,label] of [['input','Input'],['output','Output'],['cacheRead','Cache read'],['cacheWrite','Cache write'],['reasoning','Reasoning']])add('token-'+key,label+' subtotal','dimension',summary?.tokenDimensions?.[key],{dimension:key});
 for(const c of summary?.credits||[])add(`credit:${c.provider}:${c.unit}`,`Credits · ${c.provider} · ${c.unit}`,'credit',c.value,{provider:c.provider,unit:c.unit});
 for(const c of summary?.costs||[])add(`cost:${c.currency}:${c.basis}`,`${c.currency} · ${c.basis==='provider-metered'?'Provider-metered usage':c.basis==='model-reference'?'Provider model reference':c.basis==='credit-replacement'?'Add-on replacement equivalent':c.basis==='api-equivalent'?'API-equivalent estimate':c.basis==='plan-debit'?'Plan debit':'Cost (unspecified basis)'}`,'cost',c.value,{currency:c.currency,basis:c.basis});
 return out;
}
function analyticsMeasureSummary(summary,measure){if(!summary||!measure)return null;if(measure.kind==='reference')return summary.referenceValue;if(measure.kind==='tokens')return summary.tokens;if(measure.kind==='dimension')return summary.tokenDimensions?.[measure.dimension];if(measure.kind==='credit')return summary.credits?.find(c=>c.provider===measure.provider&&c.unit===measure.unit)?.value;if(measure.kind==='cost')return summary.costs?.find(c=>c.currency===measure.currency&&c.basis===measure.basis)?.value;return null}
function analyticsMeasureExact(summary,measure){const row=analyticsMeasureSummary(summary,measure);return row?.effectiveTotal??row?.knownSubtotal??null}
function analyticsChartValue(exact){if(exact==null)return null;const n=Number(exact);return Number.isFinite(n)&&n>=0?n:null}
function analyticsMeasureValueForAttempt(attempt,measure){return analyticsChartValue(analyticsMeasureExact(attempt.accountingSummary,measure))}
function analyticsFormatMeasureValue(measure,value){return value==null?'Unavailable':measure?.kind==='tokens'||measure?.kind==='dimension'?fmtTokens(value):String(value)}
function analyticsReporting(row){return (row?.attempts?.complete||0)+(row?.attempts?.partial||0)}
function analyticsPrecisionWarning(exact){if(exact==null)return false;const n=Number(exact);return !Number.isFinite(n)||(!String(exact).includes('.')&&!Number.isSafeInteger(n))||String(n)!==String(exact)}
