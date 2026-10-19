#!/usr/bin/env python3
"""Sequential production HTTP/SQLite/Qwen3 replay regressions; synthetic provider.
No unit/widget tests, dotenv reads or paid requests. Inspect actual provider input,
not merely the context-builder audit. All runs stay in one real conversation.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import sqlite3
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from accounts import login, seed
from foundation import start, stop
from agentmemory import seed_research, json_call, wait_run
from grounding import Provider as Responses
from history_replay import OLD_NOTE, NEW_NOTE, OLD_CLAIM, FEEDBACK

ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / '.bb/runtime/t22/history-lifecycle'
V2 = 'SYNTHETIC REVISED FEEDBACK V2: earlier feedback is superseded; verify the original evaluation protocol.'
CASES = ['note_off', 'note_on', 'feedback_revision', 'feedback_aged', 'legacy_notice',
         'dependency_limit', 'foreign_dependency', 'archive_hash']


def sha(text):
    return hashlib.sha256(text.encode()).hexdigest()


class Provider(BaseHTTPRequestHandler):
    phase = ''
    case = ''
    failed = None
    inputs = []
    first_report = None

    def log_message(self, *_):
        pass

    def do_POST(self):
        try:
            req = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            contents = [m.get('content') or '' for m in req['messages']]
            wire = '\n'.join(contents)
            history = [json.loads(s.split(': ', 1)[1]) for s in contents if s.startswith('HISTORICAL REPORT CONTEXT')]
            assert history, 'silently absent history'
            assert 'PRIVATE_ACCOUNT_CONTEXT' not in wire
            assert not any(m['role'] == 'assistant' and OLD_CLAIM in (m.get('content') or '') for m in req['messages'])
            states = [h['current'] for h in history]
            prose = '\n'.join(h['historical_text'] for h in history)
            if self.phase == 'edited':
                assert any(n['text'] == NEW_NOTE for s in states for n in (s['current_notes'] or []))
                assert OLD_NOTE not in prose
            elif self.phase in ('v1', 'v2'):
                expected = FEEDBACK if self.phase == 'v1' else V2
                assert all(any(a.get('text') == expected for a in (s['current_annotations'] or [])) for s in states)
                assert OLD_CLAIM not in prose
                if self.phase == 'v2':
                    assert FEEDBACK not in wire, 'obsolete correction leaked back to provider'
                    if self.case == 'feedback_aged':
                        assert len(history) == 5 and all(s['report_id'] != 'report_a' for s in states)
                snapshots = [json.loads(s.split(': ', 1)[1]) for s in contents if s.startswith('Research memory snapshots')]
                assert all(not snap['hits'] for ss in snapshots for snap in ss), 'fixture must prove dependency handling independently of top-k'
            elif self.phase == 'cleared':
                assert NEW_NOTE not in wire and OLD_NOTE not in wire, 'cleared note leaked back to provider'
                assert all(any(n['state'] == 'cleared' and n['text'] == '' for n in (s['current_notes'] or [])) for s in states)
                assert all(h['historical_text'] == '' for h in history)
            elif self.phase == 'broken':
                current = next(h for h in history if h['current']['report_id'] == self.first_report)
                expected = 'withheld_history_dependency_limit' if self.case == 'dependency_limit' else 'withheld_annotations_unavailable'
                assert current['current']['status'] == expected, current
                assert current['historical_text'] == ''
            current_chars = sum(len(json.dumps(h['current'], ensure_ascii=False, separators=(',', ':')).replace('&', '\\u0026').replace('<', '\\u003c').replace('>', '\\u003e')) for h in history)
            snapshot_chars = sum(len(s.split(': ', 1)[1]) for s in contents if s.startswith('Research memory snapshots'))
            assert current_chars + snapshot_chars <= 12000
            type(self).inputs.append({'case': self.case, 'phase': self.phase,
                                     'history': history, 'current_plus_snapshots_runes': current_chars + snapshot_chars})
            answer = {'scope': 'Synthetic sequential replay integration', 'findings': [],
                      'comparisons': [], 'notes': [], 'memory_reason': 'Synthetic provider abstains; current feedback is data, not scientific evidence.',
                      'limitations': ['SYNTHETIC SOURCE/PROVIDER; NO PAID CALLS']}
            payload = json.dumps(Responses.response(json.dumps(answer))).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
        except Exception as err:
            type(self).failed = str(err) or repr(err)
            self.send_error(500)


def check_case(binary, provider, case):
    Provider.case = case
    Provider.failed = None
    with tempfile.TemporaryDirectory(prefix=case+'-', dir=RUNTIME) as directory:
        work = Path(directory)
        env = os.environ.copy() | {
            'HTTP_ADDR': '127.0.0.1:0', 'SQLITE_PATH': str(work/'state.sqlite'), 'CACHE_DIR': str(work/'cache'),
            'SEED_USER_1_LOGIN': 'lifecycle-a', 'SEED_USER_2_LOGIN': 'lifecycle-b',
            'SEED_USER_1_PASSWORD': secrets.token_urlsafe(20), 'SEED_USER_2_PASSWORD': secrets.token_urlsafe(20),
            'DEEPSEEK_BASE_URL': f'http://127.0.0.1:{provider.server_port}', 'DEEPSEEK_API_KEY': 'fixture-only',
            'DEEPSEEK_MODEL': 'fixture', 'DEEPSEEK_THINKING': 'disabled',
            'RESEARCH_MEMORY_ENABLED': 'false' if case == 'note_off' else 'true'}
        seed(binary, env)
        process, base = start(binary, env)
        try:
            a = login(base, 'lifecycle-a', env['SEED_USER_1_PASSWORD'])
            b = login(base, 'lifecycle-b', env['SEED_USER_2_PASSWORD'])
            token = a['session_token']
            seed_research(env['SQLITE_PATH'], a['user']['id'], b['user']['id'])
            json_call(base, 'PATCH', '/api/v1/library/paper_a', token, {'note': OLD_NOTE})
            json_call(base, 'GET', '/api/v1/reports/report_a/research-memory', token)
            with sqlite3.connect(env['SQLITE_PATH']) as db:
                db.execute("UPDATE reports SET markdown=? WHERE id='report_a'", (OLD_CLAIM+'\n'+OLD_NOTE,))
                db.execute("UPDATE runs SET report_id='report_a' WHERE id='old_a'")
                db.execute("UPDATE research_memory_jobs SET updated_at='2026-01-01T00:00:00Z' WHERE kind='note'")
                for seq, role, text in [(1, 'user', 'Synthetic historical request'), (2, 'assistant', OLD_CLAIM+'\n'+OLD_NOTE)]:
                    db.execute("INSERT INTO messages(id,user_id,conversation_id,run_id,seq,role,content,created_at) VALUES(?,?,'conv_a','old_a',?,?,?,'2026-01-02T00:00:00Z')", ('seed'+str(seq), a['user']['id'], seq, role, text))
                episode, rev = db.execute("SELECT id,revision FROM research_memories WHERE owner=? AND kind='episode' AND facet='report_a'", (a['user']['id'],)).fetchone()
            saved = {}
            runs = []

            def followup(label):
                rid = json_call(base, 'POST', '/api/v1/conversations/conv_a/runs', token,
                                {'message': 'Synthetic replay lifecycle: verify current context only. No science asserted.', 'client_request_id': label}, 202)['run_id']
                state = wait_run(base, token, rid)
                assert Provider.failed is None, Provider.failed
                assert state['status'] == 'completed', state
                report = json_call(base, 'GET', '/api/v1/reports/'+state['report_id'], token)
                assert 'history_replay' not in report, 'private replay provenance exposed in public API'
                assert report['sources'] == [] and (report['research_memory'] is None or report['research_memory']['referenced'] == [])
                saved[report['id']] = sha(report['markdown'])
                runs.append(rid)
                return report

            if case.startswith('feedback'):
                json_call(base, 'PATCH', '/api/v1/library/paper_a', token, {'note': ''})
                # Exclude the origin so both recall branches cannot compensate
                # for a missing history dependency. Its correction remains current.
                patched = json_call(base, 'PATCH', '/api/v1/research-memory/'+episode, token, {'expected_revision': rev, 'correction': FEEDBACK})
                patched = json_call(base, 'PATCH', '/api/v1/research-memory/'+episode, token, {'expected_revision': patched['revision'], 'excluded': True})
                Provider.phase = 'v1'
                for i in range(7 if case == 'feedback_aged' else 1):
                    first = followup('v1-'+str(i))
                    assert FEEDBACK in first['markdown']
                json_call(base, 'PATCH', '/api/v1/research-memory/'+episode, token, {'expected_revision': patched['revision'], 'correction': V2})
                Provider.phase = 'v2'
                last = followup('v2')
                assert V2 in last['markdown'] and FEEDBACK not in last['markdown']
            else:
                json_call(base, 'PATCH', '/api/v1/library/paper_a', token, {'note': NEW_NOTE})
                Provider.phase = 'edited'
                first = followup('edited')
                assert NEW_NOTE in first['markdown']
                Provider.first_report = first['id']
                if case == 'legacy_notice':
                    # Exact pre-fix2 persisted format: same immutable Markdown
                    # and existing history audit, without new private provenance.
                    with sqlite3.connect(env['SQLITE_PATH']) as db:
                        db.execute('UPDATE reports SET history_replay_json=NULL WHERE id=?', (first['id'],))
                elif case in ('dependency_limit', 'foreign_dependency', 'archive_hash'):
                    with sqlite3.connect(env['SQLITE_PATH']) as db:
                        raw = db.execute('SELECT history_replay_json FROM reports WHERE id=?', (first['id'],)).fetchone()[0]
                        meta = json.loads(raw)
                        if case == 'dependency_limit':
                            meta['reports'] = ['synthetic-overflow-'+str(i) for i in range(33)]
                        elif case == 'foreign_dependency':
                            meta['reports'] = ['report_private']
                        else:
                            meta['archive_sha256'] = '0'*64
                        db.execute('UPDATE reports SET history_replay_json=? WHERE id=?', (json.dumps(meta), first['id']))
                json_call(base, 'PATCH', '/api/v1/library/paper_a', token, {'note': ''})
                Provider.phase = 'broken' if case in ('dependency_limit', 'foreign_dependency', 'archive_hash') else 'cleared'
                last = followup('cleared')
                assert NEW_NOTE not in last['markdown'] and OLD_NOTE not in last['markdown']
            with sqlite3.connect(env['SQLITE_PATH']) as db:
                for report, digest in saved.items():
                    assert sha(db.execute('SELECT markdown FROM reports WHERE id=?', (report,)).fetchone()[0]) == digest
                # Exact prefix/hash excludes appended notices, not guessed headings.
                meta = json.loads(db.execute('SELECT history_replay_json FROM reports WHERE id=?', (last['id'],)).fetchone()[0])
                prefix = last['markdown'].encode()[:meta['archive_bytes']].decode()
                assert sha(prefix) == meta['archive_sha256'] and NEW_NOTE not in prefix and FEEDBACK not in prefix
                assert len(meta['reports']) <= 32
                assert db.execute("SELECT content FROM messages WHERE run_id=? AND role='assistant' AND content IS NOT NULL", (runs[-1],)).fetchall() == [(last['markdown'],)]
            return {'case': case, 'completed_followups': len(runs), 'immutable_prior_reports': True, 'paid_requests': 0}
        finally:
            stop(process)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--case', choices=CASES, action='append')
    parser.add_argument('--binary', type=Path)
    parser.add_argument('--output', type=Path, default=RUNTIME/'result.json')
    args = parser.parse_args()
    RUNTIME.mkdir(parents=True, exist_ok=True)
    binary = args.binary or RUNTIME/'paper-scout'
    if args.binary is None:
        subprocess.run(['go', 'build', '-mod=readonly', '-o', str(binary), './cmd/paper-scout'], cwd=ROOT, check=True)
    provider = ThreadingHTTPServer(('127.0.0.1', 0), Provider)
    threading.Thread(target=provider.serve_forever, daemon=True).start()
    checks = []
    try:
        for case in args.case or CASES:
            result = check_case(binary.resolve(), provider, case)
            checks.append(result)
            print('PASS', case, flush=True)
    finally:
        provider.shutdown()
        provider.server_close()
        args.output.write_text(json.dumps({'checks': checks, 'provider_input': Provider.inputs, 'failure': Provider.failed}, indent=2))
    print('PASS sequential production replay; actual HTTP provider input, real SQLite/Qwen3; paid0')


if __name__ == '__main__':
    main()
