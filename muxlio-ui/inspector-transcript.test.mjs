import assert from 'node:assert/strict';
import vm from 'node:vm';

const mergeSource = `
const activationTranscripts=new Map();
function decodeLogData(data){return data}
function mergeActivationLogs(actId,logs){
  let state=activationTranscripts.get(actId);
  if(!state){state={agent:'',seen:new Set()};activationTranscripts.set(actId,state)}
  const agentChunks=(logs||[]).filter(x=>x.stream==='agent').sort((a,b)=>(a.seq||0)-(b.seq||0));
  for(const chunk of agentChunks){
    const key=chunk.stream+':'+chunk.seq;
    if(state.seen.has(key))continue;
    state.seen.add(key);
    state.agent+=decodeLogData(chunk.data);
  }
  return state.agent;
}
`;

const merge = vm.runInNewContext(`${mergeSource}; mergeActivationLogs`, {});
assert.equal(
  merge('a', [{ stream: 'agent', seq: 1, data: 'partial ' }, { stream: 'agent', seq: 2, data: 'text' }]),
  'partial text',
);
console.log('inspector transcript merge check passed');

// ── resolveInspectorAgent tests ──────────────────────────────────────────
// The resolve function reads `runAttempts` from the enclosing scope.
const resolveSource = `
function resolveInspectorAgent(act) {
  const attempts = typeof runAttempts !== 'undefined' ? runAttempts : [];
  const actAttempts = attempts.filter(a => a.activationId === act.id && a.state !== 'queued')
    .sort((a, b) => (b.retryNumber || 0) - (a.retryNumber || 0));
  return actAttempts[0]?.executor || act.executorHint || act.executor || '—';
}
`;

// Test: falls back to activation executor when no attempts
const resolve1 = vm.runInNewContext(`${resolveSource}; resolveInspectorAgent`, { runAttempts: [] });
assert.equal(
  resolve1({ id: 'act-1', executor: 'agent/kiro' }),
  'agent/kiro',
  'should use activation executor when no attempts',
);

const resolveQueued = vm.runInNewContext(`${resolveSource}; resolveInspectorAgent`, {
  runAttempts: [
    { activationId: 'act-4', executor: 'agent/kiro', retryNumber: 1, state: 'failed' },
    { activationId: 'act-4', executor: 'agent/cursor', retryNumber: 2, state: 'queued' },
  ],
});
assert.equal(resolveQueued({ id: 'act-4', executor: 'agent/kiro' }), 'agent/kiro',
  'a queued retry must not replace the executor that actually ran');

// Test: prefers latest attempt executor over activation executor
const resolve2 = vm.runInNewContext(`${resolveSource}; resolveInspectorAgent`, {
  runAttempts: [
    { activationId: 'act-2', executor: 'agent/cursor', retryNumber: 1, state: 'failed' },
    { activationId: 'act-2', executor: 'agent/kiro', retryNumber: 2, state: 'running' },
  ],
});
assert.equal(
  resolve2({ id: 'act-2', executor: 'agent/old' }),
  'agent/kiro',
  'should use latest attempt executor (highest retryNumber)',
);

// Test: returns — when nothing is set
const resolve3 = vm.runInNewContext(`${resolveSource}; resolveInspectorAgent`, { runAttempts: [] });
assert.equal(
  resolve3({ id: 'act-3' }),
  '—',
  'should return — when no executor is set',
);

console.log('inspector agent resolution checks passed');

