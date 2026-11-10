// Optional real-browser regression test. Uses only local mock APIs and never
// connects to the user's coordinator or sends subscriptions to external hosts.
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {once} from 'node:events';
import {createUIServer} from './server.mjs';
process.env.MUXLIO_TOKEN='browser-fixture-token';
const {chromium}=await import(process.env.MUXLIO_BROWSER_MODULE||'playwright');
const digest='sha256:'+'a'.repeat(64),requests=[],errors=[];
const usage={tokens:{observed:0,estimated:null,unknown:1},costUsd:{observed:null,estimated:null,unknown:1}};
const base={id:'run-a',workflowName:'Operational fixture',state:'succeeded',createdAt:'2026-01-01T00:00:00Z',updatedAt:'2026-01-01T01:00:00Z',input:{count:0},planDigest:digest,limits:{maxAttempts:10,maxTokens:1000},overview:{activations:1,completed:1,attempts:1,latestEventSeq:3,usage,attemptStates:{awaiting_reconciliation:1},heldAttempts:1}};
const runs=new Map([['run-a',{...base}],['run-b',{...base,id:'run-b',input:{count:1}}]]);
const node={id:'node-a',runId:'run-a',nodeId:'echo',scopePath:[],occurrence:0,state:'succeeded',outputs:{report:{data:'recorded output',mime:'text/plain'}}};
let held=true,pins=[],reports=[],schedules=[],triggers=[],subscriptions=[],deadletters=[{id:'delivery',subscriptionId:'notify',runId:'run-a',attempts:2,maxRetries:1,lastError:'Temporary receiver outage'}];
const revisions=[{seq:5,runId:'run-a',subjectId:'attempt-a',timestamp:'2026-01-01T00:00:00Z',payload:{activationId:'node-a',snapshot:{name:'report',digest,mime:'text/plain'}}}];
const coordinator=createServer(async(req,res)=>{
 let raw='';for await(const chunk of req)raw+=chunk;
 const url=new URL(req.url,'http://localhost'),path=url.pathname,body=raw?JSON.parse(raw):null;requests.push({method:req.method,path,body});
 const send=(value,status=200)=>{res.writeHead(status,{'content-type':'application/json'});res.end(JSON.stringify(value))};
 try{
  if(path==='/healthz')return send({status:'ok'});
  if(path==='/v1/runs')return send({runs:[...runs.values()],total:runs.size,states:{succeeded:2}});
  if(path==='/v1/operations')return send({items:held?[{kind:'attention',runId:'run-a',activationId:'node-a',attemptId:'attempt-a',state:'awaiting_reconciliation',reason:'Verify remote outcome'}]:[],counts:{attention:Number(held),queue:0,detached:1,recovery:2},hasMore:false});
  if(path==='/v1/workers')return send({serverTime:'2026-01-01T00:00:00Z',heartbeatWindowSeconds:90,workers:[],registeredWorkers:[{workerId:'generic-worker',capacity:2,lastSeen:'2025-12-31T23:59:55Z',capabilities:[{name:'generic/echo',readiness:{ready:false,checks:[{name:'binary',passed:false,message:'Executable unavailable'}]},contract:{contractVersion:'1',capabilities:{session:true},environment:{digest:'env-digest'}}}]},{workerId:'stale-worker',capacity:1,lastSeen:'2025-12-01T00:00:00Z',capabilities:[]}]});
  if(path==='/v1/workflow-versions')return send({workflowVersions:[{digest,name:'Echo workflow'}]});
  if(path==='/v1/automation'){const rows={schedules,triggers,subscriptions}[body.kind],item=rows.find(r=>r.id===body.id);if(item){if(body.kind==='schedules')item.paused=!body.enabled;else item.enabled=body.enabled;}return send({updated:true});}
  if(path==='/v1/automation/history')return send({records:url.searchParams.get('kind')==='deadletters'?deadletters:[{state:'started',runId:'run-a',dueAt:base.createdAt}],hasMore:false});
  if(path==='/v1/subscriptions/deadletters')return send({deliveries:deadletters});
  if(path.endsWith('/requeue')){deadletters=[];return send({requeued:true});}
  for(const [kind,rows] of Object.entries({schedules,triggers,subscriptions})){
   if(path===`/v1/${kind}`){if(req.method==='POST'){const {secret,...safe}=body;rows.push({...safe,nextFire:body.startAt,paused:false});return send(safe,201);}return send({[kind]:rows});}
   if(path.startsWith(`/v1/${kind}/`))return send(kind==='schedules'?{fires:[{state:'started',runId:'run-a',dueAt:base.createdAt}]}:{deliveries:[{state:'delivered',runId:'run-a',timestamp:base.createdAt,attempts:1}]});
  }
  if(path==='/v1/retention'){
   if(req.method==='POST'){if(body.pinned===true)pins.push({kind:body.kind,id:body.id});if(body.pinned===false)pins=pins.filter(p=>p.kind!==body.kind||p.id!==body.id);if(body.kind==='run'&&runs.has(body.id)){runs.get(body.id).pinned=pins.some(p=>p.kind==='run'&&p.id===body.id);if('retainedUntil'in body)runs.get(body.id).retainedUntil=body.retainedUntil;}return send({updated:true});}
   return send({pins,total:pins.length,hasMore:false,policy:{eventHistory:604800e9,stdoutStderr:259200e9,artifacts:2592000e9,caches:1209600e9}});
  }
  if(path==='/v1/reports'){if(req.method==='POST'){const report={...body,id:`report-${reports.length}`,importedAt:base.createdAt};reports.push(report);return send({...report,document:undefined},201);}return send({reports:reports.map(({document,...r})=>r),hasMore:false});}
  if(path.startsWith('/v1/reports/'))return send(reports.find(r=>r.id===path.split('/').at(-1)));
  if(path==='/v1/revisions')return send({revisions,nextBefore:5,hasMore:false});
  if(path.startsWith('/v1/artifacts/')){res.writeHead(200,{'content-type':'text/plain'});return res.end(req.method==='HEAD'?undefined:'Snapshot content');}
  if(path.startsWith('/v1/runs/')){
   const [, , ,id,part,...rest]=path.split('/'),run=runs.get(id)||base;
   if(!part)return send({...run,pinned:pins.some(p=>p.kind==='run'&&p.id===id)});
   if(part==='graph')return send({activations:[{...node,runId:id}],scopes:[]});
   if(part==='outputs')return send({outputs:[{activationId:node.id,nodeId:node.nodeId,name:'report'}],hasMore:false});
   if(part==='input-requests')return send({requests:[]});
   if(part==='events'){if(rest[0]==='stream')return send({},404);return send({events:[3,2,1].map(seq=>({seq,type:'activation.succeeded',timestamp:base.createdAt,subjectId:node.id})),nextBefore:1,hasMore:false});}
   if(part==='attempts'){if(rest.at(-1)==='resolve'){held=false;return send({resolved:true});}const a={id:'attempt-a',runId:id,activationId:node.id,executor:'generic/echo',state:held?'awaiting_reconciliation':'succeeded',awaitReconciliationReason:held?'Verify remote outcome':'',startedAt:base.createdAt,executionDurationMs:0,usage:{input:0,output:0,cached:0}};return send(rest.length?a:{attempts:[a],hasMore:false});}
   if(part==='activations')return send({...node,runId:id,attempts:[]});
   if(part==='fork'){const created={...base,id:'run-fork',state:'created',fork:{sourceRunId:id}};runs.set(created.id,created);return send({runId:created.id,started:body.start},201);}
   if(part==='compare')return send({before:runs.get(id),after:runs.get(url.searchParams.get('other')),beforePlan:{digest},afterPlan:{digest},inputsChanged:true,planChanged:false,resultsChanged:true,beforeUsage:usage,afterUsage:usage,reusedOutputs:[],activations:[{nodeId:'echo',scopePath:[],before:node,after:{...node,outputs:{report:'changed'}},changed:true}]});
  }
  return send({message:`Unmocked endpoint ${req.method} ${path}`},404);
 }catch(err){send({message:err.message},500)}
});
coordinator.listen(0,'127.0.0.1');await once(coordinator,'listening');
const ui=createUIServer({coordinatorURL:`http://127.0.0.1:${coordinator.address().port}`});ui.listen(0,'127.0.0.1');await once(ui,'listening');
const browser=await chromium.launch({headless:true});
const page=await browser.newPage({viewport:{width:1440,height:1000}});page.on('pageerror',error=>errors.push(error.message));
const goto=async hash=>{await page.goto(`http://127.0.0.1:${ui.address().port}${hash}`);await page.waitForTimeout(100)};
const waitText=async(selector,text)=>{await page.locator(selector).filter({hasText:text}).first().waitFor()};
try{
 await goto('/operations');await waitText('#operationsList','Verify remote outcome');
 await page.locator('#operationsList [data-ops-action="resolve"]').click();await page.locator('dialog textarea[name="reason"]').fill('Confirmed old operation cannot commit');await page.locator('dialog input[name="verified"]').check();await page.locator('dialog button[type="submit"]').click();await page.waitForFunction(()=>!document.querySelector('dialog'));
 assert.ok(requests.some(r=>r.path.endsWith('/resolve')&&r.body.outcome==='not-applied'));
 await goto('/workers');await waitText('#workerRoster','stale-worker');await page.locator('#workerRoster .worker-capability-chip').first().click();await waitText('#workerCapabilityContent','Executable unavailable');assert.match(page.url(),/\/worker-capabilities\//);assert.equal(await page.locator('dialog').count(),0);await page.reload();await waitText('#workerCapabilityContent','Executable unavailable');await page.locator('#workerCapabilityView a[href="/workers"]').click();
 await goto('/automation');await page.locator('#createAutomation').click();
 await page.locator('dialog [name="id"]').fill('daily');await page.locator('dialog [name="kind"]').selectOption('interval');await page.locator('dialog [name="interval"]').fill('2');await page.locator('dialog [name="unit"]').selectOption('hours');await page.locator('dialog button[type="submit"]').click();await waitText('#automationList','daily');
 assert.equal(requests.find(r=>r.path==='/v1/schedules'&&r.method==='POST').body.intervalSeconds,7200);
 await page.locator('#automationList [data-ops-action="automation-toggle"]').click();await waitText('#automationList','paused');
 await page.locator('#automationList [data-ops-action="automation-history"]').click();await waitText('#automationHistory','started');
 await page.getByRole('button',{name:'Webhooks',exact:true}).click();await page.locator('#createAutomation').click();await page.locator('dialog [name="id"]').fill('inbound');await page.locator('dialog [name="secret"]').fill('test-secret-only-123');await page.locator('dialog button[type="submit"]').click();await waitText('#automationList','inbound');assert.doesNotMatch(await page.locator('#automationList').innerText(),/test-secret-only/);
 await page.getByRole('button',{name:'Subscriptions',exact:true}).click();await page.locator('#createAutomation').click();await page.locator('dialog [name="id"]').fill('outbound');await page.locator('dialog [name="target"]').fill('https://example.invalid/events');await page.locator('dialog [name="secret"]').fill('test-secret-only-123');await page.locator('dialog button[type="submit"]').click();await waitText('#automationList','outbound');
 await page.getByRole('button',{name:'Dead letters',exact:true}).click();await page.getByRole('button',{name:'Review redelivery'}).click();await page.locator('dialog input[type="checkbox"]').check();await page.locator('dialog button[type="submit"]').click();await waitText('#automationList','No dead letters');
 await goto('/runs/run-a');await page.locator('#forkRunBtn').click();await page.locator('dialog button[type="submit"]').click();await waitText('#wbTitle','Operational fixture');assert.equal(requests.find(r=>r.path.endsWith('/fork')).body.start,false);
 await page.getByRole('tab',{name:'Operations & usage'}).click();await waitText('#runAttemptsPanel','generic/echo');await page.locator('#runAttemptsPanel [data-attempt-details]').click();await waitText('#outputModalBody','executionDurationMs');await page.locator('#closeOutputModal').click();
 await page.getByRole('tab',{name:'Artifacts',exact:true}).click();await waitText('#runRevisionsPanel','report');await page.locator('#runRevisionsPanel [data-ops-action="check-artifact"]').click();await waitText('#runRevisionsPanel','Available');await page.locator('#runRevisionsPanel [data-ops-action="view-revision"]').click();await waitText('#outputModalBody','Snapshot content');await page.locator('#closeOutputModal').click();
 await goto('/retention');await page.getByRole('button',{name:'Pin data',exact:true}).click();await page.locator('dialog [name="id"]').fill('run-a');await page.locator('dialog button[type="submit"]').click();await waitText('#retentionPins','run-a');await page.getByRole('button',{name:'Review unpin'}).click();await page.locator('dialog button[type="submit"]').click();await waitText('#retentionPins','No explicit pins');
 await page.getByRole('button',{name:'Set run retention deadline',exact:true}).click();await page.locator('dialog [name="id"]').fill('run-a');await page.locator('dialog [name="until"]').fill('2030-01-02T12:00');await page.locator('dialog button[type="submit"]').click();await page.waitForFunction(()=>!document.querySelector('dialog'));assert.ok(requests.some(r=>r.path==='/v1/retention'&&r.body?.retainedUntil));
 await page.setViewportSize({width:390,height:844});await goto('/operations');await waitText('#operationsView','Operations');assert.ok(await page.locator('#operationsView').isVisible());assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth+1));await page.screenshot({path:'/private/tmp/muxlio-ui-browser-check/operations-mobile.png',fullPage:true});
 assert.equal(requests.filter(r=>r.method==='POST'&&r.path.endsWith('/start')).length,0,'reviewing operations and importing evidence never starts work');
 assert.deepEqual(errors,[]);console.log('Operational browser flows passed: reconciliation, worker health, automation, fork, revisions, and retention.');
}finally{await browser.close();ui.closeAllConnections();coordinator.closeAllConnections();await Promise.all([new Promise(r=>ui.close(r)),new Promise(r=>coordinator.close(r))]);}
