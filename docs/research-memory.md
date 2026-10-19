# Память между исследованиями

Статус: T19–T21 реализуют хранилище, capture/consolidation, hybrid search,
automatic recall в production и Flutter. T22 завершил implement-приёмку с
реальными arXiv/DeepSeek/Ollama: A → note → OS restart → новый диалог B,
проверенные primary reads и дословная note; lifecycle, semantic correction,
negative recall, isolation и отдельный memory-off control. Полное новое видео
и точные ограничения: [T22](verification/T22.md). Ревью/упрощение/итоговый контроль
координатора ещё следуют; это не merged/done DAG.
Выбор подхода обоснован в [memory-approaches.md](memory-approaches.md).
Прежняя сдача дня35 и failed T22 recordings сохранены отдельно.

Уточнение отбора T22: если лучший hit paper-group — user_note, сначала
пробуется лучший scientific claim того же paper из текущего owner-filtered
BM25/dense union. Note присоединяется к нему как mandatory annotation.
Так note не вытесняет report/evidence origin единственным местом группы.
Ранги/score кандидатов не переписываются. Нет подходящего/помещающегося claim —
остаётся standalone note; она не получает выдуманного scientific provenance.

## Пользовательский результат

После исследования A пользователь оставляет заметку. После перезапуска он
создаёт **другой диалог** и исследует работу B, не называя A. Агент самостоятельно
находит релевантное прежнее исследование, учитывает заметку, сравнивает A и B
и даёт ссылки на прежний отчёт и исходные фрагменты обеих работ.

Пример: «В MemoryBank вы уже изучали забывание по кривой Эббингауза и отметили,
что важны проверки в длительных диалогах. В новой работе механизм другой;
вот различия и границы экспериментальных подтверждений». Это пример формы
ответа; реальные различия при приёмке проверяются по двум источникам.

Нельзя закрыть сценарий обычным follow-up в прежнем диалоге, ручной передачей
названия A, готовым сравнением в prompt или простым показом сохранённой заметки.

## Первая реализация

Используем три уровня: **эпизоды → атомарные записи → рабочий контекст**.
Сохраняем исходное исследование, извлекаем компактные сведения с происхождением,
сопоставляем с прежними, а перед следующим исследованием автоматически читаем
релевантную память. Поиск целых Markdown-отчётов без write/consolidation фазы
не закрывает новый результат.

Три вида записей имеют явное происхождение:

- `episode`: вопрос, название диалога, прежний отчёт, время, источники и coverage.
  Исходная сущность остаётся `reports`; это маршрут к опыту исследования.
- `claim`: одно наблюдение с facet `method|result|limitation`, analysis ID,
  paper/document ID, evidence IDs и фактическим coverage. Источник — **уже
  сохранённый** `Analysis.Synthesis`, иначе только observations успешных частей.
  Это прежний вывод модели с опорой на статью, не безусловно истинный факт.
- `user_note` / `user_correction`: заметка пользователя или явная поправка к
  конкретной записи; authority=user, версия, время и scope. Это мнение или
  предпочтение пользователя, не утверждение авторов статьи.

Существующие `Observations{Methods,Results,Limitations}` уже получаются при
анализе и содержат `Claim{Text,EvidenceIDs}`. Поэтому первая write-фаза не
требует новой LLM: bounded извлечение, валидация и консолидация выполняются Go.
Новый отдельный «memory agent» и второй provider client не добавляются.

Write/consolidation после materialize отчёта: ADD новой записи; NOOP для
точного дубликата `(owner,paper,facet,content_hash)` с сохранением множества
origin-ссылок; новая ревизия только при явном изменении соответствующего
источника/заметки. Источники разных научных результатов сосуществуют.
Семантическое сходство — кандидат на связь `related`, а не основание слияния
или объявления противоречия. Связь не доказывает научного различия A/B.
Автоматические semantic UPDATE/DELETE и переписывание scientific claims
откладываются; их безопасность ещё не проверена нашим приложением.

Явная correction помечает научную derived-запись `disputed`; исходный эпизод и
ревизия сохраняются, актуальный recall обязательно отдаёт поправку и provenance
вместе со спорным claim и требует проверки оригинала. Это не объявляет
пользовательский научный комментарий доказанной истиной. `superseded` применяется
к действительно заменённой версии пользовательской заметки/поправки. Возврат
к старой записи из backfill не должен отменять correction или forget.

Профиль продолжает передаваться отдельно. Отметки saved/read помогают отличать
известные работы, но сами не считаются обратной связью о качестве исследования.
Пустая заметка удаляет её из будущего recall; изменение не оставляет старую
версию в текущем индексе. Доступны explicit exclude/restore записи и correction.
Снимки прежних запусков сохраняются для объяснения, какие данные тогда были
показаны модели. User-scoped журнал ревизий не становится общей памятью.

## Хранение и retrieval

`internal/researchmemory` содержит bounded запись/консолидацию и поиск по
памяти. Это часть того же Go-приложения, не отдельный агент/сервис. SQLite,
FTS5/BM25, **существующий** EmbeddingClient Qwen3 и RRF; никаких memory/agent
frameworks. Индекс хранит owner, origin, версию/hash, ограниченные chunks,
FTS и совместимые embeddings. Report IDs, paper IDs и evidence IDs не меняются.

Записи исходных эпизодов/claims можно восстановить из reports/analyses:
существующие исследования T01–T18 доступны без повторного AI-анализа.
Явные user corrections/exclusions — самостоятельные данные, rebuild обязан
их сохранить. Не индексировать private provider reasoning,
failed/cancelled/interrupted сообщения и чужие данные. Допустимы completed
отчёты и persisted partial-отчёты, но их реальный статус/coverage сохраняется.

