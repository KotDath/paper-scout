import 'dart:async';
import 'dart:convert';
import 'dart:io';

class ApiException implements Exception {
  const ApiException(this.message, {this.status, this.code, this.requestId});
  final String message;
  final int? status;
  final String? code;
  final String? requestId;

  @override
  String toString() => message;
}

/// One transport for the Go API. Credentials never reach redirects or logs.
class ApiClient {
  ApiClient(String baseUrl) : baseUrl = validateBaseUrl(baseUrl);

  final Uri baseUrl;
  String? _token;
  final HttpClient _http = HttpClient()
    ..connectionTimeout = const Duration(seconds: 10);
  void Function()? onUnauthorized;

  static Uri validateBaseUrl(String value) {
    final uri = Uri.tryParse(value.trim());
    if (uri == null ||
        !const ['http', 'https'].contains(uri.scheme) ||
        uri.host.isEmpty ||
        uri.userInfo.isNotEmpty ||
        uri.hasQuery ||
        uri.hasFragment ||
        (uri.path.isNotEmpty && uri.path != '/')) {
      throw const ApiException('Укажите адрес сервера: http://127.0.0.1:8080');
    }
    return uri.replace(path: '/', query: null, fragment: null);
  }

  void setToken(String? token) => _token = token;

  Future<Map<String, dynamic>> requestJson(
    String method,
    String path, {
    Map<String, dynamic>? body,
    bool authenticated = true,
  }) async {
    // Resolve paths only within this API origin, including future T14 routes.
    if (!path.startsWith('/api/v1/') ||
        path.contains('://') ||
        path.contains('#')) {
      throw const ApiException('Некорректный путь API.');
    }
    final token = authenticated ? _token : null;
    HttpClientRequest? pending;
    var timedOut = false;
    try {
      return await (() async {
        pending = await _http.openUrl(method, baseUrl.resolve(path));
        final request = pending!;
        if (timedOut) {
          request.abort();
          throw TimeoutException('Connection deadline');
        }
        request.followRedirects = false;
        request.headers.set(HttpHeaders.acceptHeader, 'application/json');
        if (token != null) {
          request.headers.set(HttpHeaders.authorizationHeader, 'Bearer $token');
        }
        if (body != null) {
          request.headers.contentType = ContentType.json;
          request.write(jsonEncode(body));
        }
        final response = await request.close();
        // Invalidate before parsing: even a malformed 401 must end the session.
        if (response.statusCode == 401 && authenticated && _token == token) {
          _token = null;
          onUnauthorized?.call();
        }
        final bytes = <int>[];
        await for (final chunk in response) {
          bytes.addAll(chunk);
          if (bytes.length > 4 * 1024 * 1024) {
            throw const ApiException('Ответ сервера слишком большой.');
          }
        }
        if (response.statusCode == 401) {
          throw ApiException(
            authenticated
                ? 'Сессия завершена. Войдите снова.'
                : 'Неверный логин или пароль.',
            status: 401,
            code: 'unauthorized',
          );
        }
        Map<String, dynamic> data = {};
        if (bytes.isNotEmpty) {
          final decoded = jsonDecode(utf8.decode(bytes));
          if (decoded is! Map<String, dynamic>) {
            throw const FormatException();
          }
          data = decoded;
        }
        if (response.statusCode < 200 || response.statusCode >= 300) {
          final error = data['error'];
          throw ApiException(
            response.statusCode >= 500
                ? 'Сервер недоступен. Повторите попытку.'
                : 'Не удалось выполнить запрос (${response.statusCode}).',
            status: response.statusCode,
            code: error is Map && error['code'] is String
                ? error['code'] as String
                : null,
            requestId: data['request_id'] is String
                ? data['request_id'] as String
                : null,
          );
        }
        return data;
      })().timeout(const Duration(seconds: 20));
    } on TimeoutException {
      timedOut = true;
      pending?.abort();
      throw const ApiException('Сервер не ответил вовремя. Повторите попытку.');
    } on SocketException {
      throw const ApiException(
        'Нет соединения с сервером. Проверьте адрес и подключение.',
      );
    } on HandshakeException {
      throw const ApiException('Не удалось установить защищённое соединение.');
    } on HttpException {
      throw const ApiException(
        'Соединение с сервером прервано. Повторите попытку.',
      );
    } on FormatException {
      throw const ApiException('Сервер вернул некорректный ответ.');
    }
  }

  void close() {
    _token = null;
    _http.close(force: true);
  }
}
