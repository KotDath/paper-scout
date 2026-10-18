# T11: инструменты исследования

`internal/researchtools.New(store, arxivClient, documentsService,
retrievalService, analyzer)` возвращает `*agent.Registry` для
`agent.New`. Все зависимости — уже существующие shared services. Новых
таблиц, HTTP маршрутов, provider clients или agent loops нет.
T12 собирает этот реестр с lifecycle worker и `RunWithFinalizer`, сохраняет
отчёт и проверяет его citations. Вызов `AgentRunner.Run` уже сохраняет
assistant/tool историю, usage, события и terminal state, но не создаёт report.

Пять tools имеют JSON schema и серверную валидацию. Owner, conversation и run
не входят в аргументы; перед каждым исполнением проверяется `Scope`, владение,
состояние running и отмена. Неизвестные поля, произвольные URL, превышенные
лимиты и неверные даты отклоняются до исполнения. Выходы — данные, а не
инструкции; это также закреплено системным prompt AgentSession и Analyzer.

- `search_papers(query, from?, to?, limit?)`: arXiv search без неявного
  периода, максимум 10 кандидатов; версии, canonical URLs, cache/attempts,
  `previously_known`, `saved`, `read` только текущего владельца. Сам поиск
  доступа к документам не предоставляет.
- `import_paper(arxiv_id, selection_reason?)`: разрешение latest/exact версии,
  выбор через T10 `SelectSource`, затем T08 `AddPaper`, HTML/PDF/fallback и
  индексирование. Не более трёх версий в run; четвёртая не получает membership
  и не скачивается. Catalog metadata при этом может оказаться в публичном
  кэше. Повторный import не меняет saved/read/note, показывает известную работу.
  Возвращаются paper/document IDs, extraction warnings, cache provenance,
  `index` и отдельный `analysis_status`. Полный текст не вставляется в outer
  историю. Ошибка embeddings сохраняет импортированный источник с явным
  failed/partial index; lexical fallback не называется hybrid.
- `analyze_paper(paper_version_id, question)`: только источник текущего run;
  выбирает документ текущих extractor/chunking versions и вызывает T10 Analyzer.
  Единственный gateway — `Scope.Complete`; его budget/context ошибки переводятся
  в T10 sentinel errors с сохранением response/usage. Прямого DeepSeek вызова
  нет. Ошибка анализа возвращает доступный partial результат с причиной;
  исчерпание общего бюджета всё равно останавливает runner. Модель получает
  analysis ID, coverage/ranges, warnings и наблюдения с исходными evidence IDs.
  Полный durable результат доступен через `Analyzer.Analysis`; статья не
  дублируется целиком в outer history. Повторный analyze — новая оплачиваемая
  попытка, не автоматический resume.
- `search_library(query, lexical_terms?, paper_version_ids?, limit?)`: T08
  hybrid retrieval с owner-фильтрами в обеих ветках. Модель получает до 8 hits
  в 16000 code points, режим, причины и coverage. Полная выдача обеих веток и
  исходный/лексический запрос сохраняются в private event payload; публичное
  `retrieval_completed` содержит фактический mode и размеры списков. Private
  payload здесь содержит retrieval diagnostics, не reasoning.
- `read_passages(evidence_ids)`: 1–8 уникальных IDs, до 16000 code points
  целиком, source URL/section/pages/offsets; ownership проверяется для каждого
  ID. При превышении — passage_limit, без скрытого обрезания. Достаточно сузить
  запрос. Чтение passages не повышает analysis coverage.

Tools не редактируют профиль/заметки/отметки прочтения; для этого предусмотрены
явные пользовательские API. Отчёт не является необязательным tool call.
`none`, `partial`, `complete`, `abstract_only` относятся к Analyzer; index ready
и retrieval_mode описывают другие независимые оси. Формальная адресуемость
citation не означает автоматической проверки научного утверждения.

## Проверки

```sh
GOPROXY=off python3 integration/researchtools.py
DEEPSEEK_MODEL=deepseek-flash DEEPSEEK_THINKING=disabled GOPROXY=off \
  python3 integration/researchtools.py --live \
  --env-file /home/kotdath/omp/personal/ai/paper-scout/.env \
  --artifact "$PWD/.bb/runtime/t11/live-result.json"
```

Оба режима используют реальные production модули, временную SQLite, ephemeral
HTTP ports и настоящую установленную Qwen3 в Ollama. Default подменяет только
внешние arXiv/document/DeepSeek HTTP boundaries собственными fixtures;
это не live evidence модели/статьи. Live использует официальные arXiv и DeepSeek,
свежую БД и статью `1301.3781v1`, затем follow-up в том же диалоге.
Primary ограничен 12 DeepSeek requests (включая вложенные), 180000 tokens,
2048 output/request, без retries. Follow-up имеет те же максимумы, но ожидает
только search/read/answer. Analyzer дополнительно ограничен 6 requests,
12000 code points/part. Общие run/tool deadlines остаются T09.
Dotenv загружает только Go-процесс по пути; значения не печатаются.

Локальный launcher направляет build/module caches, TMPDIR, binary, DB и
документы внутрь `.bb/runtime/t11` этого worktree и удаляет временную DB/cache
после прогона. `--allow-partial` допустим только с `--live`: продолжает smoke
проверку retrieval после явно сохранённого partial с успешным reading prefix;
он не подтверждает полное чтение. Без флага требуется complete.
`--artifact` сохраняет только sanitized public результаты и
retrieval trace, без credentials/provider reasoning. Выбирать путь внутри
этого worktree. Факты и ограничения: [verification/T11.md](verification/T11.md).
