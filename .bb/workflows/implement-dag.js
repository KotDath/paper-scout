export const meta = {
  name: "implement-dag",
  description: "Implement Paper Scout in isolated task worktrees with two reviews, fixes, simplification and serialized integration",
  phases: [
    { title: "Node pipelines", detail: "Three persistent role threads per node, two review cycles and simplification" },
    { title: "Integration", detail: "Serialize merges and unlock dependent DAG nodes" },
  ],
};

const nodeSchema = {
  type: "object",
  required: ["node_id", "status", "branch", "worktree", "base_sha", "head_sha", "threads", "checks", "summary", "blockers"],
  additionalProperties: false,
  properties: {
    node_id: { type: "string" },
    status: { enum: ["ready", "blocked"] },
    branch: { type: "string" },
    worktree: { type: "string" },
    base_sha: { type: "string" },
    head_sha: { type: "string" },
    threads: { type: "object", additionalProperties: { type: "string" } },
    checks: { type: "array", items: { type: "string" } },
    summary: { type: "string" },
    blockers: { type: "array", items: { type: "string" } },
  },
};
const mergeSchema = {
  type: "object",
  required: ["node_id", "status", "head_sha", "summary", "checks", "blockers"],
  additionalProperties: false,
  properties: {
    node_id: { type: "string" },
    status: { enum: ["merged", "blocked"] },
    head_sha: { type: "string" },
    summary: { type: "string" },
    checks: { type: "array", items: { type: "string" } },
    blockers: { type: "array", items: { type: "string" } },
  },
};
const states = Object.fromEntries(args.tasks.map(t => [t.id, t.status === "done" ? "done" : "pending"]));
const running = new Map();
const results = [];
let integrationHead = args.base_sha;
let integrationBlocked = false;
const maxNodes = 3;
log("Coverage: all " + args.tasks.length + " DAG nodes; at most " + maxNodes + " independent node pipelines at once. No node sampling.");

function snapshot() {
  return args.tasks.map(t => ({ id: t.id, depends_on: t.depends_on, status: states[t.id] }));
}

function start(task) {
  states[task.id] = "running";
  const payload = {
    task,
    graph: snapshot(),
    root: args.root,
    base_sha: integrationHead,
    project_id: args.project_id,
    origin_thread: args.origin_thread,
    selection: args.selection,
  };
  const inputPath = args.root + "/.bb/runtime/requests/" + task.id + ".json";
  const prompt = `Coordinate ONLY DAG node ${task.id}. Do not implement product code yourself.
User explicitly requested isolated worktrees, executor/reviewer/simplifier threads, two review/fix cycles and independent DAG pipelines.
Read ${args.root}/AGENTS.md and docs/execution-protocol.md.
Create the JSON input below verbatim at ${inputPath} using apply_patch or another safe filesystem write.
Run python3 ${args.root}/.bb/workflows/execute-node.py --input-file ${inputPath}.
The helper creates the node worktree and three persistent BB role threads. It performs all stage transitions and saves a resume ledger. Do not create duplicate threads or edit the root tracked files.
Use an exec session with a short initial yield and wait in <=60-second increments, giving useful progress while it runs. Let role threads work; do not repeatedly inspect their logs.
Return the helper's final JSON object through the workflow result tool, EXACTLY matching the result schema. If helper fails before returning, inspect only the relevant ledger and report blocked with concrete evidence; never claim ready without both full reviews, fixes, simplification and final approval.
INPUT:
${JSON.stringify(payload, null, 2)}`;
  const job = agent(prompt, { label: task.id + " implement/review/simplify", phase: "Node pipelines", schema: nodeSchema })
    .then(result => ({ task, result }), error => ({ task, result: null, error: String(error) }));
  running.set(task.id, job);
  log("Dispatched " + task.id + " from " + integrationHead + "; dependencies merged: " + task.depends_on.join(", "));
}

