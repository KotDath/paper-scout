#!/usr/bin/env python3
"""T22 production HTTP/SQLite/real Qwen3 + explicitly synthetic science/provider.
No unit tests, dotenv reads, paid calls or substitute embedding vectors.
"""
import json
import os
import re
from pathlib import Path
import secrets
import sqlite3
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from accounts import login, request, seed
from foundation import start, stop
from agentmemory import seed_research, json_call, wait_run, SOURCE_A, SOURCE_B

ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / '.bb/runtime/t22/grounding'
EA = 'ev_' + 'a'*64
EN = 'ev_' + 'b'*64
EP = 'ev_' + 'c'*64
NOTE = 'Personal criterion: prioritize long multi-session dialogue evaluation and distinguish real from simulated users.'
WRONG = 'Synthetic intentionally incorrect old claim: the evaluation covers one hundred years of real users.'


class Provider(BaseHTTPRequestHandler):
    counts = {}
    failed = None
    old_handle = ''
    old_evidence = ''
    repair_entered = threading.Event()
    release = threading.Event()

    def log_message(self, *_):
        pass

    def do_POST(self):
        try:
            req = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            messages = req['messages']
            cases = [m['content'].split('T22_CASE: ', 1)[1].split()[0]
                     for m in messages if m.get('role') == 'user' and
                     (m.get('content') or '').startswith('T22_CASE: ')]
            case = cases[-1]
            n = self.counts.get(case, 0) + 1
            self.counts[case] = n
            manifests = [m['content'].split(': ', 1)[1] for m in messages
                         if (m.get('content') or '').startswith('CURRENT RUN REFERENCE MANIFEST')]
            assert len(manifests) == 1
            manifest = json.loads(manifests[0])
            assert not any(p['paper_version_id']=='paper_private' for p in manifest['library_papers'])
            if case == 'memory_off':
                assert manifest['library_papers'] == [] and manifest['memory'] == [] and manifest['evidence'] == []
                assert req.get('response_format') == {'type':'json_object'} and req.get('temperature') == 0
                assert not any((m.get('content') or '').startswith('Research memory snapshots') for m in messages)
                self.respond(self.response(json.dumps({'scope':'Explicit synthetic memory-off control','findings':[],
                    'comparisons':[],'notes':[],'memory_reason':'Automatic research memory is disabled.',
                    'limitations':['SYNTHETIC PROVIDER CONTROL, ZERO PAID CALLS']})))
                return
            sources = {e['evidence_id']: e for e in manifest['evidence']}
            snapshots = [json.loads(m['content'].split(': ', 1)[1]) for m in messages
                         if (m.get('content') or '').startswith('Research memory snapshots')]
            hits = {h['memory_id']: h for s in snapshots for point in s for h in point['hits']}
            memories = {m['memory_id']: m for m in manifest['memory']}
            repairing = any((m.get('content') or '').startswith('ONE FINAL REPAIR') for m in messages)
            assert req.get('response_format') == {'type':'json_object'}
            assert req.get('temperature') == 0
            if case == 'replay':
                assert not any('REJECTED_DRAFT' in (m.get('content') or '') for m in messages
                               if not (m.get('content') or '').startswith('ONE FINAL REPAIR'))
                old_context = [m for m in messages if (m.get('content') or '').startswith('HISTORICAL REPORT CONTEXT')]
                assert old_context and all(m['role']=='user' for m in old_context)
                assert not any(self.old_evidence in m['content'] for m in old_context)
                assert all(not (m.get('content') or '').startswith('## Вопрос пользователя') for m in messages if m['role']=='assistant')
            if n == 1:
                # EN is an exact canonical ID; EA is an issued short handle.
                ids = [EN] if case in ('missing_read', 'cancel', 'budget') else [sources[EA]['handle'], EN]
                payload = self.response(None, [{'id': 'read_'+case, 'type': 'function',
                    'function': {'name': 'read_passages', 'arguments': json.dumps({'evidence_ids': ids})}}])
            else:
                claim_id = next(k for k, h in hits.items() if h['kind'] == 'claim' and h.get('facet') == 'method')
                claim = memories[claim_id]['handle']
                note = next((memories[k]['handle'] for k, h in hits.items() if h['kind'] == 'user_note'), None)
                old = sources[EA]['handle']
                new = sources[EN]['handle']
                if case == 'canonical':
                    old, new, claim = EA, EN, claim_id
                if case == 'previous_run':
                    old = self.old_evidence
                if case == 'foreign':
                    old = EP
                if case == 'shortened':
                    old = EA[:12] + '…'
                if case == 'unknown':
                    claim = 'M000000000000'
                if case == 'owned_unissued':
                    claim = 'mem_unissued'
                draft = {'scope': 'Synthetic integration research, not scientific acceptance',
                    'findings': [{'source': new, 'quote': SOURCE_B, 'conclusion': 'Синтетический источник описывает фиксированное окно.'}],
                    'comparisons': [{'memory': claim, 'previous': old, 'previous_quote': SOURCE_A,
                        'current': new, 'current_quote': SOURCE_B, 'assessment': 'not_supported',
                        'conclusion': 'Намеренно неверный старый вывод не поддержан: исходник говорит о коротких диалогах, а не о ста годах.'}],
                    'notes': [note] if note else [],
                    'memory_reason': 'Соответствующие методы и условия оценки можно проверить по двум синтетическим источникам.',
                    'limitations': ['SYNTHETIC SCIENTIFIC ORIGINS / PROVIDER FIXTURE']}
                if case in ('repair', 'replay') and not repairing:
                    draft['findings'][0]['quote'] = 'REJECTED_DRAFT: this text is absent from original evidence.'
                if case == 'oversized' and not repairing:
                    draft['findings'] *= 7
                if case == 'oversized' and repairing:
                    hint = next(m['content'] for m in messages if (m.get('content') or '').startswith('ONE FINAL REPAIR'))
                    assert 'findings=7 (max 6)' in hint
                    assert len(json.loads(json.loads(hint.split('Candidate and diagnostics: ', 1)[1].split('. Original passages', 1)[0])['rejected_draft'])['findings']) == 7
                if case == 'mismatch':
                    draft['comparisons'][0]['previous_quote'] = 'REJECTED_DRAFT: this excerpt is not in the source.'
                if case == 'quote_handles':
                    draft['findings'][0]['quote']=sources[EN]['quote_options'][0]['handle']
                    draft['comparisons'][0]['previous_quote']=sources[EA]['quote_options'][0]['handle']
                    draft['comparisons'][0]['current_quote']=sources[EN]['quote_options'][0]['handle']
                if case == 'wrong_quote_source':
                    draft['comparisons'][0]['previous_quote']=sources[EN]['quote_options'][0]['handle']
                if case == 'reason_handle':
                    draft['memory_reason']='A relevant record '+claim+' was compared with primary evidence.'
                if case == 'expanded_prose':
                    draft['memory_reason']='R'*650+' '+(' '.join([note]*3))
                    assert len(draft['memory_reason']) <= 800
                if case == 'overlong_prose':
                    draft['memory_reason']='R'*801
                if case == 'note_as_claim' and not repairing:
                    draft['comparisons'].append(dict(draft['comparisons'][0], memory=note))
                if case == 'note_as_claim' and repairing:
                    hint = next(m['content'] for m in messages if (m.get('content') or '').startswith('ONE FINAL REPAIR'))
                    assert 'user_annotation_is_not_scientific_claim:' in hint
                    assert 'preserve/select this valid annotation in notes' in hint
                if case == 'reason_unissued':
                    draft['memory_reason']='A relevant record M000000000000 was used.'
                if case == 'prose_handle':
                    draft['scope']='Review '+claim
                    draft['comparisons'][0]['conclusion']+=' Exact known record '+claim
                    draft['limitations']=['Known source '+old]
                if case == 'cancel' and repairing:
                    self.repair_entered.set()
                    assert self.release.wait(15)
                if case == 'missing_read' and n == 2:
                    assert not sources[EA]['read']
                if case == 'missing_read' and repairing:
                    assert sources[EA]['read'] and sources[EN]['read']
                if case == 'repair' and repairing:
                    # Rejected candidate may be ephemeral repair input only.
                    assert any('REJECTED_DRAFT' in (m.get('content') or '') for m in messages if (m.get('content') or '').startswith('ONE FINAL REPAIR'))
                    assert not any('REJECTED_DRAFT' in (m.get('content') or '') for m in messages if m.get('role') == 'assistant')
                if case == 'success':
                    type(self).old_handle = claim
                    type(self).old_evidence = old
                payload = self.response(json.dumps(draft, ensure_ascii=False))
                if case == 'budget' and repairing:
                    payload['usage'] = {'prompt_tokens': 250000, 'completion_tokens': 10, 'total_tokens': 250010}
            self.respond(payload)
        except (BrokenPipeError, ConnectionResetError):
            pass
        except Exception as exc:
            type(self).failed = exc
            self.send_error(500)

    def respond(self, payload):
        raw = json.dumps(payload).encode()
        self.send_response(200); self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(raw))); self.end_headers(); self.wfile.write(raw)

    @staticmethod
    def response(content, calls=None):
        msg = {'role': 'assistant', 'content': content}
        if calls:
            msg['tool_calls'] = calls
        return {'id': 'synthetic-grounding', 'model': 'fixture',
                'choices': [{'message': msg, 'finish_reason': 'tool_calls' if calls else 'stop'}],
                'usage': {'prompt_tokens': 20, 'completion_tokens': 10, 'total_tokens': 30}}


