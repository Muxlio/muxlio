/* ── event bindings ───────────────────────────── */
// Safe querySelector helper - won't throw on missing elements
function get(id){return (document.getElementById(id) as UIElement)}

get('overviewNav').onclick=()=>{};
get('runsNav').onclick=()=>{};
get('workersNav').onclick=()=>{};
const refreshOverview=get('refreshOverview');if(refreshOverview)refreshOverview.onclick=loadOverview;
const refreshRuns=get('refreshRuns');if(refreshRuns)refreshRuns.onclick=loadRuns;
const refreshWorkers=get('refreshWorkers');if(refreshWorkers)refreshWorkers.onclick=loadWorkers;
const refreshWorkflowsEl=get('refreshWorkflows');if(refreshWorkflowsEl)refreshWorkflowsEl.onclick=()=>loadWorkflows(true);
const refreshArtifactsEl=get('refreshArtifacts');if(refreshArtifactsEl)refreshArtifactsEl.onclick=()=>loadArtifacts(true);
const searchEl=get('search');if(searchEl)searchEl.oninput=updateRunDiscoveryFilters;
const stateFilterEl=get('stateFilter');if(stateFilterEl)stateFilterEl.onchange=updateRunDiscoveryFilters;
const sortOrderEl=get('sortOrder');if(sortOrderEl)sortOrderEl.onchange=updateRunDiscoveryFilters;
const artifactSearchEl=get('artifactSearch');if(artifactSearchEl)artifactSearchEl.oninput=()=>loadArtifacts();
const moreEventsWbEl=get('moreEventsWb');if(moreEventsWbEl)moreEventsWbEl.onclick=loadOlder;
document.querySelectorAll<UIElement>('[data-wb-tab]').forEach(b=>b.onclick=()=>showWbTab(b.dataset.wbTab));
// check connection
async function checkConnection(){
  try{await api('/v1/runs?limit=0');const el=$('#connection');if(el){el.textContent='Connected';el.className='connection-ok'}}
  catch(e){const el=$('#connection');if(el){el.textContent='Disconnected';el.className='connection-err'}}
}
// stub implementations — workspace.js provides the real versions
// workspace.js will call navigate() and handle connection status
window.addEventListener('popstate',navigate);
// initial navigation handled by workspace.js

for(const id of Object.values(runDiscoveryFields)){
 if(id==='search')continue;
 const control=get(id);if(control)control.onchange=updateRunDiscoveryFilters;
}
