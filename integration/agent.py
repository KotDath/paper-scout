#!/usr/bin/env python3
"""T09 integration: actual runner, SQLite, lifecycle, tools and HTTP provider.

Fixture mode is not live provider evidence. All builds/caches/runtime files are
inside this worktree. Live mode has at most three requests, 192 output tokens
per request, no retries; credentials are loaded only by the Go process.
"""
import os
from pathlib import Path
import subprocess
import sys
import tempfile

root = Path(__file__).resolve().parents[1]
runtime = root / '.bb/runtime/t09'
env = os.environ.copy()
for variable, name in [('GOCACHE', 'gocache'), ('GOMODCACHE', 'gomod'),
                       ('GOPATH', 'gopath'), ('TMPDIR', 'tmp')]:
    path = runtime / name
    path.mkdir(parents=True, exist_ok=True)
    env[variable] = str(path)
with tempfile.TemporaryDirectory(prefix='agent-', dir=runtime) as directory:
    binary = Path(directory) / 'agent-check'
    subprocess.run(['go', 'build', '-race', '-o', str(binary), './integration/agent'],
                   cwd=root, env=env, check=True)
    subprocess.run([str(binary), '-runtime', directory, *sys.argv[1:]],
                   cwd=root, env=env, check=True, timeout=240)
