import 'dart:async';
import 'package:flutter/widgets.dart';
import 'research_api.dart';
import '../api/api_client.dart';

/// Owns one conversation. Disposed on account change; no overlapping polling.
class ConversationController extends ChangeNotifier
    with WidgetsBindingObserver {
  ConversationController(this.api, this.id) {
    WidgetsBinding.instance.addObserver(this);
    refresh();
  }
  final ResearchApi api;
  final String id;
  String title = 'Исследование';
  List<Json> messages = [], runs = [], events = [];
  Json? run;
  bool loading = false, sending = false, cancelling = false;
  String? error;
  String? pendingMessage, pendingId;
  Timer? _timer;
  bool _disposed = false, _foreground = true;
  int _eventSeq = 0;
  String? _eventRun;
  bool get hasActiveRun => runs.any(activeRun) || activeRun(run);

  void _emit() {
    if (!_disposed) notifyListeners();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    _foreground = state == AppLifecycleState.resumed;
    _timer?.cancel();
    if (_foreground) refresh();
  }

  Future<void> refresh({String? selectRun}) async {
    if (_disposed || loading) return;
    _timer?.cancel();
    loading = true;
    error = null;
    _emit();
    try {
      // Public history is paginated independently of the run references.
      final all = <Json>[];
      var after = 0;
      Json page;
      do {
        page = await api.conversation(id, after: after);
        if (_disposed) return;
        all.addAll(rows(page['messages']));
        final next = page['next_after_seq'] as int?;
        if (next != null && next <= after) throw const FormatException();
        after = next ?? after;
      } while (page['next_after_seq'] != null);
      title = (page['conversation'] as Json)['title'] as String;
      messages = all;
      runs = rows(page['runs']);
      final selected = selectRun ?? run?['id'] as String?;
      final chosen = runs.where((r) => r['id'] == selected).firstOrNull;
      final current =
          runs.where(activeRun).firstOrNull ?? chosen ?? runs.firstOrNull;
      if (current != null) {
        final state = await api.run(current['id'] as String);
        if (_disposed) return;
        run = state;
        if (_eventRun != state['id']) {
          _eventRun = state['id'] as String;
          _eventSeq = 0;
          events = [];
        }
        Json eventPage;
        do {
          eventPage = await api.events(_eventRun!, _eventSeq);
          if (_disposed) return;
          final next = eventPage['next_after_seq'] as int;
          if (eventPage['has_more'] == true && next <= _eventSeq) {
            throw const FormatException();
          }
          events.addAll(rows(eventPage['items']));
          _eventSeq = next;
        } while (eventPage['has_more'] == true);
      }
    } catch (e) {
      if (!_disposed) error = failure(e);
    } finally {
      loading = false;
      _emit();
      if (!_disposed && _foreground && (hasActiveRun || error != null)) {
        _timer = Timer(Duration(seconds: error == null ? 1 : 5), refresh);
      }
    }
  }

  Future<bool> send(String text) async {
    if (_disposed || sending || loading || hasActiveRun) return false;
    final message = pendingMessage ?? text.trim();
    if (message.isEmpty || message.runes.length > 20000) {
      error = 'Введите вопрос до 20000 символов.';
      _emit();
      return false;
    }
    pendingMessage = message;
    pendingId ??= requestId();
    sending = true;
    error = null;
    _emit();
    var accepted = false;
    String? newRun;
    try {
      final result = await api.submit(id, message, pendingId!);
      if (_disposed) return false;
      newRun = result['run_id'] as String;
      pendingId = pendingMessage = null;
      // Block a second submission even while a pre-existing refresh finishes.
      run = {'id': newRun, 'status': result['status']};
      accepted = true;
    } catch (e) {
      if (!_disposed) error = failure(e);
      // Preserve ID/text on ambiguous transport failure. A retry is the same
      // operation, including if the first POST committed and lost its response.
      if (e is ApiException &&
          e.status != null &&
          e.status! >= 400 &&
          e.status! < 500) {
        pendingId = pendingMessage = null;
      }
    } finally {
      sending = false;
      _emit();
    }
    if (accepted && !_disposed) {
      // A running refresh is allowed to finish; its scheduled poll recovers it.
      if (loading) {
        _timer?.cancel();
        _timer = Timer(
          const Duration(seconds: 1),
          () => refresh(selectRun: newRun),
        );
      } else {
        await refresh(selectRun: newRun);
      }
    }
    return accepted;
  }

  Future<void> cancel() async {
    if (_disposed || cancelling || !activeRun(run)) return;
    cancelling = true;
    error = null;
    _emit();
    try {
      final updated = await api.cancel(run!['id'] as String);
      if (!_disposed) run = updated;
    } catch (e) {
      if (!_disposed) error = failure(e);
    } finally {
      cancelling = false;
      _emit();
    }
    await refresh();
  }

  @override
  void dispose() {
    _disposed = true;
    _timer?.cancel();
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }
}