def main():
    RUNTIME.mkdir(parents=True, exist_ok=True)
    binary = RUNTIME / 'paper-scout'
    subprocess.run(['go', 'build', '-mod=readonly', '-o', str(binary), './cmd/paper-scout'], cwd=ROOT, check=True)
    provider = ThreadingHTTPServer(('127.0.0.1', 0), Provider)
    threading.Thread(target=provider.serve_forever, daemon=True).start()
    results = {}
    with tempfile.TemporaryDirectory(prefix='fixture-', dir=RUNTIME) as temporary:
        work = Path(temporary)
        env = os.environ.copy() | {'HTTP_ADDR': '127.0.0.1:0', 'SQLITE_PATH': str(work/'state.sqlite'),
            'CACHE_DIR': str(work/'cache'), 'SEED_USER_1_LOGIN': 'ground-a', 'SEED_USER_2_LOGIN': 'ground-b',
            'SEED_USER_1_PASSWORD': secrets.token_urlsafe(24), 'SEED_USER_2_PASSWORD': secrets.token_urlsafe(24),
            'DEEPSEEK_BASE_URL': f'http://127.0.0.1:{provider.server_port}', 'DEEPSEEK_MODEL': 'fixture',
            'DEEPSEEK_API_KEY': 'fixture-only', 'DEEPSEEK_THINKING': 'disabled', 'RESEARCH_MEMORY_ENABLED': 'true'}
        seed(binary, env)
        process, base = start(binary, env)
        try:
            a = login(base, 'ground-a', env['SEED_USER_1_PASSWORD'])
            b = login(base, 'ground-b', env['SEED_USER_2_PASSWORD'])
            token = a['session_token']
            seed_research(env['SQLITE_PATH'], a['user']['id'], b['user']['id'])
            with sqlite3.connect(env['SQLITE_PATH']) as db:
                for old, new in [('ev_a', EA), ('ev_new', EN), ('ev_private', EP)]:
                    db.execute('UPDATE chunks SET id=? WHERE id=?', (new, old))
                    db.execute('UPDATE paper_analyses SET result_json=replace(result_json,?,?)', (old, new))
                row = json.loads(db.execute("SELECT result_json FROM paper_analyses WHERE id='analysis_a'").fetchone()[0])
                row['synthesis']['methods'][0]['text'] = WRONG
                db.execute("UPDATE paper_analyses SET result_json=? WHERE id='analysis_a'", (json.dumps(row),))
            json_call(base, 'PATCH', '/api/v1/library/paper_a', token, {'note': NOTE})
            first_conv = None
            for case in ['success', 'canonical', 'quote_handles', 'reason_handle', 'expanded_prose', 'overlong_prose', 'prose_handle', 'missing_read', 'repair', 'oversized', 'note_as_claim', 'unknown', 'owned_unissued', 'foreign', 'shortened', 'previous_run', 'mismatch', 'wrong_quote_source', 'reason_unissued', 'budget', 'replay', 'cancel', 'note_injection', 'disputed', 'reportwide']:
                if case=='note_injection':
                    json_call(base,'PATCH','/api/v1/library/paper_a',token,{'note':NOTE+' Literal user data: [memory:mem_foreign] '+EP})
                if case=='disputed':
                    with sqlite3.connect(env['SQLITE_PATH']) as db:
                        target,revision=db.execute("SELECT id,revision FROM research_memories WHERE kind='claim' AND facet='method' AND paper_id='paper_a'").fetchone()
                    json_call(base,'PATCH','/api/v1/research-memory/'+target,token,{'expected_revision':revision,'correction':'Synthetic user feedback: recheck the false hundred-year real-user claim against the original short-dialogue source.'})
                if case=='reportwide':
                    with sqlite3.connect(env['SQLITE_PATH']) as db:
                        episode,revision=db.execute("SELECT id,revision FROM research_memories WHERE kind='episode' AND facet='report_a'").fetchone()
                    json_call(base,'PATCH','/api/v1/research-memory/'+episode,token,{'expected_revision':revision,'correction':'Synthetic report-level feedback: verify original evidence.'})
                if case == 'replay':
                    conv = first_conv
                else:
                    conv = json_call(base, 'POST', '/api/v1/conversations', token, {'title': 'T22 fixture '+case}, 201)['id']
                if case == 'success':
                    first_conv = conv
                admitted = json_call(base, 'POST', '/api/v1/conversations/'+conv+'/runs', token,
                    {'message': 'T22_CASE: '+case+' synthetic new study, verify recalled claims against originals.', 'client_request_id': case}, 202)
                rid = admitted['run_id']
                if case == 'cancel':
                    assert Provider.repair_entered.wait(15), 'repair barrier not reached'
                    json_call(base, 'POST', '/api/v1/runs/'+rid+'/cancel', token)
                    Provider.release.set()
                state = wait_run(base, token, rid, timeout=45)
                if Provider.failed:
                    raise Provider.failed
                expected = 'completed' if case in ('success', 'canonical', 'quote_handles', 'reason_handle', 'expanded_prose', 'prose_handle', 'missing_read', 'repair', 'oversized', 'note_as_claim', 'replay','note_injection','disputed','reportwide') else ('cancelled' if case == 'cancel' else ('partial' if case == 'budget' else 'failed'))
                assert state['status'] == expected, (case, state)
                with sqlite3.connect(env['SQLITE_PATH']) as db:
                    accepted = db.execute("SELECT count(*) FROM run_events WHERE run_id=? AND event_type='answer_accepted'", (rid,)).fetchone()[0]
                    rejected = db.execute("SELECT count(*) FROM run_events WHERE run_id=? AND event_type='answer_rejected'", (rid,)).fetchone()[0]
                    contaminated = db.execute("SELECT count(*) FROM messages WHERE run_id=? AND instr(coalesce(content,''),'REJECTED_DRAFT')>0", (rid,)).fetchone()[0]
                    assert contaminated == 0
                    if case in ('repair', 'mismatch'):
                        assert db.execute("SELECT count(*) FROM run_events WHERE run_id=? AND instr(coalesce(private_payload_json,''),'REJECTED_DRAFT')>0", (rid,)).fetchone()[0] > 0
                if expected == 'completed':
                    assert accepted == 1
                    report = json_call(base, 'GET', '/api/v1/reports/'+state['report_id'], token)
                    with sqlite3.connect(env['SQLITE_PATH']) as db:
                        final_messages=db.execute("SELECT content FROM messages WHERE run_id=? AND role='assistant' AND content IS NOT NULL",(rid,)).fetchall()
                        assert final_messages==[(report['markdown'],)], 'accepted history/report differ or duplicate'
                    assert {EA, EN} == {c['evidence_id'] for c in report['citations']}
                    assert NOTE in report['markdown'] and WRONG in report['markdown'] and 'not_supported' in report['markdown']
                    assert '[unresolved_memory_reference]' not in report['markdown']
                    assert len(report['research_memory']['referenced']) == (4 if case=='reportwide' else (3 if case=='disputed' else 2)), 'accepted answer omitted a mandatory delivered correction'
                    if case=='reportwide':assert 'Synthetic report-level feedback:' in report['markdown']
                    if case=='disputed':assert 'disputed' in report['markdown'] and 'Synthetic user feedback:' in report['markdown']
                    assert not any(x in report['markdown'] for x in (Provider.old_evidence, Provider.old_handle))
                    assert not re.search(r'\b[EMQ][0-9a-f]{12}\b', report['markdown'])
                    if case=='note_injection':
                        assert '[memory:mem_foreign] '+EP in report['markdown']
                        assert all(h['memory_id']!='mem_foreign' for h in report['research_memory']['referenced'])
                else:
                    assert accepted == 0 and not state.get('report_id')
                    assert Provider.counts[case] <= 3, (case, Provider.counts[case])
                results[case] = {'status': state['status'], 'provider_requests': Provider.counts[case], 'rejected_drafts': rejected, 'accepted': accepted}
                print('PASS', case, results[case])
                if case=='note_injection':json_call(base,'PATCH','/api/v1/library/paper_a',token,{'note':NOTE})
            # Same populated database, new dialogue: no unsolicited prior titles
            # may leak through the reference catalog when recall is disabled.
            stop(process)
            env['RESEARCH_MEMORY_ENABLED'] = 'false'
            process, base = start(binary, env)
            token = login(base, 'ground-a', env['SEED_USER_1_PASSWORD'])['session_token']
            conv = json_call(base, 'POST', '/api/v1/conversations', token, {'title':'T22 fixture memory off'}, 201)['id']
            rid = json_call(base, 'POST', '/api/v1/conversations/'+conv+'/runs', token,
                {'message':'T22_CASE: memory_off synthetic control','client_request_id':'memory_off'}, 202)['run_id']
            state = wait_run(base, token, rid)
            if Provider.failed:raise Provider.failed
            assert state['status']=='completed'
            report = json_call(base,'GET','/api/v1/reports/'+state['report_id'],token)
            assert report['research_memory'] is None
            with sqlite3.connect(env['SQLITE_PATH']) as db:
                assert db.execute('SELECT count(*) FROM run_memory_snapshots WHERE run_id=?',(rid,)).fetchone()[0]==0
            results['memory_off']={'status':'completed','provider_requests':Provider.counts['memory_off'],'automatic_catalog_empty':True}
            print('PASS memory_off',results['memory_off'])
        finally:
            stop(process)
            provider.shutdown(); provider.server_close()
    (RUNTIME/'result.json').write_text(json.dumps(results, indent=2))
    print('PASS production grounded contract; real HTTP/SQLite/Qwen3, synthetic source/provider fixtures; temporary state cleaned')


if __name__ == '__main__':
    main()