После нового отчёта запускается идемпотентный capture (без DeepSeek).
Если процесс оборвался между report commit и capture, следующий recall
восстанавливает пропущенную запись из origin; оплачиваемый анализ не повторяет.
Write-сбой не теряет готовый отчёт и виден в статусе памяти. Перед новым run
поиск актуализирует изменённые проекции в ограниченном объёме,
с context/cancellation. Вызовы Ollama вне SQL-транзакций. Изменение origin во
время inference не публикует устаревший ready. Незавершённая индексация и
lexical-only деградация явные; недоступность памяти не маскируется как «истории
нет». Не терять отчёт, если embeddings временно недоступны. Индексировать
пакетами, не делать неограниченный rebuild всей библиотеки на каждый вопрос.
Correction/exclusion применяется ко всем представлениям target: нельзя обойти её
возвратом того же старого утверждения из episode/дубликата. При чтении исходного
исторического отчёта UI/контекст явно указывают актуальные поправки; исходник
не изменяется. Консолидация не повышает статус claim до проверенной истины.

Обе ветки фильтруют owner **до** top-k. Исключить текущий run и будущие данные.
Сначала 20 lexical / 20 dense кандидатов, RRF k=60; дедупликация origin,
максимум 5 origins и 12000 Unicode-символов в автоматически добавленном
контексте. Длинные отчёты разбиваются на passages с координатами; ограничение
контекста не называется полным чтением отчёта. Актуальные личные заметки и corrections
присоединяются по найденным paper/target детерминированно; они не конкурируют
с chunks за top-k. Ограничение 12000 символов включает эти дополнения; при
нехватке бюджета уменьшить число обычных hits, а не скрыть correction. Оценка релевантности не сводится к одному факту top-k:
при отсутствии подходящего материала не выдумывать связь.

## Контракт, фиксируемый T19

T19 реализует и документирует точные экспортируемые Go-типы для следующих
нод. Не изменяет AgentRunner или Flutter. Публичные JSON-поля фиксированы:

```json
{
  "query": "current research question",
  "retrieval_mode": "hybrid",
  "reasons": [],
  "hits": [{
    "memory_id": "opaque server ID",
    "kind": "claim",
    "facet": "limitation",
    "authority": "model",
    "state": "active",
    "revision": 1,
    "report_id": "report_previous",
    "conversation_id": "conversation_previous",
    "paper_version_id": null,
    "title": "Previous research",
    "text": "Bounded recalled passage",
    "content_hash": "sha256",
    "evidence_ids": ["ev_original"],
    "lexical_rank": 1,
    "dense_rank": 2,
    "rrf_score": 0.0325
  }]
}
```

Неактуальные поля для kinds могут быть null/отсутствовать. IDs и scores
выше демонстрационные. Сохраняются фактические status, coverage и
limitations; notes явно маркируются `kind=user_note`. T19 не должен считать текст
старого отчёта научным evidence. `evidence_ids` — проверенные ссылки на
оригинальные passages, а не ссылки на сгенерированный пересказ.

HTTP: `POST /api/v1/research-memory/search` (`query`, `limit<=5`),
`GET /api/v1/research-memory/{memory_id}` и
`PATCH /api/v1/research-memory/{memory_id}`. PATCH принимает либо
`excluded: bool`, либо `correction: string` (не одновременно), версия текущей
записи `expected_revision` обязательна; устаревшая правка=409, чужой ID=404.
Correction создаёт явную user-ревизию с указанием target, не редактирует
оригинальную статью/отчёт. Пустая correction недопустима; границы текста T19
фиксирует в контракте, максимум 4000 Unicode-символов. Только auth owner.
Ответ GET возвращает текущий доступный origin/passages и ссылки на существующий
отчёт/статью, не cache paths или provider payloads. Чтение обычных отчётов
остаётся через существующий `GET /api/v1/reports/{id}`.

## Подключение к агенту — T20

Перед **первым** Chat Completions нового run выполняется recall по текущему
вопросу автоматически, независимо от решения модели вызвать tool. Материал
передаётся как данные с provenance. После получения методов/результатов новой
статьи допускается ещё **одна** автоматическая точка recall по её содержанию,
чтобы находить A даже по вопросу с одним неизвестным названием B. Итого максимум
две автоматические точки на run; это не новый LLM-вызов. Дополнительный recall
не открывает пользовательской заметке доступ к чужому origin. Профиль, retrieval и статьи не могут
переопределять системные правила. Пустая память — допустимый явный результат.

Модель получает правило: при содержательной релевантности сравни с прошлой
работой, явно отдели предпочтение пользователя, прежний вывод и факт новой
статьи; проверь научное сравнение по исходным passages через existing tools.
Не приписывай пользователю автоматически выведенные предпочтения. Разрешён
дополнительный tool `search_research_history` с тем же контрактом и бюджетом,
но он не заменяет автоматический recall.

Используй маркер `[memory:<memory_id>]` для ссылки на прошлое исследование или
заметку. Сервер проверяет, что origin принадлежит пользователю и memory_id
был выдан **этому** run. Одного упоминания похожего заголовка недостаточно.
Невалидный memory ID не публикуется как подтверждённая ссылка; ограничение
показывается явно. Существующие `ev_...` проверяются своим прежним механизмом.

