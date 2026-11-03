const $=s=>document.querySelector(s),esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])),pretty=x=>JSON.stringify(x??null,null,2),date=x=>x?new Date(x).toLocaleString():'—';
let inspectorRequest=0;
let runs=[],current=null,after=0,hasMore=false,selectedActivation=null,activationData=[],inspectorTab='outputs';
const RUNS_PAGE_SIZE=25;
let runsPage=1;
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

function uiFetch(path,opts){return fetch(path.startsWith('/v1/')?`/_ui-api${path}`:path,opts)}
async function api(path,opts){const r=await uiFetch(path,opts);const d=await r.json().catch(()=>({}));if(!r.ok)throw Error(d.message||d.error||`HTTP ${r.status}`);return d}
function message(text,error=false){$('#message').innerHTML=text?`<div class="notice${error?' error':''}" role="${error?'alert':'status'}">${esc(text)}</div>`:''}
function setView(view){
  for(const id of ['overview','runs','live','detail','workers','workflows','artifacts','transcript'])$('#'+id+'View').hidden=id!==view;
  if(view!=='detail')document.querySelector('.content')?.classList.remove('content--wb');
  for(const [id,key] of [['overviewNav','overview'],['runsNav','runs'],['liveNav','live'],['workersNav','workers'],['workflowsNav','workflows'],['artifactsNav','artifacts']]){
    const el=$('#'+id);if(!el)continue;
    if(key===view)el.setAttribute('aria-current','page');else el.removeAttribute('aria-current');
  }
  $('#topContext').textContent=view==='detail'?'Run detail':view==='transcript'?'Transcript':view[0].toUpperCase()+view.slice(1);
}
function navigate(){message('');const hash=location.hash;const id=hash.startsWith('#run/')?decodeURIComponent(hash.slice(5)):'';const transcriptMatch=hash.startsWith('#transcript/')?hash.slice('#transcript/'.length).split('/'):null;if(transcriptMatch&&transcriptMatch.length>=2){const runId=decodeURIComponent(transcriptMatch[0]),actId=decodeURIComponent(transcriptMatch[1]);current=runId;setView('transcript');if(typeof loadTranscriptPage==='function')loadTranscriptPage(runId,actId)}else if(id){current=id;setView('detail');openRun(id)}else if(hash==='#workers'){setView('workers');loadWorkers()}else if(hash==='#live'){setView('live');loadLive()}else if(hash==='#runs'){setView('runs');loadRuns()}else if(hash==='#workflows'){setView('workflows');loadWorkflows()}else if(hash==='#artifacts'){setView('artifacts');loadArtifacts()}else{setView('overview');loadOverview()}}
async function fetchRuns(){return (await api('/v1/runs')).runs||[]}
async function loadOverview(){try{runs=await fetchRuns();message('');renderOverview();renderRuns()}catch(e){if(e.name==='AbortError')return;$('#summaryGrid').innerHTML='';$('#recentRuns').innerHTML=`<div class="notice error" role="alert">Unable to load runs: ${esc(e.message)}</div>`;message(e.message,true)}}
function renderOverview(){const states=['running','waiting','succeeded','failed','cancelled'];const counts=Object.fromEntries(states.map(s=>[s,runs.filter(r=>r.state===s).length]));$('#summaryGrid').innerHTML=`<div class="summary"><div class="summary-label">Total runs</div><div class="summary-value">${runs.length}</div></div>${states.map(s=>`<div class="summary"><div class="summary-label">${esc(s[0].toUpperCase()+s.slice(1))}</div><div class="summary-value ${s==='failed'&&counts[s]?'red':s==='succeeded'&&counts[s]?'green':(['running','waiting'].includes(s)&&counts[s]?'amber':'')}">${counts[s]}</div></div>`).join('')}`;const list=[...runs].sort((a,b)=>new Date(b.updatedAt)-new Date(a.updatedAt)).slice(0,6);$('#recentRuns').innerHTML=runTable(list,'No runs yet','Runs will appear here when the coordinator records an execution.');}
function duration(a,b){if(!a||!b)return '—';const ms=Math.max(0,new Date(b)-new Date(a));if(ms<1000)return `${ms} ms`;if(ms<60000)return `${(ms/1000).toFixed(1)} s`;return `${Math.floor(ms/60000)}m ${Math.floor(ms%60000/1000)}s`}
function runTable(list,title='No matching runs',description='Adjust the search or state filter.'){if(!list.length)return `<div class="tablebox empty"><strong>${esc(title)}</strong>${esc(description)}</div>`;return `<div class="tablebox"><table><thead><tr><th>Workflow / run</th><th>State</th><th>Created</th><th>Updated</th><th>Duration</th></tr></thead><tbody>${list.map(r=>`<tr><td class="primary-cell"><strong><a href="#run/${encodeURIComponent(r.id)}">${esc(r.workflowName||'Unnamed workflow')}</a></strong><small><button class="copy-id" data-runid="${esc(r.id)}">${esc(r.id)}</button></small></td><td><span class="state ${esc(r.state)}">${esc(r.state)}</span></td><td class="num">${esc(date(r.createdAt))}</td><td class="num">${esc(date(r.updatedAt))}</td><td class="num">${esc(duration(r.createdAt,r.updatedAt))}</td></tr>`).join('')}</tbody></table></div>`}
function paginate(items,page,pageSize){const pageCount=Math.max(1,Math.ceil(items.length/pageSize)),currentPage=Math.min(Math.max(1,page),pageCount),start=(currentPage-1)*pageSize;return {items:items.slice(start,start+pageSize),page:currentPage,pageCount,total:items.length}}
function pagerMarkup(page,pageCount,total){if(pageCount<=1)return '';return `<nav class="pager" aria-label="Run history pages"><span>Page ${page} of ${pageCount} · ${total} runs</span><div class="pager-controls"><button class="btn ghost" data-page="${page-1}"${page===1?' disabled':''}>Previous</button><button class="btn ghost" data-page="${page+1}"${page===pageCount?' disabled':''}>Next</button></div></nav>`}
async function loadRuns(){try{runs=await fetchRuns();renderRuns()}catch(e){if(e.name==='AbortError')return;$('#runList').innerHTML=`<div class="notice error" role="alert">Unable to load runs: ${esc(e.message)}</div>`;$('#runCount').textContent='Data unavailable'}}
function renderRuns(){const q=(get('search')?.value||'').trim().toLowerCase(),st=get('stateFilter')?.value||'',sort=get('sortOrder')?.value||'updated';let list=runs.filter(r=>(!st||r.state===st)&&(!q||`${r.id} ${r.workflowName||''}`.toLowerCase().includes(q)));if(sort==='workflow')list.sort((a,b)=>(a.workflowName||'').localeCompare(b.workflowName||''));else list.sort((a,b)=>new Date(b[sort==='created'?'createdAt':'updatedAt'])-new Date(a[sort==='created'?'createdAt':'updatedAt']));const page=paginate(list,runsPage,RUNS_PAGE_SIZE);runsPage=page.page;const runCountEl=get('runCount');if(runCountEl)runCountEl.textContent=`${list.length} matching ${list.length===1?'run':'runs'} · ${runs.length} total`;const runListEl=get('runList');if(runListEl)runListEl.innerHTML=runTable(page.items)+pagerMarkup(page.page,page.pageCount,page.total)}
document.addEventListener('click',event=>{const button=event.target.closest('#runList .pager [data-page]');if(!button||button.disabled)return;runsPage=Number(button.dataset.page)||1;renderRuns()});

