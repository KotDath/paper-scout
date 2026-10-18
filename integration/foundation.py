#!/usr/bin/env python3
"""T01 integration check: real server process, HTTP, SQLite and restart.

Uses only safe generated configuration, ephemeral ports and disposable files
inside this worktree. Does not read shared dotenv or contact paid services.
"""

import concurrent.futures
import json
import os
from pathlib import Path
import selectors
import signal
import sqlite3
import subprocess
import tempfile
import urllib.error
import urllib.request


ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / ".bb/runtime/t01"


def request(base, path):
    try:
        with urllib.request.urlopen(base + path, timeout=5) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.load(error)


def start(binary, env, *args):
    process = subprocess.Popen(
        [str(binary), *args], cwd=ROOT, env=env,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
    )
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)
            if not selector.select(timeout=20):
                raise AssertionError("server did not announce listening address")
            event = json.loads(process.stdout.readline())
        assert event["msg"] == "server listening", event
        return process, "http://" + event["addr"]
    except BaseException:
        process.kill()
        process.communicate(timeout=5)
        raise


def stop(process):
    process.send_signal(signal.SIGTERM)
    output, _ = process.communicate(timeout=15)
    assert process.returncode == 0, output


def main():
    RUNTIME.mkdir(parents=True, exist_ok=True)
    build_env = os.environ.copy()
    for key, name in [("GOCACHE", "gocache"), ("GOMODCACHE", "gomod"),
                      ("GOPATH", "gopath"), ("TMPDIR", "tmp")]:
        directory = RUNTIME / name
        directory.mkdir(exist_ok=True)
        build_env[key] = str(directory)

    with tempfile.TemporaryDirectory(prefix="foundation-", dir=RUNTIME) as temporary:
        work = Path(temporary)
        binary = work / "paper-scout"
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/paper-scout"],
                       cwd=ROOT, env=build_env, check=True)
        env = build_env | {
            "HTTP_ADDR": "127.0.0.1:0",
            "SQLITE_PATH": str(work / "db with ? & spaces" / "scout.sqlite"),
            "CACHE_DIR": str(work / "cache"),
        }
        # Load safe generated config by path; process env must win over file.
        config = work / "safe-config"
        config.write_text("HTTP_ADDR=invalid\n" +
                          f"SQLITE_PATH='{env['SQLITE_PATH']}'\n" +
                          f"CACHE_DIR='{env['CACHE_DIR']}'\n")
        dotenv_env = {key: value for key, value in env.items()
                      if key not in {"SQLITE_PATH", "CACHE_DIR"}}
        process, base = start(binary, dotenv_env, "-env-file", str(config))
        try:
            assert request(base, "/healthz") == (200, {"status": "ok"})
            status, body = request(base, "/readyz")
            assert status == 200 and body == {
                "status": "ready",
                "checks": {"database_fts5": True, "pdftotext": True, "pdfinfo": True},
            }, body
            with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
                results = list(pool.map(lambda _: request(base, "/readyz"), range(16)))
            assert all(status == 200 for status, _ in results)
            with sqlite3.connect(env["SQLITE_PATH"]) as db:
                assert db.execute("PRAGMA journal_mode").fetchone() == ("wal",)
                ledger = db.execute("SELECT * FROM schema_migrations").fetchall()
                expected_migrations = sorted(
                    "migrations/" + path.name
                    for path in (ROOT / "internal/store/migrations").glob("*.sql")
                )
                assert sorted(row[0] for row in ledger) == expected_migrations
                assert ledger[0][0] == "migrations/T01_foundation.sql"
                assert len(ledger[0][1]) == 64
                # A real unrelated row must survive restart and migration replay.
                db.execute("CREATE TABLE integration_persistence(value TEXT NOT NULL)")
                db.execute("INSERT INTO integration_persistence VALUES ('survives restart')")
                assert db.execute("PRAGMA integrity_check").fetchone() == ("ok",)
            assert (work / "cache").is_dir()
            assert Path(env["SQLITE_PATH"]).stat().st_mode & 0o777 == 0o600
        finally:
            stop(process)
        print("PASS: HTTP health/readiness, concurrent FTS5/BM25 driver probes, WAL, private DB, dotenv precedence")

        process, base = start(binary, env)
        try:
            assert request(base, "/readyz")[0] == 200
            with sqlite3.connect(env["SQLITE_PATH"]) as db:
                assert db.execute("SELECT * FROM schema_migrations").fetchall() == ledger
                assert db.execute("SELECT value FROM integration_persistence").fetchone() == ("survives restart",)
        finally:
            stop(process)
        print("PASS: graceful SIGTERM, restart, unchanged migration ledger and persisted row")

        process, base = start(binary, env | {"PATH": str(work / "missing-tools")})
        try:
            assert request(base, "/healthz")[0] == 200
            status, body = request(base, "/readyz")
            assert status == 503
            assert body == {"status": "not_ready", "checks": {
                "database_fts5": True, "pdftotext": False, "pdfinfo": False}}
        finally:
            stop(process)
        print("PASS: missing extraction tools give 503 readiness while health stays 200")

        with sqlite3.connect(env["SQLITE_PATH"]) as db:
            db.execute("UPDATE schema_migrations SET checksum = 'changed'")
        failure = subprocess.run([str(binary)], cwd=ROOT, env=env,
                                 capture_output=True, text=True, timeout=20)
        assert failure.returncode == 1 and "migration checksum mismatch" in failure.stdout
        with sqlite3.connect(env["SQLITE_PATH"]) as db:
            assert all(row == ("changed",) for row in db.execute("SELECT checksum FROM schema_migrations"))
            assert db.execute("SELECT COUNT(*) FROM integration_persistence").fetchone() == (1,)
        print("PASS: migration drift blocks startup without overwriting existing data")

        for invalid in ["invalid", "0.0.0.0:8080", "127.0.0.1:65536"]:
            failure = subprocess.run([str(binary)], cwd=ROOT,
                                     env=env | {"HTTP_ADDR": invalid},
                                     capture_output=True, text=True, timeout=5)
            assert failure.returncode == 1 and "HTTP_ADDR" in failure.stdout
        print("PASS: invalid/non-loopback HTTP configuration rejected")

        # Parser diagnostics can contain a whole input line. Only the generic
        # failure is allowed to reach logs, even for this harmless fixture.
        config.write_text("!invalid_config_marker=value\n")
        failure = subprocess.run([str(binary), "-env-file", str(config)],
                                 cwd=ROOT, env=env, capture_output=True,
                                 text=True, timeout=5)
        assert failure.returncode == 1 and "cannot load env file" in failure.stdout
        assert "invalid_config_marker" not in failure.stdout + failure.stderr
        print("PASS: dotenv parser diagnostics sanitized")


if __name__ == "__main__":
    main()
