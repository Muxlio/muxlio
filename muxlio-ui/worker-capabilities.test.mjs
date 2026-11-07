import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';
import test from 'node:test';

const source=readFileSync(new URL('./js/inspector.js',import.meta.url),'utf8');
const render=vm.runInNewContext(source.slice(source.indexOf('function formatWorkerCapability('),source.indexOf('async function loadWorkers('))+';workerCapabilityDetailMarkup',{
 esc:value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))
});
const chip=vm.runInNewContext(source.slice(source.indexOf('function formatWorkerCapability('),source.indexOf('async function loadWorkers('))+';workerCapabilityMarkup',{
 esc:value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))
});
test('worker chips retain readiness while deferring verbose details',()=>{
 const html=chip({name:'generic/echo',version:'2',readiness:{ready:false,checks:[{name:'binary',passed:false,message:'Verbose diagnostic'}]}},'worker');
 assert.match(html,/worker-capability-chip--blocked/);assert.match(html,/Inspect generic\/echo: Blocked/);assert.doesNotMatch(html,/Verbose diagnostic|Version 2|<details/);
 assert.match(html,/href="#worker-capability\/worker\/generic%2Fecho\/2"/);assert.doesNotMatch(html,/data-ops-action|<button/);
});

test('capability page loads directly from the API and handles withdrawn capabilities',async()=>{
 const hub=readFileSync(new URL('./js/operations-hub.js',import.meta.url),'utf8');
 let workers=[{workerId:'worker',capabilities:[{name:'generic/echo',version:'2',readiness:{ready:true}}]}],html='',requests=0;
 const load=vm.runInNewContext(hub.slice(hub.indexOf('async function loadWorkerCapabilityPage('),hub.indexOf('const automationNames='))+';loadWorkerCapabilityPage',{
  api:async path=>{assert.equal(path,'/v1/workers?includeStale=true');requests++;return {registeredWorkers:workers}},
  hubSet:(_id,value)=>{html=value},hubRead:async(_id,work)=>{html=await work()},esc:s=>String(s),workerCapabilityDetailMarkup:render
 });
 await load('worker','generic/echo','2');assert.match(html,/<details open /);assert.match(html,/Version 2/);assert.match(html,/worker-readiness--ready/);
 workers=[];await assert.rejects(load('worker','generic/echo','2'),/no longer registered/);
 workers=[{workerId:'worker',capabilities:[]}];await assert.rejects(load('worker','generic/echo','2'),/no longer reported/);assert.equal(requests,3);
});
test('worker capability cards show reported readiness and supported features',()=>{
 const html=render({name:'generic/echo',version:'2',readiness:{ready:true,checks:[{name:'binary',passed:true,message:'Available'}]},contract:{contractVersion:'1',capabilities:{session:true,usage:false}}},'worker');
 assert.match(html,/worker-readiness--ready/);assert.match(html,/Sessions/);assert.doesNotMatch(html,/>Usage reporting</);assert.match(html,/Version 2/);assert.match(html,/aria-label="Passed"/);
});
test('failed and unavailable readiness stay distinct and details are escaped',()=>{
 const html=render({name:'<script>',readiness:{ready:false,checks:[{name:'auth',passed:false,message:'<img src=x>'},{name:'tools'}]},contract:{environment:{digest:'sha256:environment'}}},'worker');
 assert.match(html,/worker-readiness--blocked/);assert.match(html,/1 check needs attention/);assert.match(html,/aria-label="Not reported"/);assert.doesNotMatch(html,/<script>|<img/);
 assert.match(render('legacy/echo'),/worker-readiness--unknown/);assert.match(render('legacy/echo'),/No readiness check details reported/);
});