Сохраняется снимок фактически выданных модели hits/hash/text/provenance и
режима поиска, а не только ссылка на изменяемую заметку. Публичное событие
`memory_recalled` несёт режим и ограниченные hits. Отчёт API получает поле:

```json
{
  "research_memory": {
    "retrieval_mode": "hybrid",
    "reasons": [],
    "recalled": [],
    "referenced": []
  }
}
```

`recalled` — реальные выданные материалы; `referenced` — проверенные origins,
на которые сослался финальный ответ. Не называть все recalled «учтёнными»:
передача в контекст не доказывает смысловое использование. Поле nullable для
исторических отчётов без снимка. Report/terminal commit и связанные снимки
должны сохранять согласованность при отмене и сбое. T20 фиксирует окончательный
typed contract в этом документе, не вводя второй отчёт или отдельный agent loop.

Capture отчёта и актуализация заметки также обязательны в production.
Обязательное подключение — **cmd/paper-scout**. Старые integration callers
сохраняют совместимость; bounded live harness не является единственным местом,
где функция работает. Пределы production позволяют выполнить приёмку с явным
конечным бюджетом; не отключать ограничения и не делать скрытых оплачиваемых
повторов. Новые обращения только через существующий DeepSeekClient/gateway.

## Flutter — T21, параллельно T20 после T19

Экран отчёта различает «Найдено в прошлых исследованиях» и «Ссылки в ответе».
Для каждого origin доступны тип, название, фрагмент, статус/ограничения и
переход к прошлому отчёту или пользовательской заметке/статье. Старый отчёт
открывается без потери выбранного нового run. До появления research_memory
поле отсутствует без ошибки: T21 разрабатывается параллельно T20.

Изменение заметки остаётся в существующем UI. В деталях найденной записи
доступны «Исправить» / «Не использовать» / восстановить исключённую запись,
provenance и revision; не вводить отдельный обязательный большой экран.
Показать, что заметка может использоваться в будущих исследованиях.
Не показывать lexical-only как hybrid. При 401 смене
аккаунта очищаются также memory links и previews.

## Приёмка — T22

1. Обычный `cmd/paper-scout`, настоящие DeepSeek/arXiv/Ollama; Flutter через
   claude-in-mobile. Исследование A по памяти агентов, проверяемый отчёт и
   исходные citations, хотя бы одна полная доступная статья.
2. Пользователь оставляет содержательную заметку о важном для него критерии,
   например оценке на длительных диалогах. Это не готовое сравнение A и B.
3. Перезапуск Go/Flutter. **Новый диалог**, запрос по другой реальной работе B
   в той же теме без названия A, его report ID или подсказки «вспомни A».
4. До первого запроса модели записан automatic recall A и заметки. В ответе
   осмысленное отличие A/B, влияние заметки и проверенные ссылки на прежний
   отчёт и исходные passages обеих работ. Не требовать заранее выдуманного
   различия: три содержательных утверждения сверяются с реальными источниками.
5. Открыть прошлый отчёт из нового; показать память и provenance в UI.
6. В write-журнале A есть реально созданные atomic method/result/limitation
   cards; повторный capture даёт NOOP, origins не теряются. Явное исправление/
   exclusion не отменяется рестартом/backfill. Изменённая/очищенная заметка не
   возвращается старым текстом в будущем recall; исторический снимок остаётся
   точным. Нерелевантный вопрос не даёт выдуманного
   сравнения. Чужая память недоступна аккаунту B, включая прямой HTTP/оба top-k.
7. Адресные интеграции проверяют: два противоречащих результата разных статей
   не объединяются в «последнюю правду»; старый неверный claim с существующим
   evidence ID не принимается на веру, проверка оригинала/пользовательская
   correction меняет будущий ответ; отключённый recall не получает личную
   заметку и прошлое исследование в контексте. Контроль не выдаётся за общий
   benchmark качества. Отказы/races допустимы через явно отмеченные fixtures.
8. Сохранить новый артефакт и **отдельное видео** `artifacts/research-memory-demo.mp4`,
   не перезаписывать принятую запись дня35. Должны быть видны новый диалог,
   самопроизвольная связь с A, заметка, сравнение и переход к источникам.

Live-проверка обязательна; fixtures допустимы для гонок, отсутствия зависимости,
отмены и лимитов. Unit/widget tests запрещены. Текст статьи не обязан быть новым
network fetch при каждом повторе, но кэш и свежие вызовы различаются явно.
Покрытие, бюджеты, платные попытки и неудачи документируются честно. Итоговые
README/runbook/acceptance обновляются по фактическим результатам T22.

## DAG продолжения

`T18 → T19 → {T20, T21} → T22`.

Каждая нода: worktree/ветка, исполнитель, два review/fix, отдельный упрощатель,
итоговый контроль и сериализованное слияние по прежнему execution-protocol.


## Реализованный Go/HTTP-контракт T19

Исходные определения: `internal/researchmemory/types.go`. Конструктор
`New(db *sql.DB, embed *embeddings.EmbeddingClient) *Service`; nil encoder
означает явный `lexical_only`. Используется существующий pinned клиент Qwen3,
дополнительного клиента генерации нет. `httpapi.NewWithRuns` регистрирует память
с тем же encoder, что передан существующему retrieval; `RegisterResearchMemory`
поддерживает самостоятельно составленные серверы. Обычный cmd получает эти
маршруты через существующий NewWithRuns. AgentRunner и Flutter в T19 не меняются.

Экспортируемые операции (все принимают `context.Context` первым аргументом):

