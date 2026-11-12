/* ── transcript page ──────────────────────────── */
const TPAGE_POLL_MS = 1200;
let tpageRunId = null, tpageActId = null;
let tpagePollTimer = null, tpagePollToken = 0;
let tpageActState = '';
let tpageLogOffset=0;
const transcriptPageLogs = new Map();

function stopTranscriptPage() {
  if (tpagePollTimer) { clearTimeout(tpagePollTimer); tpagePollTimer = null; }
  tpagePollToken++;
}

function updateTranscriptPageBody(text, live) {
  const el = (document.getElementById('transcriptPageBody') as UIElement);
  if (!el) return;
  const wrap = el.closest<UIElement>('.transcript-body-wrap');
  const atBottom = wrap ? wrap.scrollHeight - wrap.scrollTop - wrap.clientHeight < 80 : false;
  if (!text) {
    if (live) {
      el.textContent = 'Waiting for agent output…';
      el.classList.add('waiting');
    } else {
      el.textContent = 'No transcript recorded.';
      el.classList.remove('waiting');
    }
  } else {
    el.innerHTML = renderMarkdown(text);
    el.classList.remove('waiting');
  }
  if (live && atBottom && wrap) wrap.scrollTop = wrap.scrollHeight;
}

function renderTranscriptMeta(runId, act, attempt) {
  const meta = (document.getElementById('transcriptMeta') as UIElement);
  if (!meta) return;
  if (!act) { meta.innerHTML = renderPlaceholder("Loading…", "inspector-placeholder"); return; }
  const executor = (attempt && attempt.state !== 'queued' ? attempt.executor : '') || act.executorHint || act.executor || '—';
  const attemptState = attempt?.state || '—';
  meta.innerHTML = renderExecutionMetadata({runId,nodeId:act.nodeId,state:act.state,stateTone:stateClass(act.state)||'',executor,className:'transcript-meta-kv',attempt:attempt?{retryNumber:attempt.retryNumber,tone:stateClass(attemptState)||'',label:attemptState}:undefined});
  const desc = (document.getElementById('transcriptDesc') as UIElement);
  if (desc) desc.textContent = `${act.nodeId} · ${act.state}`;
  const back = (document.getElementById('transcriptBackLink') as UIElement);
  if (back) { back.href = `/runs/${encodeURIComponent(runId)}`; back.textContent = '← Back to run'; }
}

function scheduleTranscriptPagePoll(runId, actId) {
  stopTranscriptPage();
  const token = ++tpagePollToken;
  let statePollCount = 0;
  async function tick() {
    if (token !== tpagePollToken || tpageRunId !== runId || tpageActId !== actId) return;
    try {
      const streaming = tpageActState === 'running';
      const d = await api(streaming
        ? `/v1/runs/${encodeURIComponent(runId)}/logs?activationId=${encodeURIComponent(actId)}`
        : `/v1/runs/${encodeURIComponent(runId)}/logs?activationId=${encodeURIComponent(actId)}&offset=${tpageLogOffset}&limit=1`);
      if (token !== tpagePollToken || tpageRunId !== runId || tpageActId !== actId) return;
      if (!streaming) activationTranscripts.delete(actId);
      const text = mergeActivationLogs(actId, d.logs || []);
      const pager=(document.getElementById('transcriptPager') as UIElement);
      if(pager){
        pager.innerHTML=streaming?'':renderPagination({label:'Transcript pages',summary:`Page ${tpageLogOffset+1}`,previous:{attributes:{'data-transcript-page':-1},disabled:tpageLogOffset===0},next:{attributes:{'data-transcript-page':1},disabled:!d.hasMore}});
        if(!streaming)pager.querySelectorAll<UIElement>('[data-transcript-page]').forEach(button=>button.onclick=()=>{tpageLogOffset=Math.max(0,tpageLogOffset+Number(button.dataset.transcriptPage));scheduleTranscriptPagePoll(runId,actId);});
      }
      transcriptPageLogs.set(`${runId}:${actId}`, text);
      updateTranscriptPageBody(text, tpageActState === 'running');
    } catch {}
    if (token !== tpagePollToken || tpageRunId !== runId || tpageActId !== actId) return;
    // Periodically re-check the activation state so the page detects
    // when a running activation finishes and stops polling.
    statePollCount++;
    if (statePollCount % 5 === 0) {
      try {
        const act = await api(`/v1/runs/${encodeURIComponent(runId)}/activations/${encodeURIComponent(actId)}`);
        if (token !== tpagePollToken || tpageRunId !== runId || tpageActId !== actId) return;
        if (act) {
          const wasRunning = tpageActState === 'running';
          tpageActState = act.state || tpageActState;
          // A poll that began as streaming hides the history pager. Fetch once
          // more in paged mode after completion so the terminal view can expose it.
          if (wasRunning && tpageActState !== 'running') {
            scheduleTranscriptPagePoll(runId, actId);
            return;
          }
        }
      } catch {}
    }
    if (token !== tpagePollToken || tpageRunId !== runId || tpageActId !== actId) return;
    // Only continue polling while the activation is still running.
    if (tpageActState === 'running') {
      tpagePollTimer = setTimeout(tick, TPAGE_POLL_MS);
    }
  }
  tick();
}

