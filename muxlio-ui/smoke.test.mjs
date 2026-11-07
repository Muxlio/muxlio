import assert from 'node:assert/strict';
import vm from 'node:vm';
import { createServer as createCoordinator } from 'node:http';
import { once } from 'node:events';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { createUIServer } from './server.mjs';

async function freePort() {
  const server = createCoordinator();
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const { port } = server.address();
  await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
  return port;
}

const coordinator = createCoordinator((req, res) => {
  res.writeHead(200, { 'content-type': 'application/json' });
  res.end(JSON.stringify({ path: req.url, method: req.method }));
});
coordinator.listen(0, '127.0.0.1');
await once(coordinator, 'listening');

const ui = createUIServer({ coordinatorURL: `http://127.0.0.1:${coordinator.address().port}` });
ui.listen(0, '127.0.0.1');
await once(ui, 'listening');
const baseURL = `http://127.0.0.1:${ui.address().port}`;

try {
  const page = await fetch(baseURL);
  assert.equal(page.status, 200);
  assert.match(page.headers.get('content-type'), /text\/html/);
  const html = await page.text();
  const app = (await Promise.all(['format.js', 'markdown.js', 'core.js', 'workbench.js', 'inspector.js', 'live.js', 'operations.js', 'boot.js'].map(async name => {
    const response = await fetch(`${baseURL}/js/${name}`);
    assert.equal(response.status, 200, name);
    return response.text();
  }))).join('\n');
  for (const feature of ['Durable agent workflows', 'Overview', 'Runs', 'Live', 'Workers', 'Workflow graph', 'Execution journal', 'Click a node', 'aria-label="Main navigation"']) {
    assert.ok(html.includes(feature), `app should render ${feature}`);
  }
  assert.match(html, /src="\/js\/core.js"/);
  assert.match(html, /href="\/app.css\?v=[^"\s]+"/);
  assert.match(html, /src="\/js\/workbench.js\?v=[^"\s]+"/);
  assert.match(app, /function graphMarkup\(acts,scopes,runComplete\)/, 'graph renderer should include root-scope activations');
  assert.match(app, /function renderRuns\(\)/, 'run search, filter, and sort interactions should be wired');
  assert.match(app, /function paginate\(items,page,pageSize\)/, 'run history should use the reusable pager helper');
  assert.match(app, /pagerMarkup\(runsPage,pageCount,total\)/, 'run history should render page navigation after filtering and sorting');
  const pagingSource = app.slice(app.indexOf('function paginate('), app.indexOf('async function loadRuns('));
  const paging = vm.runInNewContext(`${pagingSource};({paginate,pagerMarkup})`);
  const records = Array.from({length:51}, (_, index) => index + 1);
  assert.deepEqual(Array.from(paging.paginate(records, 2, 25).items), records.slice(25, 50));
  assert.equal(paging.paginate(records, 99, 25).page, 3, 'stale page numbers clamp to the last page');
  assert.equal(paging.paginate([], 1, 25).pageCount, 1, 'empty results retain a valid page');
  assert.match(paging.pagerMarkup(1, 3, 51), /data-page="2"/);
  assert.match(html, /id="runInputInspector"/, 'run input should use a structured inspector');
  assert.match(html, /id="runOutputInspector"/, 'run output should use an artifact inspector');
  assert.match(app, /function treeMarkup\(/, 'nested input values should render as an expandable tree');
  assert.match(app, /function outputMarkup\(/, 'outputs should be rendered by the artifact browser');
  assert.match(app, /data-download="artifact"/, 'artifact downloads should be available');
  assert.match(app, /PREVIEW_LIMIT=4096/, 'text previews should stay bounded');
  const helperSource = app.match(/const PREVIEW_LIMIT=4096;[\s\S]*?let currentRunInput,currentRunOutput;/)?.[0];
  assert.ok(helperSource, 'inspector rendering helpers should be present');
  const markdownSource = await (await fetch(`${baseURL}/js/markdown.js`)).text();
  const documentStub = {getElementById(){return null},addEventListener(){}};
  const helpers = vm.runInNewContext(`${markdownSource};const esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])); ${helperSource}; ({treeMarkup,inputMarkup,outputMarkup,safePreview})`, { TextEncoder, document:documentStub });
  const nested = helpers.inputMarkup({name:'<script>alert(1)</script>', nested:[1,true,null,{ok:'yes'}]});
  assert.match(nested, /<details/);
  assert.match(nested, /<span class="data-type">boolean<\/span>/);
  assert.match(nested, /null/);
  assert.ok(!nested.includes('<script>'), 'API values should be escaped');
  assert.match(helpers.inputMarkup(null), /Input is null/);
  const output = helpers.outputMarkup({report:{mime:'text/markdown',path:'reports/out.md',digest:'sha256:abc',size:12,data:'# Hello <script>'}, map:[{ok:true}]});
  for (const metadata of ['text/markdown','reports/out.md','sha256:abc','12']) assert.ok(output.includes(metadata), `artifact metadata should include ${metadata}`);
  assert.match(output, /data-view-artifact="report"/, 'artifact cards open the shared output viewer');
  assert.ok(!output.includes('# Hello <script>'), 'artifact preview must not inject HTML');
  assert.match(output, /map/);
  assert.equal(helpers.safePreview('x'.repeat(5000), 'text/plain').text.length, 4096);
  assert.match(helpers.outputMarkup({data:{mime:'application/json',path:'x.json',data:{a:1}}}), /data-view-artifact="data"/);
  assert.match(helpers.outputMarkup({data:{mime:'application/octet-stream',path:'x.bin',data:'abc'}}), /application\/octet-stream/);
  let copied, downloaded, filename, clipboardFailure, copyCalls=0;
  const actions = vm.runInNewContext(`const esc=String;${helperSource};currentRunOutput={report:{data:'wrong run payload'}};inspectorAction`, {
    TextEncoder, Blob, ArrayBuffer, selectedActivation:'selected', activationData:[{id:'selected',outputs:{report:{mime:'text/markdown',path:'report.md',data:'# Selected\n\n**raw payload**'}}}],
    navigator:{clipboard:{async writeText(value){copied=value;copyCalls++}}},
    message(value){clipboardFailure=value},
    URL:{createObjectURL(blob){downloaded=blob;return 'blob:test'},revokeObjectURL(){}},
    document:{...documentStub,createElement(){return {click(){filename=this.download}}}},setTimeout(){},
  });
  const actionEvent = kind => ({target:{closest(selector){if(selector==='#inspectorContent')return {};if(selector==='[data-'+kind+']'||(kind==='download'&&selector==='[data-copy]'))return {dataset:{copy:'run-output',...(kind==='download'?{download:'artifact'}:{}),name:'report'}};return null}}});
  await actions(actionEvent('copy'));
  assert.equal(copied,JSON.stringify('# Selected\n\n**raw payload**'));
  await actions(actionEvent('download'));
  assert.equal(await downloaded.text(),'# Selected\n\n**raw payload**');
  assert.equal(copyCalls,1,'Download must not invoke the copy action');
  assert.equal(filename,'report.md');

  // Exercise overlapping responses for the same activation/tab, not just tab changes.
  const pending = [];
  const inspector = {innerHTML: "", scrollTop: 35, scrollLeft: 7, isConnected: true};
  const labels = {textContent: ""};
  const renderSource = app.slice(app.indexOf("function renderInspector()"), app.indexOf("async function loadActivation("));
  const render = vm.runInNewContext(`let inspectorRequest=0; const selectedActivation="a",inspectorTab="logs",current="run",activationData=[{id:"a",nodeId:"task",state:"running"}];${renderSource};renderInspector`, {
    $(selector){return selector==="#inspectorContent"?inspector:labels},
    api(){return new Promise(resolve=>pending.push(resolve))},esc:String,date:String,
    atob,TextDecoder,Uint8Array,
  });
  render(); render();
  const encoded = Buffer.from("new café ✓").toString("base64");
  pending[1]({logs:[{data:encoded,stream:"executor"}]});
  await new Promise(resolve=>setImmediate(resolve));
  assert.match(inspector.innerHTML,/new café ✓/);
  assert.equal(inspector.scrollTop,35);
  assert.equal(inspector.scrollLeft,7);
  pending[0]({logs:[{data:Buffer.from("old response").toString("base64")}]});
  await new Promise(resolve=>setImmediate(resolve));
  assert.ok(!inspector.innerHTML.includes("old response"),"older same-tab response must not overwrite fresh logs");

  assert.match(app,/scheduleTranscriptPoll\(act,current\)/,'workbench inspector should poll activation logs while running');
  assert.match(html,/id="liveView"/,'live output page should be present');
  assert.match(app,/hash==='#live'/, 'navigation should route to the live output page');
  assert.match(app,/logs\?activationId=/,'live page should reuse the log chunk API');
  const liveSource = await (await fetch(`${baseURL}/js/live.js`)).text();
  assert.match(liveSource,/function stopLivePoll\(/,'live page should stop polling on navigation');
  const appCss = await (await fetch(`${baseURL}/app.css`)).text();
  assert.match(appCss,/\.live-monitor-body\{[^}]*overflow:visible/, 'live transcript should grow with the page scroll');

  const workspace = await (await fetch(`${baseURL}/workspace.js`)).text();
  assert.match(workspace, /e\.target\?\.closest\?\.\('#inspRerun'\)/, 'inspector retry should tolerate non-element click targets');
  assert.match(workspace, /data-run-action="retry"/, 'failed and cancelled runs should expose the run retry action');
  assert.match(workspace, /async function retryRun\(/, 'run retry should use the in-app modal flow');
  assert.match(workspace, /if\(retryModalResolve\)return Promise\.resolve\(false\)/, 'only one retry confirmation should be pending at a time');
  assert.match(workspace, /retryRunModalCancel'\)\?\.addEventListener\('click',\(\)=>closeRetryRunModal\(false\)/, 'cancel should close the dialog without confirming');
  assert.match(workspace, /retryRunModalConfirm'\)\?\.addEventListener\('click',\(\)=>closeRetryRunModal\(true\)/, 'confirm should proceed with retry');
  assert.match(workspace, /e\.target===e\.currentTarget\)closeRetryRunModal\(false\)/, 'backdrop clicks should cancel');
  assert.match(workspace, /e\.key!=='Escape'[\s\S]*?closeRetryRunModal\(false\)/, 'Escape should cancel');
  assert.match(workspace, /api\(`\/v1\/runs\/\$\{encodeURIComponent\(runId\)\}\/retry`,\{method:'POST'\}\)/, 'confirmed retry should POST to the run retry endpoint');
  assert.match(workspace, /await openRun\(runId\)/, 'successful retry should refresh the run');
  assert.doesNotMatch(workspace, /action==='retry'&&!confirm/, 'run retry must not use window.confirm');
  assert.doesNotMatch(workspace, /inspRerun[\s\S]{0,120}confirm\(/, 'inspector retry must not use window.confirm');
  assert.match(html, /id="retryRunModal"/, 'retry confirmation modal should be present in the shell');
  assert.match(html, /retryRunModalConfirm/, 'retry modal should expose a confirm action');
  assert.match(html, /Already-succeeded steps will be kept/, 'retry modal should explain skipped succeeded steps');
  // cancel run modal
  assert.match(html, /id="cancelRunModal"/, 'cancel confirmation modal should be present in the shell');
  assert.match(html, /cancelRunModalConfirm/, 'cancel modal should expose a confirm action');
  assert.match(html, /An operation already in progress may have external effects/, 'cancel modal should warn about in-progress effects');
  assert.match(workspace, /if\(cancelModalResolve\)return Promise\.resolve\(false\)/, 'only one cancel confirmation should be pending at a time');
  assert.match(workspace, /cancelRunModalCancel'\)\?\.addEventListener\('click',\(\)=>closeCancelRunModal\(false\)/, 'keep-running button should close the dialog without confirming');
  assert.match(workspace, /cancelRunModalConfirm'\)\?\.addEventListener\('click',\(\)=>closeCancelRunModal\(true\)/, 'cancel-run button should confirm cancellation');
  assert.match(workspace, /e\.target===e\.currentTarget\)closeCancelRunModal\(false\)/, 'cancel modal backdrop clicks should dismiss');
  assert.match(workspace, /e\.key!=='Escape'[\s\S]*?closeCancelRunModal\(false\)/, 'Escape should dismiss the cancel modal');
  assert.doesNotMatch(workspace, /action==='cancel'&&!confirm/, 'cancel action must not use window.confirm');
  // copy run ID removed: workbench controls no longer include a copy-id button
  assert.doesNotMatch(workspace, /copy-id/, 'workbench controls should not include a copy-id button');
  assert.doesNotMatch(workspace, /Copy run ID/, 'workbench should not have a Copy run ID button');
  const inspectorSource = await (await fetch(`${baseURL}/js/inspector.js`)).text();
  assert.match(inspectorSource, /actionsEl\.hidden=!\['failed','cancelled'\]\.includes\(wbCurrentRunState\)/, 'inspector retry should only be available for failed or cancelled runs');
  const css = await (await fetch(`${baseURL}/workspace.css`)).text();
  const progressSource = workspace.slice(workspace.indexOf('function deriveProgress'), workspace.indexOf('function captureDetail'));
  const progress = vm.runInNewContext(`${progressSource}; ({deriveProgress,progressMarkup})`, { esc: String, duration: () => '2 s' });
  const acts = [
    {id:'a',nodeId:'implement',scopePath:['map:features:feature-a'],state:'succeeded'},
    {id:'b',nodeId:'review',scopePath:['map:features:feature-a'],state:'running'},
    {id:'c',nodeId:'verify',scopePath:['map:features:feature-b'],state:'succeeded'},
    {id:'root',nodeId:'collect',scopePath:[],state:'waiting'},
  ];
  const attempts = [{activationId:'b',retryNumber:1,executor:'kiro',state:'failed',failure:'diagnostic'}, {activationId:'b',retryNumber:2,executor:'codex',state:'queued'}];
  const derived = progress.deriveProgress(acts, attempts);
  assert.equal(derived.known, 2);
  assert.equal(derived.completed, 1);
  assert.equal(derived.groups[0].id, 'feature-a');
  assert.equal(derived.groups[0].stages[1].state, 'queued');
  assert.equal(progress.deriveProgress([acts[0]]).completed,0,'implementation alone must not imply a completed feature pipeline');
  const markup = progress.progressMarkup(acts, attempts);
  for (const label of ['1 completed / 2 known', 'codex', 'diagnostic', 'Root workflow', 'data-stage="b"']) assert.ok(markup.includes(label));
  assert.ok(!markup.includes('%'), 'unknown totals must not produce percentages');
  assert.match(css, /max-width:620px/);
  assert.match(css, /minmax\(0,1fr\)/);
  assert.match(css, /max-width:none/);
  assert.match(html, /id="eventsPager"/);
  assert.match(app, /e.target.closest\('#inspectorContent'\)/, 'artifact actions must use selected activation data');
  assert.match(app, /Copy failed:/);
  assert.match(app, /tab!==inspectorTab/, 'tab responses must be fenced');
  const timers = new Map();
  const polling = vm.runInNewContext(`let refreshTimer=null,refreshPaused=false;function refreshWorkspace(){};${workspace.slice(workspace.indexOf('function stopRefresh'), workspace.indexOf('async function refreshWorkspace'))};({scheduleRefresh,stopRefresh,pause(){refreshPaused=true}})`, {
    document:{hidden:false}, location:{hash:'#runs'}, setTimeout(fn,ms){assert.equal(ms,5000);timers.set(1,fn);return 1}, clearTimeout(id){timers.delete(id)},
  });
  polling.scheduleRefresh();polling.scheduleRefresh();assert.equal(timers.size,1);
  polling.stopRefresh();assert.equal(timers.size,0);
  polling.pause();polling.scheduleRefresh();assert.equal(timers.size,0);
  assert.match(workspace, /requestControllers\)c.abort/);
  assert.match(workspace, /if\(saved\)restoreDetail\(saved\)/);
  const pagination = vm.runInNewContext(`const RUN_DETAIL_PAGE_SIZE=50;let eventRecords=[],after=0,hasMore=false,eventCursors=[0],eventPageIndex=0,wbHasMore=false;${workspace.slice(workspace.indexOf('function mergeEvents'),workspace.indexOf('function stopEventStream'))};({mergeEvents,get:()=>({eventRecords,after,hasMore})})`);
  pagination.mergeEvents({events:[{seq:2},{seq:1}],nextAfter:2,hasMore:true});
  pagination.mergeEvents({events:[{seq:2},{seq:3}],nextAfter:3,hasMore:false});
  assert.deepEqual(Array.from(pagination.get().eventRecords,e=>e.seq),[2,3]);
  assert.equal(pagination.get().after,3);
  assert.equal(pagination.get().hasMore,false);

  for (const path of ['/v1/runs?state=running', '/healthz']) {
    const response = await fetch(`${baseURL}${path}`);
    assert.equal(response.status, 200);
    assert.deepEqual(await response.json(), { path, method: 'GET' });
  }

  const post = await fetch(`${baseURL}/v1/runs`, { method: 'POST', body: '{"plan":{}}' });
  assert.deepEqual(await post.json(), { path: '/v1/runs', method: 'POST' });

  const standalonePort = await freePort();
  const standalone = spawn(process.execPath, [join(dirname(fileURLToPath(import.meta.url)), 'server.mjs')], {
    env: { ...process.env, PORT: String(standalonePort), MUXLIO_API_URL: `http://127.0.0.1:${coordinator.address().port}` },
    stdio: 'ignore',
  });
  try {
    let response;
    for (let attempt = 0; attempt < 30; attempt++) {
      try { response = await fetch(`http://127.0.0.1:${standalonePort}`); break; } catch { await new Promise(resolve => setTimeout(resolve, 50)); }
    }
    assert.ok(response, 'standalone server should start');
    assert.equal(response.status, 200);
    assert.match(await response.text(), /Workflow overview/);
    const proxied = await fetch(`http://127.0.0.1:${standalonePort}/v1/runs?state=running`);
    assert.deepEqual(await proxied.json(), { path: '/v1/runs?state=running', method: 'GET' });
  } finally {
    standalone.kill('SIGTERM');
  }
  console.log('UI server smoke check passed');
} finally {
  ui.close();
  coordinator.close();
}
