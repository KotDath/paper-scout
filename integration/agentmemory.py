#!/usr/bin/env python3
"""T20 production-process integration. Synthetic scientific origins are labeled.
Real SQLite/auth/agent/tools/Qwen3. HTTP DeepSeek fixtures prove boundary behavior;
--live also runs a bounded real DeepSeek pass (no synthetic result called science).
Never reads dotenv: only cmd/paper-scout loads the optional shared config path.
"""
import argparse
import json
import os
from pathlib import Path
import re
import secrets
import sqlite3
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from accounts import login, request, seed
from foundation import start, stop

ROOT = Path(__file__).resolve().parents[1]
RUNTIME = ROOT / '.bb/runtime/t20'
NOTE = 'Personal criterion: prioritize evaluations in long dialogues.'
SOURCE_A = 'Synthetic A: episodic memory consolidation uses recurrent review and forgetting. The evaluation covers short dialogues only.'
SOURCE_B = 'Synthetic B: episodic memory consolidation uses a fixed retrieval window. The evaluation covers long dialogues, with no forgetting experiment.'


def seed_research(path, owner, private_owner):
    stamp = '2026-01-02T00:00:00Z'
    with sqlite3.connect(path) as db:
        db.execute('PRAGMA foreign_keys=ON')
        for key, user, source in [('a', owner, SOURCE_A), ('new', owner, SOURCE_B), ('private', private_owner, 'PRIVATE_ACCOUNT_CONTEXT episodic memory consolidation')]:
            paper, doc, ev = 'paper_'+key, 'doc_'+key, 'ev_'+key
            db.execute("INSERT INTO paper_versions VALUES(?,?,1,?,'[]',?,'https://arxiv.org/abs/2305.10250','https://arxiv.org/html/2305.10250v1','https://arxiv.org/pdf/2305.10250v1',?,?,?)", (paper,'synthetic-'+key,'Synthetic paper '+key,source,stamp,stamp,stamp))
            db.execute("INSERT INTO documents VALUES(?,?,'html',?,'t05-poppler-html-v1','runes-2000-overlap-250-v1','full_text','https://arxiv.org/html/2305.10250v1','',?,?,'[]',?)", (doc,paper,key*64,source,json.dumps([{'char_start':0,'char_end':len(source),'section':'Synthetic methods/results'}]),stamp))
            db.execute("INSERT INTO chunks(id,document_id,ordinal,text,char_start,char_end,section) VALUES(?,?,0,?,0,?,'Synthetic methods/results')",(ev,doc,source,len(source)))
            db.execute('INSERT INTO user_papers(user_id,paper_version_id,added_at) VALUES(?,?,?)',(user,paper,stamp))
            if key == 'new':
                continue
            conv, run, report, analysis = 'conv_'+key,'old_'+key,'report_'+key,'analysis_'+key
            db.execute('INSERT INTO conversations VALUES(?,?,?,?)',(conv,user,'Synthetic research '+key,stamp))
            db.execute("INSERT INTO runs(id,user_id,conversation_id,client_request_id,request_message,status,created_at,budget_json) VALUES(?,?,?,?,?,'completed',?,'{}')",(run,user,conv,key,'Study episodic memory consolidation',stamp))
            db.execute("INSERT INTO run_sources VALUES(?,?,'Synthetic origin','complete')",(run,paper))
            observations = {'methods':[{'text':source,'evidence_ids':[ev]}], 'results':[], 'limitations':[]}
            result = {'analysis_status':'complete','extraction_status':'full_text','total_runes':len(source),'covered_runes':len(source),'successful_ranges':[{'char_start':0,'char_end':len(source)}],'synthesis':observations}
            db.execute("INSERT INTO paper_analyses(id,run_id,document_id,question,status,result_json,created_at,finished_at) VALUES(?,?,?,'Synthetic','complete',?,?,?)",(analysis,run,doc,json.dumps(result),stamp,stamp))
            sources = [{'paper':{'id':paper,'title':'Synthetic paper '+key},'analysis_status':'complete','coverage':[]}]
            db.execute("INSERT INTO reports(id,user_id,run_id,markdown,citations_json,sources_json,coverage_json,limitations_json,created_at) VALUES(?,?,?,?,'[]',?,'[]','[]',?)",(report,user,run,'Synthetic report: '+source,json.dumps(sources),stamp))
        for memory_id,user in [('mem_unissued',owner),('mem_foreign',private_owner)]:
            db.execute("INSERT INTO research_memories(id,owner,kind,title,text,content_hash,created_at) VALUES(?,?,'episode','Unissued fixture','Unissued fixture','fixture',?)",(memory_id,user,stamp))


