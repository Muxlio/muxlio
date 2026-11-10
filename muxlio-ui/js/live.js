/* ── live output monitor ───────────────────────── */
const LIVE_POLL_MS=1200;
let livePollTimer=null,livePollToken=0;
let liveEntries=[],liveSelection=null,liveMonitorEntry=null;

function activationIsStreaming(act,attempt){
  if(!act)return false;
  if(['succeeded','failed','cancelled'].includes(act.state))return false;
  return act.state==='running'||(attempt&&attempt.state==='running');
}

function stopLivePoll(){if(livePollTimer){clearTimeout(livePollTimer);livePollTimer=null}livePollToken++}

function latestAttemptFor(attempts,activationId){
  return (attempts||[]).filter(a=>a.activationId===activationId&&a.state!=='queued').sort((a,b)=>(b.retryNumber||0)-(a.retryNumber||0))[0]||null;
}

function updateLiveTranscript(text,live){
  const el=$('#liveTranscriptBody');if(!el)return;
  const page=document.querySelector('.content');
  const atBottom=page?page.scrollHeight-page.scrollTop-page.clientHeight<80:false;
  if(!text){
    if(live){
      el.textContent='Waiting for agent output…';
      el.classList.add('waiting');
    }else{
      el.textContent='No live transcript recorded.';
      el.classList.remove('waiting');
    }
  }else{
    el.innerHTML=renderMarkdown(text);
    el.classList.remove('waiting');
  }
  if(live&&atBottom&&page)page.scrollTop=page.scrollHeight;
}

function scheduleLiveOutputPoll(entry){
  stopLivePoll();
  if(!entry)return;
  const token=livePollToken,runId=entry.runId,actId=entry.act.id;
  let statePollCount=0;
  let trackedAct=entry.act,trackedAttempt=entry.attempt;
  async function tick(){
    if(token!==livePollToken||!liveSelection||liveSelection.runId!==runId||liveSelection.activationId!==actId)return;
    try{
      const d=await api(`/v1/runs/${encodeURIComponent(runId)}/logs?activationId=${encodeURIComponent(actId)}`);
      if(token!==livePollToken||!liveSelection||liveSelection.runId!==runId||liveSelection.activationId!==actId)return;
      const row=liveEntries.find(x=>x.runId===runId&&x.act.id===actId);
      const attempt=row?.attempt||trackedAttempt;
      const act=row?.act||trackedAct;
      trackedAct=act;trackedAttempt=attempt;
      updateLiveTranscript(mergeActivationLogs(actId,d.logs||[]),activationIsStreaming(act,attempt));
    }catch{}
    if(token!==livePollToken||!liveSelection||liveSelection.runId!==runId||liveSelection.activationId!==actId)return;
    statePollCount++;
    if(statePollCount%5===0){
      try{
        const fresh=await api(`/v1/runs/${encodeURIComponent(runId)}/activations/${encodeURIComponent(actId)}`);
        if(token!==livePollToken||!liveSelection||liveSelection.runId!==runId||liveSelection.activationId!==actId)return;
        if(fresh){
          const wasStreaming=activationIsStreaming(trackedAct,trackedAttempt);
          trackedAct=fresh;
          if(liveMonitorEntry&&liveMonitorEntry.runId===runId&&liveMonitorEntry.act?.id===actId){
            liveMonitorEntry={...liveMonitorEntry,act:fresh};
          }
          const idx=liveEntries.findIndex(x=>x.runId===runId&&x.act.id===actId);
          if(idx>=0){
            const row=liveEntries[idx];
            if(activationIsStreaming(fresh,row.attempt))liveEntries[idx]={...row,act:fresh};
            else liveEntries.splice(idx,1);
            renderLiveList();
          }
          if(wasStreaming&&!activationIsStreaming(fresh,trackedAttempt)){
            renderLiveMeta(liveMonitorEntry);
            updateLiveTranscript(activationTranscripts.get(actId)?.agent||'',false);
          }
        }
      }catch{}
    }
    if(token!==livePollToken||!liveSelection||liveSelection.runId!==runId||liveSelection.activationId!==actId)return;
    const row=liveEntries.find(x=>x.runId===runId&&x.act.id===actId);
    const attempt=row?.attempt||trackedAttempt;
    const act=row?.act||trackedAct;
    if(activationIsStreaming(act,attempt))livePollTimer=setTimeout(tick,LIVE_POLL_MS);
    else{
      if(liveMonitorEntry?.act)liveMonitorEntry={...liveMonitorEntry,act:act};
      renderLiveMeta(liveMonitorEntry);
      updateLiveTranscript(activationTranscripts.get(actId)?.agent||'',false);
    }
  }
  tick();
}

