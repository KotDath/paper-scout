# T05: текст статьи и evidence chunks

`internal/documents` получает публичную **конкретную версию** arXiv. Вызывающий
код T04/T11 предварительно регистрирует `paper_versions.id`; T05 не создаёт
вторую таблицу метаданных и не назначает членство в пользовательской библиотеке.

```go
service, err := documents.New(db.DB, documents.Config{CacheDir: cacheDir})
doc, err := service.Import(ctx, documents.Source{
    PaperVersionID: paperID,
    ArxivID: "2304.03442v1",
    Abstract: abstract,
})
```

Сервис строит HTTPS URL arxiv.org самостоятельно, сначала пробует HTML, затем
PDF. Произвольные URL, пути, неверсионированные IDs, смена host/path при redirect
не поддерживаются. Пользовательский HTTPClient — точка интеграции для transport;
redirect policy сервиса применяется и к нему. Нет скрытых сетевых retries.
Общий limiter каталога/исследования подключается вызывающей стороной, например
через transport; T05 не создаёт независимый каталог или agent tool.

На каждый source download отведено до 60 секунд и 30 MiB. Проверяются HTTP
статус, MIME и PDF signature. Poppler получает частный локальный файл, fixed
argument list без shell и context cancellation. `pdfinfo` и `pdftotext` имеют
предел 30 секунд каждый; stdout ограничен 1/32 MiB, число страниц — 2000.
Командные diagnostics и HTTP response bodies в ошибки не попадают.

HTML parser ищет article/LaTeXML document, исключает navigation, scripts и
скрытые элементы, сохраняет текст в порядке DOM с названиями разделов. Чтобы
не принять metadata/error page за статью, нужны хотя бы 400 букв, три блока
текста и содержательный heading кроме заголовка/abstract/references. Это
консервативный эвристический критерий: сомнительный HTML отправляется в PDF.
Изображения и математическая семантика не интерпретируются.

PDF извлекается в UTF-8 с page breaks. Число полученных страниц должно совпасть
с `pdfinfo`. Пустые страницы сохраняются как пустые segments и предупреждения
`pdf_page_without_text`; малое количество букв на странице и replacement
characters тоже отмечаются. Документ с менее 100 букв непригоден. OCR нет.
Извлечение изображений, формул и таблиц не гарантируется и отмечено warning.

Если оба пути непригодны, сохраняются причины и аннотация с
`extraction_status=abstract_only`; без аннотации статус `failed` и chunks нет.
Отмена родительского context возвращается вызывающему коду, без записи ложного
fallback. `full_text` обозначает технически извлечённый текст, включая
предупреждения; он **не** выставляет analysis coverage, read mark или index status.

Миграция `T05_documents.sql` добавляет `documents` и `chunks`, с внешним ключом
на `paper_versions(id)`. Полный извлечённый текст, segments и warnings находятся
в SQLite. Исходный HTML/PDF пишется атомарным rename в `CacheDir` под SHA-256;
новые файлы имеют права 0600, каталог 0700. Файл не публикуется через API.
Сетевые вызовы и Poppler выполняются до короткой SQL-транзакции. Все chunks
сохраняются вместе с документом. Успешный повторный импорт версии читает SQLite
без network call. Failed/abstract-only можно повторить для восстановления
полного текста; существующие evidence IDs не перезаписываются.

Координаты `char_start`/`char_end` — **Unicode code points, 0-based, end exclusive**,
в `Document.Text`, не байты и не UTF-16 offsets. Segments образуют непрерывное
покрытие текста, пустые страницы имеют одинаковые start/end. Разделители `\n\n`
принадлежат предыдущему segment. Chunk не пересекает страницу/раздел, имеет до
2000 code points, перекрытие 250; splitter предпочитает paragraph/word boundary.
Ни один символ текста не теряется. `page_start/end` для HTML — NULL в SQLite;
`section` для PDF пустой. Chunk IDs `ev_…` стабильны для конкретных paper ID,
source hash, extractor/chunking version и диапазона. Изменение алгоритма требует
изменить `ExtractorVersion`/`ChunkingVersion` и переиндексировать новую версию.
При выборе кэша для `Import` учитываются текущие версии алгоритмов. `Get(id)`
читает сохранённый документ независимо от этих версий, чтобы прежние ссылки
анализа и отчётов сохраняли исходный текст, координаты и evidence IDs.

`Import`/`Get` — внутренний общий технический кэш. T08/T11/API обязаны проверять
ownership через user_papers или run_sources **до** выдачи текста/evidence.
T05 не добавляет незащищённых HTTP endpoints; FTS/embeddings/analysis относятся
к последующим узлам и не объявляются выполненными.

Проверки (без unit tests):

```sh
python3 integration/documents.py
python3 integration/documents.py --live
```

Первый запуск использует HTTP fixture на случайном loopback port, настоящие
Poppler, SQLite и полный набор production-миграций. Проверка композиции T04/T05
в одной БД получает Atom metadata через настоящий arXiv adapter, импортирует
две версии статьи и проверяет metadata/document/evidence IDs, внешние ключи
и кэш после повторного открытия SQLite. Остальные сценарии извлечения
заполняют полные fixture metadata в существующей схеме T04. `--live` выполняет обычный импорт
`2304.03442v1` по сети. Если получен HTML, второй импорт в новую временную БД
намеренно пропускает HTML в проверочном transport и получает меньший настоящий
`https://arxiv.org/pdf/1301.3781v1` по HTTPS в обычном 60-секундном бюджете. Проверяются pages/chunks/offsets
и restart. Пропуск HTML явно отмечается в выводе; PDF не подменяется. Файлы, сборка и временная БД находятся внутри
`.bb/runtime/t05` этого worktree, удаляются после проверки; секреты не нужны.
