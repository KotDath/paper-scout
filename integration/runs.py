#!/usr/bin/env python3
"""T07 integration: real API process + SQLite + SIGKILL/restart, no providers.

All generated credentials/runtime/build files stay inside the assigned worktree.
The Go companion integrates worker lifecycle, history and the real HTTP stack.
"""
import concurrent.futures
import json
import os
from pathlib import Path
import secrets
import selectors
import sqlite3
import subprocess
import tempfile

from accounts import login, request, seed
from foundation import start, stop

ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / ".bb/runtime/t07"


def error(result, status, code):
    assert result[0] == status, f"expected {status}, got {result[0]}"
    assert result[1]["error"]["code"] == code
    assert result[1]["request_id"]


def main():
    RUNTIME.mkdir(parents=True, exist_ok=True)
    build_env = os.environ.copy()
    for key, name in [("GOCACHE", "gocache"), ("GOMODCACHE", "gomod"),
                      ("GOPATH", "gopath"), ("TMPDIR", "tmp")]:
        directory = RUNTIME / name
        directory.mkdir(exist_ok=True)
        build_env[key] = str(directory)
    with tempfile.TemporaryDirectory(prefix="http-runs-", dir=RUNTIME) as temporary:
        work = Path(temporary)
        binary = work / "paper-scout"
        companion = work / "runs-integration"
        for output, package in [(binary, "./cmd/paper-scout"),
                                (companion, "./integration/runs")]:
            subprocess.run(["go", "build", "-race", "-o", str(output), package],
                           cwd=ROOT, env=build_env, check=True)
        subprocess.run([str(companion), str(work)], cwd=ROOT, env=build_env,
                       check=True, timeout=60)
        env = build_env | {
            "HTTP_ADDR": "127.0.0.1:0", "SQLITE_PATH": str(work / "runs.sqlite"),
            "CACHE_DIR": str(work / "cache"), "SEED_USER_1_LOGIN": "runs-a",
            "SEED_USER_2_LOGIN": "runs-b",
            "SEED_USER_1_PASSWORD": secrets.token_urlsafe(24),
            "SEED_USER_2_PASSWORD": secrets.token_urlsafe(24),
        }
        seed(binary, env)
        process, base = start(binary, env)
        try:
            a = login(base, env["SEED_USER_1_LOGIN"], env["SEED_USER_1_PASSWORD"])
            b = login(base, env["SEED_USER_2_LOGIN"], env["SEED_USER_2_PASSWORD"])
            ta, tb = a["session_token"], b["session_token"]

            def conversation(token, title):
                status, body, _ = request(base, "POST", "/api/v1/conversations", token,
                                          {"title": title})
                assert status == 201
                return body["id"]

            ca, cb = conversation(ta, "Диалог A"), conversation(tb, "Диалог B")
            path = "/api/v1/conversations/" + ca
            body = {"client_request_id": "retry-one", "message": "Память агентов"}
            for token in [None, tb]:
                status = 401 if token is None else 404
                code = "unauthorized" if token is None else "not_found"
                error(request(base, "GET", path, token), status, code)
                error(request(base, "POST", path + "/runs", token, body), status, code)
            with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
                responses = list(pool.map(lambda _: request(base, "POST", path + "/runs", ta, body), range(16)))
            assert all(item[0] == 202 for item in responses)
            ids = {item[1]["run_id"] for item in responses}
            assert len(ids) == 1
            run_id = ids.pop()
            run_path = "/api/v1/runs/" + run_id
            error(request(base, "POST", path + "/runs", ta,
                          body | {"message": "changed"}), 409, "idempotency_conflict")
            error(request(base, "POST", path + "/runs", ta,
                          body | {"client_request_id": "new"}), 409, "active_run")
            for token in [None, tb]:
                status = 401 if token is None else 404
                code = "unauthorized" if token is None else "not_found"
                for method, suffix in [("GET", ""), ("GET", "/events"), ("POST", "/cancel")]:
                    error(request(base, method, run_path + suffix, token), status, code)
            own = request(base, "GET", path, ta)[1]
            assert len(own["messages"]) == 1 and own["messages"][0]["seq"] == 1
            assert own["messages"][0]["content"] == body["message"]
            assert len(own["runs"]) == 1
            assert request(base, "GET", "/api/v1/conversations", tb)[1]["items"][0]["id"] == cb
            print("PASS: authenticated owner-scoped conversations/runs/events/cancel, concurrent same-ID retry creates one run/message/event")

            # Fill the two-server-run capacity with another user's real run.
            rb = request(base, "POST", "/api/v1/conversations/" + cb + "/runs", tb, body)[1]["run_id"]
            cc = conversation(ta, "Capacity")
            capacity = request(base, "POST", "/api/v1/conversations/" + cc + "/runs", ta, body)
            error(capacity, 429, "run_capacity")
            assert capacity[1]["error"]["retryable"]
            # A replay still succeeds at capacity and does not enqueue again.
            assert request(base, "POST", path + "/runs", ta, body)[1]["run_id"] == run_id
            cancelled = request(base, "POST", run_path + "/cancel", ta)[1]
            assert cancelled["status"] == "cancelled" and cancelled["cancel_requested"]
            assert cancelled["finished_at"]
            assert request(base, "POST", run_path + "/cancel", ta)[1] == cancelled
            assert request(base, "POST", path + "/runs", ta, body)[1]["status"] == "cancelled"
            event_page = request(base, "GET", run_path + "/events?limit=1", ta)[1]
            assert event_page["has_more"] and event_page["next_after_seq"] == 1
            assert event_page["items"][0]["event_type"] == "run_queued"
            event_page = request(base, "GET", run_path + "/events?limit=1&after_seq=1", ta)[1]
            assert not event_page["has_more"] and event_page["next_after_seq"] == 2
            assert event_page["items"][0]["event_type"] == "run_finished"
            empty = request(base, "GET", run_path + "/events?after_seq=2", ta)[1]
            assert empty == {"items": [], "next_after_seq": 2, "has_more": False}
            # Distinct IDs racing for one conversation cannot both win.
            with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
                results = list(pool.map(lambda i: request(base, "POST", path + "/runs", ta,
                    body | {"client_request_id": "race-" + str(i)}), range(8)))
            assert sum(item[0] == 202 for item in results) == 1
            assert sum(item[0] == 409 for item in results) == 7
            interrupted_id = next(item[1]["run_id"] for item in results if item[0] == 202)
            print("PASS: two-run server capacity, one active per conversation, queued cancellation, immutable terminal replay and event cursors")

            for bad in ["limit=0", "limit=101", "limit=no", "after_seq=-1", "after_seq=bad",
                        "limit=1&limit=2", "user_id=foreign"]:
                error(request(base, "GET", run_path + "/events?" + bad, ta), 400, "invalid_input")
            for payload in [{"title": " "}, {"title": "x" * 201},
                            {"title": "title", "user_id": b["user"]["id"]}]:
                error(request(base, "POST", "/api/v1/conversations", ta, payload), 400, "invalid_input")
            for payload in [body | {"user_id": b["user"]["id"]}, body | {"message": " "},
                            body | {"client_request_id": ""}, body | {"message": "x" * 20001}]:
                error(request(base, "POST", path + "/runs", ta, payload), 400, "invalid_input")
            error(request(base, "POST", run_path + "/cancel", ta, {"user_id": "foreign"}), 400, "invalid_input")
            # More than one full collection page, with stable opaque cursors.
            for i in range(101):
                conversation(ta, "Page " + str(i))
            found, cursor = [], ""
            while True:
                page = request(base, "GET", "/api/v1/conversations?limit=100&cursor=" + cursor, ta)[1]
                assert len(page["items"]) <= 100
                found.extend(item["id"] for item in page["items"])
                cursor = page["next_cursor"]
                if not cursor:
                    break
            assert len(found) == len(set(found)) == 103 and ca in found and cc in found and cb not in found
            with sqlite3.connect(env["SQLITE_PATH"]) as db:
                assert db.execute("SELECT count(*) FROM runs WHERE id=?", (run_id,)).fetchone() == (1,)
                assert db.execute("SELECT count(*) FROM messages WHERE run_id=?", (run_id,)).fetchone() == (1,)
                assert db.execute("SELECT count(*) FROM run_events WHERE run_id=?", (run_id,)).fetchone() == (2,)
                assert db.execute("PRAGMA integrity_check").fetchone() == ("ok",)
                assert db.execute("PRAGMA foreign_key_check").fetchall() == []
            print("PASS: strict pagination/input limits, owner injection rejected, 103-conversation pagination and SQLite integrity")
            # A hard process crash must not cause automatic replay on startup.
            process.kill()
            process.communicate(timeout=10)
            assert process.returncode == -9
        finally:
            if process.poll() is None:
                stop(process)

        for iteration in range(2):
            process, base = start(binary, env)
            try:
                for token, rid in [(ta, interrupted_id), (tb, rb)]:
                    state = request(base, "GET", "/api/v1/runs/" + rid, token)[1]
                    assert state["status"] == "interrupted" and state["error_code"] == "run_interrupted"
                    events = request(base, "GET", "/api/v1/runs/" + rid + "/events", token)[1]["items"]
                    assert [event["event_type"] for event in events] == ["run_queued", "run_finished"]
                assert request(base, "GET", run_path, ta)[1] == cancelled
                assert request(base, "GET", path, ta)[1]["messages"][0]["content"] == body["message"]
                if iteration == 0:
                    again = request(base, "POST", "/api/v1/conversations/" + cb + "/runs", tb, body)
                    assert again[1]["run_id"] == rb and again[1]["status"] == "interrupted"
                    assert request(base, "POST", "/api/v1/conversations/" + cb + "/runs", tb,
                                   body | {"client_request_id": "explicit-new"})[0] == 202
            finally:
                stop(process)
        print("PASS: SIGKILL/restart preserves history/terminal runs/auth, recovers queued as interrupted once, no implicit replay; explicit new request allowed")

        worker = subprocess.Popen([str(companion), "serve-running", env["SQLITE_PATH"], a["user"]["id"]],
                                  cwd=ROOT, env=build_env, stdout=subprocess.PIPE,
                                  stderr=subprocess.STDOUT, text=True)
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(worker.stdout, selectors.EVENT_READ)
                assert selector.select(timeout=20), "worker did not persist running state"
                running = json.loads(worker.stdout.readline())
            with sqlite3.connect(env["SQLITE_PATH"]) as db:
                assert db.execute("SELECT status FROM runs WHERE id=?", (running["run_id"],)).fetchone() == ("running",)
            worker.kill()
            worker.communicate(timeout=10)
            assert worker.returncode == -9
        finally:
            if worker.poll() is None:
                worker.kill()
                worker.communicate(timeout=10)
        process, base = start(binary, env)
        try:
            state = request(base, "GET", "/api/v1/runs/" + running["run_id"], ta)[1]
            assert state["status"] == "interrupted" and state["started_at"] and state["finished_at"]
            history = request(base, "GET", "/api/v1/conversations/" + running["conversation_id"], ta)[1]
            assert history["messages"][-1]["content"] == "Persisted before interruption"
        finally:
            stop(process)
        print("PASS: SIGKILL of a real lifecycle worker persists running state and messages; production startup recovers it as interrupted")


if __name__ == "__main__":
    main()
