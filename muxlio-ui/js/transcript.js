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
  const el = document.getElementById('transcriptPageBody');
  if (!el) return;
  const wrap = el.closest('.transcript-body-wrap');
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
  const meta = document.getElementById('transcriptMeta');
  if (!meta) return;
  if (!act) { meta.innerHTML = '<p class="inspector-placeholder">Loading…</p>'; return; }
  const executor = (attempt && attempt.state !== 'queued' ? attempt.executor : '') || act.executorHint || act.executor || '—';
  const attemptState = attempt?.state || '—';
  meta.innerHTML = `<dl class="live-meta-kv transcript-meta-kv">
    <dt>Run</dt><dd class="mono"><a href="#run/${encodeURIComponent(runId)}" style="color:var(--teal)">${esc(runId)}</a></dd>
    <dt>Node</dt><dd>${esc(act.nodeId)} <span class="state ${esc(stateClass(act.state) || '')}">${esc(act.state)}</span></dd>
    <dt>Agent</dt><dd>${esc(executor)}</dd>
    <dt>Attempt</dt><dd>${attempt ? `#${esc(attempt.retryNumber)} · <span class="state ${esc(stateClass(attemptState) || '')}">${esc(attemptState)}</span>` : '—'}</dd>
  </dl>`;
  const desc = document.getElementById('transcriptDesc');
  if (desc) desc.textContent = `${act.nodeId} · ${act.state}`;
  const back = document.getElementById('transcriptBackLink');
  if (back) { back.href = `#run/${encodeURIComponent(runId)}`; back.textContent = '← Back to run'; }
}

function scheduleTranscriptPagePoll(runId, actId) {
  stopTranscriptPage();
  const token = ++tpagePollToken;
  let statePollCount = 0;
  async function tick() {
    if (token !== tpagePollToken || tpageRunId !== runId || tpageActId !== actId) return;
    try {
      const d = await api(`/v1/runs/${encodeURIComponent(runId)}/logs?activationId=${encodeURIComponent(actId)}&offset=${tpageLogOffset}&limit=1`);
      if (token !== tpagePollToken || tpageRunId !== runId || tpageActId !== actId) return;
      activationTranscripts.delete(actId);
      const text = mergeActivationLogs(actId, d.logs || []);
      const pager=document.getElementById('transcriptPager');
      if(pager){pager.innerHTML=`<nav class="pager" aria-label="Transcript pages"><span>Page ${tpageLogOffset+1}</span><button class="btn ghost" data-transcript-page="-1"${tpageLogOffset===0?' disabled':''}>Previous</button><button class="btn ghost" data-transcript-page="1"${!d.hasMore?' disabled':''}>Next</button></nav>`;pager.querySelectorAll('[data-transcript-page]').forEach(button=>button.onclick=()=>{tpageLogOffset=Math.max(0,tpageLogOffset+Number(button.dataset.transcriptPage));scheduleTranscriptPagePoll(runId,actId);});}
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
        if (act) tpageActState = act.state || tpageActState;
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
    const attempts = (attemptsData.attempts || []).filter(a => a.activationId === actId)
      .sort((a, b) => (b.retryNumber || 0) - (a.retryNumber || 0));
    const latestAttempt = attempts[0] || null;
    renderTranscriptMeta(runId, act, latestAttempt);
    // Show cached text while polling loads fresh content
    const cached = transcriptPageLogs.get(`${runId}:${actId}`) || activationTranscripts.get(actId)?.agent || '';
    updateTranscriptPageBody(cached, tpageActState === 'running');
    scheduleTranscriptPagePoll(runId, actId);
  } catch (err) {
    if (tpageRunId !== runId || tpageActId !== actId) return;
    const meta = document.getElementById('transcriptMeta');
    if (meta) meta.innerHTML = `<div class="notice error" role="alert">Unable to load transcript: ${esc(err.message)}</div>`;
  }
}

async function loadTranscriptPage(runId, actId) {
  stopTranscriptPage();
  tpageRunId = runId;
  tpageActId = actId;
  tpageLogOffset=0;activationTranscripts.clear();transcriptPageLogs.clear();
  tpageActState = '';
  const meta = document.getElementById('transcriptMeta');
  if (meta) meta.innerHTML = '<p class="inspector-placeholder">Loading…</p>';
  const body = document.getElementById('transcriptPageBody');
  if (body) body.textContent = '';
  // Show any cached transcript immediately
  const cached = transcriptPageLogs.get(`${runId}:${actId}`) || activationTranscripts.get(actId)?.agent || '';
  if (cached) updateTranscriptPageBody(cached, false);
  await refreshTranscriptState(runId, actId);
}

document.getElementById('refreshTranscript')?.addEventListener('click', () => {
  if (tpageRunId && tpageActId) refreshTranscriptState(tpageRunId, tpageActId);
});
