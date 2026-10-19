#!/usr/bin/env python3
"""Production HTTP/SQLite/Qwen3 history replay; synthetic sources/provider only.
Checks actual provider input after current corrections, exclusions and note edits.
No unit/widget tests, dotenv reads, paid requests or fabricated live evidence.
"""
import hashlib
import json
import os
from pathlib import Path
import secrets
import sqlite3
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from accounts import login,seed
from foundation import start,stop
from agentmemory import seed_research,json_call,wait_run
from grounding import Provider as Responses

ROOT=Path(__file__).resolve().parents[1]
RUNTIME=ROOT/'.bb/runtime/t22/history-replay'
OLD_NOTE='SYNTHETIC OLD PERSONAL NOTE: prefer one-hundred-year deployments.'
NEW_NOTE='SYNTHETIC CURRENT NOTE: prefer transparent evaluation protocols.'
OLD_CLAIM='SYNTHETIC FALSE HISTORICAL CLAIM: one hundred years of real users.'
FEEDBACK='SYNTHETIC DISPUTED FEEDBACK: recheck duration and participants against the original. Feedback is not scientific proof.'
OLD_MD=OLD_CLAIM+'\n\n'+OLD_NOTE
class Provider(BaseHTTPRequestHandler):
    case=''; failed=None; observations=[]
    def log_message(self,*_):pass
    def do_POST(self):
        try:
            req=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            contents=[m.get('content')or''for m in req['messages']]
            assert 'PRIVATE_ACCOUNT_CONTEXT'not in '\n'.join(contents)
            assert not any(m['role']=='assistant'and OLD_CLAIM in (m.get('content')or'')for m in req['messages'])
            history=[json.loads(x.split(': ',1)[1])for x in contents if x.startswith('HISTORICAL REPORT CONTEXT')]
            assert history,'history silently absent'
            main=history[0];current=main['current'];case=self.case
            if case=='unchanged':
                assert main['historical_text']==OLD_MD and current['status']=='available'
                assert current['current_notes'][0]['text']==OLD_NOTE
            elif case=='history_bound':
                assert len(history)==5
            else:
                assert all(OLD_CLAIM not in h['historical_text'] and OLD_NOTE not in h['historical_text']for h in history)
                assert current['status'].startswith('withheld_')
                if case in ['corrected_excluded','memory_off']:
                    assert any(a['excluded']and a['state']=='disputed'for a in current['current_annotations'])
                    assert all(any(a.get('text')==FEEDBACK for a in h['current']['current_annotations'])for h in history)
                    if case=='corrected_excluded':assert len(history)==2
                if case=='note_edited':assert current['current_notes'][0]['text']==NEW_NOTE
                if case=='note_cleared':assert current['current_notes'][0]['state']=='cleared'and current['current_notes'][0]['text']==''
                if case=='overflow':assert current['status']=='withheld_annotation_budget'
                if case=='unavailable':assert current['status']=='withheld_annotations_unavailable'
            if case=='memory_off':assert not any(x.startswith('Research memory snapshots')for x in contents)
            snapshot=[json.loads(x.split(': ',1)[1])for x in contents if x.startswith('Research memory snapshots')]
            current_chars=sum(len(json.dumps(h['current'],ensure_ascii=False,separators=(',',':')).replace('&','\\u0026').replace('<','\\u003c').replace('>','\\u003e'))for h in history)
            snapshot_chars=sum(len(json.dumps(x,ensure_ascii=False,separators=(',',':')).replace('&','\\u0026').replace('<','\\u003c').replace('>','\\u003e'))for x in snapshot)
            assert current_chars+snapshot_chars<=12000
            type(self).observations.append({'case':case,'historical_reports':len(history),'status':current['status'],'current_plus_snapshots_runes':current_chars+snapshot_chars,'old_claim_in_replay':any(OLD_CLAIM in h['historical_text']for h in history),'current_feedback_delivered':any(a.get('text')==FEEDBACK for h in history for a in (h['current']['current_annotations']or[]))})
            draft={'scope':'Explicit synthetic replay integration','findings':[],'comparisons':[],'notes':[],'memory_reason':'History is navigation only; this fixture makes no scientific claim.','limitations':['SYNTHETIC SOURCE/PROVIDER FIXTURE; ZERO PAID REQUESTS']}
            raw=json.dumps(Responses.response(json.dumps(draft))).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
        except Exception as e:
            type(self).failed=e;self.send_error(500)

