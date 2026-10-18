#!/usr/bin/env python3
"""T15 integration suite. No unit/widget tests or internal mocks.

Default: external HTTP fixtures plus REAL installed Ollama for retrieval and
composed research API. --live: separate bounded official arXiv/DeepSeek suite.
All builds, caches, databases, documents and sanitized logs stay in this worktree.
"""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / '.bb/runtime/t15'
GROUPS = ('foundation', 'accounts', 'deepseek', 'arxiv', 'documents', 'embeddings',
          'runs', 'agent', 'analysis', 'retrieval-fixture', 'retrieval-live',
          'researchtools', 'researchapi', 'process')
PACKAGES = ('paper-scout', 'deepseek', 'arxiv', 'documents', 'embeddings', 'runs',
            'agent', 'analysis', 'retrieval', 'researchtools')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--only', action='append', choices=GROUPS,
                        help='repeat to rerun selected integration groups')
    parser.add_argument('--live', action='store_true',
                        help='ONLY official arXiv/DeepSeek API run, up to 12 requests per run, two runs; real Ollama')
    parser.add_argument('--env-file', help='passed only to live Go application; Python never opens dotenv')
    args = parser.parse_args()
    if args.env_file and not args.live:
        parser.error('--env-file requires --live')
    if args.live and args.only:
        parser.error('--live and --only are separate suites')
    RUNTIME.mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    # A fixture run cannot accidentally inherit paid provider or seed credentials.
    if not args.live:
        env = {k: v for k, v in env.items()
               if not k.startswith(('DEEPSEEK_', 'SEED_USER_'))}
    for key, name in [('GOCACHE', 'gocache'), ('GOMODCACHE', 'gomod'),
                      ('GOPATH', 'gopath'), ('TMPDIR', 'tmp')]:
        path = RUNTIME / name
        path.mkdir(exist_ok=True)
        env[key] = str(path)
    env['PYTHONDONTWRITEBYTECODE'] = '1'
    env['GOTELEMETRY'] = 'off'
    # Imported legacy launchers use the same worktree-local build cache.
    os.environ.clear()
    os.environ.update(env)
    selected = ['live'] if args.live else args.only or list(GROUPS)
    packages = ['researchtools'] if args.live else list(PACKAGES)
    results = []
    with tempfile.TemporaryDirectory(prefix='suite-', dir=RUNTIME) as temporary:
        work = Path(temporary)
        binaries = work / 'bin'
        binaries.mkdir()
        targets = ['./cmd/paper-scout' if p == 'paper-scout' else './integration/' + p
                   for p in packages]
        subprocess.run(['go', 'build', '-race', '-mod=readonly', '-o', str(binaries) + '/', *targets],
                       cwd=ROOT, env=env, check=True, timeout=600)
        for group in selected:
            directory = work / group
            directory.mkdir()
            log = RUNTIME / (group + '.log')
            artifact = RUNTIME / (group + '-result.json')
            artifact.unlink(missing_ok=True)
            command = None
            if group in ('foundation', 'accounts'):
                # Reuse the existing HTTP + actual OS-process/SQLite checks.
                command = [sys.executable, '-c',
                           f'import {group}; from pathlib import Path; '
                           f'{group}.RUNTIME=Path({str(RUNTIME)!r}); '
                           f'{group}.main(Path({str(binaries / "paper-scout")!r}))']
            elif group == 'process':
                command = [sys.executable, '-c',
                           'from t15_process import verify_process; import os; '
                           f'verify_process({str(binaries / "paper-scout")!r}, os.environ.copy(), {str(directory)!r})']
            else:
                package = {'retrieval-fixture': 'retrieval', 'retrieval-live': 'retrieval',
                           'researchapi': 'researchtools', 'live': 'researchtools'}.get(group, group)
                command = [str(binaries / package)]
                if group == 'runs':
                    command += [str(directory)]
                elif group not in ('deepseek', 'arxiv', 'embeddings'):
                    command += ['--runtime', str(directory)]
                if group == 'arxiv':
                    command += ['--db', str(directory / 'arxiv.sqlite')]
                if group == 'retrieval-fixture':
                    command += ['--fixture']
                if group in ('retrieval-fixture', 'retrieval-live', 'researchtools', 'researchapi', 'live'):
                    command += ['--artifact', str(artifact)]
                if group in ('researchapi', 'live'):
                    command += ['--api']
                if args.live:
                    command += ['--live']
                    if args.env_file:
                        command += ['--env-file', args.env_file]
            began = time.monotonic()
            print(f'RUN {group}', flush=True)
            # Keep failure traces in an ignored artifact, not a credential-bearing
            # environment dump. Existing executables emit sanitized diagnostics.
            with log.open('w') as output:
                os.chmod(log, 0o600)
                try:
                    with subprocess.Popen(command, cwd=ROOT / 'integration' if group in
                                          ('foundation', 'accounts', 'process') else ROOT,
                                          env=env, stdout=output, stderr=subprocess.STDOUT,
                                          start_new_session=True) as child:
                        try:
                            code = child.wait(timeout=900 if args.live else 480)
                        except subprocess.TimeoutExpired:
                            os.killpg(child.pid, signal.SIGKILL)
                            child.wait()
                            code = 124
                except OSError:
                    output.write('cannot start integration process\n')
                    code = 125
            elapsed = round(time.monotonic() - began, 2)
            results.append({'group': group, 'status': 'pass' if code == 0 else 'fail',
                            'exit_code': code, 'seconds': elapsed})
            print(f'{"PASS" if code == 0 else "FAIL"} {group} ({elapsed}s)', flush=True)
    summary = {'suite': 'official-live' if args.live else 'integration',
               'finished_at': datetime.now(timezone.utc).isoformat(),
               'commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
               'go': subprocess.check_output(['go', 'version'], cwd=ROOT, env=env, text=True).strip(),
               'race_enabled': True, 'checks': results}
    summary_name = 'live-summary' if args.live else 'summary'
    if args.only:
        summary_name += '-' + '-'.join(args.only)
    summary_path = RUNTIME / (summary_name + '.json')
    summary_path.write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')
    os.chmod(summary_path, 0o600)
    return int(any(item['status'] != 'pass' for item in results))


if __name__ == '__main__':
    sys.exit(main())
