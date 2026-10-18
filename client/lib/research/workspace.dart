import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:url_launcher/url_launcher.dart';
import '../auth/auth_controller.dart';
import 'conversation_controller.dart';
import 'research_api.dart';
import 'research_memory.dart';

part 'memory_widgets.dart';

typedef OpenResource =
    void Function(String kind, String id, {String? memoryId});
typedef ResourceTarget = (String, String, String?);

String statusText(dynamic status) =>
    const {
      'queued': 'В очереди',
      'running': 'Исследование идёт',
      'completed': 'Завершено',
      'partial': 'Частично',
      'failed': 'Ошибка',
      'cancelled': 'Отменено',
      'interrupted': 'Прервано при перезапуске',
      'complete': 'Проанализирован весь извлечённый текст',
      'abstract_only': 'Только аннотация',
      'none': 'Анализ не выполнен',
      'full_text': 'Полный текст извлечён',
      'ready': 'Готов',
      'pending': 'Ожидается',
      'hybrid': 'Гибридный поиск: BM25 + dense + RRF',
      'hybrid_partial': 'Гибридный поиск с неполным индексом',
      'lexical_only': 'Только лексический поиск: dense недоступен',
    }[status] ??
    '$status';

String runFailure(dynamic code) =>
    const {
      'output_limit':
          'Ответ модели обрезан по лимиту вывода. Задайте более короткий вопрос.',
      'provider_unavailable':
          'Модель недоступна. Проверьте конфигурацию сервера.',
      'provider_rate_limited': 'Провайдер ограничил запросы. Повторите позже.',
      'budget_exceeded':
          'Исчерпан бюджет запуска. Доступный результат сохранён.',
      'context_limit':
          'История не помещается в контекст. Создайте новый диалог с той же библиотекой.',
      'run_interrupted': 'Сервер перезапущен. Автоматического повтора нет.',
    }[code] ??
    'Причина: $code';

String coordinates(Json hit) => [
  if (hit['page_start'] != null)
    'стр. ${hit['page_start']}–${hit['page_end'] ?? hit['page_start']}',
  if ((hit['section'] as String? ?? '').isNotEmpty) hit['section'] as String,
  if (hit['char_start'] != null)
    'символы ${hit['char_start']}–${hit['char_end']}',
].join(' · ');

Widget heading(BuildContext context, String text) => Padding(
  padding: const EdgeInsets.symmetric(vertical: 12),
  child: Text(text, style: Theme.of(context).textTheme.titleLarge),
);

Widget problem(String message, VoidCallback? retry) => Column(
  crossAxisAlignment: CrossAxisAlignment.start,
  children: [
    Semantics(
      liveRegion: true,
      child: Text(message, style: const TextStyle(color: Colors.red)),
    ),
    if (retry != null)
      TextButton.icon(
        onPressed: retry,
        icon: const Icon(Icons.refresh),
        label: const Text('Повторить загрузку'),
      ),
  ],
);

class ResearchWorkspace extends StatefulWidget {
  const ResearchWorkspace({super.key, required this.auth});
  final AuthController auth;
  @override
  State<ResearchWorkspace> createState() => _ResearchWorkspaceState();
}

class _ResearchWorkspaceState extends State<ResearchWorkspace> {
  late final api = ResearchApi(widget.auth.api);
  ConversationController? conversation;
  int tab = 0, revision = 0;
  ResourceTarget? detail;

  void openConversation(String id) {
    conversation?.dispose();
    setState(() {
      conversation = ConversationController(api, id);
      detail = null;
      tab = 0;
    });
  }

  void open(String kind, String id, {String? memoryId}) => setState(() {
    if (detail != null) _history.add(detail!);
    detail = (kind, id, memoryId);
  });