def json_call(base, method, path, token, body=None, code=200):
    status, out, _ = request(base, method, path, token, body)
    assert status == code, (path,status,out)
    return out


def create_run(base, token, label, message):
    conv=json_call(base,'POST','/api/v1/conversations',token,{'title':label},201)
    out=json_call(base,'POST',f"/api/v1/conversations/{conv['id']}/runs",token,{'message':message,'client_request_id':label},202)
    return out['run_id']


def wait_run(base, token, run, timeout=90):
    deadline=time.monotonic()+timeout
    while time.monotonic()<deadline:
        state=json_call(base,'GET','/api/v1/runs/'+run,token)
        if state['status'] not in ('queued','running'):
            return state
        time.sleep(.05)
    raise AssertionError('run deadline exceeded')


class Provider(BaseHTTPRequestHandler):
    calls=[]
    snapshots=[]
    failure=None
    block=None
    block_ready=None
    path_db=None

    def log_message(self,*_): pass

    def do_POST(self):
        try:
            req=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            type(self).calls.append(req)
            messages=req['messages']
            if any(m.get('role')=='user' and 'T20_CASE: NO_MEMORY' in (m.get('content') or '') for m in messages):
                assert not any((m.get('content') or '').startswith('Research memory snapshots') for m in messages)
                raw=json.dumps(self.response('Explicit control: no automatic research memory supplied.')).encode()
                self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
                return
            # Actual Analyzer's nested request, with tools disabled.
            if not req.get('tools'):
                answer=json.dumps({'methods':[{'text':SOURCE_B,'evidence_ids':['ev_new']}], 'results':[], 'limitations':[]})
                payload=self.response(answer)
            else:
                snaps=[m['content'].split(': ',1)[1] for m in messages if (m.get('content') or '').startswith('Research memory snapshots')]
                assert len(snaps)==1,'automatic memory missing before provider'
                snapshots=json.loads(snaps[0]); type(self).snapshots.append(snapshots)
                assert len(json.dumps(snapshots,ensure_ascii=False,separators=(',',':')))<=12000
                tools=[m for m in messages if m['role']=='tool']
                label=next(m['content'] for m in messages if m['role']=='user' and (m.get('content') or '').startswith('T20_CASE:'))
                if 'EMPTY' not in label:
                    assert 'PRIVATE_ACCOUNT_CONTEXT' not in snaps[0]
                with sqlite3.connect(self.path_db) as db:
                    run=db.execute("SELECT id FROM runs WHERE request_message=? AND status='running'",(label,)).fetchone()[0]
                    assert db.execute('SELECT count(*) FROM run_memory_snapshots WHERE run_id=?',(run,)).fetchone()[0]==len(snapshots)
                    db.execute("INSERT OR IGNORE INTO run_sources VALUES(?,'paper_new','Synthetic boundary fixture','none')",(run,))
                    if 'WRITE_FAULT' in label:
                        db.execute("CREATE TRIGGER t20_injected_capture_failure BEFORE INSERT ON research_memories BEGIN SELECT RAISE(ABORT,'injected capture failure'); END")
                if 'CANCEL' in label:
                    if self.block_ready: self.block_ready.set()
                    if self.block: self.block.wait(10)
                    payload=self.response('Cancelled fixture response')
                elif 'SIMPLE' in label or 'EMPTY' in label or 'BUDGET' in label:
                    payload=self.response('Memory unavailable or unrelated; no scientific comparison asserted.')
                elif not tools:
                    assert len(snapshots)==1 and snapshots[0]['point']=='question'
                    assert NOTE in snaps[0]
                    payload=self.tool('analyze_paper',{'paper_version_id':'paper_new','question':'Compare synthetic consolidation methods and evaluation limits.'},'analyze1')
                elif len(tools)==1:
                    assert len(snapshots)==2 and snapshots[1]['point']=='content'
                    assert 'fixed retrieval window' in snapshots[1]['query']
                    payload=self.tool('read_passages',{'evidence_ids':['ev_a','ev_new']},'read')
                elif len(tools)==2:
                    payload=self.tool('analyze_paper',{'paper_version_id':'paper_new','question':'Review synthetic limitations.'},'analyze2')
                else:
                    assert len(snapshots)==2,'more than two automatic recall points'
                    hits=[h for s in snapshots for h in s['hits']]
                    claim=next(h['memory_id'] for h in hits if h['kind']=='claim' and h.get('paper_version_id')=='paper_a')
                    note=next(h['memory_id'] for h in hits if h['kind']=='user_note')
                    payload=self.response(f'Synthetic comparison: A uses recurrent review, B a fixed window [memory:{claim}] ev_a ev_new. User preference: {NOTE} [memory:{note}]. Invalid fixtures [memory:mem_unissued] [memory:mem_unknown] [memory:mem_foreign].')
            raw=json.dumps(payload).encode()
            self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
        except (BrokenPipeError,ConnectionResetError):
            pass
        except BaseException as exc:
            type(self).failure=exc
            self.send_response(500);self.end_headers()

    @staticmethod
    def response(text):
        return {'id':'t20-fixture','model':'fixture','choices':[{'index':0,'finish_reason':'stop','message':{'role':'assistant','content':text}}], 'usage':{'prompt_tokens':40,'completion_tokens':40,'total_tokens':80}}

    @classmethod
    def tool(cls,name,args,identifier):
        out=cls.response(None);out['choices'][0]['finish_reason']='tool_calls'
        out['choices'][0]['message']['tool_calls']=[{'id':identifier,'type':'function','function':{'name':name,'arguments':json.dumps(args)}}]
        return out