async function refreshTranscriptState(runId, actId) {
  if (!runId || !actId) return;
  try {
    const [graphData, attemptsData] = await Promise.all([
      api(`/v1/runs/${encodeURIComponent(runId)}/activations/${encodeURIComponent(actId)}`),
      api(`/v1/runs/${encodeURIComponent(runId)}/attempts?activationId=${encodeURIComponent(actId)}&offset=0&limit=50&details=false`).catch(() => ({ attempts: [] })),
    ]);
    if (tpageRunId !== runId || tpageActId !== actId) return;
    const act = graphData;
    if (!act) return;
    tpageActState = act.state || '';
    const attempts = (attemptsData.attempts || []).filter(a => a.activationId === actId && a.state !== 'queued')
      .sort((a, b) => (b.retryNumber || 0) - (a.retryNumber || 0));
    const latestAttempt = attempts[0] || null;
    renderTranscriptMeta(runId, act, latestAttempt);
    // Show cached text while polling loads fresh content
    const cached = transcriptPageLogs.get(`${runId}:${actId}`) || activationTranscripts.get(actId)?.agent || '';
    updateTranscriptPageBody(cached, tpageActState === 'running');
    scheduleTranscriptPagePoll(runId, actId);
  } catch (err) {
    if (tpageRunId !== runId || tpageActId !== actId) return;
    const meta = (document.getElementById('transcriptMeta') as UIElement);
    if (meta) meta.innerHTML = renderNotice(`Unable to load transcript: ${err.message??''}`, {error:true});
  }
}

async function loadTranscriptPage(runId, actId) {
  stopTranscriptPage();
  tpageRunId = runId;
  tpageActId = actId;
  tpageLogOffset=0;activationTranscripts.clear();transcriptPageLogs.clear();
  tpageActState = '';
  const meta = (document.getElementById('transcriptMeta') as UIElement);
  if (meta) meta.innerHTML = renderPlaceholder("Loading…", "inspector-placeholder");
  const body = (document.getElementById('transcriptPageBody') as UIElement);
  if (body) body.textContent = '';
  // Show any cached transcript immediately
  const cached = transcriptPageLogs.get(`${runId}:${actId}`) || activationTranscripts.get(actId)?.agent || '';
  if (cached) updateTranscriptPageBody(cached, false);
  await refreshTranscriptState(runId, actId);
}

(document.getElementById('refreshTranscript') as UIElement)?.addEventListener('click', () => {
  if (tpageRunId && tpageActId) refreshTranscriptState(tpageRunId, tpageActId);
});