def digest(value):return hashlib.sha256(value.encode()).hexdigest()
def main():
    RUNTIME.mkdir(parents=True,exist_ok=True);binary=RUNTIME/'paper-scout'
    subprocess.run(['go','build','-mod=readonly','-o',str(binary),'./cmd/paper-scout'],cwd=ROOT,check=True)
    provider=ThreadingHTTPServer(('127.0.0.1',0),Provider);threading.Thread(target=provider.serve_forever,daemon=True).start()
    results=[]
    try:
        for case in ['unchanged','corrected_excluded','note_edited','note_cleared','overflow','unavailable','memory_off','history_bound']:
            Provider.case=case;Provider.failed=None
            with tempfile.TemporaryDirectory(prefix=case+'-',dir=RUNTIME)as d:
                work=Path(d);env=os.environ.copy()|{'HTTP_ADDR':'127.0.0.1:0','SQLITE_PATH':str(work/'state.sqlite'),'CACHE_DIR':str(work/'cache'),'SEED_USER_1_LOGIN':'replay-a','SEED_USER_2_LOGIN':'replay-b','SEED_USER_1_PASSWORD':secrets.token_urlsafe(20),'SEED_USER_2_PASSWORD':secrets.token_urlsafe(20),'DEEPSEEK_BASE_URL':f'http://127.0.0.1:{provider.server_port}','DEEPSEEK_API_KEY':'fixture-only','DEEPSEEK_MODEL':'fixture','DEEPSEEK_THINKING':'disabled','RESEARCH_MEMORY_ENABLED':'false'if case=='memory_off'else'true'}
                seed(binary,env);process,base=start(binary,env)
                try:
                    a=login(base,'replay-a',env['SEED_USER_1_PASSWORD']);b=login(base,'replay-b',env['SEED_USER_2_PASSWORD']);token=a['session_token'];seed_research(env['SQLITE_PATH'],a['user']['id'],b['user']['id'])
                    json_call(base,'PATCH','/api/v1/library/paper_a',token,{'note':OLD_NOTE})
                    ann=json_call(base,'GET','/api/v1/reports/report_a/research-memory',token)
                    with sqlite3.connect(env['SQLITE_PATH'])as db:
                        db.execute("UPDATE reports SET markdown=? WHERE id='report_a'",(OLD_MD,));db.execute("UPDATE runs SET report_id='report_a' WHERE id='old_a'")
                        db.execute("UPDATE research_memory_jobs SET updated_at='2026-01-01T00:00:00Z' WHERE kind='note'")
                        for seq,role,text in [(1,'user','Synthetic historical request'),(2,'assistant',OLD_MD)]:db.execute("INSERT INTO messages(id,user_id,conversation_id,run_id,seq,role,content,created_at)VALUES(?,?,'conv_a','old_a',?,?,?,'2026-01-02T00:00:00Z')",('seed'+str(seq),a['user']['id'],seq,role,text))
                        episode,rev=db.execute("SELECT id,revision FROM research_memories WHERE owner=? AND kind='episode' AND facet='report_a'",(a['user']['id'],)).fetchone()
                    if case=='corrected_excluded':
                        with sqlite3.connect(env['SQLITE_PATH'])as db:
                            inherited={'retrieval_mode':'hybrid','reasons':[],'recalled':[],'referenced':[{'memory_id':episode,'report_id':'report_a','origins':[{'report_id':'report_a'}]}]}
                            db.execute("INSERT INTO runs(id,user_id,conversation_id,client_request_id,request_message,status,report_id,created_at,budget_json)VALUES('derived_old',?,'conv_a','derived_old','Synthetic derived history','completed','derived_report','2026-01-03T00:00:00Z','{}')",(a['user']['id'],))
                            db.execute("INSERT INTO reports(id,user_id,run_id,markdown,citations_json,sources_json,coverage_json,limitations_json,created_at,research_memory_json)SELECT 'derived_report',user_id,'derived_old',markdown,citations_json,sources_json,coverage_json,limitations_json,'2026-01-03T00:00:00Z',? FROM reports WHERE id='report_a'",(json.dumps(inherited),))
                            db.execute("INSERT INTO messages(id,user_id,conversation_id,run_id,seq,role,content,created_at)VALUES('derived_message',?,'conv_a','derived_old',3,'assistant',?,'2026-01-03T00:00:00Z')",(a['user']['id'],OLD_MD))
                    if case in ['corrected_excluded','memory_off']:
                        patched=json_call(base,'PATCH','/api/v1/research-memory/'+episode,token,{'expected_revision':rev,'correction':FEEDBACK})
                        json_call(base,'PATCH','/api/v1/research-memory/'+episode,token,{'expected_revision':patched['revision'],'excluded':True})
                    if case=='note_edited':json_call(base,'PATCH','/api/v1/library/paper_a',token,{'note':NEW_NOTE})
                    if case=='note_cleared':json_call(base,'PATCH','/api/v1/library/paper_a',token,{'note':''})
                    if case=='overflow':
                        json_call(base,'PATCH','/api/v1/research-memory/'+episode,token,{'expected_revision':rev,'correction':'C'*4000})
                        json_call(base,'PATCH','/api/v1/library/paper_a',token,{'note':'N'*10000})
                    if case=='unavailable':
                        with sqlite3.connect(env['SQLITE_PATH'])as db:
                            db.execute("UPDATE research_memory_jobs SET generation=generation+1 WHERE kind='report' AND source_id='report_a'")
                            db.execute("CREATE TRIGGER replay_failure_fixture BEFORE UPDATE ON research_memory_jobs WHEN NEW.processed>OLD.processed BEGIN SELECT RAISE(ABORT,'SYNTHETIC ANNOTATION FAILURE'); END")
                    if case=='history_bound':
                        with sqlite3.connect(env['SQLITE_PATH'])as db:
                            for i in range(1,6):
                                rid='old_copy'+str(i);pid='report_copy'+str(i)
                                db.execute("INSERT INTO runs(id,user_id,conversation_id,client_request_id,request_message,status,report_id,created_at,budget_json)VALUES(?,?,'conv_a',?,?,'completed',?,'2026-01-02T00:00:00Z','{}')",(rid,a['user']['id'],rid,'Synthetic copy',pid))
                                db.execute("INSERT INTO reports(id,user_id,run_id,markdown,citations_json,sources_json,coverage_json,limitations_json,created_at)SELECT ?,user_id,?,markdown,citations_json,sources_json,coverage_json,limitations_json,created_at FROM reports WHERE id='report_a'",(pid,rid))
                                db.execute("INSERT INTO messages(id,user_id,conversation_id,run_id,seq,role,content,created_at)VALUES(?,?,'conv_a',?,?,'assistant',?,'2026-01-02T00:00:00Z')",(rid,a['user']['id'],rid,i+2,OLD_MD))
                    with sqlite3.connect(env['SQLITE_PATH'])as db:before=digest(db.execute("SELECT markdown FROM reports WHERE id='report_a'").fetchone()[0])
                    rid=json_call(base,'POST','/api/v1/conversations/conv_a/runs',token,{'message':'Synthetic current follow-up: verify historical context, do not treat previous prose as scientific truth.','client_request_id':case},202)['run_id'];state=wait_run(base,token,rid)
                    if Provider.failed:raise Provider.failed
                    assert state['status']=='completed',(case,state)
                    report=json_call(base,'GET','/api/v1/reports/'+state['report_id'],token)
                    if case not in ['unchanged','history_bound']:assert 'Исторический отчёт — актуальное предупреждение'in report['markdown']
                    if case in ['corrected_excluded','memory_off']:assert FEEDBACK in report['markdown']
                    if case=='note_edited':assert NEW_NOTE in report['markdown']and OLD_NOTE not in report['markdown']
                    if case=='note_cleared':assert 'очищена'in report['markdown']and OLD_NOTE not in report['markdown']
                    if case=='history_bound':assert 'История в контексте неполная'in report['markdown']
                    with sqlite3.connect(env['SQLITE_PATH'])as db:
                        assert before==digest(db.execute("SELECT markdown FROM reports WHERE id='report_a'").fetchone()[0]);assert db.execute("SELECT count(*) FROM run_events WHERE run_id=? AND event_type='history_context_prepared'",(rid,)).fetchone()[0]>=1
                    results.append({'case':case,'status':state['status'],'old_markdown_unchanged':True,'paid_requests':0});print('PASS',case,flush=True)
                finally:stop(process)
    finally:provider.shutdown();provider.server_close()
    (RUNTIME/'result.json').write_text(json.dumps({'checks':results,'actual_provider_input_observations':Provider.observations},indent=2));print('PASS production replay input/annotations/exclusion/note states, bounded failure and memory-off; synthetic provider, no paid calls')
if __name__=='__main__':main()