| Операция | Остальные аргументы | Результат |
| --- | --- | --- |
| `Capture` | `owner, reportID string` | `(RefreshResult, error)` — атомарная проекция одного persisted completed/partial отчёта |
| `Refresh` | `owner string` | `(RefreshResult, error)` — максимум 16 изменённых notes и 2 reports |
| `Rebuild` | `owner string` | `error` — только requeue reports этого владельца, затем bounded Refresh; user-данные и IDs сохраняются |
| `Index` | `owner string, req SearchRequest` | `(IndexResult, error)` — максимум 32 passages / один document batch |
| `Search` | `owner string, req SearchRequest` | `(SearchResult, error)` — Refresh + Index + query inference + свежий согласованный SQL snapshot |
| `Get` | `owner, memoryID string` | `(Entry, error)` — текущая запись, passages, annotations, provenance |
| `Patch` | `owner, memoryID string, p Patch` | `(Entry, error)` — explicit correction либо exclude/restore |

`SearchRequest` имеет `Query string`, `Limit int` (0=5, иначе 1..5), а также
серверные `ExcludeRunID string`, `Before time.Time` с `json:"-"`. Они запрещены
в HTTP body вместе с любыми неизвестными полями. T20 передаёт текущий run ID и
время создания run: будущие origins/notes/corrections исключаются. Если Before
не задан, используется время после bounded Refresh. Capture ставит timestamp
эпизода/claim по времени исходного отчёта, а не позднего backfill. При правке
время текущей ревизии меняется; исторический cutoff не получает будущую правку.

`Hit` содержит все поля JSON-примера выше; `facet` выдаётся только для claim.
Дополнительные поля: `target_id` для user_correction, `origins []Origin`,
`char_start/char_end` (Unicode, полуинтервал в memory text), `bm25`, `cosine`.
`content_hash` — SHA-256 полного актуального текста записи; для claim текст
нормализован по whitespace. Passage имеет свой внутренний hash точного
embedding input. Короткий passage и полный memory text не отождествляются.
`authority` = `model` для claims, `research_history` для эпизода, `user` для
notes/corrections. Ни одно значение не обозначает доказанную научную истину.

`Origin` содержит `report_id`, `run_id`, `conversation_id`, `created_at`,
`status` исходного run; для научной карточки — `analysis_id`, `document_id`,
`paper_version_id`, `analysis_status`, `extraction_status`, `evidence_ids`.
`coverage` — сохранённая JSON-проекция фактических диапазонов/статуса/warnings,
`limitations []string` — ограничения отчёта. Для эпизодов origins также
перечисляют статьи и их coverage. Evidence проверяется по существующему chunk
и document при capture и чтении, generated Markdown evidence не становится.
Документы/статьи/отчёты остаются прежними сущностями и IDs. Кэш-пути, provider
payloads, input_json и reasoning в память/API не проецируются.

`SearchResult` возвращает `query`, `retrieval_mode`, `reasons []string`,
`hits []Hit`, диагностические `lexical_candidates []Hit`, `dense_candidates
[]Hit`, `index_coverage float64`, `refresh RefreshResult`. Массивы не null.
`RefreshResult`: `processed`, `added`, `noop`, `pending` (int), `reasons`.
`IndexResult`: `attempted`, `published` (int), `reasons`. Coverage индекса
описывает совместимые векторы в текущем разрешённом scope, а не чтение модели.
Для пустой памяти coverage=0; успешный query inference всё равно проверяет
Ollama, а отсутствие данных не подменяет сбой зависимости.

Обе ветки выбирают до 20 passages с owner/cutoff/exclusion фильтрами **до**
top-k. Exact cosine, затем RRF `1/(60+rank)`, устойчивый tie-break по memory ID
и координате. Search ограничен 20000 разрешённых passages; превышение —
`ErrScope` / HTTP400 `memory_scope_limit`, без молчаливого усечения scope.
Origins одной карточки читаются с явным пределом 64 (превышение тоже ErrScope).
В контекст допускаются до `limit` групп papers/reports и максимум 5 различных
report origins. Notes/corrections добавляются по связи с найденными targets/
papers; они могут увеличить длину hits сверх limit. Сумма **текстов** всех hits
не больше 12000 Unicode-символов; T20 при форматировании должен дополнительно
учесть metadata в общем бюджете prompt. Пустые/нерелевантные кандидаты не являются
доказательством содержательной связи. Как в T08, cosine не имеет калиброванного
порога релевантности; решение о научном сравнении требует исходных passages.

Текущая note (до существующих 10000 символов) передаётся целиком, даже когда
поиск выбрал лишь её passage. Сначала проверяется стоимость обязательных
annotations: если группа вместе с ними не помещается, пропускается обычный hit,
correction не обрезается и не скрывается. Это включает поправки к самим
присоединённым user_note: сначала строится вся обязательная группа
claim → note → correction, затем рассчитывается её бюджет. Причина пропуска
слишком большой группы — `context_budget`. Поиск связей может просмотреть все
origins консолидированного claim, чтобы найти поправку к пропущенному эпизоду.
Каждая correction получает фактическое происхождение **своего target**, как при
GET этой correction: report/run/conversation и evidence IDs другого найденного
claim ей не приписываются. User_note и correction к ней не получают научные
evidence/report origins, которых у личной заметки нет.

