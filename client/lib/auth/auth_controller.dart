import 'package:flutter/foundation.dart';
import '../api/api_client.dart';

class ScoutUser {
  ScoutUser.fromJson(Map<String, dynamic> json)
    : id = json['id'] as String,
      login = json['login'] as String;
  final String id;
  final String login;
}

/// The token lives only in ApiClient memory; each new process requires login.
class AuthController extends ChangeNotifier {
  AuthController() {
    try {
      api = ApiClient(
        const String.fromEnvironment(
          'API_BASE_URL',
          defaultValue: 'http://127.0.0.1:8080',
        ),
      );
    } on ApiException catch (e) {
      api = ApiClient('http://127.0.0.1:8080');
      error = e.message;
    }
    api.onUnauthorized = _unauthorized;
  }

  late ApiClient api;
  ScoutUser? user;
  DateTime? expiresAt;
  bool busy = false;
  String? error;

  void _unauthorized() {
    user = null;
    expiresAt = null;
    error = 'Сессия завершена. Войдите снова.';
    notifyListeners();
  }

  Future<void> login(String baseUrl, String login, String password) async {
    if (busy) return;
    busy = true;
    error = null;
    notifyListeners();
    try {
      final next = ApiClient(baseUrl);
      api.close();
      api = next..onUnauthorized = _unauthorized;
      final result = await api.requestJson(
        'POST',
        '/api/v1/auth/login',
        authenticated: false,
        body: {'login': login.trim(), 'password': password},
      );
      final loggedIn = ScoutUser.fromJson(
        result['user'] as Map<String, dynamic>,
      );
      final expiry = DateTime.parse(result['expires_at'] as String);
      final token = result['session_token'] as String;
      if (token.isEmpty || !expiry.isAfter(DateTime.now())) {
        throw const FormatException();
      }
      api.setToken(token);
      user = loggedIn;
      expiresAt = expiry;
    } on ApiException catch (e) {
      error = e.message;
    } on FormatException {
      error = 'Сервер вернул некорректную сессию.';
    } on TypeError {
      error = 'Сервер вернул некорректную сессию.';
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  Future<void> refresh() async {
    if (busy || user == null) return;
    busy = true;
    error = null;
    notifyListeners();
    try {
      user = ScoutUser.fromJson(await api.requestJson('GET', '/api/v1/me'));
    } on ApiException catch (e) {
      error = e.message;
    } on TypeError {
      error = 'Сервер вернул некорректный профиль.';
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  Future<void> logout() async {
    if (busy || user == null) return;
    busy = true;
    error = null;
    notifyListeners();
    try {
      await api.requestJson('POST', '/api/v1/auth/logout');
      api.setToken(null);
      user = null;
      expiresAt = null;
    } on ApiException catch (e) {
      // Keep the session on network failure so server revocation can be retried.
      error = e.message;
    } finally {
      busy = false;
      notifyListeners();
    }
  }

  @override
  void dispose() {
    api.onUnauthorized = null;
    api.close();
    super.dispose();
  }
}
