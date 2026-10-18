#!/usr/bin/env python3
"""T06: real Go client over HTTP fixtures, or explicit existing Ollama preflight.

Only the Go process may load an optional dotenv path; values are never printed.
All build outputs and caches remain inside this worktree.
"""
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / ".bb/runtime/t06"


def main():
    RUNTIME.mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    for key, name in [("GOCACHE", "gocache"), ("GOMODCACHE", "gomod"),
                      ("GOPATH", "gopath"), ("TMPDIR", "tmp")]:
        directory = RUNTIME / name
        directory.mkdir(exist_ok=True)
        env[key] = str(directory)
    with tempfile.TemporaryDirectory(prefix="embeddings-", dir=RUNTIME) as directory:
        binary = Path(directory) / "embeddings-integration"
        subprocess.run(["go", "build", "-o", str(binary), "./integration/embeddings"],
                       cwd=ROOT, env=env, check=True)
        return subprocess.run([str(binary), *sys.argv[1:]], cwd=ROOT, env=env).returncode


if __name__ == "__main__":
    sys.exit(main())