  @override
  void dispose() {
    conversation?.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final inner = detail;
    final canBack = inner != null || (tab == 0 && conversation != null);
    final content = inner != null
        ? ResourcePane(
            key: ValueKey(inner),
            api: api,
            kind: inner.$1,
            id: inner.$2,
            memoryId: inner.$3,
            open: open,
          )
        : switch (tab) {
            0 =>
              conversation == null
                  ? ConversationsPane(
                      key: ValueKey(revision),
                      api: api,
                      open: openConversation,
                    )
                  : ConversationPane(controller: conversation!, open: open),
            1 => LibraryPane(key: ValueKey(revision), api: api, open: open),
            _ => ProfilePane(api: api, auth: widget.auth),
          };
    return PopScope(
      canPop: !canBack,
      onPopInvokedWithResult: (didPop, _) {
        if (!didPop) back();
      },
      child: Scaffold(
        appBar: AppBar(
          title: Text(
            inner != null
                ? switch (inner.$1) {
                    'reports' => 'Отчёт',
                    'research-memory' => 'Память исследования',
                    'evidence' => 'Доказательный фрагмент',
                    _ => 'Статья',
                  }
                : [
                    'Диалоги',
                    'Библиотека',
                    'Аккаунт: ${widget.auth.user!.login}',
                  ][tab],
          ),
          leading: canBack
              ? IconButton(
                  tooltip: 'Назад',
                  icon: const Icon(Icons.arrow_back),
                  onPressed: back,
                )
              : null,
        ),
        body: SafeArea(
          child: Center(
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 800),
              child: content,
            ),
          ),
        ),
        bottomNavigationBar: inner != null
            ? null
            : NavigationBar(
                selectedIndex: tab,
                onDestinationSelected: (value) => setState(() {
                  tab = value;
                  revision++;
                }),
                destinations: const [
                  NavigationDestination(
                    icon: Icon(Icons.forum_outlined),
                    label: 'Диалоги',
                  ),
                  NavigationDestination(
                    icon: Icon(Icons.local_library_outlined),
                    label: 'Библиотека',
                  ),
                  NavigationDestination(
                    icon: Icon(Icons.person_outline),
                    label: 'Профиль',
                  ),
                ],
              ),
      ),
    );
  }

  // Resource history stays in this account's widget tree, never in global routes.
  final List<ResourceTarget> _history = [];
  void back() => setState(() {
    if (detail != null) {
      detail = _history.isEmpty ? null : _history.removeLast();
    } else {
      conversation?.dispose();
      conversation = null;
      revision++;
    }
  });
}

class ConversationsPane extends StatefulWidget {
  const ConversationsPane({super.key, required this.api, required this.open});
  final ResearchApi api;
  final void Function(String) open;
  @override
  State<ConversationsPane> createState() => _ConversationsPaneState();
}

class _ConversationsPaneState extends State<ConversationsPane> {
  final title = TextEditingController();
  List<Json> items = [];
  String cursor = '', error = '';
  bool busy = false, loaded = false;
  @override
  void initState() {
    super.initState();
    load();
  }

  @override
  void dispose() {
    title.dispose();
    super.dispose();
  }

