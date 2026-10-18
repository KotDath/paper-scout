#!/usr/bin/env python3
"""T21 Dart repository / Android UI -> Go HTTP/auth/SQLite and real Ollama.

Persisted studies and optional T20 report snapshot are labelled fixtures.
No DeepSeek calls, source downloads, dotenv reads or unit/widget tests.
--ui serves an ephemeral API until SIGTERM for claude-in-mobile interaction.
"""
import argparse
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / '.bb/runtime/t21'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--ui', action='store_true')
    args = parser.parse_args()
    RUNTIME.mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    for key, name in [('GOCACHE', 'gocache'), ('GOMODCACHE', 'gomod'),
                      ('GOPATH', 'gopath'), ('TMPDIR', 'tmp'), ('PUB_CACHE', 'pub')]:
        directory = RUNTIME / name
        directory.mkdir(exist_ok=True)
        env[key] = str(directory)
    env['CI'] = 'true'
    with tempfile.TemporaryDirectory(prefix='flutter-memory-', dir=RUNTIME) as temporary:
        binary = Path(temporary) / 'flutter-memory'
        subprocess.run(['go', 'build', '-mod=readonly', '-o', str(binary),
                        './integration/flutter_memory'], cwd=ROOT, env=env, check=True)
        command = [str(binary), '--runtime', temporary]
        if args.ui:
            command.append('--ui')
        process = subprocess.Popen(command, cwd=ROOT, env=env)
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