После раскрытия всех обязательных annotations Search сначала резервирует
первый (канонический, совпадающий с GET) origin каждой записи группы, затем
заполняет оставшиеся места дополнительными действительными origins. Поправка
к седьмому эпизоду вытесняет необязательный origin claim, сохраняя свой настоящий
target. Публикуется только подмножество собственных origins каждой записи;
report_id/conversation_id и evidence_ids вычисляются из этого подмножества.
Существующие origins ранее опубликованных групп также входят в общий предел
пяти разных reports. Если первичные origins всех обязательных поправок вместе
с группой не помещаются, вся группа пропускается с `origin_budget`: поправка
не скрывается и не получает выдуманное происхождение. GET продолжает показывать
полное доступное происхождение target, включая не вошедшие в Search origins. Причины
`capture_backlog`, `episode_projection_truncated`, `claim_projection_truncated`,
`claim_evidence_unavailable`, `origin_budget`, `source_changed`,
`index_incomplete_or_incompatible` и коды существующего EmbeddingClient явные.
Индексирование ограничено одним batch на search; поэтому большой backlog даёт
`hybrid_partial`, следующие вызовы продолжают его. Ошибка query inference —
`lexical_only`. Сетевых вызовов внутри SQL-транзакций нет.

`Entry` — поля Hit плюс `excluded bool`, `passages []Hit`, `annotations []Hit`.
GET разрешает владельцу открыть исключённую запись и восстановить её. Старый
эпизод показывает актуальные corrections в annotations, не меняя report text.
При чтении исторического отчёта T20/T21 используют эту текущую проекцию;
обычный GET /reports продолжает возвращать исходный immutable отчёт.

`Patch{ExpectedRevision int, Excluded *bool, Correction *string}`:
обязательна положительная revision и ровно одно действие. Correction — 1..4000
Unicode-символов, не whitespace; текст сохраняется точно. Правка target создаёт
или обновляет стабильную user_correction с target_id, отдельной revision и
user authority; target получает `disputed` и новую revision. Старые снимки обеих
записей остаются в research_memory_revisions. Повторная correction делается
через **target ID** и его актуальную revision; PATCH самой correction запрещён
(400), чтобы нельзя было скрыть её отдельно от disputed target. Exclude/restore
также увеличивает target revision и сохраняет snapshot. Старая revision=409
`revision_conflict`; чужой/несуществующий ID=404 `not_found`; нет сессии=401.
Некорректный JSON/действие=400 `invalid_input`; сбой памяти=503
`memory_unavailable`, а не пустая история. Если обязательная заметка ещё ждёт
проекции (включая backlog после 16 note jobs или правку во время inference),
возвращается `ErrPending` / HTTP503 `memory_pending`, retryable=true. Это также
относится к устаревшей доступной владельцу note в GET/PATCH: состояние не
выдаётся за отсутствие ресурса. Search не публикует claim без актуальной
обязательной заметки; следующий bounded Refresh может завершить проекцию.
Context/cancellation передаются всем
операциям; сетевые вызовы используют существующие пределы EmbeddingClient.

Проекция защищена консервативно: свободные episode passages пользователя не
участвуют в recall после появления/изменения его note (включая очистку) либо
при наличии другой disputed/excluded записи. Это защита на уровне owner:
поздний отчёт с другим набором выбранных статей тоже может пересказать старую
заметку/ошибочный claim. Только paper-фильтр не защищал бы от такого обхода.
Сам disputed episode допустим вместе со своей correction; исходный свободный
пересказ не может обойти исправление/forget через другой episode. Научные карточки с текущими annotations и исходный report остаются
доступны. Exclude эпизода также исключает все его derived cards, включая
консолидированные дубликаты из другого report. Correction эпизода присоединяется
к его derived cards; correction отдельного claim применяется к тому же stable
ID во всех origins. Это намеренное уменьшение recall, не semantic deletion.

Миграция T19 создаёт durable jobs для существующих reports/notes. Report INSERT
добавляет pending capture в той же исходной транзакции. Note INSERT/UPDATE
синхронно сохраняет каждое явное изменение в owner-scoped note_history,
увеличивает source generation и удаляет прежние passages/vectors. Даже серия
правок до Refresh не теряет исходные пользовательские версии. Sync пишет
актуальную memory revision; old text не попадает в текущий индекс. Publication
embedding проверяет passage hash и revision после inference. Capture/rebuild
не сбрасывает excluded/disputed, user corrections и историю notes.

Bounded capture: до 32 analyses, 48 claims / 96000 суммарных символов, отдельный
claim до 4000; episode до 64 passages по 1200 символов. Непоместившееся
содержимое остаётся в исходном report/Analysis, а ограничение записывается в
статус job и search.refresh.reasons. Synthesis имеет приоритет; иначе берутся
только успешные reading segments, с объединением реальных диапазонов и partial.
Claim без валидного evidence не индексируется. Точный NOOP не создаёт новую
карточку, но сохраняет дополнительный origin нового исследования. Различающиеся
claims разных статей не объединяются автоматически.

Для T20: вызвать Capture после атомарного report commit; сбой памяти сохранить
как ограничение, не терять report. Search сам восстанавливает пропущенный
capture из jobs без DeepSeek. Автоматические точки recall, run snapshots,
проверка `[memory:...]`, report.research_memory и public memory_recalled event
остаются задачей T20. Для T21: использовать Entry/Patch и текущую revision;
notes отделять от научных данных, origins открывать через существующие IDs.

Проверки T19 и их границы: [verification/T19.md](verification/T19.md).

## Итоговое видео всех функций (уточнение пользователя)

Последнее явное указание пользователя: «после исправлений перезапиши видео где
все фичи будут видны». Это расширяет T22 до нового полного пользовательского
walkthrough и имеет приоритет над прежним узким роликом только A/B memory.
Запись начинается после исправлений и успешного ревью/слияния T19–T21.
Новых продуктовых функций в T19 это уточнение не добавляет.