  Future<void> load({bool more = false}) async {
    if (busy) return;
    setState(() {
      busy = true;
      error = '';
    });
    try {
      final page = await widget.api.collection(
        'conversations',
        cursor: more ? cursor : '',
      );
      if (!mounted) return;
      setState(() {
        items = [if (more) ...items, ...rows(page['items'])];
        cursor = page['next_cursor'] as String;
        loaded = true;
      });
    } catch (e) {
      if (mounted) setState(() => error = failure(e));
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  Future<void> create() async {
    if (busy || title.text.trim().isEmpty) return;
    setState(() {
      busy = true;
      error = '';
    });
    try {
      final next = await widget.api.createConversation(title.text.trim());
      if (mounted) widget.open(next['id'] as String);
    } catch (e) {
      if (mounted) setState(() => error = failure(e));
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  @override
  Widget build(BuildContext context) => ListView(
    padding: const EdgeInsets.all(16),
    children: [
      heading(context, 'Новое исследование'),
      TextField(
        controller: title,
        maxLength: 200,
        decoration: const InputDecoration(labelText: 'Название диалога'),
        onSubmitted: (_) => create(),
      ),
      FilledButton.icon(
        onPressed: busy ? null : create,
        icon: const Icon(Icons.add),
        label: const Text('Создать диалог'),
      ),
      heading(context, 'Сохранённые диалоги'),
      OutlinedButton(
        onPressed: busy ? null : () => load(),
        child: const Text('Обновить историю'),
      ),
      if (error.isNotEmpty) problem(error, () => load()),
      if (busy) const LinearProgressIndicator(),
      if (loaded && items.isEmpty)
        const Text('Диалогов пока нет. Создайте первый.'),
      for (final item in items)
        ListTile(
          title: Text(item['title'] as String),
          subtitle: Text(item['created_at'] as String),
          trailing: const Icon(Icons.chevron_right),
          onTap: () => widget.open(item['id'] as String),
        ),
      if (cursor.isNotEmpty)
        TextButton(
          onPressed: busy ? null : () => load(more: true),
          child: const Text('Ещё диалоги'),
        ),
    ],
  );
}

class ConversationPane extends StatefulWidget {
  const ConversationPane({
    super.key,
    required this.controller,
    required this.open,
  });
  final ConversationController controller;
  final OpenResource open;
  @override
  State<ConversationPane> createState() => _ConversationPaneState();
}

class _ConversationPaneState extends State<ConversationPane> {
  final question = TextEditingController();
  @override
  void dispose() {
    question.dispose();
    super.dispose();
  }

  Future<void> send() async {
    FocusScope.of(context).unfocus();
    if (await widget.controller.send(question.text) && mounted) {
      question.clear();
    }
  }

  @override
  Widget build(BuildContext context) => ListenableBuilder(
    listenable: widget.controller,
    builder: (context, _) {
      final c = widget.controller;
      final run = c.run;
      final executing = c.executingRun;
      return ListView(
        padding: const EdgeInsets.all(16),
        children: [
          heading(context, c.title),
          OutlinedButton(
            onPressed: c.loading ? null : () => c.refresh(),
            child: const Text('Обновить диалог'),
          ),
          if (c.error != null) problem(c.error!, () => c.refresh()),
          for (final message in c.messages)
            Card(
              child: ExpansionTile(
                title: Text(message['role'] == 'user' ? 'Вы' : 'Помощник'),
                subtitle: Text(
                  message['content'] as String? ?? '',
                  maxLines: 3,
                  overflow: TextOverflow.ellipsis,
                ),
                children: [
                  Padding(
                    padding: const EdgeInsets.all(12),
                    child: SelectableText(message['content'] as String? ?? ''),
                  ),
                ],
              ),
            ),
          if (executing != null && activeRun(executing)) ...[
            heading(context, statusText(executing['status'])),
            const LinearProgressIndicator(),
            const Text(
              'Этапы поступают с сервера. Процент готовности модели неизвестен.',
            ),
            if (executing['id'] != run?['id']) ...[
              const Text(
                'В диалоге выполняется другой запуск. Вы просматриваете сохранённый результат.',
              ),
              TextButton(
                onPressed: c.loading
                    ? null
                    : () => c.refresh(selectRun: executing['id'] as String),
                child: const Text('Показать активный запуск'),
              ),
            ],
            OutlinedButton(
              onPressed: c.cancelling || executing['cancel_requested'] == true
                  ? null
                  : c.cancel,
              child: Text(
                executing['cancel_requested'] == true
                    ? 'Отмена запрошена'
                    : 'Отменить исследование',
              ),
            ),
          ],
          if (run != null) ...[
            if (run['id'] != executing?['id'])
              heading(context, statusText(run['status'])),
            if (run['error_code'] != null) Text(runFailure(run['error_code'])),
            if (!activeRun(run) && run['report_id'] == null)
              const Text(
                'Готового отчёта нет. Доступные источники и ход работы сохранены.',
              ),
            if (run['report_id'] != null)
              FilledButton(
                onPressed: () =>
                    widget.open('reports', run['report_id'] as String),
                child: const Text('Открыть отчёт'),
              ),
            for (final source in rows(run['sources']))
              SourceCard(source: source, open: widget.open),
            ExpansionTile(
              title: Text('Ход работы · ${c.events.length} событий'),
              initiallyExpanded: activeRun(run),
              children: [for (final event in c.events) EventTile(event: event)],
            ),
          ],
          if (c.runs.length > 1)
            ExpansionTile(
              title: const Text('Предыдущие запуски и отчёты'),
              children: [
                for (final old in c.runs)
                  ListTile(
                    title: Text(statusText(old['status'])),
                    subtitle: Text(old['created_at'] as String),
                    onTap: c.loading
                        ? null
                        : () => c.refresh(selectRun: old['id'] as String),
                  ),
              ],
            ),
          heading(
            context,
            c.messages.isEmpty
                ? 'Исследовательский вопрос'
                : 'Уточняющий вопрос',
          ),
          if (c.pendingMessage != null)
            Padding(
              padding: const EdgeInsets.only(bottom: 8),
              child: Text(
                'Ответ на отправку не получен. Повторите тот же запрос: ${c.pendingMessage}',
              ),
            ),
          TextField(
            controller: question,
            enabled: !c.sending && !c.hasActiveRun && c.pendingMessage == null,
            maxLength: 20000,
            minLines: 2,
            maxLines: 6,
            decoration: const InputDecoration(
              labelText: 'Вопрос',
              hintText: 'Найди работы о долговременной памяти AI-агентов…',
            ),
          ),
          FilledButton(
            onPressed: c.sending || c.loading || c.hasActiveRun ? null : send,
            child: Text(
              c.sending
                  ? 'Отправка…'
                  : c.pendingId != null
                  ? 'Повторить тот же запрос'
                  : c.messages.isEmpty
                  ? 'Начать исследование'
                  : 'Задать follow-up',
            ),
          ),
          const SizedBox(height: 16),
        ],
      );
    },
  );
}

class EventTile extends StatelessWidget {
  const EventTile({super.key, required this.event});
  final Json event;
  @override
  Widget build(BuildContext context) {
    final p = event['payload'] as Json? ?? {};
    final type = event['event_type'] as String;
    final label =
        const {
          'run_queued': 'Запуск в очереди',
          'run_started': 'Запуск начат',
          'search_started': 'Поиск публикаций',
          'papers_found': 'Найдены публикации',
          'document_loaded': 'Документ получен',
          'document_extracted': 'Текст извлечён',
          'index_progress': 'Индексация',
          'analysis_progress': 'Анализ текста',
          'retrieval_completed': 'Поиск по библиотеке',
          'memory_recalled': 'Найдена память прошлых исследований',
          'tool_failed': 'Ошибка инструмента',
          'report_saved': 'Отчёт сохранён',
          'run_finished': 'Запуск завершён',
        }[type] ??
        type;
    return ExpansionTile(
      title: Text('${event['seq']}. $label'),
      subtitle: p['retrieval_mode'] != null
          ? Text(
              type == 'memory_recalled'
                  ? '${statusText(p['retrieval_mode'])}\nПередача в контекст не доказывает использование в ответе.'
                  : '${statusText(p['retrieval_mode'])}\nBM25: ${p['lexical_candidates']}, dense: ${p['dense_candidates']}',
            )
          : p['covered_runes'] != null
          ? Text(
              'Проанализировано ${p['covered_runes']} из ${p['total_runes']} символов',
            )
          : null,
      children: [
        Padding(
          padding: const EdgeInsets.all(12),
          child: SelectableText(const JsonEncoder.withIndent('  ').convert(p)),
        ),
      ],
    );
  }
}

class SourceCard extends StatelessWidget {
  const SourceCard({super.key, required this.source, required this.open});
  final Json source;
  final OpenResource open;
  @override
  Widget build(BuildContext context) {
    final paper = source['paper'] as Json;
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text(
              paper['title'] as String,
              style: Theme.of(context).textTheme.titleMedium,
            ),
            Text('Анализ: ${statusText(source['analysis_status'])}'),
            if ((source['selection_reason'] as String? ?? '').isNotEmpty)
              Text('Выбор: ${source['selection_reason']}'),
            for (final coverage in rows(source['coverage'])) ...[
              Text(
                'Покрытие: ${coverage['covered_runes']}/${coverage['total_runes']} символов',
              ),
              ExpansionTile(
                title: const Text('Диапазоны анализа и происхождение'),
                children: [
                  SelectableText(
                    const JsonEncoder.withIndent('  ').convert(coverage),
                  ),
                ],
              ),
            ],
            TextButton(
              onPressed: () => open('papers', paper['id'] as String),
              child: const Text('Открыть статью и заметку'),
            ),
          ],
        ),
      ),
    );
  }
}

