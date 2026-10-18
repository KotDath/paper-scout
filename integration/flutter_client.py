#!/usr/bin/env python3
"""T13: production Dart client -> real Go process -> temporary SQLite."""
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
from foundation import start, stop

ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / '.bb/runtime/t13'


def main():
    RUNTIME.mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    for key, name in [('GOCACHE', 'gocache'), ('GOMODCACHE', 'gomod'),
                      ('GOPATH', 'gopath'), ('TMPDIR', 'tmp'), ('PUB_CACHE', 'pub')]:
        directory = RUNTIME / name
        directory.mkdir(exist_ok=True)
        env[key] = str(directory)
    with tempfile.TemporaryDirectory(prefix='dart-client-', dir=RUNTIME) as temporary:
        work = Path(temporary)
        binary = work / 'paper-scout'
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/paper-scout'],
                       cwd=ROOT, env=env, check=True)
        password_a, password_b = secrets.token_urlsafe(24), secrets.token_urlsafe(24)
        env |= {'HTTP_ADDR': '127.0.0.1:0', 'SQLITE_PATH': str(work / 'scout.sqlite'),
                'CACHE_DIR': str(work / 'cache'), 'SEED_USER_1_LOGIN': 'client-a',
                'SEED_USER_2_LOGIN': 'client-b', 'SEED_USER_1_PASSWORD': password_a,
                'SEED_USER_2_PASSWORD': password_b}
        seed = subprocess.run([str(binary), '-seed'], cwd=ROOT, env=env,
                              capture_output=True, timeout=20)
        assert seed.returncode == 0, 'seed failed'
        process, base = start(binary, env)
        try:
            check = subprocess.run(['dart', '--enable-asserts', 'run',
                                    'integration/flutter_client.dart'], cwd=ROOT,
                                   env=env | {'SCOUT_BASE': base,
                                              'SCOUT_PASSWORD_A': password_a,
                                              'SCOUT_PASSWORD_B': password_b},
                                   capture_output=True, text=True, timeout=60)
            assert password_a not in check.stdout + check.stderr
            assert password_b not in check.stdout + check.stderr
            print(check.stdout, end='')
            assert check.returncode == 0, 'Dart client integration failed: ' + check.stderr
        finally:
            stop(process)
    print('PASS: ephemeral loopback port and temporary DB; credentials not logged; runtime cleaned')


if __name__ == '__main__':
    main()
