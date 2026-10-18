// Repository integration over real Go HTTP/auth/SQLite/Qwen3. Persisted research
// and report_b's T20 snapshot are explicit fixtures, not live agent evidence.
import 'dart:io';
import '../client/lib/api/api_client.dart';
import '../client/lib/research/research_api.dart';
import '../client/lib/research/research_memory.dart';

void ensure(bool value, String message) {
  if (!value) throw StateError(message);
}

Future<void> rejected(Future<Json> future, int status) async {
  try {
    await future;
    throw StateError('expected HTTP $status');
  } on ApiException catch (e) {
    ensure(e.status == status, 'expected $status, received ${e.status}');
  }
}

Future<void> main() async {
  final a = ApiClient(Platform.environment['SCOUT_BASE']!);
  final b = ApiClient(Platform.environment['SCOUT_BASE']!);
  final api = ResearchApi(a), other = ResearchApi(b);
  try {
    for (final pair in [(a, 'owner_a'), (b, 'owner_b')]) {
      final result = await pair.$1.requestJson(
        'POST',
        '/api/v1/auth/login',
        authenticated: false,
        body: {'login': pair.$2, 'password': 'integration-password-only'},
      );
      pair.$1.setToken(result['session_token'] as String);
    }
    final previous = await api.report('report_a');
    ensure(
      ReportMemory.fromReport(previous) == null,
      'legacy report without snapshot',
    );
    ensure(
      ReportMemory.fromReport(await api.report('report_e')) == null,
      'nullable HTTP snapshot',
    );
    final report = await api.report('report_b');
    final snapshot = ReportMemory.fromReport(report)!;
    ensure(
      snapshot.recalled.length == 3 && snapshot.referenced.length == 1,
      'recalled/referenced must remain separate',
    );
    ensure(
      snapshot.recalled.any((h) => h.kind == 'user_note'),
      'note kind lost',
    );
    final claim = snapshot.referenced.single;
    final entry = await api.memory(claim.id);
    ensure(
      MemoryHit.fromJson(entry).revision == claim.revision,
      'revision mismatch',
    );
    ensure(
      claim.origins.single['report_id'] == 'report_a',
      'provenance mismatch',
    );
    ensure(
      (await api.report(claim.origins.single['report_id'] as String))['id'] ==
          'report_a',
      'old report navigation',
    );
    final evidence = await api.evidence(claim.evidenceIds.single);
    ensure(
      evidence['paper_version_id'] == 'paper_a',
      'scientific evidence origin',
    );
    ensure(
      (await api.paper('paper_a'))['note'].toString().contains('T21 FIXTURE'),
      'current note route',
    );
    final found = await api.searchMemory('episodic memory consolidation');
    ensure(
      found['retrieval_mode'] == 'hybrid' &&
          rows(found['lexical_candidates']).isNotEmpty &&
          rows(found['dense_candidates']).isNotEmpty,
      'real hybrid branches required',
    );
    final fixed = await api.updateMemory(claim.id, {
      'expected_revision': entry['revision'],
      'correction': 'T21 explicit correction: verify original source 🔬',
    });
    ensure(
      fixed['state'] == 'disputed' && fixed['revision'] == claim.revision + 1,
      'correction/revision not persisted',
    );
    final annotations = ReportMemory.hits(fixed['annotations']);
    ensure(
      annotations.any(
        (h) => h.kind == 'user_correction' && h.text.endsWith('🔬'),
      ),
      'current correction missing',
    );
    // Fetch by report ID exactly as opening from conversation history does:
    // no memory ID, query, top-k or snapshot is needed to discover corrections.
    final reportContext = await api.reportAnnotations('report_a');
    final currentItems = rows(reportContext['items']);
    ensure(
      reportContext['report_id'] == 'report_a' &&
          currentItems.any(
            (h) =>
                h['target_id'] == claim.id &&
                (h['text'] as String).endsWith('🔬'),
          ),
      'history path omitted current correction',
    );
    final otherCard = snapshot.recalled.firstWhere(
      (h) => h.kind == 'claim' && h.id != claim.id,
    );
    final otherEntry = MemoryHit.fromJson(await api.memory(otherCard.id));
    final fromOtherCard = await api.reportAnnotations(
      otherEntry.origins.first['report_id'] as String,
    );
    ensure(
      rows(fromOtherCard['items']).any((h) => h['target_id'] == claim.id),
      'different-card path omitted sibling correction',
    );
    ensure(
      currentItems.any(
            (h) => h['memory_id'] == claim.id && h['state'] == 'disputed',
          ) &&
          currentItems.any((h) => h['kind'] == 'user_note'),
      'current target/note missing',
    );
    await rejected(other.reportAnnotations('report_a'), 404);
    await rejected(api.reportAnnotations('missing-report'), 404);
    await rejected(
      api.updateMemory(claim.id, {
        'expected_revision': entry['revision'],
        'excluded': true,
      }),
      409,
    );
    final excluded = await api.updateMemory(claim.id, {
      'expected_revision': fixed['revision'],
      'excluded': true,
    });
    ensure(excluded['excluded'] == true, 'exclude failed');
    ensure(
      rows(
        (await api.reportAnnotations('report_a'))['items'],
      ).any((h) => h['memory_id'] == claim.id && h['excluded'] == true),
      'historical report omitted exclusion',
    );
    final current = await api.memory(claim.id);
    ensure(
      current['excluded'] == true,
      'excluded record must stay readable for restore',
    );
    final restored = await api.updateMemory(claim.id, {
      'expected_revision': current['revision'],
      'excluded': false,
    });
    ensure(
      restored['excluded'] == false &&
          restored['revision'] == claim.revision + 3,
      'restore/revision failed',
    );
    await rejected(other.memory(claim.id), 404);
    await rejected(
      other.updateMemory(claim.id, {
        'expected_revision': restored['revision'],
        'excluded': true,
      }),
      404,
    );
    await rejected(other.report('report_b'), 404);
    ensure(
      rows((await other.searchMemory('episodic memory'))['hits']).isEmpty,
      'foreign memory leaked',
    );
    // The fixture is immutable while current GET contains the user's edits.
    final old = ReportMemory.fromReport(await api.report('report_b'))!;
    ensure(
      old.referenced.single.revision == claim.revision,
      'run snapshot changed after explicit edits',
    );
    ensure(
      (await api.report('report_a'))['markdown'] == previous['markdown'],
      'source report changed',
    );
    for (final fixture in [
      ('report_c', 'lexical_only'),
      ('report_d', 'hybrid_partial'),
    ]) {
      final degraded = ReportMemory.fromReport(await api.report(fixture.$1))!;
      ensure(
        degraded.mode == fixture.$2 &&
            degraded.recalled.isEmpty &&
            degraded.reasons.single == 'T21_MODE_HTTP_FIXTURE_NO_REAL_OUTAGE',
        'HTTP degraded mode fixture was relabelled',
      );
    }
    var unauthorized = false;
    a.onUnauthorized = () => unauthorized = true;
    await a.requestJson('POST', '/api/v1/auth/logout');
    await rejected(api.reportAnnotations('report_a'), 401);
    ensure(unauthorized, 'memory 401 did not invalidate session');
    await b.requestJson('POST', '/api/v1/auth/logout');
    print(
      'PASS T21: Dart -> real Go HTTP/SQLite/Qwen3; nullable snapshot, separate recalled/referenced, original report/paper/note/evidence, hybrid branches, correction, stale 409, exclude/restore, report-wide current annotations from history/other card, immutable snapshot, foreign 404, empty owner-B search, 401 invalidation. Snapshot/research are labelled fixtures; no live agent recall claimed.',
    );
  } finally {
    a.close();
    b.close();
  }
}
