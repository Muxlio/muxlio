---
hide:
  - navigation
  - toc
---

<section class="home-hero">
  <div class="hero-copy">
    <div class="hero-eyebrow"><span class="status-dot"></span> Durable workflow coordinator</div>
    <h1>Build a workflow.<br><span>Let your agents run it.</span></h1>
    <p class="hero-lede">Muxlio is a durable workflow coordinator for multi-step work across AI agents and shell tools. Your assistant authors an immutable plan; the coordinator runs it as a durable run you can follow in the bundled UI or through MCP after setup.</p>
    <div class="hero-actions">
      <a class="hero-button hero-button-primary" href="#get-started">See the setup <span aria-hidden="true">↗</span></a>
      <a class="hero-button hero-button-secondary" href="#what-is-muxlio">What Muxlio is</a>
    </div>
    <p class="hero-proof">Claude · Codex · Cursor · Kiro · Antigravity (authoring) · command-line tools</p>
  </div>
  <div class="hero-story" aria-label="A workflow runs from goal to reviewable result">
    <div class="story-topline"><span>EXAMPLE: IMPLEMENT FEATURES</span><span class="story-live"><i></i> DURABLE</span></div>
    <div class="story-goal"><span class="story-label">YOUR WORKFLOW</span><strong>For each feature: implement → review → test → fix if needed → commit after checks pass.</strong></div>
    <div class="story-steps">
      <div class="story-step"><span class="step-index">01</span><span><strong>Implement</strong><small>Make the change and record the work</small></span><span class="step-state">AGENT</span></div>
      <div class="story-step"><span class="step-index">02</span><span><strong>Review</strong><small>Inspect the change and address issues</small></span><span class="step-state">AGENT</span></div>
      <div class="story-step"><span class="step-index">03</span><span><strong>Verify & repair</strong><small>Run checks; fix failures and try again</small></span><span class="step-state">CHECKS</span></div>
      <div class="story-step"><span class="step-index">04</span><span><strong>Commit locally</strong><small>You review the commit and choose when to push</small></span><span class="step-state step-state-accent">YOURS</span></div>
    </div>
    <div class="story-footer"><span class="story-pulse"></span> Repeats for each feature. Failed checks trigger fixes; after 10 unsuccessful checks, the run pauses.</div>
  </div>
</section>

<section id="what-is-muxlio" class="story-section">
  <div class="section-intro"><span class="section-kicker">WHAT MUXLIO IS</span><h2>Plans, runs, and workers</h2><p>Use Muxlio when work needs dependent steps, verification, retries, or hand-offs—not for one-off answers in the client. You or your assistant authors a <strong>workflow</strong>; Muxlio compiles it to an immutable <strong>plan</strong> and executes a <strong>run</strong> on the <strong>coordinator</strong>. Steps run on <strong>workers</strong> through <strong>adapters</strong> (agent CLIs, remote A2A, <code>command</code>, and others).</p></div>
  <div class="vocab-grid">
    <article class="vocab-card"><strong>Plan</strong><small>Validated Plan IR JSON with a content digest. A run always executes the exact bytes that were submitted.</small></article>
    <article class="vocab-card"><strong>Run</strong><small>One execution of a plan with your inputs—state, attempts, logs, and artifacts persist until completion or pause.</small></article>
    <article class="vocab-card"><strong>Workflow / recipe</strong><small>Authored logic in TypeScript, JSON, or a catalog template. Recipes in the repository are worked examples you can adapt.</small></article>
    <article class="vocab-card"><strong>Coordinator</strong><small>Go service that schedules steps, stores SQLite state, and exposes the HTTP API plus bundled UI (default port 7600). Streamable HTTP MCP listens separately when configured (default 7602).</small></article>
    <article class="vocab-card"><strong>Worker</strong><small>Process that claims steps and runs adapters in an authorized workspace (local machine, remote host, or sandbox).</small></article>
    <article class="vocab-card"><strong>Adapter</strong><small>Executor implementation on a worker—native agent CLIs, ACP, A2A, shell <code>command</code>, and custom binaries.</small></article>
  </div>
  <p class="home-readme">Setup ports and MCP clients: <a href="service/#setup-defaults">Setup defaults</a>. Architecture: <a href="architecture/overview/">overview</a> and <a href="architecture/developer-reference/#how-it-works">how it works</a>.</p>
</section>

<section id="how-it-works" class="story-section">
  <div class="section-intro"><span class="section-kicker">THE IMPLEMENT-FEATURES RECIPE</span><h2>How I use Muxlio to implement features</h2><p>I use this recipe to develop Muxlio itself. The workflow handles one feature at a time and runs the repository’s build and tests before creating a local commit.</p></div>
  <div class="benefit-grid">
    <article class="benefit-card"><div class="benefit-top"><span>01 · YOU</span><span class="benefit-icon">⌘</span></div><h3>Build the workflow with your assistant</h3><p>Describe the steps and checks you want. Your assistant authors and validates a workflow, then starts a run with your inputs. Check the plan before starting the run.</p><div class="benefit-caption">DESCRIBE → BUILD → VALIDATE → RUN</div></article>
    <article class="benefit-card"><div class="benefit-top"><span>02 · MUXLIO</span><span class="benefit-icon">⇢</span></div><h3>Implement, review, test, fix</h3><p>An agent implements the feature. A review step inspects and improves the change. Build and test commands run. If a check fails, an agent gets the failure report, fixes it, and the checks run again.</p><div class="benefit-caption compose-caption"><span>Until checks pass</span><div class="compose-primitives"><code>test</code><code>fix</code><code>retest</code></div></div></article>
    <article class="benefit-card"><div class="benefit-top"><span>03 · YOU</span><span class="benefit-icon">◷</span></div><h3>Review the result</h3><p>After checks pass, the recipe creates a local commit and moves to the next feature. Inspect the diff, review notes, test output, and run history. You decide whether to push.</p><div class="benefit-caption">CHECKS PASS → LOCAL COMMIT → NEXT FEATURE</div></article>
  </div>
