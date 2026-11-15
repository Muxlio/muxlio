import { zodToJsonSchema, type ExecuteOptions } from '@muxlio/sdk';
import { progress } from './schemas.js';

const agent = {
  executor: 'agent/kiro',
  fallbackExecutors: ['agent/claude', 'agent/codex'],
  retry: { maxAttempts: 3, backoffSeconds: 10, maxBackoffSeconds: 60, effect: 'retryable' as const },
  timeoutSeconds: 900,
  queueTimeoutSeconds: 1800,
};

// One research iteration. The agent reads the running findings document,
// decides what gap to fill next, researches it, updates the findings document,
// and declares whether research is complete.
//
// findings.md serves as the living research document — each iteration reads
// the previous version and writes an updated, complete version. This means
// the synthesize step can use the final findings.md directly.
export const researchIteration: ExecuteOptions = {
  ...agent,
  task: `You are a research assistant working iteratively on a topic.

You are given:
- topic: the overall research subject.
- questions: the specific questions the final report must answer.
- audience: who will read the report (use this to calibrate depth and language).
- depth: "brief", "standard", or "thorough".
- iteration: current iteration number (1-based).
- previousFindings: the previous findings.md artifact, materialized as a local
  file. Read that file; it is not inline text. Null on the first iteration.
- If audience is omitted, write for a general technical reader. If depth is
  omitted, use standard depth.

Your job:

1. Read previousFindings if present; otherwise start with no findings. For each question, assess whether it is:
   "answered" — well supported with evidence,
   "partial" — some evidence but meaningful gaps remain, or
   "open" — not yet researched.

2. If all questions are "answered" at the stated depth, set "done": true in
   the progress result and copy previousFindings unchanged to findings.md. No new
   research is needed; skip to step 4.

3. Otherwise, pick the single most important open or partial question and
   research it now using available tools (web search, documents, or other
   authorized sources). Cite direct source URLs or document references and
   retrieval dates for time-sensitive claims. Prefer primary sources. Distinguish
   facts from inference, record conflicting evidence, and state tool/access
   limitations. Do not invent citations or mark unsupported answers as answered.

4. Write findings.md — a complete, updated research document replacing the
   previous version. Structure it as one section per question:
   ## Question N: <question text>
   **Status:** answered | partial | open
   **Summary:** <one or two sentence answer, or "Not yet researched.">
   **Evidence:**
   - <fact> (source/basis)
   - ...
   **Gaps:** <what remains unknown, or "None.">

   On the first iteration, include every question, marking those you have not
   researched as open. On subsequent iterations, include every question and update
   only the ones you worked on.

5. Reassess coverage after researching. Set done to true only when every
   question is answered at the requested depth. Counts must sum to totalQuestions
   and match findings.md. Write the pathless progress output as raw JSON to the
   exact scalar output file specified by the executor, with this shape:
   {
     "iteration": <integer>,
     "done": <boolean>,
     "answeredCount": <integer — questions with status "answered">,
     "partialCount": <integer>,
     "openCount": <integer>,
     "totalQuestions": <integer>,
     "thisIterationQuestion": "<question researched, or JSON null if no research was needed>"
   }

Do not delegate to another agent. Write only findings.md and the declared progress scalar output file.`,
  outputs: {
    findings: { mime: 'text/markdown', path: 'findings.md' },
    progress: { mime: 'application/json', schema: zodToJsonSchema(progress) },
  },
};