// Exercise the production renderer and view functions, rather than copies.
const { readFile } = await import('node:fs/promises');
const source = name => readFile(new URL(name, import.meta.url), 'utf8');
const [markdown, inspector, live, transcript, page] = await Promise.all([
  source('js/markdown.js'), source('js/inspector.js'), source('js/live.js'),
  source('js/transcript.js'), source('index.html'),
]);
const classes = new Set();
const listeners = new Map();
const modalElements = new Map(['outputModal','outputModalBody','outputModalTitle','closeOutputModal'].map(id=>[id,{
  hidden:true, innerHTML:'', textContent:'',
  focus(){},
  addEventListener:(type,listener)=>listeners.set(`${id}:${type}`,listener),
}]));
const scroll = {scrollHeight:500, scrollTop:400, clientHeight:100};
const body = {
  innerHTML:'', textContent:'', scrollHeight:500, scrollTop:400, clientHeight:100,
  classList:{add:c=>classes.add(c), remove:c=>classes.delete(c)},
  closest:()=>scroll,
};
const context = vm.createContext({
  TextEncoder, TextDecoder, Uint8Array, atob, Blob,
  document:{
    getElementById:id=>id==='refreshTranscript'?null:modalElements.get(id)||body,
    querySelector:()=>scroll,
    addEventListener:(type,listener)=>listeners.set(`document:${type}`,listener),
  },
  $:selector=>modalElements.get(selector.slice(1))||body,
  esc:s=>String(s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])),
});
vm.runInContext(`${markdown}\n${inspector}\n${live}\n${transcript}`, context);
assert.equal(typeof listeners.get('closeOutputModal:click'),'function');
assert.equal(typeof listeners.get('outputModal:click'),'function');
assert.equal(typeof listeners.get('document:keydown'),'function');
const render = context.renderMarkdown;
const formatted = '# Report\n\nFirst **bold** and *emphasis*.\n\nSecond paragraph with `a < b` and [docs](https://example.test/?a=1&b=2).\n\n- one\n- two\n  - nested\n\n3. third\n4. fourth\n\n```js\nconst x = "<script>";\n```';
const rendered = render(formatted);
for(const expected of [
  '<h1>Report</h1>', '<p>First <strong>bold</strong> and <em>emphasis</em>.</p>',
  '<p>Second paragraph', '<code>a &lt; b</code>',
  'href="https://example.test/?a=1&amp;b=2"', '<ul><li><p>one</p></li>',
  '<p>nested</p>', '<ol start="3">', '<pre><code>const x = &quot;&lt;script&gt;&quot;;</code></pre>',
])assert.ok(rendered.includes(expected), `Markdown should contain ${expected}`);
assert.equal(render('`**literal** [link](https://example.test)`'), '<p><code>**literal** [link](https://example.test)</code></p>');
assert.match(render('[email](mailto:reader@example.test) [relative](/docs) [anchor](#part)'), /href="mailto:reader@example.test"/);
assert.match(render('[docs](https://example.test/function(x))'), /href="https:\/\/example.test\/function\(x\)"/);
assert.match(render('[entity](https://example.test/?a=&amp;)'), /a=&amp;amp;/, 'URLs must escape exactly once');
assert.match(render('~~~ html\r\n<img src=x onerror=alert(1)>\r\n~~~'), /<pre><code>&lt;img/);
assert.equal(render('```js\n**streamed'), '<pre><code>**streamed</code></pre>');
assert.match(render('```\ncode\n``` trailing\nmore\n```'), /``` trailing\nmore/, 'only a standalone fence closes code');
for(const incomplete of ['```js extra', '---suffix', '***unfinished', '###', '~~~ unfinished info']){
  assert.equal(typeof vm.runInContext(`renderMarkdown(${JSON.stringify(incomplete)})`,context,{timeout:1000}), 'string');
}
for(const unsafe of [
  'javascript:alert(1)', 'JaVaScRiPt:alert(1)', 'data:text/html,<script>alert(1)</script>',
  'vbscript:msgbox(1)', 'file:///etc/passwd', 'java\nscript:alert(1)',
  'java\tscript:alert(1)', 'javascript&#58;alert(1)', '//example.test', '\\example.test',
]){
  assert.doesNotMatch(render(`[unsafe](${unsafe})`), /<a\b/, `${unsafe} must be inert`);
}
const hostile = '<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n\n<iframe src="https://example.test"></iframe>\n\n![image](https://example.test/image.png)';
assert.doesNotMatch(render(hostile), /<(script|img|iframe|object|embed)\b/i);
assert.match(render(hostile), /&lt;script&gt;alert\(1\)&lt;\/script&gt;/, 'HTML remains readable text');
assert.doesNotMatch(render('[" onclick="alert](https://example.test/" onmouseover="alert)'), /<a\b/);

