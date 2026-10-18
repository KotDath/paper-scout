#!/usr/bin/env python3
"""Exercise real HTTP ingestion + Poppler + SQLite; --live fetches an arXiv PDF.
All caches, builds and temporary data stay in this worktree. No dotenv is read.
"""
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / '.bb/runtime/t05'


def main():
    RUNTIME.mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    for key, name in [('GOCACHE', 'gocache'), ('GOMODCACHE', 'gomod'),
                      ('GOPATH', 'gopath'), ('TMPDIR', 'tmp')]:
        directory = RUNTIME / name
        directory.mkdir(exist_ok=True)
        env[key] = str(directory)
    with tempfile.TemporaryDirectory(prefix='documents-', dir=RUNTIME) as directory:
        binary = Path(directory) / 'documents-integration'
        subprocess.run(['go', 'build', '-o', str(binary), './integration/documents'],
                       cwd=ROOT, env=env, check=True)
        return subprocess.run([str(binary), '--runtime', directory, *sys.argv[1:]],
                              cwd=ROOT, env=env).returncode


if __name__ == '__main__':
    sys.exit(main())