// Final synthesis step. Receives the last iteration's findings.md and produces
// the report, the structured metrics file, and the Canvas document.
export const synthesize: ExecuteOptions = {
  ...agent,
  task: `You are a research synthesizer. The research loop has completed.

You are given:
- topic, questions, audience, depth.
- findings: the final findings.md produced by the research loop. This is the
  authoritative research document, materialized as a local file. Read it and
  preserve its citations, uncertainty, and limitations.
- progress: final structured coverage counts; these must match metrics.json.
- iterations: total number of research iterations completed.

Produce three output files:

── report.md ──
A polished research report for the stated audience and depth. Structure:
1. Explain what was investigated, why it matters to this audience, and the main
   answer in plain language. State scope and relevant dates.
2. One section per question: answer it directly, explain the supporting evidence
   with source links, and describe the practical implications. Include uncertainty
   and conflicting evidence alongside the claim they qualify. Write connected
   prose; status labels and evidence counts are not answers.
3. Explain what the reader can conclude or do next, overall confidence, and the
   remaining limitations or open questions.
Use Markdown headings. Do not fabricate evidence.

── metrics.json ──
Structured data for the Canvas viewer, with this exact shape:
{
  "topic": "<topic>",
  "iterations": <total iterations>,
  "summary": {
    "answered": <count of questions with status "answered">,
    "partial": <count with "partial">,
    "open": <count with "open">
  },
  "questions": [
    {
      "id": "q<n>",
      "text": "<question text>",
      "status": "answered" | "partial" | "open",
      "confidence": "high" | "medium" | "low",
      "evidenceCount": <integer — distinct evidence points cited in findings.md>
    },
    ...
  ]
}

── report.canvas.json ──
A Muxlio Canvas v1 document that binds to metrics.json via
  "sources": { "metrics": { "artifactPort": "metrics" } }

Use only Canvas v1 component types. The schema is available through
muxlio canvas catalog --json; no MCP connection is required. Metric cards use
{ "type": "metric", "id": "...", "label": "...", "value": <number> };
metric values are literals, not data bindings. Table columns are objects with
"field" and "label" keys, with optional "kind" (e.g. "integer" or "badge").
The Canvas must be a self-contained research document. A reader must understand
what was investigated, what was learned, why the evidence supports the answers,
and what remains uncertain without opening another file. The narrative is the
main content; charts and coverage counts support it. Do not substitute a few
bullet points or a question/status table for the report.

Embed the full report.md body in a markdown component near the top, preserving
its explanations, citations, and limitations. Omit a duplicate topic heading if
report.md has one. Use plain-language chart labels and explain what each chart
measures. Evidence count means distinct cited evidence points, not proof of
correctness or strength of confidence. Research iterations are completed
research passes, not the number of sources or executor retries.

Build this structure (adapt wording to fit the content):

{
  "version": 1,
  "title": "<topic>",
  "sources": { "metrics": { "artifactPort": "metrics" } },
  "content": {
    "type": "stack", "id": "root", "gap": "comfortable",
    "children": [
      heading (level 1, the topic),
      markdown (id "research-report", the full report.md body with the context,
                substantive answer to every question, supporting citations,
                practical implications, and limitations),
      heading (level 2, "Research coverage"),
      text (explain how many research passes were completed and what was covered;
            completion means the requested questions were addressed at the
            stated depth, not that every uncertainty has been eliminated),
      callout (tone based on coverage: success/warning,
               title "Coverage after <n> research passes",
               text explaining answered/partial/open counts and any access gaps),
      grid (3 columns): metric cards for answered, partial, open counts,
      text (explain that the chart counts distinct cited evidence points per
            question; more points do not automatically mean a stronger answer),
      barChart (title "Cited evidence points by question",
                data { source "metrics", pointer "/questions" },
                categoryField "id", valueField "evidenceCount"),
      table (title "Coverage by question",
             data { source "metrics", pointer "/questions" },
             columns: id, text, status, confidence, evidenceCount,
             sortable: true),
      artifactLink (id "report-link", port "report", label "Open Markdown report")
    ]
  }
}

All component ids must be unique strings matching [a-zA-Z0-9_.-]+.
Validate before finishing: run muxlio canvas validate report.canvas.json
Fix any errors and re-validate until it passes.

Keep all outputs consistent: include each input question exactly once, preserve
question order with ids q1, q2, etc., and derive counts from the final findings.
Evidence count is coverage, not a confidence score. Do not perform new research.
Write only report.md, metrics.json, and report.canvas.json. Do not delegate.`,
  outputs: {
    report: { mime: 'text/markdown', path: 'report.md' },
    metrics: { mime: 'application/json', path: 'metrics.json' },
    canvas: { mime: 'application/vnd.muxlio.canvas+json', path: 'report.canvas.json' },
  },
};