T22 использует обычный `cmd/paper-scout` и итоговый Flutter APK на эмуляторе.
`integration/researchtools` не является основным сервером этой демонстрации.
Основной исследовательский проход выполняется live с настоящими источниками,
DeepSeek и Ollama. Результат — `artifacts/paper-scout-full-demo.mp4` и читаемый
сценарий/таблица «функция → действие → ожидаемый результат → таймкод → supporting
artifact». Старые `day35-demo.mp4` и исследовательские артефакты не
перезаписываются. Новая полная запись становится основной ссылкой README/сдачи.

До записи сопоставить чек-лист с фактически реализованными README, API и
экранами. В итоговой матрице отмечать пункт показанным только при наличии
соответствующей сцены/кадра; интеграционный лог сам по себе не заменяет видео.
Минимальные сцены:

1. Настройка адреса API, вход/выход, понятная ошибка неверного входа; профиль
   интересов пользователя.
2. Создание и история диалогов, исследовательский запрос, реальный поиск arXiv,
   выбор/анализ источников, наблюдаемый прогресс, отчёт и usage/coverage.
3. Полное чтение доступной статьи, предупреждения извлечения, источники/evidence,
   координаты фрагментов и переход к оригиналу. Полный текст, индекс и RAG
   показываются как разные характеристики, не как взаимозаменяемое доказательство.
4. Уточняющий вопрос, настоящий BM25+Qwen3+RRF, цитаты; доступ к прошлому отчёту
   во время выполняющегося follow-up; отмена активного запуска и возможность
   продолжить работу.
5. Библиотека и поиск, saved/read и личная заметка; сохранение этих данных после
   перезапуска Go и Flutter.
6. Новая память: создание semantic method/result/limitation записей из A;
   заметка → рестарт → **новый диалог** с другой статьёй B без имени/ID A → агент
   самостоятельно связывает A/B и использует note. Открытие прошлого отчёта и
   исходных citations обеих работ. Recalled и referenced видны раздельно.
7. Поправка записи, disputed/provenance/revision, исключение/восстановление;
   влияние актуальной правки на последующий recall/ответ. Старый неверный текст
   не возвращается как действующая память после rebuild/restart.
8. Второй аккаунт: отдельные история, библиотека, профиль и память; чужих данных
   нет.
9. Реально реализованные пользовательские fallback/сбои: partial/abstract_only,
   pending/недоступная память, lexical-only/недоступные embeddings, сеть/401.
   Сначала составить точный перечень реализованных состояний, затем показать
   доступные безопасные воспроизводимые сценарии. Основной исследовательский
   проход остаётся live; если отдельный сбой требует fixture, явно обозначить
   его и отделить от live-доказательства. Непоказанные состояния честно указать;
   не заявлять «все фичи на видео» на основании одних интеграционных логов.

Для записи применять `record-task-demo` с явным пользовательским
переопределением Computer Use: **только claude-in-mobile** для всех UI-действий;
adb допускается только для APK, reverse, screenrecord и transfer. Компьютерный
Orca для Android не требуется. Не показывать credentials, private reasoning и
другие окна; микрофон и звук выключены. Проверить рост файла во время записи,
finally-stop, ffprobe, полное декодирование, визуальную читаемость начала/конца
и всех ключевых сцен. При ускорении всего прохода явно подписать скорость;
не скрывать неудачи склейками и не подставлять готовые ответы.

Это требование к новой полной записи, а не переписыванию прежнего видео.
Собственные Qwen3-интеграции T19 не являются будущей live/UI/video-проверкой.

## Реализованное подключение AgentRunner — T20

Production `cmd/paper-scout` передаёт один `researchmemory.Service` в
`researchapp.Attach(..., memory)`; он использует существующий EmbeddingClient.
Legacy `Attach` / `agent.New` без памяти совместимы. Перед первым outer
Chat Completions автоматически выполняется `Search` по текущему вопросу;
`ExcludeRunID` — текущий run, `Before` — его persisted `created_at`.
После первого успешного `analyze_paper`, вернувшего непустые observations,
один дополнительный recall использует название и method/result/limitation
тексты новой работы. Дополнительного DeepSeek-запроса для recall нет.
Два разных анализа и повторные model requests не открывают третью точку.
Новый memory tool не добавлен: обе точки выполняются независимо от модели.

Один recall имеет deadline 30 секунд, унаследованный от run cancellation.
Каждый Search сохраняет границы T19: до 16 notes / 2 report projections,
один batch <=32 passages, один query embedding, top20 обеих веток / RRF60.
Query ограничен 4000 Unicode-символами. В автоматически добавляемые данные
включается не более **12000 Unicode-символов всего JSON snapshots**, включая
query, provenance, coverage и annotations; первая точка использует до 6000,
оставляя место для content recall. Размер измеряется по фактическому Go JSON,
с учётом экранирования (`&` → `\u0026`), а не после decode/re-encode.
При необходимости Query сокращается по границе Unicode **до Search** с
`memory_query_truncated`; original request остаётся в run без изменения.
После сокращения hits/diagnostics итоговая сериализация проверяется снова;
непомещающийся контекст не сохраняется (`memory_context_limit`). Server-only `SearchResult.Groups` (`json:"-"`) сохраняет полное annotation
closure каждого base, даже когда публичные hits дедуплицируют общую correction.
Группы base+mandatory annotations принимаются
целиком либо пропускаются с `memory_prompt_budget`; число разных report origins
по обеим точкам <=5. Полностью повторившиеся группы повторно не вставляются.
Профиль и обычная conversation history остаются отдельным контекстом;
общие ContextBytes/token/request/time ограничения AgentRunner не снимаются.

