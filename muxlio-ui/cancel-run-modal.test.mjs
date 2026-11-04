// cancel-run-modal.test.mjs
// Focused UI tests for the cancel-run confirmation modal:
//   1. Opening the modal shows it and does not immediately call the API.
//   2. Dismissal paths (Keep running button, backdrop click, Escape) resolve
//      the promise as false and send no cancel request.
//   3. Confirmation resolves the promise as true and sends exactly one POST
//      to the cancel endpoint.

import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const workspaceSource = readFileSync(new URL('./workspace.js', import.meta.url), 'utf8');

// ── Extract the cancel-modal state machine from workspace.js ──────────────
// We isolate just the modal logic so tests run synchronously without a full
// DOM. The section spans from `let cancelModalResolve` through the keydown
// listener (inclusive).

const modalStart = workspaceSource.indexOf('let cancelModalResolve=null;');
assert.ok(modalStart !== -1, 'cancel modal source block must be present in workspace.js');

const modalEnd = workspaceSource.indexOf(
  '// ── wire #inspRerun',
  modalStart,
);
assert.ok(modalEnd !== -1, 'expected anchor comment after cancel modal block');

const modalSource = workspaceSource.slice(modalStart, modalEnd);

// ── Minimal DOM helpers shared across test cases ──────────────────────────
function makeModal(id = 'cancelRunModal') {
  return {
    id,
    hidden: true,
    style: {},
    _listeners: {},
    addEventListener(event, fn) {
      (this._listeners[event] = this._listeners[event] || []).push(fn);
    },
    dispatchEvent(event, detail = {}) {
      for (const fn of this._listeners[event] || []) fn(detail);
    },
  };
}

function makeButton(id) {
  return {
    id,
    _listeners: {},
    addEventListener(event, fn) {
      (this._listeners[event] = this._listeners[event] || []).push(fn);
    },
    click() {
      for (const fn of this._listeners['click'] || []) fn({});
    },
  };
}

function buildContext() {
  const modal = makeModal('cancelRunModal');
  const cancelBtn = makeButton('cancelRunModalCancel');
  const confirmBtn = makeButton('cancelRunModalConfirm');

  const keydownListeners = [];
  const domDocument = {
    getElementById(id) {
      if (id === 'cancelRunModal') return modal;
      if (id === 'cancelRunModalCancel') return cancelBtn;
      if (id === 'cancelRunModalConfirm') return confirmBtn;
      return null;
    },
    addEventListener(event, fn) {
      if (event === 'keydown') keydownListeners.push(fn);
    },
    _fireKeydown(e) {
      for (const fn of keydownListeners) fn(e);
    },
  };

  const ctx = vm.createContext({ document: domDocument });
  vm.runInContext(modalSource, ctx);

  return { ctx, modal, cancelBtn, confirmBtn, domDocument };
}

// ── Test 1: opening the modal ─────────────────────────────────────────────
{
  const { ctx, modal } = buildContext();

  // Before calling openCancelRunModal the modal should be hidden.
  assert.equal(modal.hidden, true, 'modal starts hidden');

  const promise = vm.runInContext('openCancelRunModal()', ctx);
  assert.ok(promise && typeof promise.then === 'function', 'openCancelRunModal returns a thenable Promise');
  assert.equal(modal.hidden, false, 'modal is shown after openCancelRunModal()');

  // Calling openCancelRunModal again while one is pending should immediately
  // resolve to false (single-pending guard).
  const second = vm.runInContext('openCancelRunModal()', ctx);
  assert.equal(await second, false, 'second open while pending resolves false immediately');

  // Clean up — resolve the first promise by dismissing.
  vm.runInContext('closeCancelRunModal(false)', ctx);
  assert.equal(await promise, false);
}
console.log('cancel modal open checks passed');

