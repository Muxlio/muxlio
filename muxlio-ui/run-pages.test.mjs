import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';

const source=readFileSync(new URL('./workspace.js',import.meta.url),'utf8');
const listeners=[],panels=new Map(),calls=[],renders=[];
let latestEvent=1000;
const panel=id=>{if(!panels.has(id))panels.set(id,{innerHTML:'',hidden:false});return panels.get(id)};
const context=vm.createContext({
 document:{addEventListener:(type,fn)=>{if(type==='click')listeners.push(fn)},getElementById:panel},
 esc:String,current:'large-run',selectedActivation:null,selectedActivationDetail:null,after:0,hasMore:false,wbHasMore:false,
 openRun(){},stopTranscriptPoll(){},stopEventStream(){},renderRunOperations(){},renderLiveEvents(){},
 connectionOK(){},connectionError(){},message(){},operationsRun:null,wbActiveTab:'graph',
 renderWorkbench:(run,graph,events)=>renders.push({run,graph,events}),
 api:async path=>{
  calls.push(path);const url=new URL(path,'http://coordinator');
  if(url.searchParams.get('summary')==='true')return {id:'large-run',state:'succeeded',overview:{activations:10000,latestEventSeq:99999}};
  const offset=Number(url.searchParams.get('offset')||0),before=Number(url.searchParams.get('before')||0);
  if(url.pathname.endsWith('/graph'))return {activations:Array.from({length:10000},(_,i)=>({id:`node-${i}`})),scopes:[]};
  if(url.pathname.endsWith('/outputs'))return {outputs:[],offset,hasMore:true};
  if(url.pathname.endsWith('/attempts'))return {attempts:[],hasMore:false};
  if(url.pathname.endsWith('/events')){const first=before?before-1:latestEvent;return {events:Array.from({length:50},(_,i)=>({seq:first-i})),nextBefore:first-49,hasMore:true};}
  throw Error(`unexpected unbounded request ${path}`);
 },
});
const prefix=source.slice(0,source.indexOf('// ── event renderer'));
const merge=source.slice(source.indexOf('function mergeEvents'),source.indexOf('function stopEventStream'));
const open=source.slice(source.indexOf('const _baseOpenRun='),source.indexOf('// ── connection status'));
vm.runInContext(prefix+merge+open,context);
await context.openRun('large-run');
assert.equal(renders.length,1);
assert.equal(renders[0].graph.activations.length,10000,'the graph includes all nodes');
assert.equal(renders[0].events.length,50);
await context.openRun('large-run');
assert.equal(renders.at(-1).events[0].seq,1000,'the first page starts at the newest event');
assert.ok(calls.some(path=>path.endsWith('/graph')),'the graph is loaded without paging');
assert.ok(calls.filter(path=>path.includes('/events?')||path.includes('/outputs?')).every(path=>path.includes('limit=50')),'history reads remain bounded');
async function page(key,direction){
 const button={disabled:false,dataset:{runPage:key,direction:String(direction)}};
 const event={target:{closest:selector=>selector==='[data-run-page]'?button:null}};
 for(const listener of listeners)await listener(event);
}
await page('events',1);
assert.equal(renders.at(-1).events[0].seq,950);
assert.equal(renders.at(-1).events.length,50,'history pages replace rather than append');
latestEvent=1001;
await context.openRun('large-run');
assert.equal(renders.at(-1).events[0].seq,950,'new events do not shift a historical page');
await page('events',-1);
assert.equal(renders.at(-1).events[0].seq,1001,'previous returns to the latest page');
await page('outputs',1);
assert.ok(calls.some(path=>path.includes('/outputs?offset=50')),'artifact pages advance independently');
assert.equal(renders.at(-1).graph.activations.length,10000,'paging history never truncates the graph');

const format=readFileSync(new URL('./js/format.js',import.meta.url),'utf8');
const usage=readFileSync(new URL('./js/usage-summary.js',import.meta.url),'utf8');
const operations=readFileSync(new URL('./js/operations.js',import.meta.url),'utf8');
const opsContext=vm.createContext({document:{addEventListener(){},getElementById:panel},esc:s=>String(s).replaceAll('<','&lt;'),fmtDuration:String,date:String,stateClass:String,activationData:[{id:'activation',nodeId:'find-and-fix'}]});
vm.runInContext(format+'\n'+usage+'\n'+operations,opsContext);
const wb=readFileSync(new URL('./js/workbench.js',import.meta.url),'utf8');
vm.runInContext(wb,opsContext);
vm.runInContext(`wbActivityAttempts=[{id:'attempt-held',activationId:'activation',awaitReconciliationReason:'Verify remote outcome'}]`,opsContext);
panel('wbActivity');
opsContext.renderRunOperations({id:'large-run',planDigest:'sha256:pinned',limits:{maxAttempts:100},overview:{attempts:20,heldAttempts:1,attemptStates:{detached:2,awaiting_reconciliation:1},accounting:{tokens:{collectedSubtotal:"0",knownSubtotal:"0",effectiveTotal:null,completeness:"partial"},costs:[{currency:"USD",basis:"api-equivalent",value:{estimatedFallbackSubtotal:"1",knownSubtotal:"1",effectiveTotal:null,completeness:"partial"}}]}}});
assert.equal(panel('runAttention').hidden,true,'legacy banner is consolidated into the activity panel');
assert.match(panel('wbActivity').innerHTML,/Resolve uncertain execution/);
assert.match(panel('wbActivity').innerHTML,/Detached remote operations/);
assert.match(panel('runContract').innerHTML,/<dt>Collected<\/dt><dd>0<\/dd>/,'reported zero stays distinct from missing usage');
assert.match(panel('runContract').innerHTML,/<dt>Estimated fallback<\/dt><dd>1<\/dd>/);
assert.match(panel('runContract').innerHTML,/<dt>Coverage<\/dt><dd>partial<\/dd>/);
assert.match(panel('runSettings').innerHTML,/sha256:pinned/);
assert.doesNotMatch(opsContext.attemptMetadataMarkup([{executor:'<script>',id:'attempt',state:'detached',operationRef:{id:'<img>'}}]),/<script>|<img>/);
const row=opsContext.attemptRowMarkup({id:'attempt',executor:'generic',state:'succeeded',accounting:{revision:1,final:true,entries:[{id:"call",tokens:{input:{collected:{value:"0",source:"fixture",fidelity:"metered"}}}}]},executionDurationMs:0});
assert.match(row,/Input · collected<\/dt><dd>0<\/dd>/);
assert.doesNotMatch(row,/Output · collected/);
const heldRow=opsContext.attemptRowMarkup({id:'attempt',activationId:'activation',state:'cancelled',awaitReconciliationReason:'Outcome unknown'});
assert.match(heldRow,/<strong>find-and-fix<\/strong>/);
assert.match(heldRow,/Needs reconciliation/);
opsContext.renderRunOperations({id:'bad-usage',overview:{attempts:1,usage:{costUsd:{observed:NaN,estimated:-1,unknown:1.5}}}});
assert.match(panel('runContract').innerHTML,/<h3>Tokens<\/h3>/);
assert.match(panel('runContract').innerHTML,/<h3>Credits<\/h3>/);
assert.match(panel('runContract').innerHTML,/<h3>Cost<\/h3>/);
assert.match(panel('runContract').innerHTML,/>—<\/dd>/);
assert.doesNotMatch(panel('runContract').innerHTML,/\$NaN|\$-1/);

console.log('bounded run pages, stable refresh, and operational accounting checks passed');
