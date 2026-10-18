// Live integration of the production Dart transport with Go HTTP + SQLite.
// Run through flutter_client.py; no mocks, Flutter tests or external services.
import 'dart:io';
import '../client/lib/api/api_client.dart';

void ensure(bool condition, String scenario) {
  if (!condition) throw StateError('FAIL: $scenario');
}

Future<void> main() async {
  final env = Platform.environment;
  final a = ApiClient(env['SCOUT_BASE']!);
  final b = ApiClient(env['SCOUT_BASE']!);
  var invalidations = 0;
  a.onUnauthorized = () => invalidations++;
  try {
    try {
      await a.requestJson(
        'POST',
        '/api/v1/auth/login',
        authenticated: false,
        body: {'login': 'client-a', 'password': 'wrong-password'},
      );
      throw StateError('bad credentials accepted');
    } on ApiException catch (e) {
      ensure(e.status == 401 && invalidations == 0, 'login 401 stays on login');
    }
    final accounts = <Map<String, dynamic>>[];
    for (final entry in [
      (a, 'client-a', 'SCOUT_PASSWORD_A'),
      (b, 'client-b', 'SCOUT_PASSWORD_B'),
    ]) {
      final session = await entry.$1.requestJson(
        'POST',
        '/api/v1/auth/login',
        authenticated: false,
        body: {'login': entry.$2, 'password': env[entry.$3]},
      );
      entry.$1.setToken(session['session_token'] as String);
      final me = await entry.$1.requestJson('GET', '/api/v1/me');
      ensure(me['login'] == entry.$2, 'server identity');
      accounts.add(me);
    }
    ensure(accounts[0]['id'] != accounts[1]['id'], 'separate account IDs');
    await a.requestJson(
      'PUT',
      '/api/v1/profile',
      body: {
        'interests': 'T13 account A research interests',
        'preferred_language': 'ru',
      },
    );
    final profileA = await a.requestJson('GET', '/api/v1/profile');
    final profileB = await b.requestJson('GET', '/api/v1/profile');
    ensure(
      profileA['interests'] == 'T13 account A research interests' &&
          profileB['interests'] == '' &&
          profileB['user_id'] == accounts[1]['id'],
      'profile isolation',
    );
    print(
      'PASS: Dart production API client, login 401, two real accounts, me, isolated profiles',
    );
    await a.requestJson('POST', '/api/v1/auth/logout');
    try {
      await a.requestJson('GET', '/api/v1/me');
      throw StateError('revoked token accepted');
    } on ApiException catch (e) {
      ensure(
        e.status == 401 && invalidations == 1,
        'protected 401 invalidates',
      );
    }
    try {
      await a.requestJson('GET', '/api/v1/me');
      throw StateError('missing token accepted');
    } on ApiException catch (e) {
      ensure(e.status == 401, 'cleared token cannot authenticate');
    }
    ensure(
      (await b.requestJson('GET', '/api/v1/me'))['id'] == accounts[1]['id'],
      'B survives A logout',
    );
    ensure(
      (await b.requestJson('POST', '/api/v1/auth/logout')).isEmpty,
      '204 decoding',
    );
    print(
      'PASS: logout 204, revoked-token 401 callback, cleared session, account B unaffected',
    );
  } finally {
    a.close();
    b.close();
  }
}
