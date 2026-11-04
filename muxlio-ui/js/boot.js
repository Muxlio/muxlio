/* ── event bindings ───────────────────────────── */
// Safe querySelector helper - won't throw on missing elements
function get(id){return document.getElementById(id)}

get('overviewNav').onclick=()=>{};
get('runsNav').onclick=()=>{};
get('workersNav').onclick=()=>{};
const refreshOverview=get('refreshOverview');if(refreshOverview)refreshOverview.onclick=loadOverview;
const refreshRuns=get('refreshRuns');if(refreshRuns)refreshRuns.onclick=loadRuns;
const refreshWorkers=get('refreshWorkers');if(refreshWorkers)refreshWorkers.onclick=loadWorkers;
const refreshWorkflowsEl=get('refreshWorkflows');if(refreshWorkflowsEl)refreshWorkflowsEl.onclick=()=>loadWorkflows(true);
const refreshArtifactsEl=get('refreshArtifacts');if(refreshArtifactsEl)refreshArtifactsEl.onclick=()=>loadArtifacts(true);
const searchEl=get('search');if(searchEl)searchEl.oninput=()=>{runsPage=1;renderRuns()};
const stateFilterEl=get('stateFilter');if(stateFilterEl)stateFilterEl.onchange=()=>{runsPage=1;renderRuns()};
const sortOrderEl=get('sortOrder');if(sortOrderEl)sortOrderEl.onchange=()=>{runsPage=1;renderRuns()};
const artifactSearchEl=get('artifactSearch');if(artifactSearchEl)artifactSearchEl.oninput=renderArtifacts;
const moreEventsWbEl=get('moreEventsWb');if(moreEventsWbEl)moreEventsWbEl.onclick=loadOlder;
document.querySelectorAll('[data-wb-tab]').forEach(b=>b.onclick=()=>showWbTab(b.dataset.wbTab));
document.addEventListener('click',async e=>{
  const btn=e.target.closest('.copy-id');
  if(!btn)return;
  const id=btn.dataset.runid;
  try{await navigator.clipboard.writeText(id);message('Run ID copied.')}catch(err){message('Copy failed: '+err.message,true)}
});
// check connection
async function checkConnection(){
  try{await api('/v1/runs?limit=0');const el=$('#connection');if(el){el.textContent='Connected';el.className='connection-ok'}}
  catch(e){const el=$('#connection');if(el){el.textContent='Disconnected';el.className='connection-err'}}
}
// stub implementations — workspace.js provides the real versions
function loadWorkflows(force){}
function loadArtifacts(force){}
function renderArtifacts(){}
function loadAnalytics(){}
// workspace.js will call navigate() and handle connection status
window.addEventListener('hashchange',navigate);
// initial navigation handled by workspace.js