// ── Test 2: dismissal paths send no cancel request ────────────────────────
{
  // Helper: tracks API calls
  function trackingContext() {
    const calls = [];
    const { ctx, modal, cancelBtn, confirmBtn, domDocument } = buildContext();
    // api is not defined in the isolated source; we inject a tracker to ensure
    // it is never called from the modal logic itself.
    vm.runInContext('function api(){apiCalled=true;}', ctx);
    ctx.apiCalled = false;
    ctx.apiCalls = calls;
    return { ctx, modal, cancelBtn, confirmBtn, domDocument, calls };
  }

  // 2a: Keep running button
  {
    const { ctx, modal, cancelBtn } = trackingContext();
    const p = vm.runInContext('openCancelRunModal()', ctx);
    assert.equal(modal.hidden, false, 'modal visible before dismiss');
    cancelBtn.click();
    assert.equal(await p, false, 'keep-running resolves false');
    assert.equal(modal.hidden, true, 'modal hidden after keep-running');
    assert.equal(ctx.apiCalled, false, 'no API call on keep-running');
  }

  // 2b: Backdrop click (target === currentTarget)
  {
    const { ctx, modal, domDocument } = trackingContext();
    const p = vm.runInContext('openCancelRunModal()', ctx);
    // Simulate a click on the backdrop: target is the modal itself
    modal.dispatchEvent('click', { target: modal, currentTarget: modal });
    assert.equal(await p, false, 'backdrop click resolves false');
    assert.equal(modal.hidden, true, 'modal hidden after backdrop click');
    assert.equal(ctx.apiCalled, false, 'no API call on backdrop click');
  }

  // 2c: Escape key
  {
    const { ctx, modal, domDocument } = trackingContext();
    const p = vm.runInContext('openCancelRunModal()', ctx);
    domDocument._fireKeydown({ key: 'Escape', preventDefault() {} });
    assert.equal(await p, false, 'Escape resolves false');
    assert.equal(modal.hidden, true, 'modal hidden after Escape');
    assert.equal(ctx.apiCalled, false, 'no API call on Escape');
  }

  // 2d: Escape while modal is hidden (should be a no-op)
  {
    const { ctx, modal, domDocument } = trackingContext();
    // modal is still hidden — fire Escape without opening the modal
    domDocument._fireKeydown({ key: 'Escape', preventDefault() {} });
    assert.equal(modal.hidden, true, 'Escape while hidden leaves modal hidden');
  }

  // 2e: Non-Escape key is ignored
  {
    const { ctx, modal, domDocument } = trackingContext();
    const p = vm.runInContext('openCancelRunModal()', ctx);
    let defaultPrevented = false;
    domDocument._fireKeydown({ key: 'Enter', preventDefault() { defaultPrevented = true; } });
    assert.equal(modal.hidden, false, 'Enter does not dismiss');
    assert.equal(defaultPrevented, false, 'Enter does not preventDefault');
    // clean up
    vm.runInContext('closeCancelRunModal(false)', ctx);
    await p;
  }
}
console.log('cancel modal dismissal checks passed');

// ── Test 3: confirmation sends exactly one request ────────────────────────
{
  const { ctx, modal, confirmBtn } = buildContext();

  let cancelCalls = 0;
  const runId = 'run-abc-123';

  // Inject a simplified run-controls click handler that mirrors the actual
  // logic in workspace.js: await openCancelRunModal(), then POST to cancel.
  vm.runInContext(`
    async function simulateCancelAction(runId, apiFn) {
      if (!await openCancelRunModal()) return 'skipped';
      await apiFn('/v1/runs/' + encodeURIComponent(runId) + '/cancel', { method: 'POST' });
      return 'cancelled';
    }
  `, ctx);

  const apiSpy = async (path, opts) => {
    cancelCalls++;
    assert.equal(opts.method, 'POST', 'cancel must use POST');
    assert.ok(path.includes(encodeURIComponent(runId)), 'cancel path must include run ID');
  };

  // Start the action — it will pause at openCancelRunModal()
  const actionPromise = vm.runInContext(
    `simulateCancelAction(runId, apiSpy)`,
    Object.assign(ctx, { runId, apiSpy }),
  );

  // Confirm the modal
  confirmBtn.click();
  const result = await actionPromise;

  assert.equal(result, 'cancelled', 'confirmed action should proceed to cancel');
  assert.equal(cancelCalls, 1, 'exactly one cancel request sent after confirmation');
  assert.equal(modal.hidden, true, 'modal hidden after confirmation');
}
console.log('cancel modal confirmation checks passed');
