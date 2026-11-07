const $=s=>document.querySelector(s),esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])),pretty=x=>JSON.stringify(x??null,null,2);
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
document.addEventListener('toggle',event=>{const details=event.target;if(details?.tagName==='DETAILS'&&details.closest('[id]')){const root=details.closest('[id]');disclosureState.set(disclosureKey(root,details),details.open)}},true);

function uiFetch(path,opts){return fetch(path,opts)}
async function api(path,opts){const r=await uiFetch(path,opts);const d=await r.json().catch(()=>({}));if(!r.ok)throw Error(d.message||d.error||`HTTP ${r.status}`);return d}
function message(text,error=false){$('#message').innerHTML=text?`<div class="notice${error?' error':''}" role="${error?'alert':'status'}">${esc(text)}</div>`:''}
function setView(view){
  for(const id of ['overview','runs','live','detail','workers','workflows','artifacts','transcript','analytics'])$('#'+id+'View').hidden=id!==view;
  if(view!=='detail')document.querySelector('.content')?.classList.remove('content--wb');
  for(const [id,key] of [['overviewNav','overview'],['runsNav','runs'],['liveNav','live'],['workersNav','workers'],['workflowsNav','workflows'],['artifactsNav','artifacts'],['analyticsNav','analytics']]){
    const el=$('#'+id);if(!el)continue;
    if(key===view)el.setAttribute('aria-current','page');else el.removeAttribute('aria-current');
  }
  $('#topContext').textContent=view==='detail'?'Run detail':view==='transcript'?'Transcript':view[0].toUpperCase()+view.slice(1);
}
function navigate(){message('');const hash=location.hash;const id=hash.startsWith('#run/')?decodeURIComponent(hash.slice(5)):'';const transcriptMatch=hash.startsWith('#transcript/')?hash.slice('#transcript/'.length).split('/'):null;if(transcriptMatch&&transcriptMatch.length>=2){const runId=decodeURIComponent(transcriptMatch[0]),actId=decodeURIComponent(transcriptMatch[1]);current=runId;setView('transcript');if(typeof loadTranscriptPage==='function')loadTranscriptPage(runId,actId)}else if(id){current=id;setView('detail');openRun(id)}else if(hash==='#workers'){setView('workers');loadWorkers()}else if(hash==='#live'){setView('live');loadLive()}else if(hash==='#runs'){setView('runs');loadRuns()}else if(hash==='#workflows'){setView('workflows');loadWorkflows()}else if(hash==='#artifacts'){setView('artifacts');loadArtifacts()}else if(hash==='#analytics'){setView('analytics');loadAnalytics()}else{setView('overview');loadOverview()}}
async function fetchRuns(){
 const history=location.hash==='#runs';
 const q=history?(get('search')?.value||'').trim():'',state=history?get('stateFilter')?.value||'':'',sort=history?get('sortOrder')?.value||'updated':'updated';
 const page=await api(`/v1/runs?limit=25&offset=${history?(runsPage-1)*RUNS_PAGE_SIZE:0}&q=${encodeURIComponent(q)}&state=${encodeURIComponent(state)}&sort=${encodeURIComponent(sort)}`);
 if(!Number.isInteger(page.total)||!Array.isArray(page.runs))throw Error('Coordinator update required for paged run history.');
 runsListing=page;
 const items=page.runs||[];
 await attachRunExecutionTimes(items);return items;
}
async function loadOverview(){try{runs=await fetchRuns();message('');renderOverview();renderRuns()}catch(e){if(e.name==='AbortError')return;$('#summaryGrid').innerHTML='';$('#recentRuns').innerHTML=`<div class="notice error" role="alert">Unable to load runs: ${esc(e.message)}</div>`;message(e.message,true)}}
function renderOverview(){const states=['running','waiting','succeeded','failed','cancelled'];const counts=Object.fromEntries(states.map(s=>[s,runsListing?.states?.[s]??runs.filter(r=>r.state===s).length]));$('#summaryGrid').innerHTML=`<div class="summary"><div class="summary-label">Total runs</div><div class="summary-value">${Object.values(runsListing?.states||{}).reduce((a,b)=>a+b,0)||runs.length}</div></div>${states.map(s=>`<div class="summary"><div class="summary-label">${esc(s[0].toUpperCase()+s.slice(1))}</div><div class="summary-value ${s==='failed'&&counts[s]?'red':s==='succeeded'&&counts[s]?'green':(['running','waiting'].includes(s)&&counts[s]?'amber':'')}">${counts[s]}</div></div>`).join('')}`;const list=[...runs].sort((a,b)=>new Date(b.updatedAt)-new Date(a.updatedAt)).slice(0,6);$('#recentRuns').innerHTML=runTable(list,'No runs yet','Runs will appear here when the coordinator records an execution.');}
function runTable(list,title='No matching runs',description='Adjust the search or state filter.'){if(!list.length)return `<div class="tablebox empty"><strong>${esc(title)}</strong>${esc(description)}</div>`;return `<div class="tablebox"><table><thead><tr><th>Workflow / run</th><th>State</th><th>Created</th><th>Updated</th><th>Elapsed time</th><th>Total execution time</th></tr></thead><tbody>${list.map(r=>`<tr><td class="primary-cell"><strong><a href="#run/${encodeURIComponent(r.id)}">${esc(r.workflowName||'Unnamed workflow')}</a></strong><small><span class="run-id">${esc(r.id)}</span></small></td><td><span class="state ${esc(r.state)}">${esc(r.state)}</span></td><td class="num">${esc(date(r.createdAt))}</td><td class="num">${esc(date(r.updatedAt))}</td><td class="num">${esc(duration(r.createdAt,!['succeeded','failed','cancelled'].includes(r.state)?new Date().toISOString():r.updatedAt))}</td><td class="num">${esc(r.totalExecutionTimeMs==null?'Unavailable':duration(new Date(0),new Date(r.totalExecutionTimeMs)))}</td></tr>`).join('')}</tbody></table></div>`}
function paginate(items,page,pageSize){const pageCount=Math.max(1,Math.ceil(items.length/pageSize)),currentPage=Math.min(Math.max(1,page),pageCount),start=(currentPage-1)*pageSize;return {items:items.slice(start,start+pageSize),page:currentPage,pageCount,total:items.length}}
function pagerMarkup(page,pageCount,total){if(pageCount<=1)return '';return `<nav class="pager" aria-label="Run history pages"><span>Page ${page} of ${pageCount} · ${total} runs</span><div class="pager-controls"><button class="btn ghost" data-page="${page-1}"${page===1?' disabled':''}>Previous</button><button class="btn ghost" data-page="${page+1}"${page===pageCount?' disabled':''}>Next</button></div></nav>`}
async function loadRuns(){try{runs=await fetchRuns();renderRuns()}catch(e){if(e.name==='AbortError')return;$('#runList').innerHTML=`<div class="notice error" role="alert">Unable to load runs: ${esc(e.message)}</div>`;$('#runCount').textContent='Data unavailable'}}
async function attachRunExecutionTimes(items){
  // Bound concurrent requests so a large history cannot flood the coordinator.
  for(let offset=0;offset<items.length;offset+=8){
    await Promise.all(items.slice(offset,offset+8).map(async r=>{
      try{const data=await api(`/v1/runs/${encodeURIComponent(r.id)}?summary=true`);r.totalExecutionTimeMs=data.overview?.executionDurationMs??null}
      catch{r.totalExecutionTimeMs=null}
    }));
  }
}
function renderRuns(){
 const total=runsListing?.total??runs.length,pageCount=Math.max(1,Math.ceil(total/RUNS_PAGE_SIZE));
 const count=get('runCount');if(count)count.textContent=`${total} matching runs`;
 const list=get('runList');if(list)list.innerHTML=runTable(runs)+pagerMarkup(runsPage,pageCount,total);
}
document.addEventListener('click',event=>{const button=event.target.closest('#runList .pager [data-page]');if(!button||button.disabled)return;runsPage=Number(button.dataset.page)||1;loadRuns()});

