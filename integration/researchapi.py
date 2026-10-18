#!/usr/bin/env python3
"""HTTP + SQLite + real research modules/Ollama; --live enables real sources/provider."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / '.bb/runtime/t12'


def main():
    RUNTIME.mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    for key, name in [('GOCACHE', 'gocache'), ('GOMODCACHE', 'gomod'),
                      ('GOPATH', 'gopath'), ('TMPDIR', 'tmp')]:
        directory = RUNTIME / name
        directory.mkdir(exist_ok=True)
        env[key] = str(directory)
    with tempfile.TemporaryDirectory(prefix='researchapi-', dir=RUNTIME) as directory:
        binary = Path(directory) / 'researchapi-integration'
        subprocess.run(['go', 'build', '-mod=readonly', '-o', str(binary),
                        './integration/researchtools'], cwd=ROOT, env=env, check=True)
        result = subprocess.run([str(binary), '--api', '--runtime', directory, *sys.argv[1:]],
                              cwd=ROOT, env=env).returncode
        if result != 0 or '--live' in sys.argv:
            return result
        from researchapi_process import verify_process
        server = Path(directory) / 'paper-scout'
        subprocess.run(['go', 'build', '-mod=readonly', '-o', str(server),
                        './cmd/paper-scout'], cwd=ROOT, env=env, check=True)
        verify_process(server, env, directory)
        return 0


if __name__ == '__main__':
    sys.exit(main())