Единые типы находятся в `internal/store/research_memory.go`:

- `MemoryOrigin`, `MemoryHit` — прежние поля T19 без изменения JSON;
  `researchmemory.Origin` / `Hit` — Go aliases, не новые сущности.
- `MemorySnapshot{Point, Query, RetrievalMode, Reasons, Hits}` — JSON
  `point=question|content`, `query`, `retrieval_mode`, `reasons: []`, `hits: []`.
  Hit содержит фактически выбранные text/hash/revision/provenance/ranks.
- `ResearchMemory{RetrievalMode, Reasons, Recalled, Referenced}` — JSON
  `retrieval_mode`, `reasons: []`, `recalled: []MemoryHit`,
  `referenced: []MemoryHit`. Это nullable `Report.ResearchMemory` /
  `report.research_memory`; старые отчёты и explicit disabled runs имеют null.

Миграция `T20_run_memory.sql` добавляет owner-scoped через runs таблицу
`run_memory_snapshots` (ordinal 0/1) и nullable `reports.research_memory_json`.
Snapshot и public event `memory_recalled` с **тем же** payload фиксируются одной
транзакцией до следующего запроса модели; cancelled/terminal run новый snapshot
не принимает. AgentSession добавляет только снимки **текущего** run как user data
после статических правил/профиля, не копирует снимки прежних runs. Snapshot —
предварительно сохранённый точный контекст запроса; при сетевом обрыве/краше он
не доказывает получение провайдером, чтение или смысловое использование. Durable
`model_request_started/finished` и unknown usage сохраняют эту неопределённость.

При memory read error модель получает `retrieval_mode=unavailable`, пустые hits
и явный reason (`memory_pending`, `memory_scope_limit`, `memory_timeout` либо
`memory_unavailable`); это не успешная пустая история. Ollama degradation остаётся
`lexical_only`/`hybrid_partial` из T19. В report агрегируется худший режим двух
точек (unavailable > lexical_only > hybrid_partial > hybrid), reasons объединяются.
Негибридный режим также попадает в limitations. Для здоровой пустой памяти
допустим hybrid с пустыми recalled/referenced; это не требование сравнивать.

Report materialization разрешает `[memory:<id>]` только когда ID выдан этому run
и запись принадлежит owner. Собственный, но не выданный ID, чужой ID и
несуществующий ID заменяются `[unresolved_memory_reference]`; limitation
`unresolved_memory_reference` явна. `referenced` содержит только валидные
упомянутые origins, `recalled` — уникальные выданные passages/revisions.
Ссылки не доказывают научную истинность: системное правило требует открыть
оригинальные passages обеих работ и проверить смысл, отделить user preference,
старый model claim, correction/disputed и новое evidence. Прежний механизм
проверки `ev_...` сохранён. Memory metadata и report/terminal status фиксируются
в одном `CompleteReport`; отмена не публикует report. Исходные отчёты immutable;
актуальные annotations к историческому origin читаются через memory GET (T21).

После terminal/report commit выполняется bounded Capture без DeepSeek. Ошибка
не откатывает report: T19 job остаётся pending и следующий Search восстанавливает
его без платного replay. Public `memory_captured` содержит
`status=captured|pending`, processed/added/noop/reasons; сохранение диагностики
имеет отдельный deadline. Логи `research memory read`, `research memory write`,
`research memory embedding` отделены от durable DeepSeek usage; текст, векторы,
credentials/provider reasoning в них не пишутся. Note edits актуализируются
через existing durable jobs перед новым recall, без отдельного UI действия.

`RESEARCH_MEMORY_ENABLED=true` по умолчанию. Явное `false` в production отключает
автоматические recall/capture для контрольного прогона T22; durable source jobs
остаются и могут быть обработаны после включения. Memory HTTP API остаётся
доступным. Это переключатель эксперимента, а не автоматический fallback.

Проверки и точные live-ограничения: [verification/T20.md](verification/T20.md).

## T21: актуальные аннотации всего отчёта (review1 fix)

`GET /api/v1/reports/{report_id}/research-memory` — authenticated owner-scoped
read, независимый от navigation memory ID, search/top-k и снимка run. Ответ:
`{"report_id":"...","items":[Entry]}`. Items — текущие disputed/excluded
episode/claim targets этого отчёта и связанные актуальные notes/corrections,
дедуплицированные по memory ID. `excluded` сохраняется явно; собственные origins,
authority и revision берутся из T19. Passages/annotations внутри этих Entry —
пустые массивы: полный текст/история выбранной записи доступны через прежний GET.

Связь строится по owner + report episode / research_memory_origins всех derived
claims, включая исключённые записи. Выполняется bounded идемпотентный Capture
именно этого отчёта и обычный bounded Refresh заметок, без DeepSeek/embeddings.
Чтение связей и пользовательских ревизий — в одном SQL snapshot. Чужой и
отсутствующий report одинаково 404; нет сессии 401; pending 503. Максимум 128
связанных targets и 2 MiB JSON; превышение — явный `memory_scope_limit`, а не
успешный ответ без части поправок. Остальные ограничения чтения T19 сохраняются.
При ошибке Flutter показывает недоступность аннотаций рядом с исходным отчётом,
не «поправок нет». Markdown и nullable historical research_memory не меняются.
Это дополнительный read adapter T21, не изменение Go-agent или T20 recall.

## T22: исполняемый контракт принятия ответа