def main():
    parser=argparse.ArgumentParser();parser.add_argument('--live',action='store_true');parser.add_argument('--env-file',default='');args=parser.parse_args()
    RUNTIME.mkdir(parents=True,exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='agentmemory-',dir=RUNTIME) as temp:
        work=Path(temp);binary=work/'paper-scout'
        subprocess.run(['go','build','-o',str(binary),'./cmd/paper-scout'],cwd=ROOT,check=True)
        provider=ThreadingHTTPServer(('127.0.0.1',0),Provider)
        threading.Thread(target=provider.serve_forever,daemon=True).start()
        env=os.environ.copy()|{'HTTP_ADDR':'127.0.0.1:0','SQLITE_PATH':str(work/'memory.sqlite'),'CACHE_DIR':str(work/'cache'), 'SEED_USER_1_LOGIN':'memory-a','SEED_USER_2_LOGIN':'memory-b','SEED_USER_1_PASSWORD':secrets.token_urlsafe(24),'SEED_USER_2_PASSWORD':secrets.token_urlsafe(24), 'DEEPSEEK_BASE_URL':f'http://127.0.0.1:{provider.server_port}','DEEPSEEK_MODEL':'fixture','DEEPSEEK_API_KEY':'fixture-only','DEEPSEEK_THINKING':'disabled'}
        Provider.path_db=env['SQLITE_PATH']
        seed(binary,env)
        process,base=start(binary,env)
        try:
            a=login(base,'memory-a',env['SEED_USER_1_PASSWORD']);b=login(base,'memory-b',env['SEED_USER_2_PASSWORD'])
            token=a['session_token'];other=b['session_token']
            seed_research(env['SQLITE_PATH'],a['user']['id'],b['user']['id'])
            assert json_call(base,'GET','/api/v1/reports/report_a',token)['research_memory'] is None
            json_call(base,'PATCH','/api/v1/library/paper_a',token,{'note':NOTE})
            # Leave report capture pending, then kill/restart the process normally.
            with sqlite3.connect(env['SQLITE_PATH']) as db:
                assert db.execute("SELECT processed FROM research_memory_jobs WHERE source_id='report_a'").fetchone()[0]==0
        finally: stop(process)
        process,base=start(binary,env)
        try:
            run=create_run(base,token,'new-dialogue','T20_CASE: Compare episodic memory consolidation in a new study.')
            state=wait_run(base,token,run)
            if Provider.failure: raise Provider.failure
            assert state['status']=='completed',state
            report=json_call(base,'GET','/api/v1/reports/'+state['report_id'],token)
            memory=report['research_memory']
            assert memory['retrieval_mode']=='hybrid',memory['reasons']
            assert len(memory['referenced'])==2
            assert {h['kind'] for h in memory['referenced']}=={'claim','user_note'}
            assert all(x not in report['markdown'] for x in ['[memory:mem_unknown]','[memory:mem_unissued]','[memory:mem_foreign]'])
            assert report['markdown'].count('[unresolved_memory_reference]')==3
            assert 'unresolved_memory_reference' in report['limitations']
            assert {'ev_a','ev_new'}=={h['evidence_id'] for h in report['citations']}
            events=json_call(base,'GET',f'/api/v1/runs/{run}/events?limit=100',token)['items']
            recalled=[e for e in events if e['event_type']=='memory_recalled']
            assert len(recalled)==2
            assert next(e['seq'] for e in events if e['event_type']=='model_request_started')>recalled[0]['seq']
            with sqlite3.connect(env['SQLITE_PATH']) as db:
                snapshots=[json.loads(row[0]) for row in db.execute('SELECT snapshot_json FROM run_memory_snapshots WHERE run_id=? ORDER BY ordinal',(run,))]
                assert snapshots==Provider.snapshots[-1]
                assert db.execute("SELECT processed FROM research_memory_jobs WHERE source_id='report_a'").fetchone()[0]==1
                assert db.execute('SELECT processed FROM research_memory_jobs WHERE source_id=?',(state['report_id'],)).fetchone()[0]==1
            assert json_call(base,'GET','/api/v1/reports/'+state['report_id'],other,code=404)['error']['code']=='not_found'
            print('PASS production first-request automatic hybrid recall after restart; missed capture repaired without replay; content recall exactly once after two real Analyzer calls; original passages A/B; validated recalled/referenced links; post-commit capture')
            old_report=report;old_path='/api/v1/reports/'+state['report_id'];old_count=len(Provider.calls)
            # Corrections/edits change future prompts, never historical snapshots.
            note=next(h for h in memory['referenced'] if h['kind']=='user_note')
            claim=next(h for h in memory['referenced'] if h['kind']=='claim')
            correction='Synthetic correction: recheck the short-dialogue condition in A.'
            json_call(base,'PATCH','/api/v1/research-memory/'+claim['memory_id'],token,{'expected_revision':claim['revision'],'correction':correction})
            json_call(base,'PATCH','/api/v1/library/paper_a',token,{'note':'Updated personal preference: check reproducibility.'})
        finally: stop(process)
        process,base=start(binary,env)
        try:
            assert json_call(base,'GET',old_path,token)==old_report
            assert len(Provider.calls)==old_count,'restart replayed model'
            run=create_run(base,token,'edited','T20_CASE: SIMPLE episodic memory consolidation')
            state=wait_run(base,token,run);assert state['status']=='completed',state
            snap=Provider.snapshots[-1];wire=json.dumps(snap)
            assert NOTE not in wire and 'Updated personal preference' in wire and correction in wire
            assert any(h['state']=='disputed' for s in snap for h in s['hits'])
            json_call(base,'PATCH','/api/v1/library/paper_a',token,{'note':''})
            run=create_run(base,token,'cleared','T20_CASE: SIMPLE episodic memory consolidation')
            assert wait_run(base,token,run)['status']=='completed'
            assert 'Updated personal preference' not in json.dumps(Provider.snapshots[-1])
            # The second account gets only its own prior material.
            run=create_run(base,other,'isolation','T20_CASE: EMPTY episodic memory consolidation')
            # This account has a private fixture report; the fixture endpoint's
            # strict PRIVATE assertion intentionally applies only to owner A.
            state=wait_run(base,other,run)
            assert state['status']=='completed',state
            private=json_call(base,'GET','/api/v1/reports/'+state['report_id'],other)['research_memory']
            assert all(h.get('paper_version_id')!='paper_a' for h in private['recalled'])
            # Oversized mandatory annotation drops its whole scientific group.
            json_call(base,'PATCH','/api/v1/library/paper_a',token,{'note':'Long personal criterion. '*375})
            run=create_run(base,token,'budget','T20_CASE: BUDGET episodic memory consolidation')
            assert wait_run(base,token,run)['status']=='completed'
            snapshot=Provider.snapshots[-1][0]
            assert 'memory_prompt_budget' in snapshot['reasons']
            assert not any(h.get('paper_version_id')=='paper_a' for h in snapshot['hits'])
            json_call(base,'PATCH','/api/v1/library/paper_a',token,{'note':''})
            # Cancellation retains audit snapshot, never a report/partial commit.
            Provider.block=threading.Event()
            Provider.block_ready=threading.Event()
            before=len(Provider.calls)
            run=create_run(base,token,'cancel','T20_CASE: CANCEL episodic memory consolidation')
            deadline=time.monotonic()+20
            while len(Provider.calls)==before and time.monotonic()<deadline: time.sleep(.02)
            assert len(Provider.calls)>before
            assert Provider.block_ready.wait(5), 'cancel provider did not reach read barrier'
            json_call(base,'POST','/api/v1/runs/'+run+'/cancel',token,code=200)
            Provider.block.set()
            state=wait_run(base,token,run)
            assert state['status']=='cancelled' and state['report_id'] is None
            with sqlite3.connect(env['SQLITE_PATH']) as db:
                assert db.execute('SELECT count(*) FROM run_memory_snapshots WHERE run_id=?',(run,)).fetchone()[0]==1
                vector_sql=db.execute("SELECT sql FROM sqlite_master WHERE name='research_memory_vectors'").fetchone()[0]
                db.execute('DROP TABLE research_memory_vectors')
            # A concrete SQL read failure is unavailable, never empty/hybrid history.
            run=create_run(base,token,'unavailable','T20_CASE: SIMPLE unavailable memory')
            state=wait_run(base,token,run);assert state['status']=='completed'
            degraded=json_call(base,'GET','/api/v1/reports/'+state['report_id'],token)
            assert degraded['research_memory']['retrieval_mode']=='unavailable'
            assert 'memory_unavailable' in degraded['research_memory']['reasons']
            assert 'research_memory_unavailable' in degraded['limitations']
            with sqlite3.connect(env['SQLITE_PATH']) as db: db.execute(vector_sql)
            run=create_run(base,token,'write-fault','T20_CASE: SIMPLE WRITE_FAULT episodic memory consolidation')
            state=wait_run(base,token,run);assert state['status']=='completed'
            # Wait for the post-terminal diagnostic, not just the report commit.
            deadline=time.monotonic()+5
            while time.monotonic()<deadline:
                events=json_call(base,'GET',f'/api/v1/runs/{run}/events?limit=100',token)['items']
                capture=[e for e in events if e['event_type']=='memory_captured']
                if capture: break
                time.sleep(.02)
            assert capture and capture[0]['payload']['status']=='pending'
            with sqlite3.connect(env['SQLITE_PATH']) as db:
                assert db.execute('SELECT processed FROM research_memory_jobs WHERE source_id=?',(state['report_id'],)).fetchone()[0]==0
                db.execute('DROP TRIGGER t20_injected_capture_failure')
            failed_report=state['report_id'];before=len(Provider.calls)
            run=create_run(base,token,'repair','T20_CASE: SIMPLE episodic memory consolidation')
            assert wait_run(base,token,run)['status']=='completed'
            with sqlite3.connect(env['SQLITE_PATH']) as db:
                assert db.execute('SELECT processed FROM research_memory_jobs WHERE source_id=?',(failed_report,)).fetchone()[0]==1
            assert len(Provider.calls)==before+1,'capture recovery replayed paid research'
            print('PASS capture failure preserves report, durable pending event, next bounded recall repairs capture without paid replay')
            print('PASS prompt metadata budget with indivisible annotations, cancellation without report, explicit memory read failure preserves completed report')
            with sqlite3.connect(env['SQLITE_PATH']) as db:
                episode,revision=db.execute("SELECT id,revision FROM research_memories WHERE owner=? AND kind='episode' AND facet='report_private'",(b['user']['id'],)).fetchone()
            json_call(base,'PATCH','/api/v1/research-memory/'+episode,other,{'expected_revision':revision,'excluded':True})
            run=create_run(base,other,'empty','T20_CASE: EMPTY quantum optics unrelated question')
            state=wait_run(base,other,run);assert state['status']=='completed'
            nohits=json_call(base,'GET','/api/v1/reports/'+state['report_id'],other)['research_memory']
            assert nohits['retrieval_mode']=='hybrid' and nohits['recalled']==[] and nohits['referenced']==[]
            print('PASS empty automatic hybrid result is explicit and does not fabricate comparison')
            print('PASS immutable report/snapshots after OS restart, no paid replay, current note edit/clear and disputed correction in future context, owner isolation')
        finally: stop(process)
        process,base=start(binary,env|{'RESEARCH_MEMORY_ENABLED':'false'})
        try:
            run=create_run(base,token,'memory-off','T20_CASE: NO_MEMORY episodic memory consolidation')
            state=wait_run(base,token,run);assert state['status']=='completed'
            assert json_call(base,'GET','/api/v1/reports/'+state['report_id'],token)['research_memory'] is None
            with sqlite3.connect(env['SQLITE_PATH']) as db:
                assert db.execute('SELECT count(*) FROM run_memory_snapshots WHERE run_id=?',(run,)).fetchone()[0]==0
            print('PASS production RESEARCH_MEMORY_ENABLED=false control has no recall snapshot or injected note')
        finally: stop(process)
        provider.shutdown();provider.server_close()
        if args.live:
            live_env={k:v for k,v in env.items() if not k.startswith('DEEPSEEK_')}
            live_env['DEEPSEEK_THINKING']='disabled'
            live_env['DEEPSEEK_MODEL']=os.environ.get('DEEPSEEK_MODEL','deepseek-flash')
            live_env['SQLITE_PATH']=str(work/'fresh-live.sqlite')
            seed(binary,live_env)
            # Fresh note, before run cutoff; only the production child loads secrets.
            process,base=start(binary,live_env,'-env-file',args.env_file)
            try:
                live_a=login(base,'memory-a',live_env['SEED_USER_1_PASSWORD'])
                live_b=login(base,'memory-b',live_env['SEED_USER_2_PASSWORD'])
                token=live_a['session_token']
                seed_research(live_env['SQLITE_PATH'],live_a['user']['id'],live_b['user']['id'])
                json_call(base,'PATCH','/api/v1/library/paper_a',token,{'note':NOTE})
                run=create_run(base,token,'live','Study synthetic work B, paper_version_id paper_new. Use analyze_paper for this saved source. Compare its memory consolidation mechanism and evaluation with relevant earlier research if available. Open original passages for both; state that these are synthetic integration sources and distinguish any user preference. Finish in Russian in at most eight sentences with exact memory/evidence links and the exact server analysis status; compare the supported methods, dialogue conditions and missing experiment separately. Do not search or import external sources.')
                with sqlite3.connect(live_env['SQLITE_PATH']) as db:
                    db.execute("INSERT OR IGNORE INTO run_sources VALUES(?,'paper_new','Synthetic live verification source','none')",(run,))
                state=wait_run(base,token,run,timeout=600)
                assert state['status']=='completed',state
                report=json_call(base,'GET','/api/v1/reports/'+state['report_id'],token)
                memory=report['research_memory']
                with sqlite3.connect(live_env['SQLITE_PATH']) as db:
                    passages=[]
                    for row in db.execute("SELECT content FROM messages WHERE run_id=? AND role='tool'",(run,)):
                        passages.extend(h['evidence_id'] for h in json.loads(row[0]).get('passages',[]))
                    snapshots=[json.loads(row[0]) for row in db.execute('SELECT snapshot_json FROM run_memory_snapshots WHERE run_id=? ORDER BY ordinal',(run,))]
                artifact=json.dumps({'report':report,'usage':state['usage'],'snapshots':snapshots,'opened_evidence_ids':passages},ensure_ascii=False,indent=2)
                (RUNTIME/('live-result-'+str(time.time_ns())+'.json')).write_text(artifact)
                (RUNTIME/'live-result.json').write_text(artifact)
                assert memory['retrieval_mode']=='hybrid'
                assert any(h['kind']=='user_note' for h in memory['referenced']), 'live model omitted note memory link'
                assert any(h.get('paper_version_id')=='paper_a' for h in memory['referenced']), 'live model omitted earlier claim memory link'
                assert {'ev_a','ev_new'}<={h['evidence_id'] for h in report['citations']}
                assert {'ev_a','ev_new'}<=set(passages), 'live comparison did not open both originals'
                assert len(snapshots)==2
                assert not any(h.get('paper_version_id')=='paper_new' for s in snapshots for h in s['hits']), 'B was already in old research memory'
                assert state['usage']['deepseek_requests']<=12,'live smoke exceeded expected bounded requests'
                print('PASS LIVE production DeepSeek + real Qwen3 automatic recall, note and earlier claim referenced, A/B evidence citations; SYNTHETIC scientific origins; sanitized runtime result saved')
            finally: stop(process)


if __name__=='__main__': main()
