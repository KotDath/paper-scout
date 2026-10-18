// Integration executable: production Dart API repository -> HTTP -> actual Go
// harness, tools, documents, SQLite and real Qwen3. External boundaries are
// explicitly fixtures; this executable is not evidence of live research/UI.
import 'dart:io';
import '../client/lib/api/api_client.dart';
import '../client/lib/research/research_api.dart';

void ensure(bool value, String message) {
  if (!value) throw StateError(message);
}

Future<Json> poll(ResearchApi api, String id) async {
  final deadline = DateTime.now().add(const Duration(minutes: 3));
  while (DateTime.now().isBefore(deadline)) {
    final run = await api.run(id);
    if (!activeRun(run)) return run;
    await Future<void>.delayed(const Duration(milliseconds: 50));
  }
  throw StateError('run polling deadline');
}

Future<void> absent(Future<Json> operation) async {
  try {
    await operation;
    throw StateError('foreign data was accessible');
  } on ApiException catch (e) {
    ensure(e.status == 404, 'foreign/missing resources must return 404');
  }
}

Future<void> main() async {
  final a = ApiClient(Platform.environment['SCOUT_BASE']!);
  final b = ApiClient(Platform.environment['SCOUT_BASE']!);
  final api = ResearchApi(a), other = ResearchApi(b);
  try {
    for (final entry in [(a, 'tools-alice'), (b, 'tools-bob')]) {
      final session = await entry.$1.requestJson(
        'POST',
        '/api/v1/auth/login',
        authenticated: false,
        body: {'login': entry.$2, 'password': 'api-integration-password'},
      );
      entry.$1.setToken(session['session_token'] as String);
    }
    final conversation = await api.createConversation(
      'T14 Dart research integration',
    );
    final id = conversation['id'] as String;
    final key = requestId();
    final primary = await api.submit(
      id,
      'T11_PRIMARY: integration research',
      key,
    );
    final same = await api.submit(id, 'T11_PRIMARY: integration research', key);
    ensure(primary['run_id'] == same['run_id'], 'admission must be idempotent');
    final run = await poll(api, primary['run_id'] as String);
    ensure(
      run['status'] == 'completed' && run['report_id'] != null,
      'missing completed report',
    );
    final report = await api.report(run['report_id'] as String);
    final source = rows(report['sources']).single;
    final coverage = rows(source['coverage']).single;
    ensure(
      source['analysis_status'] == 'complete' &&
          coverage['covered_runes'] == coverage['total_runes'],
      'coverage mismatch',
    );
    final citation = rows(report['citations']).first;
    final evidenceId = citation['evidence_id'] as String;
    final paperId = (source['paper'] as Json)['id'] as String;
    final evidence = await api.evidence(evidenceId);
    ensure(
      (evidence['text'] as String).isNotEmpty && coordinatesPresent(evidence),
      'evidence lacks source coordinates',
    );
    final patched = await api.updatePaper(paperId, {
      'saved': true,
      'read': true,
      'note': 'T14 сохранённая заметка',
    });
    ensure(
      patched['saved'] == true && patched['read'] == true,
      'library marks not saved',
    );
    ensure(
      (await api.paper(paperId))['note'] == 'T14 сохранённая заметка',
      'note did not persist',
    );
    ensure(
      rows((await api.collection('library'))['items']).single['id'] == paperId,
      'library missing imported paper',
    );
    final follow = await api.submit(
      id,
      'T11_FOLLOWUP: integration follow-up',
      requestId(),
    );
    final followRun = await poll(api, follow['run_id'] as String);
    ensure(followRun['report_id'] != null, 'follow-up missing report');
    final events = <Json>[];
    var seq = 0;
    Json page;
    do {
      page = await api.events(followRun['id'] as String, seq);
      for (final event in rows(page['items'])) {
        ensure((event['seq'] as int) > seq, 'event order');
        seq = event['seq'] as int;
        events.add(event);
      }
      ensure(page['next_after_seq'] == seq, 'event cursor');
    } while (page['has_more'] == true);
    final retrieval =
        events.singleWhere(
              (e) => e['event_type'] == 'retrieval_completed',
            )['payload']
            as Json;
    ensure(
      retrieval['retrieval_mode'] == 'hybrid' &&
          (retrieval['lexical_candidates'] as int) > 0 &&
          (retrieval['dense_candidates'] as int) > 0,
      'follow-up not hybrid',
    );
    final result = await api.search('memory consolidation');
    ensure(
      result['retrieval_mode'] == 'hybrid' && rows(result['hits']).isNotEmpty,
      'library search not hybrid',
    );
    ensure(
      rows((await api.conversation(id))['messages']).length >= 4,
      'public conversation history missing',
    );
    await api.updateProfile('T14 memory research');
    ensure(
      (await api.profile())['interests'] == 'T14 memory research',
      'profile not saved',
    );
    ensure((await other.profile())['interests'] == '', 'profile leaked');
    ensure(
      rows((await other.collection('conversations'))['items']).isEmpty &&
          rows((await other.collection('library'))['items']).isEmpty,
      'foreign collections leaked',
    );
    await absent(other.conversation(id));
    await absent(other.run(run['id'] as String));
    await absent(other.events(run['id'] as String, 0));
    await absent(other.report(report['id'] as String));
    await absent(other.paper(paperId));
    await absent(other.evidence(evidenceId));
    await absent(other.updatePaper(paperId, {'note': 'foreign'}));
    print(
      'PASS: production Dart research repository; real Go HTTP/SQLite/tools/Qwen3; idempotent admission, report, complete fixture coverage, evidence, same-dialog hybrid follow-up, library/note/profile and two-account isolation',
    );
    final slow = await api.createConversation('T14 cancel');
    final cancelRun = await api.submit(
      slow['id'] as String,
      'T12_SLOW: cancellation fixture',
      requestId(),
    );
    await api.cancel(cancelRun['run_id'] as String);
    final cancelled = await poll(api, cancelRun['run_id'] as String);
    ensure(
      cancelled['status'] == 'cancelled' && cancelled['report_id'] == null,
      'cancel did not terminate run',
    );
    for (var i = 0; i < 50; i++) {
      await api.createConversation('T14 pagination $i');
    }
    final first = await api.collection('conversations');
    final second = await api.collection(
      'conversations',
      cursor: first['next_cursor'] as String,
    );
    final firstIds = rows(first['items']).map((r) => r['id']).toSet();
    ensure(
      firstIds.length == 50 &&
          first['next_cursor'] != '' &&
          rows(second['items']).length == 2 &&
          rows(second['items']).every((r) => !firstIds.contains(r['id'])),
      'collection pagination failed',
    );
    await a.requestJson('POST', '/api/v1/auth/logout');
    await b.requestJson('POST', '/api/v1/auth/logout');
    print(
      'PASS: cancellation, 52 conversations paginated without duplicates, logout; no unit/widget tests',
    );
  } finally {
    a.close();
    b.close();
  }
}

bool coordinatesPresent(Json e) =>
    e['char_start'] != null &&
    e['char_end'] != null &&
    (e['source_url'] as String).startsWith('https://arxiv.org/');