Production `cmd/paper-scout` включает `AgentRunner.WithGrounding()` также при
memory-off. Legacy composed callers без этой опции сохраняют прежний контракт.
Это один существующий tool loop и DeepSeek gateway, без второго клиента/отчёта.

Модель получает отдельный bounded reference manifest текущего run. `E`/`M` +
12 hex — scoped handles evidence/memory; отображение детерминировано SHA-256
от run/kind/canonical ID, не зависит от порядка выдачи. Коллизия закрывает run.
Идентификаторы выдаются только из текущих snapshots и успешных typed results
`analyze_paper`, `search_library`, `read_passages`, с повторной owner/DB проверкой.
Произвольный prose не выдаёт handle. Точные длинные canonical IDs поддерживаются;
сокращения/prefix/fuzzy expansion, чужие и старые handles не поддерживаются.
Manifest не равен всей библиотеке: новые источники можно найти обычными tools.
Он также содержит каталог до 20 owner-scoped названий/paper IDs с явным
`library_catalog_limited=true` (всегда scoped), отдельными
`library_catalog_truncated`/`library_catalog_scope`; это navigation metadata,
не inventory и не новые evidence. Memory-derived catalog учитывается в memory cap.

У успешно прочитанного passage есть до шести `quote_options`: `Q` scoped handle,
Unicode полуинтервал относительно passage и **неизменённый** slice текста до
350 символов. Final quote принимает exact Q только своего E либо literal excerpt
20..800 символов с точным substring совпадением. Нет исправления приблизительных
цитат. Оригинальный текст из read_passages и options остаётся данными.
Quote options — primary-read tool context, не automatic memory; полный запрос
подчиняется прежнему byte/token/request budget. Snapshot JSON + memory alias
metadata повторно проверяются на общий предел 12000; overflow явный.

Production grounded requests и Analyzer используют тот же DeepSeek client с
`response_format={"type":"json_object"}` и temperature=0. Provider grammar
не заменяет gate: schema, exact IDs, excerpts, ownership и budgets проверяются
сервером. Empty/length/invalid provider responses не считаются успешными.
Официальный контракт: https://api-docs.deepseek.com/guides/json_mode/ .
Reference catalog содержит только current selected / issued / recalled papers;
он не вводит названия прошлых исследований при memory-off. Обычный library search
остаётся доступен как явный tool; каталог не объявляется полной библиотекой.

No-tool final answer — JSON: scope, findings[{source,quote,conclusion}],
comparisons[{memory,previous,previous_quote,current,current_quote,assessment,
conclusion}], notes[], memory_reason, limitations[]. Максимум 6 findings,
3 comparisons, 8 notes/limitations. Assessment supported/not_supported/uncertain.
Comparisons требуют original read A и B из разных papers и evidence origin
именно выбранного claim. Для review без межстатейного сравнения current и
current_quote пусты; UI/Markdown явно называет это только проверкой старого claim.
Нерелевантный recall допускает пустые findings/comparisons и явный abstention.
Scope модели не публикуется: сервер показывает исходный вопрос пользователя.

Notes выбираются по выданным M-handles и выводятся сервером **дословно**,
отдельно от научных выводов. Релевантные критерии заметки применяются к
проверке научных данных (нужные attributes подтверждены/не установлены),
а не только отображаются; предпочтение не усиливается до real-only. Correction closure materially used claim/note
обязательна, включая доставленный feedback на origin report episode; это feedback, а не доказанная истина. Typed selections определяют
canonical report citations/referenced; текст пользовательской note не может
создать научную ссылку. Известные issued identifiers в текстовых полях
нормализуются точно как plain canonical IDs и сами по себе не считаются
used/referenced. Prose limits применяются до exact ID expansion; итоговая
нормализация отдельно ограничена max*8. Чужой, невыданный или старый handle в prose тоже отклоняется.

Gate работает **до** completed/report finalizer. При первом invalid draft —
точная диагностика с фактическими counts и одна repair-фаза. Rejected candidate
передаётся только как временные недоверенные данные этой repair-фазы, чтобы
сохранить уже валидные поля; он не добавляется в messages/accepted replay.
Недостающие выбранные original passages
сервер читает зарегистрированным read_passages (<=8/16000), с обычными call IDs,
tool accounting, deadline/cancel и journal. Repair использует тот же budget.complete
и run limits; последующий invalid final draft = failed answer_validation_failed,
без report. Бюджет/отмена сохраняют прежнюю семантику. В 5-секундном finalizer
provider I/O отсутствует. Совпадение quote и валидная ссылка **не доказывают**
полную семантическую корректность conclusion — ручная live сверка обязательна.

Raw drafts и provider reasoning остаются только private model_request_finished
audit. В messages публикуются canonical tool calls с согласованными tool_call IDs
и принятый canonical final, без rejected drafts/aliases. Final message,
answer_accepted и report/terminal status фиксируются одной транзакцией
CompleteReport; concurrent cancellation не публикует final message/report.
Следующий grounded run
в том же диалоге получает старый user question и immutable report, а не старые
tool chains/отклонённые ответы. Это применимо и к legacy completed reports;
исторический ответ остаётся помеченными недоверенными данными.

Адресная проверка: `python3 -B integration/grounding.py` — production executable,
real HTTP/SQLite/Qwen3, явно synthetic science/provider fixtures. Она заменяет
старые production ожидания T20 fixture, допускавшие completed с invalid refs;
сам composed legacy agent API не меняется. Live/видео статус определяется
только фактическими результатами [T22](verification/T22.md).
