// Browser routes use the History API; API and static-asset URLs remain separate.
function currentRoute(){return ((location.pathname||'/').replace(/\/$/,'')||'/')+(location.search||'')}
function isUIPath(path){return /^\/(?:runs(?:\/[^/]+)?|transcripts\/[^/]+\/[^/]+|worker-capabilities\/[^/]+\/[^/]+(?:\/[^/]+)?|operations|automation|retention|workers|live|workflows|artifacts|analytics|compare|evidence)\/?$/.test(path)||path==='/'}
function navigateTo(path,{replace=false}={}){
  const url=new URL(path,location.href);
  if(url.origin!==location.origin||!isUIPath(url.pathname))return;
  const target=url.pathname+url.search+url.hash;
  if(target===currentRoute()+location.hash)return;
  window.history[replace?'replaceState':'pushState'](null,'',target);
  window.dispatchEvent(new PopStateEvent('popstate'));
}
function migrateLegacyRoute(){
  if(!/^#\/?(?:overview|runs?|transcript|worker-capability|operations|automation|retention|workers|live|workflows|artifacts|analytics|compare|evidence)(?:[/?]|$)/.test(location.hash))return;
  let path=location.hash.replace(/^#\/?/,'/').replace(/^\/run\//,'/runs/').replace(/^\/transcript\//,'/transcripts/').replace(/^\/worker-capability\//,'/worker-capabilities/').replace(/^\/overview$/,'/');
  if(isUIPath(path.split('?')[0]))window.history.replaceState(null,'',path);
}
document.addEventListener('click',event=>{
  if(event.defaultPrevented||event.button!==0||event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return;
  const link=(event.target as UIElement).closest?.('a[href]') as HTMLAnchorElement;
  if(!link||link.hasAttribute('download')||(link.target&&link.target!=='_self'))return;
  const href=link.getAttribute('href');if(!href||href.startsWith('#'))return;
  const url=new URL(href,location.href);
  if(url.origin!==location.origin||!isUIPath(url.pathname)||url.hash)return;
  event.preventDefault();navigateTo(url.href);
});

const $=s=>document.querySelector<UIElement>(s),esc=componentEscape,pretty=x=>JSON.stringify(x??null,null,2);
let inspectorRequest=0;
let runs=[],current=null,after=0,hasMore=false,selectedActivation=null,activationData=[],inspectorTab='outputs';
const RUNS_PAGE_SIZE=25;
let runsPage=1,runsListing=null;
let wbAfter=0,wbHasMore=false,wbCurrentRun=null;
const disclosureState=new Map();
function disclosureKey(root,details){
  if(details.dataset.preserveKey)return `${current||''}:${root.id||'view'}:${details.dataset.preserveKey}`;
  const parts=[];let node=details;
  const artifact=details.closest('.artifact');
  if(artifact)parts.push(`artifact:${artifact.querySelector('.artifact-head strong')?.textContent.trim()||''}`);
  while(node&&node!==root){
    if(node.tagName==='DETAILS'){
      const summary=[...node.children].find(child=>child.tagName==='SUMMARY')?.textContent.trim()||'';
      const siblings=[...(node.parentElement?.children||[])].filter(child=>child.tagName==='DETAILS'&&[...child.children].find(x=>x.tagName==='SUMMARY')?.textContent.trim()===summary);
      parts.unshift(`${summary}#${siblings.indexOf(node)}`);
    }
    node=node.parentElement;
  }
  return `${current||''}:${root.id||'view'}:${parts.join('/')}`;
}
function rememberDetails(root){root?.querySelectorAll('details').forEach(details=>disclosureState.set(disclosureKey(root,details),details.open))}
function replaceHTMLPreservingDetails(root,html){
  if(!root)return;
  rememberDetails(root);root.innerHTML=html;
  root.querySelectorAll('details').forEach(details=>{const key=disclosureKey(root,details);if(disclosureState.has(key))details.open=disclosureState.get(key)});
}
document.addEventListener('toggle',event=>{const details=(event.target as UIElement);if(details?.tagName==='DETAILS'&&details.closest<UIElement>('[id]')){const root=details.closest<UIElement>('[id]');disclosureState.set(disclosureKey(root,details),details.open)}},true);

function uiFetch(path: string,opts?: RequestInit){return fetch(path,opts)}
async function api(path: string,opts?: RequestInit){const r=await uiFetch(path,opts);const d=await r.json().catch(()=>({}));if(!r.ok)throw Error(d.message||d.error||`HTTP ${r.status}`);return d}
function message(text,error=false){$('#message').innerHTML=text?renderNotice(text, {error}):''}
function setView(view){
  for(const id of ['overview','runs','live','detail','workers','workerCapability','workflows','artifacts','transcript','analytics','operations','automation','retention'])$('#'+id+'View').hidden=id!==view;
  const fork=(document.getElementById('forkRunBtn') as UIElement);if(fork&&view!=='detail')fork.hidden=true;
  if(view!=='detail')document.querySelector<UIElement>('.content')?.classList.remove('content--wb');
  for(const [id,key] of [['operationsNav','operations'],['automationNav','automation'],['retentionNav','retention'],['overviewNav','overview'],['runsNav','runs'],['liveNav','live'],['workersNav','workers'],['workflowsNav','workflows'],['artifactsNav','artifacts'],['analyticsNav','analytics']]){
    const el=$('#'+id);if(!el)continue;
    if(key===view||(key==='workers'&&view==='workerCapability'))el.setAttribute('aria-current','page');else el.removeAttribute('aria-current');
  }
  $('#topContext').textContent=view==='workerCapability'?'Capability details':view==='detail'?'Run detail':view==='transcript'?'Transcript':view[0].toUpperCase()+view.slice(1);
}
function navigate(){message('');const hash=currentRoute();const path=hash.split('?')[0];const id=path.startsWith('/runs/')?decodeURIComponent(path.slice('/runs/'.length)):'';const transcriptMatch=path.startsWith('/transcripts/')?path.slice('/transcripts/'.length).split('/'):null;if(transcriptMatch&&transcriptMatch.length>=2){const runId=decodeURIComponent(transcriptMatch[0]),actId=decodeURIComponent(transcriptMatch[1]);current=runId;setView('transcript');if(typeof loadTranscriptPage==='function')loadTranscriptPage(runId,actId)}else if(id){current=id;setView('detail');openRun(id)}else if(path==='/operations'){setView('operations');loadOpsDashboard()}else if(path==='/automation'){setView('automation');loadAutomation()}else if(path==='/retention'){setView('retention');loadRetention()}else if(path.startsWith('/worker-capabilities/')){setView('workerCapability');loadWorkerCapabilityPage(...path.slice('/worker-capabilities/'.length).split('/').map(decodeURIComponent) as [string, string, string?])}else if(path==='/workers'){setView('workers');loadWorkers()}else if(path==='/live'){setView('live');loadLive()}else if(path==='/runs'){setView('runs');restoreRunDiscoveryFilters();loadRuns()}else if(path==='/workflows'){setView('workflows');loadWorkflows()}else if(path==='/artifacts'){setView('artifacts');loadArtifacts()}else if(path.startsWith('/analytics')){setView('analytics');loadAnalytics(typeof analyticsFiltersFromURL==='function'?analyticsFiltersFromURL(hash):undefined)}else{setView('overview');loadOverview()}}
async function fetchRuns(){
 const history=currentRoute().split('?')[0]==='/runs';
 const discovery=history?runDiscoveryQuery():'';
 const page=await api(`/v1/runs?${discovery}&limit=25&offset=${history?(runsPage-1)*RUNS_PAGE_SIZE:0}`);
 if(!Number.isInteger(page.total)||!Array.isArray(page.runs))throw Error('Coordinator update required for paged run history.');
 runsListing=page;
 const items=page.runs||[];
 await attachRunExecutionTimes(items);return items;
}
async function loadOverview(){try{runs=await fetchRuns();message('');renderOverview();renderRuns()}catch(e){if(e.name==='AbortError')return;$('#summaryGrid').innerHTML='';$('#recentRuns').innerHTML=renderNotice(`Unable to load runs: ${e.message??''}`, {error:true});message(e.message,true)}}
function renderOverview(){
 const states=['running','waiting','succeeded','failed','cancelled'];
 const counts=Object.fromEntries(states.map(state=>[state,runsListing?.states?.[state]??runs.filter(run=>run.state===state).length]));
 const total=Object.values(runsListing?.states||{}).reduce((sum,count)=>Number(sum)+Number(count),0)||runs.length;
 const cards: SummaryCard[]=[{label:'Total runs',value:Number(total)}];
 for(const state of states){
  const tone=state==='failed'&&counts[state]?'red':state==='succeeded'&&counts[state]?'green':['running','waiting'].includes(state)&&counts[state]?'amber':undefined;
  cards.push({label:state[0].toUpperCase()+state.slice(1),value:counts[state],tone});
 }
 $('#summaryGrid').innerHTML=renderSummaryCards(cards);
 const list=[...runs].sort((a,b)=>new Date(b.updatedAt).getTime()-new Date(a.updatedAt).getTime()).slice(0,6);
 $('#recentRuns').innerHTML=runTable(list,'No runs yet','Runs will appear here when the coordinator records an execution.');
}
function runTable(list,title='No matching runs',description='Adjust the search or state filter.'){return renderRunTable(list,title,description)}
function paginate(items,page,pageSize){const pageCount=Math.max(1,Math.ceil(items.length/pageSize)),currentPage=Math.min(Math.max(1,page),pageCount),start=(currentPage-1)*pageSize;return {items:items.slice(start,start+pageSize),page:currentPage,pageCount,total:items.length}}
function pagerMarkup(page,pageCount,total){if(pageCount<=1)return '';return renderPagination({label:'Run history pages',summary:`Page ${page} of ${pageCount} · ${total} runs`,previous:{attributes:{'data-page':page-1},disabled:page===1},next:{attributes:{'data-page':page+1},disabled:page===pageCount}})}
async function loadRuns(){try{runs=await fetchRuns();renderRuns()}catch(e){if(e.name==='AbortError')return;$('#runList').innerHTML=renderNotice(`Unable to load runs: ${e.message??''}`, {error:true});$('#runCount').textContent='Data unavailable'}}
async function attachRunExecutionTimes(items){
  // Bound concurrent requests so a large history cannot flood the coordinator.
  for(let offset=0;offset<items.length;offset+=8){
    await Promise.all(items.slice(offset,offset+8).map(async r=>{
      try{const data=await api(`/v1/runs/${encodeURIComponent(r.id)}?summary=true`);const overview=data.overview||{};r.totalExecutionTimeMs=overview.executionDurationMs??overview.executionDurationKnownMs??null;r.totalExecutionTimeMeasuredAttempts=overview.executionDurationMeasuredAttempts??0}
      catch{r.totalExecutionTimeMs=null;r.totalExecutionTimeMeasuredAttempts=0}
    }));
  }
}
function renderRuns(){
 const total=runsListing?.total??runs.length,pageCount=Math.max(1,Math.ceil(total/RUNS_PAGE_SIZE));
 const count=get('runCount');if(count)count.textContent=`${total} matching runs`;
 const list=get('runList');if(list)list.innerHTML=runTable(runs)+pagerMarkup(runsPage,pageCount,total);
}
document.addEventListener('click',event=>{const button=(event.target as UIElement).closest<UIElement>('#runList .pager [data-page]');if(!button||button.disabled)return;runsPage=Number(button.dataset.page)||1;loadRuns()});

const runDiscoveryFields={q:'search',state:'stateFilter',sort:'sortOrder',workspaceId:'workspaceFilter',planDigest:'digestFilter',createdAfter:'createdAfterFilter',createdBefore:'createdBeforeFilter',callerId:'callerFilter'};
function runDiscoveryQuery(){
 const query=new URLSearchParams();
 for(const [key,id] of Object.entries(runDiscoveryFields)){
  let value=get(id)?.value||'';
  if(key==='q')value=value.trim();
  if(value&&(key==='createdAfter'||key==='createdBefore'))value=new Date(value).toISOString();
  if(value)query.set(key,value);
 }
 return query.toString();
}
function restoreRunDiscoveryFilters(){
 runsPage=1;
 const query=new URLSearchParams(currentRoute().split('?')[1]||'');
 for(const [key,id] of Object.entries(runDiscoveryFields)){
  const control=get(id);if(!control)continue;
  let value=query.get(key)||(key==='sort'?'updated':'');
  if(value&&(key==='createdAfter'||key==='createdBefore')){
   const date=new Date(value);value=Number.isNaN(date.getTime())?'':new Date(date.getTime()-date.getTimezoneOffset()*60000).toISOString().slice(0,-1);
  }
  control.value=value;
 }
}

function updateRunDiscoveryFilters(){
 runsPage=1;
 const query=runDiscoveryQuery();
 history.replaceState(null,'','/runs'+(query?'?'+query:''));
 loadRuns();
}