</section>

<section class="home-proof-band"><div class="proof-copy"><span class="section-kicker">HOW I USE IT</span><h2>I run feature work overnight.</h2><p>I run Muxlio on a Mac mini and start workflows through my assistant. In the morning, I check the commits and reports. If a run stopped, I read the error and decide what to do next. The chat can be closed while it runs.</p><p>You choose where it runs: your workstation, another computer, or a server. Keep the execution machine running and use the UI or your assistant to check progress.</p></div><div class="proof-list"><div><span class="proof-check">✓</span><span><strong>See what happened</strong><small>Live logs, review notes, check results, and attempts</small></span></div><div><span class="proof-check">✓</span><span><strong>See what needs attention</strong><small>A failed or paused run keeps its progress and reason</small></span></div><div><span class="proof-check">✓</span><span><strong>Review before pushing</strong><small>The recipe creates local commits; you choose when to push</small></span></div></div></section>

<section id="get-started" class="onboarding-intro">
  <div class="section-intro"><span class="section-kicker">GET STARTED</span><h2>Install Muxlio, then start the service.</h2><p>Use Homebrew on macOS or Linux. It installs the CLI, adapters, UI, and MCP server without a manual checkout.</p></div>
</section>

```sh
brew tap Muxlio/muxlio https://github.com/Muxlio/muxlio
brew install --HEAD Muxlio/muxlio/muxlio
muxlio version
```

Then, from the workspace you want to use:

```sh
muxlio init --client kiro --scope project
muxlio service start
```

`init` prepares workspace configuration, credentials, and agent instructions;
`service start` runs the coordinator and worker. Default ports and MCP client
rules match [Start Muxlio → Setup defaults](service.md#setup-defaults).

Verify stdio MCP with the [MCP guide](mcp.md) when appropriate, then ask your assistant to build a workflow for your task,
validate it, and show you the plan before running it. You do not need Muxlio's
source repository to use the installed service. The worker needs the files and
tools your workflow uses.

For the feature example, the workflow and its verification commands are in the
[implement-features recipe](https://github.com/Muxlio/muxlio/tree/main/recipes/implement-features).
Use it as a starting point and adapt the steps, agents, and checks to your work.
The [Docker Sandbox walkthrough](docker-sandbox.md) shows two dependent features
running against one branch in a private clone with Claude Code, Codex, Cursor,
and command execution available on the `sbx` worker. Select `localhost` for the
host checkout or `sandbox` for the Docker checkout.
Follow a run at `http://127.0.0.1:7600/` or ask your assistant for its status.

<div class="home-section-heading"><div><span class="section-kicker">EXPLORE MUXLIO</span><h2>Guides and reference</h2></div></div>
<div class="path-grid">
  <a class="path-card" href="adapters/"><span class="path-number">01</span><span><strong>Choosing an adapter</strong><small>Native CLIs, ACP local agents, and A2A remote services.</small></span><span class="path-arrow">↗</span></a>
  <a class="path-card" href="service/"><span class="path-number">02</span><span><strong>Service Guide</strong><small>Set up a workspace and start the service.</small></span><span class="path-arrow">↗</span></a>
  <a class="path-card" href="mcp/"><span class="path-number">03</span><span><strong>MCP Integration</strong><small>Connect an assistant and build your workflow.</small></span><span class="path-arrow">↗</span></a>
  <a class="path-card" href="ui-operations/"><span class="path-number">04</span><span><strong>Observability UI</strong><small>Follow progress, read reports, and inspect results.</small></span><span class="path-arrow">↗</span></a>
  <a class="path-card" href="benchmarks/"><span class="path-number">05</span><span><strong>Benchmarks</strong><small>Understand what the reference suite measures.</small></span><span class="path-arrow">↗</span></a>
  <a class="path-card" href="architecture/overview/"><span class="path-number">06</span><span><strong>Architecture &amp; reference</strong><small>Execution contracts, tools, configuration, and security.</small></span><span class="path-arrow">↗</span></a>
  <a class="path-card" href="https://github.com/Muxlio/muxlio/tree/main/recipes/implement-features"><span class="path-number">07</span><span><strong>Implement-features recipe</strong><small>Read the workflow, tasks, and example inputs.</small></span><span class="path-arrow">↗</span></a>
  <a class="path-card" href="accounting/"><span class="path-number">08</span><span><strong>Accounting contract and limits</strong><small>Understand usage readings, costs, and run-budget limits.</small></span><span class="path-arrow">↗</span></a>
  <a class="path-card" href="docker-sandbox/"><span class="path-number">09</span><span><strong>Docker Sandbox walkthrough</strong><small>Host coordinator, worker in <code>sbx</code>, shared branch.</small></span><span class="path-arrow">↗</span></a>
  <a class="path-card" href="architecture/execution-security/#docker-sandboxes-sbx-and-muxlio-workers"><span class="path-number">10</span><span><strong>Execution security</strong><small>Isolation, tokens, sandboxes, and agent authorization.</small></span><span class="path-arrow">↗</span></a>
</div>
<p class="home-readme">Build your own workflows with the <a href="architecture/developer-reference/">CLI and SDK reference ↗</a></p>
