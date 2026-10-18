#!/usr/bin/env python3
"""Run real AgentRunner + research tools + SQLite + Ollama integration.
Default: external arXiv/document/DeepSeek HTTP fixtures only.
--live: real arXiv acquisition and official DeepSeek; dotenv loaded by Go only.
"""
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / '.bb/runtime/t11'


def main():
    RUNTIME.mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    for key, name in [('GOCACHE', 'gocache'), ('GOMODCACHE', 'gomod'),
                      ('GOPATH', 'gopath'), ('TMPDIR', 'tmp')]:
        directory = RUNTIME / name
        directory.mkdir(exist_ok=True)
        env[key] = str(directory)
    with tempfile.TemporaryDirectory(prefix='researchtools-', dir=RUNTIME) as directory:
        binary = Path(directory) / 'researchtools-integration'
        subprocess.run(['go', 'build', '-mod=readonly', '-o', str(binary),
                        './integration/researchtools'], cwd=ROOT, env=env, check=True)
        return subprocess.run([str(binary), '--runtime', directory, *sys.argv[1:]],
                              cwd=ROOT, env=env).returncode


if __name__ == '__main__':
    sys.exit(main())
