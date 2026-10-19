part of 'workspace.dart';

String memoryKind(MemoryHit hit) => switch (hit.kind) {
  'episode' => 'Прошлое исследование',
  'claim' =>
    'Прежний вывод модели · ${switch (hit.json['facet']) {
      'method' => 'метод',
      'result' => 'результат',
      'limitation' => 'ограничение',
      _ => hit.json['facet'] ?? 'наблюдение',
    }}',
  'user_note' => 'Личная заметка · мнение пользователя',
  'user_correction' => 'Поправка пользователя · требует проверки оригинала',
  _ => hit.kind,
};

String memoryState(MemoryHit hit) => hit.json['excluded'] == true
    ? 'Исключена из будущего поиска'
    : switch (hit.json['state']) {
        'active' => 'Активна',
        'disputed' => 'Оспорена пользователем — проверьте первоисточник',
        'superseded' => 'Заменена новой версией',
        'excluded' => 'Исключена из будущего поиска',
        _ => '${hit.json['state']}',
      };

class MemorySection extends StatelessWidget {
  const MemorySection({super.key, required this.memory, required this.open});
  final ReportMemory memory;
  final OpenResource open;

  @override
  Widget build(BuildContext context) => Column(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      Text(statusText(memory.mode)),
      for (final reason in memory.reasons) Text('Ограничение памяти: $reason'),
      const Text(
        'Снимок материалов, переданных модели в этом запуске. Это не доказывает их использование в ответе. Детали показывают актуальную запись.',
      ),
      ExpansionTile(
        title: Text(
          'Найдено в прошлых исследованиях · ${memory.recalled.length}',
        ),
        children: [
          if (memory.recalled.isEmpty)
            const Text(
              'Материалы не найдены или не переданы. Проверьте режим и ограничения выше.',
            ),
          for (final hit in memory.recalled) MemoryCard(hit: hit, open: open),
        ],
      ),
      ExpansionTile(
        title: Text('Ссылки в ответе · ${memory.referenced.length}'),
        subtitle: const Text(
          'Проверены ссылки на происхождение. Научные выводы проверяйте по исходным фрагментам.',
        ),
        children: [
          if (memory.referenced.isEmpty)
            const Text('Ответ не содержит проверенных ссылок на память.'),
          for (final hit in memory.referenced) MemoryCard(hit: hit, open: open),
        ],
      ),
    ],
  );
}

class MemoryCard extends StatelessWidget {
  const MemoryCard({super.key, required this.hit, required this.open});
  final MemoryHit hit;
  final OpenResource open;

  @override
  Widget build(BuildContext context) => Card(
    child: Padding(
      padding: const EdgeInsets.all(12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(hit.title, style: Theme.of(context).textTheme.titleMedium),
          Text(memoryKind(hit)),
          Text('${memoryState(hit)} · Ревизия ${hit.revision}'),
          SelectableText(hit.text),
          for (final origin in hit.origins) ...[
            Text(
              'Исследование: ${statusText(origin['status'])} · ${origin['created_at']}',
            ),
            if (origin['analysis_status'] != null)
              Text('Анализ: ${statusText(origin['analysis_status'])}'),
            for (final limit in origin['limitations'] as List? ?? [])
              Text('Ограничение: $limit'),
          ],
          TextButton(
            onPressed: () => open('research-memory', hit.id),
            child: const Text('Открыть запись памяти'),
          ),
        ],
      ),
    ),
  );
}