function renderLiveMeta(entry){
  const meta=$('#liveStreamMeta');
  if(!meta)return;
  if(!entry){
    const hint=liveEntries.length
      ? 'Select a running activation from the list above.'
      : 'No running activations right now. Start or resume a workflow run, then return here.';
    meta.innerHTML=`<p class="live-meta-empty">${esc(hint)}</p>`;
    return;
  }
  const act=entry.act,attempt=entry.attempt;
  const executor=(attempt&&attempt.state!=='queued'?attempt.executor:'')||act.executorHint||act.executor||'—';
  const attemptState=attempt?.state||'—';
  const transcriptUrl=`/transcripts/${encodeURIComponent(entry.runId)}/${encodeURIComponent(act.id)}`;
  meta.innerHTML=`<dl class="live-meta-kv">
    <dt>Run</dt><dd class="mono"><a href="/runs/${encodeURIComponent(entry.runId)}" style="color:var(--teal)">${esc(entry.runId)}</a>${entry.workflowName?` · ${esc(entry.workflowName)}`:''}</dd>
    <dt>Node</dt><dd>${esc(act.nodeId)} <span class="state ${esc(stateClass(act.state)||'')}">${esc(act.state)}</span></dd>
    <dt>Executor</dt><dd>${esc(executor)}</dd>
    <dt>Attempt</dt><dd>${attempt?`#${esc(attempt.retryNumber)} · <span class="state ${esc(stateClass(attemptState)||'')}">${esc(attemptState)}</span>`:'—'}</dd>
  </dl>
  <div style="margin-top:10px"><a href="${transcriptUrl}" class="insp-transcript-link">Open transcript page ↗</a></div>`;
}

function selectLiveStream(runId,actId){
  const entry=liveEntries.find(x=>x.runId===runId&&x.act.id===actId);
  if(!entry)return;
  liveSelection={runId,activationId:actId};
  liveMonitorEntry=entry;
  document.querySelectorAll('[data-live-run]').forEach(btn=>{
    const on=btn.dataset.liveRun===runId&&btn.dataset.liveAct===actId;
    btn.classList.toggle('selected',on);
    btn.setAttribute('aria-pressed',String(on));
  });
  renderLiveMeta(entry);
  const cached=activationTranscripts.get(actId)?.agent||'';
  updateLiveTranscript(cached,activationIsStreaming(entry.act,entry.attempt));
  scheduleLiveOutputPoll(entry);
}

function renderLiveList(){
  const list=$('#liveActiveList'),count=$('#liveActiveCount');
  if(count)count.textContent=liveEntries.length?`${liveEntries.length} running ${liveEntries.length===1?'activation':'activations'}`:'No running activations';
  if(!list)return;
  if(!liveEntries.length){
    list.innerHTML='<div class="tablebox empty"><strong>No live streams</strong>Running activations appear here while agents execute. Open the workbench for full graph and inspector detail.</div>';
    return;
  }
  list.innerHTML=`<div class="tablebox"><table><thead><tr><th>Run</th><th>Node</th><th>Executor</th><th>Attempt</th><th>Activation</th></tr></thead><tbody>${liveEntries.map(entry=>{
    const act=entry.act,attempt=entry.attempt;
    const executor=(attempt&&attempt.state!=='queued'?attempt.executor:'')||act.executorHint||act.executor||'—';
    const attemptState=attempt?.state||'—';
    const selected=liveSelection&&liveSelection.runId===entry.runId&&liveSelection.activationId===act.id;
    return `<tr><td class="primary-cell"><button type="button" class="live-pick${selected?' selected':''}" data-live-run="${esc(entry.runId)}" data-live-act="${esc(act.id)}" aria-pressed="${selected}"><strong>${esc(entry.workflowName||'Run')}</strong><small class="mono">${esc(entry.runId)}</small></button></td><td>${esc(act.nodeId)}</td><td>${esc(executor)}</td><td>${attempt?`#${esc(attempt.retryNumber)} · ${esc(attemptState)}`:'—'}</td><td><span class="state running">${esc(act.state)}</span></td></tr>`;
  }).join('')}</tbody></table></div>`;
  list.querySelectorAll('[data-live-run]').forEach(btn=>btn.onclick=()=>selectLiveStream(btn.dataset.liveRun,btn.dataset.liveAct));
}

async function loadLive(){
  const epoch=typeof refreshEpoch==='number'?refreshEpoch:0;
  const countEl=$('#liveActiveCount');
  if(countEl&&liveEntries.length===0)countEl.textContent='Loading…';
  try{
    const allRuns=await fetchRuns();
    if(typeof refreshEpoch==='number'&&epoch!==refreshEpoch)return;
    const activeRuns=allRuns.filter(r=>['running','waiting','paused','created'].includes(r.state));
    const entries=[];
    await Promise.all(activeRuns.map(async r=>{
      if(typeof refreshEpoch==='number'&&epoch!==refreshEpoch)return;
      const [graph,attemptsResp]=await Promise.all([
        api(`/v1/runs/${encodeURIComponent(r.id)}/graph`),
        api(`/v1/runs/${encodeURIComponent(r.id)}/attempts`).catch(()=>({attempts:[]})),
      ]);
      if(typeof refreshEpoch==='number'&&epoch!==refreshEpoch)return;
      const attempts=attemptsResp.attempts||[];
      for(const act of graph.activations||[]){
        const attempt=latestAttemptFor(attempts,act.id);
        if(activationIsStreaming(act,attempt))entries.push({runId:r.id,workflowName:r.workflowName,act,attempt});
      }
    }));
    if(typeof refreshEpoch==='number'&&epoch!==refreshEpoch)return;
    entries.sort((a,b)=>`${a.runId}:${a.act.nodeId}`.localeCompare(`${b.runId}:${b.act.nodeId}`));
    liveEntries=entries;
    renderLiveList();
    if(liveSelection){
      const still=entries.find(e=>e.runId===liveSelection.runId&&e.act.id===liveSelection.activationId);
      if(still){
        liveMonitorEntry=still;
        if(activationIsStreaming(still.act,still.attempt))scheduleLiveOutputPoll(still);
        else renderLiveMeta(liveMonitorEntry);
      }else if(liveMonitorEntry){
        const selected=liveMonitorEntry;
        const [graph,attemptsResp]=await Promise.all([
          api(`/v1/runs/${encodeURIComponent(selected.runId)}/graph`),
          api(`/v1/runs/${encodeURIComponent(selected.runId)}/attempts`).catch(()=>({attempts:[]})),
        ]);
        if(typeof refreshEpoch==='number'&&epoch!==refreshEpoch)return;
        const completed=(graph.activations||[]).find(a=>a.id===selected.act.id);
        if(completed){
          liveMonitorEntry={...selected,act:completed,attempt:latestAttemptFor(attemptsResp.attempts||[],completed.id)};
          renderLiveMeta(liveMonitorEntry);
          updateLiveTranscript(activationTranscripts.get(completed.id)?.agent||'',false);
          if(activationIsStreaming(completed,liveMonitorEntry.attempt))scheduleLiveOutputPoll(liveMonitorEntry);
        }
      }
    }else if(entries.length)selectLiveStream(entries[0].runId,entries[0].act.id);
    else if(!liveSelection){liveMonitorEntry=null;renderLiveMeta(null);updateLiveTranscript('',false);stopLivePoll()}
    if(typeof connectionOK==='function')connectionOK();
  }catch(err){
    if(err.name==='AbortError')return;
    if(typeof refreshEpoch==='number'&&epoch!==refreshEpoch)return;
    const list=$('#liveActiveList');
    if(list)list.innerHTML=`<div class="notice error" role="alert">Unable to load live output: ${esc(err.message)}</div>`;
    if(typeof connectionError==='function')connectionError(err);
  }
}
