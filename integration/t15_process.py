"""Production binary + external HTTP fixture + SQLite + SIGKILL/restart.

No internal module is replaced. The source-free completion proves lifecycle,
never scientific analysis. Credentials are generated and stay in memory.
"""
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import secrets
import select
import socket
import sqlite3
import threading
import time

from accounts import login, request, seed
from foundation import start, stop


def verify_process(binary, env, directory):
    calls = {}
    entered = {name: threading.Event() for name in ('T15_CANCEL', 'T15_CRASH')}
    disconnected = {name: threading.Event() for name in entered}
    closing = threading.Event()
    lock = threading.Lock()
    private_marker = 'T15_PRIVATE_PROVIDER_REASONING'

    class Provider(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            if self.path != '/chat/completions':
                self.send_error(404)
                return
            body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            name = next(m['content'] for m in reversed(body['messages']) if m['role'] == 'user')
            with lock:
                calls[name] = calls.get(name, 0) + 1
            if name in entered:
                entered[name].set()
                # Observe actual HTTP cancellation/OS-close, rather than merely
                # trusting the API's terminal label while a request still runs.
                while not closing.is_set():
                    if select.select([self.connection], [], [], .05)[0]:
                        if self.connection.recv(1, socket.MSG_PEEK) == b'':
                            disconnected[name].set()
                            return
                return
            if name == 'T15_UNAVAILABLE':
                payload = json.dumps({'error': private_marker}).encode()
                self.send_response(503)
            else:
                payload = json.dumps({
                    'id': 't15-http-fixture', 'model': 'fixture',
                    'choices': [{'index': 0, 'finish_reason': 'stop', 'message': {
                        'role': 'assistant', 'content': 'Проверка сохранения. Источники не исследованы.',
                        'reasoning_content': private_marker}}],
                    'usage': {'prompt_tokens': 10, 'completion_tokens': 10, 'total_tokens': 20},
                }).encode()
                self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

    provider = ThreadingHTTPServer(('127.0.0.1', 0), Provider)
    threading.Thread(target=provider.serve_forever, daemon=True).start()
    work = Path(directory)
    configured = env | {
        'HTTP_ADDR': '127.0.0.1:0', 'SQLITE_PATH': str(work / 'process.sqlite'),
        'CACHE_DIR': str(work / 'cache'), 'SEED_USER_1_LOGIN': 't15-a',
        'SEED_USER_2_LOGIN': 't15-b',
        'SEED_USER_1_PASSWORD': secrets.token_urlsafe(24),
        'SEED_USER_2_PASSWORD': secrets.token_urlsafe(24),
        'DEEPSEEK_BASE_URL': f'http://127.0.0.1:{provider.server_port}',
        'DEEPSEEK_API_KEY': 'fixture-only', 'DEEPSEEK_MODEL': 'fixture',
        'DEEPSEEK_THINKING': 'disabled',
    }
    process = None

    def api(method, path, token, body=None, expected=200):
        code, result, _ = request(base, method, '/api/v1' + path, token, body)
        assert code == expected, f'{method} {path}: expected {expected}, got {code}'
        assert private_marker not in json.dumps(result), 'private provider content leaked'
        return result

    def create(message):
        conversation = api('POST', '/conversations', ta, {'title': message}, 201)['id']
        path = '/conversations/' + conversation + '/runs'
        body = {'message': message, 'client_request_id': 'one'}
        rid = api('POST', path, ta, body, 202)['run_id']
        return conversation, path, body, rid

    def terminal(rid):
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            state = api('GET', '/runs/' + rid, ta)
            if state['status'] not in ('queued', 'running'):
                return state
            time.sleep(.02)
        raise AssertionError('worker failed to reach terminal status')

    def events(rid):
        items, after = [], 0
        while True:
            page = api('GET', f'/runs/{rid}/events?limit=2&after_seq={after}', ta)
            items.extend(page['items'])
            assert all(item['seq'] > after for item in page['items'])
            assert not page['has_more'] or page['next_after_seq'] > after
            after = page['next_after_seq']
            if not page['has_more']:
                assert len({item['seq'] for item in items}) == len(items)
                return items

    try:
        seed(binary, configured)
        process, base = start(binary, configured)
        ta = login(base, 't15-a', configured['SEED_USER_1_PASSWORD'])['session_token']
        tb = login(base, 't15-b', configured['SEED_USER_2_PASSWORD'])['session_token']
        profile = {'interests': 'Консолидация памяти агентов T15', 'preferred_language': 'ru'}
        api('PUT', '/profile', ta, profile)
        conversation, completed_path, completed_body, rid = create('T15_COMPLETE')
        completed = terminal(rid)
        assert completed['status'] == 'completed' and completed['report_id']
        report_path = '/reports/' + completed['report_id']
        report = api('GET', report_path, ta)
        assert report['limitations'] == ['no_addressable_citations']
        assert [e['event_type'] for e in events(rid)][-2:] == ['report_saved', 'run_finished']
        for path in ['/conversations/' + conversation, '/runs/' + rid,
                     '/runs/' + rid + '/events', report_path]:
            api('GET', path, tb, expected=404)
        api('POST', '/runs/' + rid + '/cancel', tb, expected=404)
        history = api('GET', '/conversations/' + conversation, ta)
        assert all('reasoning_content' not in m and 'provider_json' not in m for m in history['messages'])
        assert calls == {'T15_COMPLETE': 1}

        _, _, _, cancelled = create('T15_CANCEL')
        assert entered['T15_CANCEL'].wait(10), 'provider request was not in flight'
        api('POST', '/runs/' + cancelled + '/cancel', ta)
        state = terminal(cancelled)
        assert state['status'] == 'cancelled' and state['report_id'] is None
        assert disconnected['T15_CANCEL'].wait(5), 'cancel did not close provider HTTP'
        assert 'report_saved' not in [e['event_type'] for e in events(cancelled)]
        assert calls['T15_CANCEL'] == 1
        print('PASS: production in-flight HTTP cancellation closes provider connection; no report; public API excludes private reasoning')

        _, failed_path, failed_body, failed = create('T15_UNAVAILABLE')
        state = terminal(failed)
        assert state['status'] == 'failed' and state['error_code'] == 'provider_unavailable'
        assert state['report_id'] is None and calls['T15_UNAVAILABLE'] == 2
        assert state['usage']['deepseek_requests'] == 2
        assert state['usage']['unknown_usage_requests'] == 2
        assert 'report_saved' not in [e['event_type'] for e in events(failed)]
        print('PASS: production 503 retries exactly once; durable failed reason and unknown usage; raw provider error hidden')

        crash_conversation, crash_path, crash_body, crashed = create('T15_CRASH')
        assert entered['T15_CRASH'].wait(10), 'crash fixture was not in flight'
        state = api('GET', '/runs/' + crashed, ta)
        assert state['status'] == 'running' and state['usage']['deepseek_requests'] == 1
        assert state['usage']['unknown_usage_requests'] == 1
        process.kill()
        output, _ = process.communicate(timeout=10)
        assert process.returncode == -9 and private_marker not in output
        assert 'WARNING: DATA RACE' not in output
        assert disconnected['T15_CRASH'].wait(5)
        with lock:
            before_restart = dict(calls)
        for _ in range(2):
            process, base = start(binary, configured)
            assert api('GET', report_path, ta) == report
            assert api('GET', '/conversations/' + conversation, ta) == history
            restored_profile = api('GET', '/profile', ta)
            assert all(restored_profile[k] == v for k, v in profile.items())
            recovered = api('GET', '/runs/' + crashed, ta)
            assert recovered['status'] == 'interrupted' and recovered['error_code'] == 'run_interrupted'
            assert recovered['report_id'] is None and recovered['usage'] == state['usage']
            assert recovered['started_at'] and recovered['finished_at']
            recovered_events = events(crashed)
            assert sum(e['event_type'] == 'run_finished' for e in recovered_events) == 1
            assert 'report_saved' not in [e['event_type'] for e in recovered_events]
            for path, body, expected_id in [(completed_path, completed_body, rid),
                                            (failed_path, failed_body, failed),
                                            (crash_path, crash_body, crashed)]:
                assert api('POST', path, ta, body, 202)['run_id'] == expected_id
            api('GET', '/conversations/' + crash_conversation, tb, expected=404)
            api('GET', report_path, tb, expected=404)
            with lock:
                assert calls == before_restart, 'restart/idempotency replayed an external request'
            with sqlite3.connect(configured['SQLITE_PATH']) as db:
                assert db.execute('PRAGMA integrity_check').fetchone() == ('ok',)
                assert db.execute('PRAGMA foreign_key_check').fetchall() == []
                assert db.execute('SELECT count(*) FROM reports').fetchone() == (1,)
            stop(process)
        print('PASS: SIGKILL while provider HTTP fixture pending, two OS restarts retain report/history/profile/session and unknown reservation; interrupted once; no automatic or idempotent replay; SQLite integrity')
    finally:
        if process is not None and process.poll() is None:
            stop(process)
        closing.set()
        provider.shutdown()
        provider.server_close()
