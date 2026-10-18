#!/usr/bin/env python3
"""T14 production Dart repository / device UI -> actual Go modules + SQLite.

Default uses external source/DeepSeek HTTP fixtures and real installed Ollama.
--ui --live uses official arXiv/DeepSeek, config loaded only by the Go process.
No unit/widget tests, stored research answers or mock internal services.
"""
import argparse
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / '.bb/runtime/t14'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--ui', action='store_true')
    parser.add_argument('--live', action='store_true')
    parser.add_argument('--env-file')
    options = parser.parse_args()
    if options.live and not options.ui:
        parser.error('--live requires --ui; live actions use claude-in-mobile')
    if options.env_file and not options.live:
        parser.error('--env-file requires --live')
    RUNTIME.mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    for key, name in [('GOCACHE', 'gocache'), ('GOMODCACHE', 'gomod'),
                      ('GOPATH', 'gopath'), ('TMPDIR', 'tmp'), ('PUB_CACHE', 'pub')]:
        directory = RUNTIME / name
        directory.mkdir(exist_ok=True)
        env[key] = str(directory)
    with tempfile.TemporaryDirectory(prefix='flutter-research-', dir=RUNTIME) as temporary:
        binary = Path(temporary) / 'flutter-research'
        subprocess.run(['go', 'build', '-mod=readonly', '-o', str(binary),
                        './integration/researchtools'], cwd=ROOT, env=env, check=True)
        args = [str(binary), '--runtime', temporary, '--client-check',
                'ui' if options.ui else 'dart']
        if options.live:
            args.append('--live')
        if options.env_file:
            args += ['--env-file', options.env_file]
        print('T14: live dependencies' if options.live else
              'T14: source/provider HTTP fixtures; real Ollama/Go/SQLite/Dart', flush=True)
        process = subprocess.Popen(args, cwd=ROOT, env=env)
        try:
            return process.wait()
        except KeyboardInterrupt:
            return 130
        finally:
            if process.poll() is None:
                process.terminate()
                process.wait(timeout=20)


if __name__ == '__main__':
    raise SystemExit(main())
