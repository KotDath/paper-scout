#!/usr/bin/env python3
"""T08/T15 integration: real SQLite/auth/HTTP/documents and existing live Ollama.
--fixture replaces only external embedding HTTP, and is NOT live inference.
No model download, paid calls, dotenv inspection, or unit tests.
"""
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / '.bb/runtime/t08'


def main():
    RUNTIME.mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    for key, name in [('GOCACHE', 'gocache'), ('GOMODCACHE', 'gomod'),
                      ('GOPATH', 'gopath'), ('TMPDIR', 'tmp')]:
        directory = RUNTIME / name
        directory.mkdir(exist_ok=True)
        env[key] = str(directory)
    with tempfile.TemporaryDirectory(prefix='retrieval-', dir=RUNTIME) as directory:
        binary = Path(directory) / 'retrieval-integration'
        subprocess.run(['go', 'build', '-o', str(binary), './integration/retrieval'],
                       cwd=ROOT, env=env, check=True)
        return subprocess.run([str(binary), '--runtime', directory, *sys.argv[1:]],
                              cwd=ROOT, env=env).returncode


if __name__ == '__main__':
    sys.exit(main())
