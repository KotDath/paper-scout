"""Production entrypoint + HTTP provider boundary + actual OS process restart."""
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import secrets
import threading
import time

from accounts import login, request, seed
from foundation import start, stop


class Provider(BaseHTTPRequestHandler):
    calls = 0

    def log_message(self, *_):
        pass

    def do_POST(self):
        assert self.path == '/chat/completions'
        json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        type(self).calls += 1
        payload = json.dumps({
            'id': 'entrypoint-fixture', 'model': 'fixture',
            'choices': [{'index': 0, 'finish_reason': 'stop',
                         'message': {'role': 'assistant',
                                     'content': 'Отчёт проверки запуска. Источники не исследованы.'}}],
            'usage': {'prompt_tokens': 10, 'completion_tokens': 10, 'total_tokens': 20},
        }).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def verify_process(binary, env, directory):
    provider = ThreadingHTTPServer(('127.0.0.1', 0), Provider)
    threading.Thread(target=provider.serve_forever, daemon=True).start()
    work = Path(directory)
    configured = env | {
        'HTTP_ADDR': '127.0.0.1:0', 'SQLITE_PATH': str(work / 'process.sqlite'),
        'CACHE_DIR': str(work / 'process-cache'), 'SEED_USER_1_LOGIN': 'process-a',
        'SEED_USER_2_LOGIN': 'process-b',
        'SEED_USER_1_PASSWORD': secrets.token_urlsafe(24),
        'SEED_USER_2_PASSWORD': secrets.token_urlsafe(24),
        'DEEPSEEK_BASE_URL': f'http://127.0.0.1:{provider.server_port}',
        'DEEPSEEK_API_KEY': 'fixture-only', 'DEEPSEEK_MODEL': 'fixture',
        'DEEPSEEK_THINKING': 'disabled',
    }
    try:
        seed(binary, configured)
        process, base = start(binary, configured)
        try:
            token = login(base, 'process-a', configured['SEED_USER_1_PASSWORD'])['session_token']
            _, conversation, _ = request(base, 'POST', '/api/v1/conversations', token,
                                          {'title': 'Entrypoint restart'})
            path = '/api/v1/conversations/' + conversation['id'] + '/runs'
            body = {'message': 'Entrypoint integration fixture', 'client_request_id': 'one'}
            code, run, _ = request(base, 'POST', path, token, body)
            assert code == 202
            run_path = '/api/v1/runs/' + run['run_id']
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                code, state, _ = request(base, 'GET', run_path, token)
                if state['status'] not in ('queued', 'running'):
                    break
                time.sleep(.02)
            assert code == 200 and state['status'] == 'completed'
            report_path = '/api/v1/reports/' + state['report_id']
            code, report, _ = request(base, 'GET', report_path, token)
            assert code == 200 and report['limitations'] == ['no_addressable_citations']
            assert Provider.calls == 1
        finally:
            stop(process)
        process, base = start(binary, configured)
        try:
            code, restored, _ = request(base, 'GET', report_path, token)
            assert code == 200 and restored == report
            code, repeated, _ = request(base, 'POST', path, token, body)
            assert code == 202 and repeated['run_id'] == run['run_id']
            assert Provider.calls == 1, 'restart or retry replayed provider call'
        finally:
            stop(process)
        # Missing credentials are explicit durable failure, with no fixture call.
        process, base = start(binary, configured | {'DEEPSEEK_API_KEY': ''})
        try:
            code, unavailable, _ = request(base, 'POST', path, token,
                                           body | {'client_request_id': 'unavailable'})
            assert code == 202
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                _, state, _ = request(base, 'GET', '/api/v1/runs/' + unavailable['run_id'], token)
                if state['status'] == 'failed':
                    break
                time.sleep(.02)
            assert state['status'] == 'failed' and state['error_code'] == 'provider_unavailable'
            assert state['report_id'] is None and Provider.calls == 1
        finally:
            stop(process)
        print('PASS: production Go entrypoint runs worker, survives OS process restart, avoids replay, records missing provider failure')
    finally:
        provider.shutdown()
        provider.server_close()