async function integrate(task, node) {
  const prompt = `You are the serialized integrator for Paper Scout DAG node ${task.id}.
The user authorized merging task worktrees into the root checkout after two reviews/fixes and simplification.
ROOT: ${args.root}; target branch main. Do not push or deploy. This is the ONLY current integration worker.
Read AGENTS.md, docs/execution-protocol.md, architecture, this task row and the node ledger at .bb/runtime/nodes/${task.id}/ledger.json.
NODE RESULT:
${JSON.stringify(node, null, 2)}
EXPECTED PREVIOUS INTEGRATION HEAD: ${integrationHead}
CURRENT GRAPH:
${JSON.stringify(snapshot(), null, 2)}
Verify root is clean, branch/main matches expected integration head (or prove this exact node already merged), base is an ancestor of main, node branch head matches approved head, worktree is clean and final reviewer approval exists. Preserve concurrent independent worktrees.
Merge with git merge --no-ff. Resolve ordinary integration conflicts preserving both behaviors; run go mod tidy for dependency-file conflicts if appropriate. Do not invent duplicate entities to avoid proper integration. If the conflict cannot be resolved confidently, abort an in-progress merge and report blocked.
Verify the integrated code with checks appropriate to the changed scope: Go build/vet when Go exists, Flutter analyze when client changed, meaningful existing integration checks. Unit/widget tests are forbidden. Do not run undocumented paid live calls. If an integration regression is clear, fix it and check again; otherwise report blocked honestly, never mark the node done.
After a passing merge, update docs/implementation-tasks.json task ${task.id} status=done. Update docs/execution-state.json with this node's worktree, branch, three role thread IDs, approved node head, actual merge commit, checks and status=done; preserve other nodes. Commit this progress metadata in a separate commit. Keep runtime/secret/artifact files untracked.
The next integration head returned is git rev-parse HEAD AFTER the progress commit. Leave main clean. Return merged only after checks pass. Return blocked with exact problem and actual HEAD otherwise.
Use the workflow result tool for the required structured output.`;
  return agent(prompt, { label: task.id + " merge and verify", phase: "Integration", schema: mergeSchema });
}

while (true) {
  if (!integrationBlocked) {
    const ready = args.tasks.filter(t => states[t.id] === "pending" && t.depends_on.every(id => states[id] === "done"));
    for (const task of ready) {
      if (running.size >= maxNodes) break;
      start(task);
    }
  }
  if (!running.size) break;
  const finished = await Promise.race(Array.from(running.values()));
  running.delete(finished.task.id);
  const task = finished.task;
  const node = finished.result;
  if (!node || node.status !== "ready") {
    states[task.id] = "blocked";
    results.push({ node_id: task.id, status: "blocked", details: node || finished.error });
    log(task.id + " blocked; independent nodes continue. " + JSON.stringify(node?.blockers || finished.error));
    continue;
  }
  if (integrationBlocked) {
    states[task.id] = "ready_unmerged";
    results.push({ node_id: task.id, status: "ready_unmerged", details: node });
    continue;
  }
  let merged;
  try {
    merged = await integrate(task, node);
  } catch (error) {
    merged = { status: "blocked", summary: String(error), blockers: [String(error)] };
  }
  results.push({ node_id: task.id, node, integration: merged });
  if (merged.status === "merged") {
    states[task.id] = "done";
    integrationHead = merged.head_sha;
    log(task.id + " merged and verified; integration head " + integrationHead);
  } else {
    states[task.id] = "merge_blocked";
    integrationBlocked = true;
    log("Integration blocked on " + task.id + "; stop new dispatches, drain existing node pipelines safely.");
  }
}

const remaining = args.tasks.filter(t => states[t.id] !== "done").map(t => ({ id: t.id, status: states[t.id], dependencies: t.depends_on }));
log("Finished coverage: " + (args.tasks.length - remaining.length) + "/" + args.tasks.length + " merged; remaining " + JSON.stringify(remaining));
return { status: remaining.length ? "incomplete" : "complete", integration_head: integrationHead, states, results, remaining };
