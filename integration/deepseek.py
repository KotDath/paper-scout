#!/usr/bin/env python3
"""Build and execute the real DeepSeek HTTP client integration boundary checks.

Pass --live and optionally --env-file PATH for the separately authorized paid
preflight. Only the Go process loads dotenv; this wrapper never reads it.
"""
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / ".bb/runtime/t03"


def main():
    RUNTIME.mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    for key, name in [("GOCACHE", "gocache"), ("GOMODCACHE", "gomod"),
                      ("GOPATH", "gopath"), ("TMPDIR", "tmp")]:
        directory = RUNTIME / name
        directory.mkdir(exist_ok=True)
        env[key] = str(directory)
    with tempfile.TemporaryDirectory(prefix="deepseek-", dir=RUNTIME) as directory:
        binary = Path(directory) / "deepseek-integration"
        subprocess.run(["go", "build", "-o", str(binary), "./integration/deepseek"],
                       cwd=ROOT, env=env, check=True)
        # Never capture or print process environment or dotenv diagnostics.
        return subprocess.run([str(binary), *sys.argv[1:]], cwd=ROOT, env=env).returncode


if __name__ == "__main__":
    sys.exit(main())
