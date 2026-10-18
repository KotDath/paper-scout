#!/usr/bin/env python3
"""HTTP → real ingestion/analyzer/DeepSeek client → migrated temporary SQLite.
--live uses arXiv and DeepSeek; dotenv is loaded only inside the Go process.
"""
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / '.bb/runtime/t10'


def main():
    RUNTIME.mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    for key, name in [('GOCACHE', 'gocache'), ('GOMODCACHE', 'gomod'),
                      ('GOPATH', 'gopath'), ('TMPDIR', 'tmp')]:
        directory = RUNTIME / name
        directory.mkdir(exist_ok=True)
        env[key] = str(directory)
    with tempfile.TemporaryDirectory(prefix='analysis-', dir=RUNTIME) as directory:
        binary = Path(directory) / 'analysis-integration'
        subprocess.run(['go', 'build', '-o', str(binary), './integration/analysis'],
                       cwd=ROOT, env=env, check=True)
        return subprocess.run([str(binary), '--runtime', directory, *sys.argv[1:]],
                              cwd=ROOT, env=env).returncode


if __name__ == '__main__':
    sys.exit(main())
