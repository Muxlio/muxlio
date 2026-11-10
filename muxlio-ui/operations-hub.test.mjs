import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';

const format=readFileSync(new URL('./js/format.js',import.meta.url),'utf8');
const usage=readFileSync(new URL('./js/usage-summary.js',import.meta.url),'utf8');
const operations=readFileSync(new URL('./js/operations.js',import.meta.url),'utf8');
const hub=readFileSync(new URL('./js/operations-hub.js',import.meta.url),'utf8');
const helpers=vm.runInNewContext(`${format}\n${usage}\n${operations}\n${hub};({evidenceDocument,reportRows,evidenceMarkup,hubUsage,hubClientPage,comparisonMarkup,attemptRowMarkup})`,{
 document:{addEventListener(){},getElementById(){return null}},loadWorkers(){},showWbTab(){},
 esc:s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])),stateClass:()=>'',date:String,current:null
});

test('cancelled attempts with unresolved effects can be resolved',()=>{
 const row=helpers.attemptRowMarkup({id:'attempt',runId:'run',state:'cancelled',awaitReconciliationReason:'External outcome unknown'});
 assert.match(row,/Resolve uncertain execution/);
 assert.doesNotMatch(helpers.attemptRowMarkup({id:'done',state:'cancelled'}),/Resolve uncertain execution/);
});

test('evidence import recognizes actual evaluation and raw benchmark bundles',()=>{
 const report={manifest:{datasetId:'typed'},results:[]};assert.equal(helpers.evidenceDocument([report]).kind,'evaluation');
 const raw=helpers.evidenceDocument([{suite:'muxlio-reference/v1',schemaVersion:1},{schemaVersion:1,case:'generic',correct:false}]);
 assert.equal(raw.kind,'benchmark');assert.equal(raw.document.observations[0].correct,false);
 assert.throws(()=>helpers.evidenceDocument([{random:'file'}]));
});
test('evaluation views retain zero scores and unavailable usage without claiming missing cases',()=>{
 const report={name:'<script>bad</script>',kind:'evaluation',document:{manifest:{datasetId:'cases',versions:[{id:'v1'},{id:'v2'}]},results:[{versionId:'v1',caseId:'<img>',durationMs:0,execution:{state:'succeeded',success:true,usage:{tokens:{observed:0,estimated:null,unknown:1},costUsd:{observed:null,estimated:null,unknown:1}}},scores:{quality:{value:0}}}]}};
 const html=helpers.evidenceMarkup(report);
 assert.match(html,/quality: 0/);assert.match(html,/0 observed/);assert.match(html,/Unavailable estimated/);assert.match(html,/Expected case identities are not included/);
 assert.match(html,/v2/);assert.doesNotMatch(html,/<script>|<img>|NaN|undefined/);
});
test('benchmark views retain failed and uncollected repetitions',()=>{
 const report={name:'recorded',kind:'benchmark',document:{manifest:{suite:'muxlio-reference/v1',cases:['generic'],repetitions:3},observations:[{case:'generic',repetition:1,correct:false,wallTimeMs:3600000}]}};
 const html=helpers.evidenceMarkup(report);assert.match(html,/0 \/ 1/);assert.match(html,/Missing cases<\/span><strong>2<\/strong>/);assert.match(html,/1h 0m/);
 assert.equal(helpers.hubUsage({observed:null,estimated:0,unknown:2},true),'Unavailable observed · $0.0000 estimated · 2 unreported');
});
test('large imported result collections render bounded pages',()=>{
 const rows=Array.from({length:125},(_,n)=>n);let count;
 const html=helpers.hubClientPage('results',rows,items=>{count=items.length;return items.join(',')});
 assert.equal(count,50);assert.match(html,/data-offset="50"/);assert.doesNotMatch(html,/,124/);
});
