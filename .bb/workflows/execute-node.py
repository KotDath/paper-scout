#!/usr/bin/env python3
"""Coordinate three persistent BB role threads in one isolated node worktree."""
import argparse
import fcntl
import json
from pathlib import Path
import subprocess
import sys
import time


def command(argv, cwd=None, timeout=120):
    result = subprocess.run(argv, cwd=cwd, text=True, capture_output=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f"Command failed ({result.returncode}): {' '.join(argv[:4])}\n{result.stderr[-2000:]}")
    return result.stdout.strip()


def bb(*args):
    return json.loads(command(["bb", *args, "--json"]))


def save(path, value):
    temp = path.with_suffix(".tmp")
    temp.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    temp.replace(path)


def role_result(output, stage):
    text = output.strip()
    if text.startswith("```"):
        text = "\n".join(text.splitlines()[1:-1])
    value = json.loads(text)
    if not isinstance(value, dict):
        raise ValueError("Role result must be an object")
    if value.get("stage") != stage:
        raise ValueError(f"Expected stage {stage}, got {value.get('stage')}")
    if value.get("status") not in {"ready", "approved", "changes_requested", "blocked"}:
        raise ValueError("Role returned unknown status")
    return value


def completed_stage(thread_id, stage):
    """Only accept a stage result from a successfully completed provider turn."""
    cursor = 0
    candidates = {}
    completed = None
    latest_status = None
    while True:
        events = bb("thread", "log", thread_id, "--after-seq", str(cursor), "--limit", "100")
        if not events:
            break
        for event in events:
            cursor = max(cursor, event["seq"])
            turn_id = event.get("scope", {}).get("turnId")
            details = event.get("data", {})
            if event["type"] in {"client/turn/requested", "turn/started"}:
                latest_status = "running"
            elif event["type"] == "item/completed":
                item = details.get("item", {})
                if item.get("type") == "agentMessage":
                    try:
                        candidates[turn_id] = role_result(item.get("text", ""), stage)
                    except (ValueError, TypeError, AttributeError):
                        pass
            elif event["type"] == "turn/completed":
                latest_status = details.get("status")
                if latest_status == "completed" and turn_id in candidates:
                    completed = candidates[turn_id]
        if len(events) < 100:
            break
    return completed, latest_status


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--input-file", required=True)
    opts = parser.parse_args()
    data = json.loads(Path(opts.input_file).read_text())
    root = Path(data["root"])
    node = data["task"]["id"]
    if node not in {f"T{i:02d}" for i in range(1, 19)}:
        raise RuntimeError("Invalid node")
    scratch = root / ".bb/runtime/nodes" / node
    scratch.mkdir(parents=True, exist_ok=True)
    lock = (scratch / "lock").open("a")
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    ledger_path = scratch / "ledger.json"
    ledger = json.loads(ledger_path.read_text()) if ledger_path.exists() else {}
    if ledger.get("status") == "ready":
        print(json.dumps(ledger["result"], ensure_ascii=False))
        return
    branch = f"dag/{node.lower()}"
    worktree = root.parent / f"{root.name}-worktrees" / node
    base = ledger.get("base_sha", data["base_sha"])
    if not worktree.exists():
        worktree.parent.mkdir(parents=True, exist_ok=True)
        command(["git", "worktree", "add", "-b", branch, str(worktree), base], root)
    if command(["git", "branch", "--show-current"], worktree) != branch:
        raise RuntimeError("Worktree branch mismatch")
    ledger.update(node_id=node, branch=branch, worktree=str(worktree), base_sha=base, status="running")
    ledger.setdefault("threads", {})
    ledger.setdefault("stages", {})
    ledger.setdefault("dispatches", {})
    save(ledger_path, ledger)
    save(scratch / "graph-snapshot.json", data["graph"])
    common = f"""Paper Scout DAG node {node}: {data['task']['title']}.
You are authorized by the user to implement this task via three role threads and two review cycles.
WORKTREE: {worktree}; BRANCH: {branch}; BASE SHA: {base}.
Every filesystem mutation and build must target THIS worktree, never the root checkout or other branches.
Read AGENTS.md and docs/execution-protocol.md in this worktree.
Read the assigned row of docs/implementation-dag.md and relevant requirements, architecture,
agent-harness, hybrid-rag, api-and-data and acceptance-and-demo docs before working.
For memory extension nodes T19-T22, docs/research-memory.md and docs/memory-approaches.md
are mandatory acceptance/contracts. They supersede earlier single-dialog memory scope.
Current execution graph at dispatch: {json.dumps(data['graph'], ensure_ascii=False)}
All declared dependencies have been merged into the base SHA. Do only this node, not its successors.
No agent libraries, no unit/widget tests. Integration checks are permitted. Device UI uses claude-in-mobile.
Do not spawn extra agents/threads. No pushes, deployments, branch merges or changing the root checkout.
Never read or print dotenv secrets. Live app processes may load shared configuration at {root / '.env'}.
Use ephemeral ports/temp DBs for checks. Missing live credentials are a concrete blocker, not permission to fake results.
Every node, including live verification/video nodes, must commit a concise sanitized result under docs/verification/{node}.md when its main outputs are ignored runtime artifacts. No credentials, private reasoning or downloaded PDFs in Git.
Return raw JSON only with keys stage, status, summary, checks (array of strings), findings (array), blockers (array).
Findings must be concrete with file/location, severity and required fix. Commit all code changes and leave worktree clean.
"""

    def run_stage(role, stage, prompt):
        previous = ledger["stages"].get(stage)
        if previous and previous.get("status") != "blocked":
            return previous
        message = common + f"\nROLE: {role}. STAGE: {stage}. Your JSON stage must be exactly {stage!r}.\n" + prompt
        prompt_file = scratch / f"{stage}.prompt.md"
        prompt_file.write_text(message)
        thread_id = ledger["threads"].get(role)
        recovered = None
        if thread_id:
            recovered, _ = completed_stage(thread_id, stage)
        if recovered is not None:
            print(f"{node} {stage}: recovered completed result from {thread_id}", file=sys.stderr, flush=True)
        if not thread_id:
            result = bb("thread", "spawn", "--project", data["project_id"],
                        "--environment", str(worktree), "--parent-thread", data["origin_thread"],
                        "--lifecycle-owner-thread", data["origin_thread"],
                        "--provider", data["selection"]["provider"],
                        "--model", data["selection"]["model"],
                        "--reasoning-level", data["selection"]["reasoning"],
                        "--permission-mode", "full", "--visibility", "visible",
                        "--title", f"{node} {role}", "--prompt-file", str(prompt_file))
            thread_id = result["id"]
            ledger["threads"][role] = thread_id
            ledger["dispatches"][stage] = thread_id
            save(ledger_path, ledger)
        elif recovered is None and stage not in ledger["dispatches"]:
            bb("thread", "tell", thread_id, "--message-file", str(prompt_file), "--mode", "auto")
            ledger["dispatches"][stage] = thread_id
            save(ledger_path, ledger)
        print(f"{node} {stage}: {thread_id}", file=sys.stderr, flush=True)
        deadline = time.monotonic() + 3600
        value = recovered
        while value is None:
            wait = subprocess.run(["bb", "thread", "wait", thread_id, "--timeout", "45s", "--json"],
                                  capture_output=True, text=True, timeout=55)
            if wait.returncode == 0:
                value, terminal_status = completed_stage(thread_id, stage)
                if value is not None:
                    break
                if terminal_status == "running":
                    if time.monotonic() >= deadline:
                        raise RuntimeError(f"Stage {stage} timeout; thread kept for inspection")
                    # The BB status projection can lag the recorded request.
                    # Wait on a state transition rather than polling/sleeping.
                    subprocess.run(["bb", "thread", "wait", thread_id, "--status", "active", "--timeout", "45s", "--json"],
                                   capture_output=True, text=True, timeout=55)
                    continue
                if terminal_status in {"interrupted", "failed", "cancelled"}:
                    raise RuntimeError(f"{stage}: provider turn {terminal_status}; no completed stage result. Worktree preserved.")
                raise RuntimeError(f"{stage}: idle without a completed JSON result for this stage; worktree preserved.")
            info = bb("thread", "show", thread_id)
            status = info["thread"]["status"]
            if status in {"error", "stopping"}:
                raise RuntimeError(f"{thread_id} became {status}")
            if time.monotonic() >= deadline:
                raise RuntimeError(f"Stage {stage} timeout; thread kept for inspection")
            print(f"{node} {stage}: waiting ({status})", file=sys.stderr, flush=True)
        (scratch / f"{stage}.output.txt").write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
        ledger["stages"][stage] = value
        save(ledger_path, ledger)
        if value["status"] == "blocked":
            raise RuntimeError(f"{stage} blocked: {json.dumps(value.get('blockers', []), ensure_ascii=False)}")
        if role != "reviewer" and command(["git", "status", "--porcelain"], worktree):
            raise RuntimeError(f"{stage} left uncommitted changes")
        return value

    def diff_file(stage):
        path = scratch / f"{stage}.patch"
        path.write_text(command(["git", "diff", base, "HEAD", "--"], worktree))
        return str(path)

    try:
        run_stage("executor", "implement", "Implement this node, run meaningful checks, commit. status=ready if node acceptance is met; otherwise blocked. Report exact checks and any live verification gaps.")
        for cycle in (1, 2):
            patch = diff_file(f"review{cycle}")
            review = run_stage("reviewer", f"review{cycle}",
                               f"Perform full diff review cycle {cycle}/2. Read {patch} and inspect actual source. Check task acceptance, correctness, isolation, contracts and prior fixes. Do not edit code. status=approved or changes_requested; blocked only for a genuine external blocker. No speculative style churn.")
            run_stage("executor", f"fix{cycle}",
                      f"Address review {cycle}/2, verify fixes and commit. Review JSON:\n{json.dumps(review, ensure_ascii=False)}\nIf no findings, verify and report without inventing changes. status=ready.")
        patch = diff_file("simplify")
        run_stage("simplifier", "simplify",
                  f"Inspect the WHOLE node diff at {patch}, source and reviews in {scratch}. Remove unnecessary/duplicate entities and incidental complexity while preserving exact behavior and required contracts. Verify equivalence with appropriate checks. Make a separate commit for changes; no artificial refactoring if already simple. status=ready.")
        patch = diff_file("verify")
        final = run_stage("reviewer", "verify",
                          f"Final targeted verification after both full reviews: inspect {patch}, ensure review findings are resolved and simplification preserves behavior. Run suitable checks if needed. Do not edit code. status=approved only when safe to merge; otherwise changes_requested/blocked with specifics.")
        if final["status"] != "approved":
            raise RuntimeError("Final review did not approve merge")
        if command(["git", "status", "--porcelain"], worktree):
            raise RuntimeError("Worktree dirty before handoff")
        head = command(["git", "rev-parse", "HEAD"], worktree)
        if head == base:
            raise RuntimeError("No implementation commit")
        checks = []
        for result in ledger["stages"].values():
            checks.extend(result.get("checks", []))
        result = dict(node_id=node, status="ready", branch=branch, worktree=str(worktree),
                      base_sha=base, head_sha=head, threads=ledger["threads"], checks=checks,
                      summary=final.get("summary", ""), blockers=[])
        ledger.update(status="ready", result=result)
        save(ledger_path, ledger)
        print(json.dumps(result, ensure_ascii=False))
    except Exception as exc:
        result = dict(node_id=node, status="blocked", branch=branch, worktree=str(worktree),
                      base_sha=base, head_sha=command(["git", "rev-parse", "HEAD"], worktree),
                      threads=ledger["threads"], checks=[], summary=str(exc), blockers=[str(exc)])
        ledger.update(status="blocked", result=result)
        save(ledger_path, ledger)
        notice = scratch / "blocker.md"
        notice.write_text(f"DAG {node} blocked; independent nodes may continue. {str(exc)}\nLedger: {ledger_path}\n")
        try:
            bb("thread", "tell", data["origin_thread"], "--message-file", str(notice), "--mode", "auto")
        except Exception:
            pass
        print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
