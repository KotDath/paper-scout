#!/usr/bin/env python3
"""T02 integration: real Go process + HTTP + SQLite, without mocks or unit tests.

All credentials are generated for disposable accounts. They and bearer tokens
stay in memory; diagnostics only report which scenario passed.
"""

import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import secrets
import sqlite3
import subprocess
import tempfile
import urllib.error
import urllib.request

from foundation import start, stop


ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / ".bb/runtime/t02"


def request(base, method, path, token=None, body=None, raw=None, headers=None):
    request_headers = {"Content-Type": "application/json"}
    if token is not None:
        request_headers["Authorization"] = "Bearer " + token
    request_headers.update(headers or {})
    payload = json.dumps(body).encode() if body is not None else raw
    req = urllib.request.Request(base + path, data=payload, method=method,
                                 headers=request_headers)
    try:
        response = urllib.request.urlopen(req, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        data = response.read()
        result = json.loads(data) if data else None
        assert response.headers.get("Cache-Control") == "no-store"
        return response.status, result, response.headers


def expect_error(result, status, code):
    actual, body, headers = result
    assert actual == status, f"expected {status}, got {actual}"
    assert set(body) == {"error", "request_id"}
    assert body["error"] == {"code": code, "message": body["error"]["message"],
                             "retryable": False}
    assert body["request_id"]
    if status == 401:
        assert headers.get("WWW-Authenticate") == "Bearer"


def seed(binary, env, success=True):
    process = subprocess.run([str(binary), "-seed"], cwd=ROOT, env=env,
                             capture_output=True, text=True, timeout=20)
    assert process.returncode == (0 if success else 1), "unexpected seed exit status"
    output = process.stdout + process.stderr
    assert all(env[key] not in output for key in
               ("SEED_USER_1_PASSWORD", "SEED_USER_2_PASSWORD") if env.get(key)), "seed leaked credential"
    return output


def login(base, login_name, password):
    status, body, _ = request(base, "POST", "/api/v1/auth/login",
                              body={"login": login_name, "password": password})
    assert status == 200, "login failed"
    assert set(body) == {"session_token", "expires_at", "user"}
    assert len(body["session_token"]) == 43
    assert set(body["user"]) == {"id", "login", "created_at"}
    assert body["user"]["login"] == login_name
    assert body["expires_at"].endswith("Z") and body["user"]["created_at"].endswith("Z")
    return body


def main(binary=None):
    RUNTIME.mkdir(parents=True, exist_ok=True)
    build_env = os.environ.copy()
    for key, name in [("GOCACHE", "gocache"), ("GOMODCACHE", "gomod"),
                      ("GOPATH", "gopath"), ("TMPDIR", "tmp")]:
        directory = RUNTIME / name
        directory.mkdir(exist_ok=True)
        build_env[key] = str(directory)

    with tempfile.TemporaryDirectory(prefix="accounts-", dir=RUNTIME) as temporary:
        work = Path(temporary)
        if binary is None:
            binary = work / "paper-scout"
            subprocess.run(["go", "build", "-o", str(binary), "./cmd/paper-scout"],
                           cwd=ROOT, env=build_env, check=True)
        env = build_env | {
            "HTTP_ADDR": "127.0.0.1:0", "SQLITE_PATH": str(work / "accounts.sqlite"),
            "CACHE_DIR": str(work / "cache"), "SEED_USER_1_LOGIN": "researcher-a",
            "SEED_USER_2_LOGIN": "researcher-b",
            "SEED_USER_1_PASSWORD": secrets.token_urlsafe(24),
            "SEED_USER_2_PASSWORD": secrets.token_urlsafe(24),
        }

        # Empty, duplicate and too-long seed credentials must never partly seed.
        for changes in [
            {"SEED_USER_2_PASSWORD": ""},
            {"SEED_USER_2_LOGIN": env["SEED_USER_1_LOGIN"]},
            {"SEED_USER_2_PASSWORD": "x" * 73},
            {"SEED_USER_2_PASSWORD": "short"},
            {"SEED_USER_2_LOGIN": "invalid login"},
        ]:
            seed(binary, env | changes, success=False)
            with sqlite3.connect(env["SQLITE_PATH"]) as db:
                assert db.execute("SELECT count(*) FROM users").fetchone() == (0,)
        seed(binary, env)
        with sqlite3.connect(env["SQLITE_PATH"]) as db:
            users_before = db.execute("SELECT * FROM users ORDER BY login").fetchall()
            assert len(users_before) == 2
            assert all(row[2].startswith("$2a$10$") for row in users_before)
            assert db.execute("SELECT count(*) FROM profiles").fetchone() == (2,)
            assert db.execute("SELECT count(*) FROM auth_sessions").fetchone() == (0,)
        changed_env = env | {"SEED_USER_1_PASSWORD": secrets.token_urlsafe(24),
                             "SEED_USER_2_PASSWORD": secrets.token_urlsafe(24)}
        seed(binary, changed_env)
        with sqlite3.connect(env["SQLITE_PATH"]) as db:
            assert db.execute("SELECT * FROM users ORDER BY login").fetchall() == users_before
        print("PASS: validated atomic two-account seed, bcrypt hashes, repeat preserves IDs/passwords/timestamps")

        process, base = start(binary, env)
        tokens = []
        try:
            assert request(base, "GET", "/healthz")[0] == 200
            assert request(base, "GET", "/readyz")[0] == 200
            for method, path in [("GET", "/api/v1/me"), ("GET", "/api/v1/profile"),
                                 ("PUT", "/api/v1/profile"), ("POST", "/api/v1/auth/logout"),
                                 ("GET", "/api/v1/unknown")]:
                expect_error(request(base, method, path), 401, "unauthorized")
            for malformed in ["invalid", "a" * 43, "", "basic payload", "Bearer x y"]:
                expect_error(request(base, "GET", "/api/v1/me",
                                     headers={"Authorization": malformed}), 401, "unauthorized")
            for bad_login, bad_password in [
                ("unknown-account", env["SEED_USER_1_PASSWORD"]),
                (env["SEED_USER_1_LOGIN"], "wrong-password"),
                (env["SEED_USER_1_LOGIN"], changed_env["SEED_USER_1_PASSWORD"]),
                ("' OR 1=1 --", "wrong-password"),
            ]:
                expect_error(request(base, "POST", "/api/v1/auth/login",
                                     body={"login": bad_login, "password": bad_password}),
                             401, "unauthorized")
            a = login(base, env["SEED_USER_1_LOGIN"], env["SEED_USER_1_PASSWORD"])
            b = login(base, env["SEED_USER_2_LOGIN"], env["SEED_USER_2_PASSWORD"])
            token_a, token_b = a["session_token"], b["session_token"]
            tokens += [token_a, token_b]
            assert a["user"]["id"] != b["user"]["id"] and token_a != token_b
            for account in [a, b]:
                assert request(base, "GET", "/api/v1/me", account["session_token"])[:2] == (200, account["user"])
                status, profile, _ = request(base, "GET", "/api/v1/profile", account["session_token"])
                assert status == 200 and profile["user_id"] == account["user"]["id"]
                assert profile["interests"] == "" and profile["preferred_language"] == "ru"

            profile_a = {"interests": "Консолидация долговременной памяти AI-агентов", "preferred_language": "ru"}
            profile_b = {"interests": "Independent library B", "preferred_language": "en"}
            for account, payload in [(a, profile_a), (b, profile_b)]:
                status, profile, _ = request(base, "PUT", "/api/v1/profile", account["session_token"], payload)
                assert status == 200 and profile["user_id"] == account["user"]["id"]
                assert all(profile[key] == value for key, value in payload.items())
            # No client-supplied owner can redirect either read or write.
            for token, foreign in [(token_a, b), (token_b, a)]:
                expect_error(request(base, "PUT", "/api/v1/profile", token,
                                     profile_a | {"user_id": foreign["user"]["id"]}), 400, "invalid_input")
                expect_error(request(base, "GET", "/api/v1/profile?user_id=" + foreign["user"]["id"], token),
                             400, "invalid_input")
                for target in [foreign["user"]["id"], "missing-id"]:
                    expect_error(request(base, "GET", "/api/v1/profile/" + target, token), 404, "not_found")
            for account, payload in [(a, profile_a), (b, profile_b)]:
                status, profile, _ = request(base, "GET", "/api/v1/profile", account["session_token"])
                assert status == 200 and all(profile[key] == value for key, value in payload.items())
            status, profile, _ = request(base, "GET", "/api/v1/profile", token_b,
                                         headers={"X-User-ID": a["user"]["id"]})
            assert status == 200 and profile["user_id"] == b["user"]["id"]
            with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
                results = list(pool.map(lambda account: request(base, "GET", "/api/v1/profile", account["session_token"]),
                                        [a, b] * 8))
            assert all(result[0] == 200 and result[1]["user_id"] == account["user"]["id"]
                       for result, account in zip(results, [a, b] * 8))
            print("PASS: real HTTP login/me, generic 401, two isolated profiles, owner injection rejected, concurrent reads")

            for raw in [b"{", b"null", b"[]", b'{} {}',
                        b'{"interests":"x","preferred_language":"xx"}',
                        json.dumps(profile_a | {"interests": "x" * 10001}).encode(),
                        b" " * (64 * 1024 + 1) + b"{}"]:
                expect_error(request(base, "PUT", "/api/v1/profile", token_a, raw=raw), 400, "invalid_input")
            expect_error(request(base, "PUT", "/api/v1/profile", token_a, profile_a,
                                 headers={"Content-Type": "text/plain"}), 400, "invalid_input")

            with sqlite3.connect(env["SQLITE_PATH"]) as db:
                sessions = db.execute("SELECT token_hash, user_id, expires_at, revoked_at FROM auth_sessions").fetchall()
                assert len(sessions) == 2 and all(len(row[0]) == 64 for row in sessions)
                assert {row[0] for row in sessions} == {
                    hashlib.sha256(token_a.encode()).hexdigest(), hashlib.sha256(token_b.encode()).hexdigest()}
                assert a["expires_at"] == next(row[2] for row in sessions if row[1] == a["user"]["id"])
                assert db.execute("PRAGMA integrity_check").fetchone() == ("ok",)
                db.execute("PRAGMA foreign_keys=ON")
                for sql in ["INSERT INTO profiles(user_id, updated_at) VALUES ('missing', 'now')",
                            "INSERT INTO auth_sessions(token_hash, user_id, expires_at) VALUES ('" + "a" * 64 + "', 'missing', 'now')"]:
                    try:
                        db.execute(sql)
                    except sqlite3.IntegrityError:
                        pass
                    else:
                        raise AssertionError("missing-owner foreign key was accepted")

            assert request(base, "POST", "/api/v1/auth/logout", token_a)[:2] == (204, None)
            expect_error(request(base, "GET", "/api/v1/me", token_a), 401, "unauthorized")
            expect_error(request(base, "POST", "/api/v1/auth/logout", token_a), 401, "unauthorized")
            assert request(base, "GET", "/api/v1/me", token_b)[0] == 200
            a_again = login(base, env["SEED_USER_1_LOGIN"], env["SEED_USER_1_PASSWORD"])
            token_a_again = a_again["session_token"]
            tokens.append(token_a_again)
            assert token_a_again != token_a
            a_parallel = login(base, env["SEED_USER_1_LOGIN"], env["SEED_USER_1_PASSWORD"])
            tokens.append(a_parallel["session_token"])
            assert request(base, "POST", "/api/v1/auth/logout", a_parallel["session_token"])[0] == 204
            assert request(base, "GET", "/api/v1/me", token_a_again)[0] == 200
            assert request(base, "POST", "/api/v1/auth/logout", token_b)[0] == 204
            assert request(base, "GET", "/api/v1/me", token_a_again)[0] == 200
            print("PASS: strict JSON/body limits, hashed session storage, foreign keys, logout revokes only the current token")
        finally:
            stop(process)

        # Seed over a used DB must also preserve profiles and session revocation.
        with sqlite3.connect(env["SQLITE_PATH"]) as db:
            snapshots = {table: db.execute("SELECT * FROM " + table + " ORDER BY 1").fetchall()
                         for table in ["users", "profiles", "auth_sessions", "schema_migrations"]}
        seed(binary, changed_env)
        with sqlite3.connect(env["SQLITE_PATH"]) as db:
            for table, before in snapshots.items():
                assert db.execute("SELECT * FROM " + table + " ORDER BY 1").fetchall() == before

        process, base = start(binary, env)
        try:
            assert request(base, "GET", "/api/v1/me", token_a_again)[:2] == (200, a["user"])
            profile = request(base, "GET", "/api/v1/profile", token_a_again)[1]
            assert all(profile[key] == value for key, value in profile_a.items())
            expect_error(request(base, "GET", "/api/v1/me", token_a), 401, "unauthorized")
            expect_error(request(base, "GET", "/api/v1/me", token_b), 401, "unauthorized")
            b_again = login(base, env["SEED_USER_2_LOGIN"], env["SEED_USER_2_PASSWORD"])
            tokens.append(b_again["session_token"])
            profile = request(base, "GET", "/api/v1/profile", b_again["session_token"])[1]
            assert all(profile[key] == value for key, value in profile_b.items())
            with sqlite3.connect(env["SQLITE_PATH"]) as db:
                db.execute("UPDATE auth_sessions SET expires_at = '2000-01-01T00:00:00Z' WHERE token_hash = ?",
                           (hashlib.sha256(token_a_again.encode()).hexdigest(),))
            expect_error(request(base, "GET", "/api/v1/me", token_a_again), 401, "unauthorized")
            assert request(base, "GET", "/api/v1/me", b_again["session_token"])[0] == 200
            # With B's own profile absent, A's existing profile remains invisible:
            # lookup returns 404 rather than selecting another user's row.
            with sqlite3.connect(env["SQLITE_PATH"]) as db:
                db.execute("DELETE FROM profiles WHERE user_id = ?", (b["user"]["id"],))
            expect_error(request(base, "GET", "/api/v1/profile", b_again["session_token"]), 404, "not_found")
            expect_error(request(base, "PUT", "/api/v1/profile", b_again["session_token"], profile_b), 404, "not_found")
            with sqlite3.connect(env["SQLITE_PATH"]) as db:
                assert db.execute("SELECT interests FROM profiles WHERE user_id = ?", (a["user"]["id"],)).fetchone() == (profile_a["interests"],)
        finally:
            stop(process)
        print("PASS: persisted profiles and sessions, seed preserves all existing rows, revoked/expired tokens rejected after restart")

        # Raw bearer strings and plaintext passwords must not be in persisted files.
        sensitive = tokens + [env["SEED_USER_1_PASSWORD"], env["SEED_USER_2_PASSWORD"]]
        for path in work.glob("accounts.sqlite*"):
            contents = path.read_bytes()
            assert all(value.encode() not in contents for value in sensitive), "plaintext credential persisted"
        print("PASS: SQLite files contain no plaintext passwords or bearer tokens; disposable runtime cleaned")


if __name__ == "__main__":
    main()