class LibraryPane extends StatefulWidget {
  const LibraryPane({super.key, required this.api, required this.open});
  final ResearchApi api;
  final OpenResource open;
  @override
  State<LibraryPane> createState() => _LibraryPaneState();
}

class _LibraryPaneState extends State<LibraryPane> {
  final query = TextEditingController();
  List<Json> items = [];
  Json? result;
  String cursor = '', error = '';
  bool busy = false, loaded = false;
  @override
  void initState() {
    super.initState();
    load();
  }

  @override
  void dispose() {
    query.dispose();
    super.dispose();
  }

  Future<void> load({bool more = false, bool search = false}) async {
    if (busy) return;
    setState(() {
      busy = true;
      error = '';
    });
    try {
      if (search) {
        if (query.text.trim().isEmpty || query.text.runes.length > 4000) {
          throw const FormatException();
        }
        final found = await widget.api.search(query.text.trim());
        if (mounted) setState(() => result = found);
      } else {
        final page = await widget.api.collection(
          'library',
          cursor: more ? cursor : '',
        );
        if (mounted) {
          setState(() {
            items = [if (more) ...items, ...rows(page['items'])];
            cursor = page['next_cursor'] as String;
            loaded = true;
            result = null;
          });
        }
      }
    } catch (e) {
      if (mounted) setState(() => error = failure(e));
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  @override
  Widget build(BuildContext context) => ListView(
    padding: const EdgeInsets.all(16),
    children: [
      TextField(
        controller: query,
        maxLength: 4000,
        decoration: const InputDecoration(labelText: 'Вопрос к библиотеке'),
        onSubmitted: (_) => load(search: true),
      ),
      FilledButton(
        onPressed: busy ? null : () => load(search: true),
        child: const Text('Найти фрагменты'),
      ),
      TextButton(
        onPressed: busy ? null : () => load(),
        child: const Text('Обновить библиотеку'),
      ),
      if (busy) const LinearProgressIndicator(),
      if (error.isNotEmpty) problem(error, null),
      if (result != null) ...[
        heading(context, statusText(result!['retrieval_mode'])),
        Text(
          'Индекс: ${result!['indexed_chunks']}/${result!['total_chunks']} фрагментов. BM25: ${rows(result!['lexical_candidates']).length}; dense: ${rows(result!['dense_candidates']).length}',
        ),
        for (final reason in result!['reasons'] as List? ?? [])
          Text('Ограничение: $reason'),
        if (rows(result!['hits']).isEmpty) const Text('Фрагменты не найдены.'),
        for (final hit in rows(result!['hits']))
          ListTile(
            title: Text(hit['title'] as String),
            subtitle: Text(
              '${coordinates(hit)}\nBM25 #${hit['lexical_rank'] ?? '—'} · dense #${hit['dense_rank'] ?? '—'} · RRF ${hit['rrf_score'] ?? '—'}',
            ),
            onTap: () => widget.open('evidence', hit['evidence_id'] as String),
          ),
      ] else ...[
        if (loaded && items.isEmpty)
          const Text(
            'Библиотека пока пуста. Источники появятся после исследования.',
          ),
        for (final item in items)
          ListTile(
            title: Text(item['title'] as String),
            subtitle: Text(
              '${item['saved'] == true ? 'Сохранена' : 'Импортирована'} · ${item['read'] == true ? 'Прочитана' : 'Не прочитана'}\n${item['note']}',
            ),
            onTap: () => widget.open('papers', item['id'] as String),
          ),
        if (cursor.isNotEmpty)
          TextButton(
            onPressed: busy ? null : () => load(more: true),
            child: const Text('Ещё статьи'),
          ),
      ],
    ],
  );
}

class ResourcePane extends StatefulWidget {
  const ResourcePane({
    super.key,
    required this.api,
    required this.kind,
    required this.id,
    this.memoryId,
    required this.open,
  });
  final ResearchApi api;
  final String kind, id;
  final String? memoryId;
  final OpenResource open;
  @override
  State<ResourcePane> createState() => _ResourcePaneState();
}

class _ResourcePaneState extends State<ResourcePane> {
  Json? data;
  Json? currentMemory;
  ReportMemory? reportMemory;
  String memoryError = '';
  String error = '', notice = '';
  bool busy = false, saved = false, read = false;
  final note = TextEditingController();
  final correction = TextEditingController();
  bool memoryNeedsReload = false;
  @override
  void initState() {
    super.initState();
    load();
  }

  @override
  void dispose() {
    note.dispose();
    correction.dispose();
    super.dispose();
  }

  Future<void> load() async {
    setState(() {
      busy = true;
      error = '';
      currentMemory = null;
      memoryError = '';
    });
    try {
      final loaded = await switch (widget.kind) {
        'reports' => widget.api.report(widget.id),
        'evidence' => widget.api.evidence(widget.id),
        'research-memory' => widget.api.memory(widget.id),
        _ => widget.api.paper(widget.id),
      };
      final parsedMemory = widget.kind == 'reports'
          ? ReportMemory.fromReport(loaded)
          : null;
      if (widget.kind == 'research-memory') {
        MemoryHit.fromJson(loaded);
      }
      if (!mounted) return;
      setState(() {
        reportMemory = parsedMemory;
        data = loaded;
        memoryNeedsReload = false;
        saved = loaded['saved'] == true;
        read = loaded['read'] == true;
        note.text = loaded['note'] as String? ?? '';
      });
      if (widget.kind == 'reports' && widget.memoryId != null) {
        try {
          final entry = await widget.api.memory(widget.memoryId!);
          if (mounted) {
            setState(() {
              currentMemory = entry;
              memoryError = '';
            });
          }
        } catch (e) {
          if (mounted) setState(() => memoryError = failure(e));
        }
      }
    } catch (e) {
      if (mounted) setState(() => error = failure(e));
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  Future<void> save() async {
    if (busy) return;
    if (note.text.runes.length > 10000) {
      setState(() => error = 'Заметка должна быть до 10000 символов.');
      return;
    }
    setState(() {
      busy = true;
      error = '';
      notice = '';
    });
    try {
      final updated = await widget.api.updatePaper(widget.id, {
        'saved': saved,
        'read': read,
        'note': note.text,
      });
      if (mounted) {
        setState(() {
          data = updated;
          notice = 'Изменения сохранены на сервере.';
        });
      }
    } catch (e) {
      if (mounted) setState(() => error = failure(e));
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  Future<void> patchMemory({bool? excluded}) async {
    if (busy || memoryNeedsReload || data == null) return;
    final text = correction.text;
    if (excluded == null && (text.trim().isEmpty || text.runes.length > 4000)) {
      setState(() => error = 'Введите поправку от 1 до 4000 символов.');
      return;
    }
    setState(() {
      busy = true;
      error = '';
      notice = '';
    });
    try {
      final updated = await widget.api.updateMemory(widget.id, {
        'expected_revision': data!['revision'],
        if (excluded != null) 'excluded': excluded else 'correction': text,
      });
      if (!mounted) return;
      setState(() {
        data = updated;
        notice = 'Изменение памяти сохранено. Исторические снимки не изменены.';
        if (excluded == null) correction.clear();
      });
    } catch (e) {
      if (mounted) {
        setState(() {
          error = failure(e);
          // A stale revision or lost response requires a fresh GET, never an
          // automatic write replay. Keep the draft for the user's next decision.
          memoryNeedsReload = true;
        });
      }
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  List<Widget> memoryDetails(BuildContext context, Json d) {
    final hit = MemoryHit.fromJson(d);
    return [
      heading(context, hit.title),
      Text(memoryKind(hit)),
      Text('${memoryState(hit)} · Ревизия ${hit.revision}'),
      if (d['excluded'] == true)
        const Text(
          'Не используется в будущих исследованиях. Можно восстановить.',
        ),
      const Text(
        'Актуальная запись. Прежний вывод модели и личное мнение не заменяют проверку статьи.',
      ),
      SelectableText(hit.text),
      if (hit.json['paper_version_id'] case final String paperId)
        TextButton(
          onPressed: () => widget.open('papers', paperId),
          child: const Text('Статья и личная заметка'),
        ),
      if (hit.targetId case final target?)
        TextButton(
          onPressed: () => widget.open('research-memory', target),
          child: const Text('Запись, к которой относится поправка'),
        ),
      for (final origin in hit.origins) ...[
        heading(context, 'Происхождение'),
        Text(
          'Создано: ${origin['created_at']} · ${statusText(origin['status'])}',
        ),
        if (origin['analysis_status'] != null)
          Text('Анализ: ${statusText(origin['analysis_status'])}'),
        if (origin['extraction_status'] != null)
          Text('Извлечение: ${statusText(origin['extraction_status'])}'),
        for (final limit in origin['limitations'] as List? ?? [])
          Text('Ограничение: $limit'),
        if (origin['report_id'] case final String reportId)
          TextButton(
            onPressed: () => widget.open('reports', reportId, memoryId: hit.id),
            child: const Text('Открыть прошлый отчёт'),
          ),
        if (origin['paper_version_id'] case final String paperId)
          TextButton(
            onPressed: () => widget.open('papers', paperId),
            child: const Text('Открыть исходную статью'),
          ),
        ExpansionTile(
          title: const Text('Покрытие и идентификаторы происхождения'),
          children: [
            SelectableText(const JsonEncoder.withIndent('  ').convert(origin)),
          ],
        ),
      ],
      for (final id in hit.evidenceIds)
        TextButton(
          onPressed: () => widget.open('evidence', id),
          child: Text('Исходный фрагмент: $id'),
        ),
      heading(context, 'Актуальные заметки и поправки'),
      if (rows(d['annotations']).isEmpty)
        const Text('Связанных заметок и поправок нет.'),
      for (final annotation in ReportMemory.hits(d['annotations']))
        MemoryCard(hit: annotation, open: widget.open),
      if (hit.kind != 'user_correction') ...[
        TextField(
          controller: correction,
          enabled: !busy && !memoryNeedsReload,
          minLines: 2,
          maxLines: 5,
          maxLength: 4000,
          decoration: const InputDecoration(
            labelText: 'Явная поправка',
            helperText:
                'Оспаривает запись; исходный отчёт и статья сохраняются.',
          ),
        ),
        FilledButton(
          onPressed: busy || memoryNeedsReload ? null : () => patchMemory(),
          child: const Text('Исправить'),
        ),
        OutlinedButton(
          onPressed: busy || memoryNeedsReload
              ? null
              : () => patchMemory(excluded: d['excluded'] != true),
          child: Text(
            d['excluded'] == true
                ? 'Восстановить использование'
                : 'Не использовать',
          ),
        ),
      ] else
        const Text(
          'Изменение или исключение поправки выполняется через исходную запись.',
        ),
      TextButton(
        onPressed: busy ? null : load,
        child: const Text('Загрузить актуальную ревизию'),
      ),
      if (notice.isNotEmpty) Semantics(liveRegion: true, child: Text(notice)),
    ];
  }

  Future<void> launch(String value) async {
    final uri = Uri.tryParse(value);
    try {
      if (uri == null ||
          uri.scheme != 'https' ||
          uri.userInfo.isNotEmpty ||
          uri.host.isEmpty ||
          !await launchUrl(uri, mode: LaunchMode.externalApplication)) {
        throw const FormatException();
      }
    } catch (_) {
      if (mounted) {
        setState(
          () => error =
              'Не удалось открыть оригинал. Можно скопировать ссылку ниже.',
        );
      }
    }
  }

  Widget link(String value) => Column(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      TextButton.icon(
        onPressed: () => launch(value),
        icon: const Icon(Icons.open_in_new),
        label: const Text('Открыть оригинал'),
      ),
      SelectableText(value),
    ],
  );
  @override
  Widget build(BuildContext context) {
    final d = data;
    return ListView(
      padding: const EdgeInsets.all(16),
      children: [
        if (busy) const LinearProgressIndicator(),
        if (error.isNotEmpty) problem(error, d == null ? load : null),
        if (d != null)
          ...switch (widget.kind) {
            'reports' => report(context, d),
            'research-memory' => memoryDetails(context, d),
            'evidence' => [
              heading(context, d['title'] as String),
              Text(coordinates(d)),
              Text('Извлечение: ${statusText(d['extraction_status'])}'),
              const SizedBox(height: 12),
              SelectableText(d['text'] as String),
              const SizedBox(height: 12),
              SelectableText('Evidence: ${d['evidence_id']}'),
              link(d['source_url'] as String),
              TextButton(
                onPressed: () =>
                    widget.open('papers', d['paper_version_id'] as String),
                child: const Text('Статья и заметка'),
              ),
            ],
            _ => [
              heading(context, d['title'] as String),
              Text((d['authors'] as List? ?? []).join(', ')),
              Text('arXiv ${d['arxiv_id']} · версия ${d['version']}'),
              heading(context, 'Аннотация'),
              SelectableText(d['abstract'] as String),
              link(d['abstract_url'] as String),
              for (final doc in rows(d['documents']))
                Card(
                  child: Padding(
                    padding: const EdgeInsets.all(12),
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          '${doc['format']}: ${statusText(doc['extraction_status'])}',
                        ),
                        Text(
                          'Индекс: ${statusText(doc['index_status'])} · ${doc['indexed_chunks']}/${doc['total_chunks']} фрагментов',
                        ),
                        if (doc['index_error'] != null)
                          Text('Ошибка индекса: ${doc['index_error']}'),
                        for (final warning in doc['warnings'] as List? ?? [])
                          Text(
                            'Предупреждение извлечения: ${jsonEncode(warning)}',
                          ),
                      ],
                    ),
                  ),
                ),
              const Text(
                'Извлечение и индексирование не означают полного анализа. Покрытие анализа указано в отчёте.',
              ),
              SwitchListTile(
                title: const Text('Сохранить статью'),
                value: saved,
                onChanged: busy
                    ? null
                    : (v) => setState(() {
                        saved = v;
                        notice = '';
                      }),
              ),
              SwitchListTile(
                title: const Text('Отметить прочитанной'),
                value: read,
                onChanged: busy
                    ? null
                    : (v) => setState(() {
                        read = v;
                        notice = '';
                      }),
              ),
              const Text(
                'Личная заметка — ваше мнение или предпочтение, а не научный факт. Она может использоваться в будущих исследованиях. Очистка убирает её из будущего поиска; прежние снимки запусков сохраняются.',
              ),
              TextField(
                controller: note,
                enabled: !busy,
                minLines: 3,
                maxLines: 8,
                maxLength: 10000,
                decoration: const InputDecoration(labelText: 'Личная заметка'),
                onChanged: (_) => setState(() => notice = ''),
              ),
              FilledButton(
                onPressed: busy ? null : save,
                child: const Text('Сохранить заметку и отметки'),
              ),
              if (notice.isNotEmpty)
                Semantics(liveRegion: true, child: Text(notice)),
            ],
          },
      ],
    );
  }

  List<Widget> report(BuildContext context, Json d) => [
    const Text(
      'Ответ помощника. Утверждения проверяйте по исходным фрагментам.',
    ),
    if (widget.memoryId != null) ...[
      heading(context, 'Актуальное состояние памяти'),
      const Text(
        'Исторический отчёт сохранён без изменений. Ниже — текущие поправки к открытой записи.',
      ),
      if (memoryError.isNotEmpty) problem(memoryError, load),
      if (currentMemory != null) ...[
        MemoryCard(hit: MemoryHit.fromJson(currentMemory!), open: widget.open),
        for (final annotation in ReportMemory.hits(
          currentMemory!['annotations'],
        ))
          MemoryCard(hit: annotation, open: widget.open),
      ],
    ],
    if (reportMemory case final memory?)
      MemorySection(memory: memory, open: widget.open),
    for (final block in (d['markdown'] as String).split('\n\n'))
      Padding(
        padding: const EdgeInsets.symmetric(vertical: 6),
        child: SelectableText(
          block,
          style: block.startsWith('#')
              ? Theme.of(context).textTheme.titleMedium
              : null,
        ),
      ),
    if (reportMemory case final memory?)
      for (final hit in memory.referenced.where(
        (h) => (d['markdown'] as String).contains('[memory:${h.id}]'),
      ))
        TextButton(
          onPressed: () => widget.open('research-memory', hit.id),
          child: Text('[memory:${hit.id}] — ${hit.title}'),
        ),
    heading(context, 'Ограничения отчёта'),
    for (final limit in d['limitations'] as List? ?? []) Text('• $limit'),
    heading(context, 'Источники и покрытие анализа'),
    for (final source in rows(d['sources']))
      SourceCard(source: source, open: widget.open),
    heading(context, 'Доказательные фрагменты'),
    if (rows(d['citations']).isEmpty)
      const Text('Адресуемых подтверждений нет.'),
    for (final hit in rows(d['citations']))
      Card(
        child: ListTile(
          title: Text(hit['title'] as String),
          subtitle: Text('${coordinates(hit)}\n${hit['evidence_id']}'),
          trailing: const Icon(Icons.chevron_right),
          onTap: () => widget.open('evidence', hit['evidence_id'] as String),
        ),
      ),
  ];
}

class ProfilePane extends StatefulWidget {
  const ProfilePane({super.key, required this.api, required this.auth});
  final ResearchApi api;
  final AuthController auth;
  @override
  State<ProfilePane> createState() => _ProfilePaneState();
}

class _ProfilePaneState extends State<ProfilePane> {
  final interests = TextEditingController();
  bool busy = false, loaded = false;
  String error = '', notice = '';
  @override
  void initState() {
    super.initState();
    load();
  }

  @override
  void dispose() {
    interests.dispose();
    super.dispose();
  }

  Future<void> load({bool save = false}) async {
    if (busy) return;
    setState(() {
      busy = true;
      error = '';
      notice = '';
    });
    try {
      final profile = save
          ? await widget.api.updateProfile(interests.text)
          : await widget.api.profile();
      if (mounted) {
        setState(() {
          interests.text = profile['interests'] as String;
          loaded = true;
          notice = save ? 'Профиль сохранён.' : '';
        });
      }
    } catch (e) {
      if (mounted) setState(() => error = failure(e));
    } finally {
      if (mounted) setState(() => busy = false);
    }
  }

  @override
  Widget build(BuildContext context) => ListView(
    padding: const EdgeInsets.all(16),
    children: [
      heading(context, 'Аккаунт: ${widget.auth.user!.login}'),
      if (busy || widget.auth.busy) const LinearProgressIndicator(),
      if (error.isNotEmpty) problem(error, () => load()),
      if (widget.auth.error != null) problem(widget.auth.error!, null),
      TextField(
        controller: interests,
        enabled: loaded && !busy,
        minLines: 3,
        maxLines: 8,
        maxLength: 4000,
        decoration: const InputDecoration(
          labelText: 'Исследовательские интересы',
        ),
      ),
      FilledButton(
        onPressed: loaded && !busy ? () => load(save: true) : null,
        child: const Text('Сохранить интересы'),
      ),
      if (notice.isNotEmpty) Text(notice),
      OutlinedButton(
        onPressed: widget.auth.busy ? null : widget.auth.refresh,
        child: const Text('Проверить сессию'),
      ),
      TextButton(
        onPressed: widget.auth.busy ? null : widget.auth.logout,
        child: const Text('Выйти и сменить аккаунт'),
      ),
    ],
  );
}