for(const update of ['updateTranscriptPanel','updateLiveTranscript','updateTranscriptPageBody']){
  context[update](formatted,true);
  assert.equal(body.innerHTML, rendered, `${update} renders Markdown`);
  context[update]('**partial',true);
  context[update]('**partial complete**',true);
  assert.equal(body.innerHTML, '<p><strong>partial complete</strong></p>', 'stream updates reparse the raw text');
  body.scrollTop=0;scroll.scrollTop=0;
  context[update](formatted,true);
  assert.equal(body.scrollTop,0,'preserve inspector scroll when reading earlier output');
  assert.equal(scroll.scrollTop,0,'preserve page scroll when reading earlier output');
  context[update]('',true);
  assert.equal(body.textContent,'Waiting for agent output…');
  assert.ok(classes.has('waiting'));
  context[update]('',false);
  assert.match(body.textContent,/No (live )?transcript recorded\./);
  assert.ok(!classes.has('waiting'));
}
const encoded = Buffer.from(formatted).toString('base64');
context.logs = [{stream:'agent',seq:1,data:encoded}];
assert.equal(vm.runInContext("mergeActivationLogs('markdown',logs)",context),formatted,'cached output stays raw');
assert.equal(vm.runInContext("mergeActivationLogs('markdown',logs)",context),formatted,'repeated chunks stay deduplicated');
assert.match(context.transcriptSectionMarkup({id:'markdown',state:'running'},'run'), /<h1>Report<\/h1>/, 'initial live inspector uses Markdown');
assert.equal(context.transcriptSectionMarkup({id:'markdown',state:'succeeded'},'run'), '', 'completed transcripts use the dedicated transcript page');
for(const id of ['liveTranscriptBody','transcriptPageBody']){
  assert.match(page,new RegExp(`<div class="[^"]*markdown-body" id="${id}">`),'block markup uses a div container');
}
assert.ok(page.indexOf('/js/markdown.js')<page.indexOf('/js/inspector.js'),'renderer loads before consumers');
const artifact = {report:{mime:'text/markdown; charset=utf-8',path:'report.md',data:formatted}};
const original = JSON.stringify(artifact);
assert.match(context.outputMarkup(artifact), /data-view-artifact="report"/, 'artifact cards expose the output viewer');
await context.showArtifactOutput(artifact.report,'report');
assert.equal(modalElements.get('outputModal').hidden,false);
assert.match(modalElements.get('outputModalBody').innerHTML, /<h1>Report<\/h1>/, 'Markdown artifacts render in the output viewer');
assert.equal(JSON.stringify(artifact),original,'previews do not mutate output data');
await context.showArtifactOutput({mime:'text/plain',data:'# raw'},'report');
assert.match(modalElements.get('outputModalBody').innerHTML, /<pre class="output-raw"># raw<\/pre>/, 'plain text remains raw in the output viewer');
listeners.get('closeOutputModal:click')();
assert.equal(modalElements.get('outputModal').hidden,true);
console.log('safe Markdown and production output view checks passed');

// Review regressions: preserve literal heading suffixes and bound incomplete links.
assert.equal(render('# C#'), '<h1>C#</h1>');
assert.equal(render('## Release# ###'), '<h2>Release#</h2>');
for (const malformed of ['['.repeat(100000), '[x]('.repeat(25000)]) {
  context.malformed = malformed;
  const result = vm.runInContext('renderMarkdown(malformed)', context, {timeout:1000});
  assert.equal(result, `<p>${malformed}</p>`, 'unfinished links remain readable without repeated suffix scans');
}
assert.match(render('[outer]([inner](https://example.test)'), /<a href="https:\/\/example.test"[^>]*>inner<\/a>/, 'an unfinished outer link does not hide a complete inner link');
assert.match(context.outputMarkup('# Direct output\n\n**readable**'), /<h1>Direct output<\/h1>/);
assert.match(context.outputMarkup({answer:'- first\n- second'}), /<ul><li>/);
assert.doesNotMatch(context.outputMarkup({answer:'<script>alert(1)</script>'}), /<script>/);
assert.doesNotMatch(context.inputMarkup('# Raw input'), /<h1>/, 'input inspection retains raw values');

// Exercise the actual workbench inspector, which is the primary activation view.
let workbenchMarkup;
const workbenchOutput = {report:{mime:'text/markdown',path:'report.md',data:formatted}};
const workbenchOriginal = JSON.stringify(workbenchOutput);
Object.assign(context, {
  activationData:[{id:'review-output'}], current:'run', wbCurrentRunState:'succeeded',
  nodeDisplayName:value=>value, stateClass:()=>'', buildAttemptUsageMarkup:()=>'',
  replaceHTMLPreservingDetails:(_element,html)=>{workbenchMarkup=html},
});
context.renderInspectorWb({id:'review-output',nodeId:'task.review',state:'succeeded',outputs:workbenchOutput});
assert.match(workbenchMarkup, /data-view-artifact="report"/, 'workbench output artifacts expose the shared output viewer');
await context.showArtifactOutput(workbenchOutput.report,'report');
assert.match(modalElements.get('outputModalBody').innerHTML, /<article class="markdown-body"><h1>Report<\/h1>/, 'workbench artifact viewer uses the shared renderer');
assert.equal(JSON.stringify(workbenchOutput),workbenchOriginal);
console.log('Markdown review regression checks passed');
