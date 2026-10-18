import 'dart:math';
import '../api/api_client.dart';

typedef Json = Map<String, dynamic>;

List<Json> rows(dynamic value) =>
    (value as List? ?? const []).map((item) => item as Json).toList();

bool activeRun(Json? run) =>
    run != null && const ['queued', 'running'].contains(run['status']);

String requestId() {
  final random = Random.secure();
  return List.generate(
    24,
    (_) => random.nextInt(256).toRadixString(16).padLeft(2, '0'),
  ).join();
}

String failure(Object error) {
  if (error is ApiException) {
    return switch (error.code) {
      'active_run' => 'В диалоге уже идёт исследование. Обновите диалог.',
      'idempotency_conflict' => 'Этот запрос уже отправлен с другим текстом.',
      'run_capacity' => 'Сервер занят двумя исследованиями. Повторите позже.',
      'not_found' => 'Ресурс не найден или недоступен вашему аккаунту.',
      'scope_limit' => 'Слишком много фрагментов. Сузьте поиск до статьи.',
      'revision_conflict' =>
        'Запись уже изменена. Загрузите актуальную версию и повторите правку.',
      'memory_pending' =>
        'Память обновляется. Повторите загрузку через несколько секунд.',
      'memory_unavailable' =>
        'Память сейчас недоступна. Это не означает отсутствия истории.',
      'memory_scope_limit' => 'Слишком много записей памяти. Сузьте запрос.',
      'invalid_input' => 'Проверьте длину и заполнение полей.',
      _ => error.message,
    };
  }
  return 'Сервер вернул некорректные данные. Повторите загрузку.';
}

/// The same authenticated transport as login, without provider logic or secrets.
class ResearchApi {
  ResearchApi(this.client);
  final ApiClient client;
  String path(String kind, String id) =>
      '/api/v1/$kind/${Uri.encodeComponent(id)}';

  Future<Json> collection(String kind, {String cursor = ''}) =>
      client.requestJson(
        'GET',
        '/api/v1/$kind?limit=50&cursor=${Uri.encodeQueryComponent(cursor)}',
      );
  Future<Json> createConversation(String title) => client.requestJson(
    'POST',
    '/api/v1/conversations',
    body: {'title': title},
  );
  Future<Json> conversation(String id, {int after = 0}) => client.requestJson(
    'GET',
    '${path('conversations', id)}?limit=100&after_seq=$after',
  );
  Future<Json> submit(String id, String message, String requestId) =>
      client.requestJson(
        'POST',
        '${path('conversations', id)}/runs',
        body: {'message': message, 'client_request_id': requestId},
      );
  Future<Json> run(String id) => client.requestJson('GET', path('runs', id));
  Future<Json> events(String id, int after) => client.requestJson(
    'GET',
    '${path('runs', id)}/events?limit=100&after_seq=$after',
  );
  Future<Json> cancel(String id) =>
      client.requestJson('POST', '${path('runs', id)}/cancel');
  Future<Json> report(String id) =>
      client.requestJson('GET', path('reports', id));
  Future<Json> paper(String id) =>
      client.requestJson('GET', path('papers', id));
  Future<Json> evidence(String id) =>
      client.requestJson('GET', path('evidence', id));
  Future<Json> updatePaper(String id, Json patch) =>
      client.requestJson('PATCH', path('library', id), body: patch);
  Future<Json> search(String query) => client.requestJson(
    'POST',
    '/api/v1/library/search',
    body: {'query': query, 'limit': 8},
  );
  Future<Json> memory(String id) =>
      client.requestJson('GET', path('research-memory', id));
  Future<Json> updateMemory(String id, Json patch) =>
      client.requestJson('PATCH', path('research-memory', id), body: patch);
  Future<Json> searchMemory(String query) => client.requestJson(
    'POST',
    '/api/v1/research-memory/search',
    body: {'query': query, 'limit': 5},
  );
  Future<Json> profile() => client.requestJson('GET', '/api/v1/profile');
  Future<Json> updateProfile(String interests) => client.requestJson(
    'PUT',
    '/api/v1/profile',
    body: {'interests': interests, 'preferred_language': 'ru'},
  );
}
