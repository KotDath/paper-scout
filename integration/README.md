# Интеграционные проверки T15

T22 production final-answer contract проверяется отдельно:
`python3 -B integration/grounding.py`. Это production cmd/paper-scout → настоящий
HTTP/auth/SQLite/Qwen3; source/provider — явно synthetic fixtures, paid calls=0.
26 сценариев проверяют exact E/M/Q handles и canonical IDs, primary reads A/B,
один repair/повторную ошибку, budgets/cancel, owner scope, ошибочный old claim с
валидным ev ID, direct/report-level disputed correction, literal user note injection, приватный
draft audit и атомарную canonical history/report публикацию. Unit tests нет.
Старые production assertions `agentmemory.py`, допускающие completed с invalid
ссылками, заменены новым строгим контрактом; historical T20 результаты остаются
в verification. Composed legacy API без WithGrounding не изменён. Process
fixtures T15/T12 теперь возвращают явный source-free JSON abstention; сценарии
ошибок/retry/crash и наблюдение HTTP cancellation сохраняются.

Из корня текущего worktree:

```sh
python3 -B integration/check.py
```

Go, C toolchain для `-race`, Python 3, `pdfinfo`, `pdftotext` и установленная
локальная Ollama с `qwen3-embedding:0.6b` обязательны. Модель не скачивается.
При первом запуске Go может скачать зависимости из `go.sum`; для заполненного
кэша можно задать `GOPROXY=off`. Launcher собирает integration executables и
production server с `-race -mod=readonly`. Unit/widget tests не запускаются.

БД, source cache, бинарники и Go caches находятся только в `.bb/runtime/t15/`
текущего worktree. Каждая группа получает отдельный временный каталог;
HTTP fixtures и API слушают `127.0.0.1:0`. Временные БД, документы и binaries
удаляются после набора. Логи и sanitized JSON остаются в игнорируемом runtime.
`summary.json` содержит итог всех групп, время, Go version и исходный HEAD.
Платные provider/seed credentials не наследуются обычным набором; dotenv
обычный набор не открывает. Локальные embedding настройки могут передаваться
через `OLLAMA_BASE_URL`, `EMBEDDING_MODEL` и `EMBEDDING_MODEL_DIGEST`.

Набор возвращает ненулевой exit code при любой неуспешной группе и продолжает
независимые группы. Timeout завершает process group, включая дочерний сервер.
Отсутствие реальной Ollama — ошибка, без подмены успешным fixture результатом.

| Группа | Проверяемая граница |
| --- | --- |
| foundation | Production HTTP, миграции, SQLite/FTS5, WAL, readiness, рестарт, drift |
| accounts | Настоящие seed/login/logout, bcrypt, изоляция профилей, отзыв/истечение сессий, сохранение |
| deepseek | Production Chat Completions HTTP client: контракт, ошибки, truncation, deadlines, закрытые поля; внешний provider fixture |
| arxiv | Atom HTTP → адаптер → SQLite: версии, cache, общий limiter, ограниченные retries, ошибки/отмена |
| documents | HTTP → HTML и многостраничный PDF → настоящий Poppler → SQLite/cache; corrupt/blank/unavailable fallback |
| embeddings | Production EmbeddingClient → внешние HTTP fixtures: encoding, digest, размеры, повреждение, deadlines |
| runs | Настоящие lifecycle/store/auth/HTTP; конкурентные transitions, admission и восстановление; записи не выдаются за model execution |
| agent | Настоящий registry/harness → provider HTTP → SQLite; tool history, invalid args, общие бюджеты, nested limits, отмена |
| analysis | HTTP source/DeepSeek fixtures → настоящий ingestion/Analyzer → SQLite: последовательные диапазоны, пропуски/сбои, partial/complete/abstract_only, PDF evidence, лимиты |
| retrieval-fixture | Извлечённый корпус → EmbeddingClient HTTP fixture → SQLite/FTS5 → authenticated HTTP search: заранее заданные BM25/dense ранги и RRF, ownership, cache, degradation |
| retrieval-live | Тот же путь с **настоящей установленной Qwen3**: exact/paraphrase/русский/нерелевантный вопросы, cache, incompatibility, outage через локальный proxy, обе ветки и evidence |
| researchtools | Production registry всех пяти tools + harness/SQLite + настоящая Qwen3; source/provider HTTP fixtures; чужие IDs, полный анализ, shared budget, abstract fallback, деградация |
| researchapi | Authenticated HTTP → фоновый worker → реальные модули → отчёт/библиотека: complete, hybrid follow-up, ownership, pool/cancel, транзакционный rollback, partial polling, сохранение |
| process | **Production OS-процесс**: завершённый отчёт, in-flight cancel с наблюдаемым закрытием HTTP, ровно один retry на 503, SIGKILL при открытом provider request, два рестарта, interrupted/unknown usage, отсутствие replay, сохранение и privacy |

`retrieval-fixture` подменяет только внешний Ollama HTTP endpoint. Для точного
запроса memory passage имеет BM25 rank 1 и dense rank 3; два нерелевантных
dense-only passage имеют dense ranks 1 и 2. Победитель RRF должен иметь
score `1/61 + 1/63`. Второй аккаунт владеет отдельным высокоранговым passage,
который не может участвовать в кандидатах первого. Это воспроизводимая
проверка fusion, не доказательство качества embedding-модели. Отдельная
`retrieval-live` группа проверяет настоящие embeddings и сохраняет ранги.

Перезапуск process fixture сохраняет source-free отчёт с явным ограничением
`no_addressable_citations`; fixture не изображает научное исследование.
Тексты и provider ответы обычного набора — собственные искусственные fixtures.
Обычный набор не подтверждает доступность настоящих arXiv/DeepSeek.

Выборочный повтор:

```sh
GOPROXY=off python3 -B integration/check.py --only process --only retrieval-fixture
```

Итог выборочного повтора пишется в `summary-<groups>.json`, сохраняя итог
полного запуска. `<group>.log` и `<group>-result.json` обновляются при повторе.
Старый JSON группы удаляется до её запуска, чтобы сбой не оставил прежний
успешный artifact. Старые launchers по нодам остаются специализированными;
для общей регрессии после T12 используется этот набор с активным worker.

Отдельный платный live-набор запускается **только явно**:

```sh
DEEPSEEK_MODEL=deepseek-flash DEEPSEEK_THINKING=disabled \
  python3 -B integration/check.py --live \
  --env-file /home/kotdath/omp/personal/ai/paper-scout/.env
```

Он использует официальный arXiv/DeepSeek, настоящую Ollama, новую SQLite,
HTTP admission, полный анализ статьи `1301.3781v1`, hybrid follow-up и
сохранение. Не более 12 DeepSeek requests, 180000 tokens и 2048 output tokens
на запрос в каждом из двух run; Analyzer — до 6 запросов внутри общего лимита.
Retries выключены. Полнота `complete` обязательна; partial даёт неуспех,
не ослабление критерия. Только Go-процесс загружает dotenv. Отсутствующие
credentials или зависимости дают ошибку. Артефакты — `live.log`,
`live-result.json`, `live-summary.json`, без reasoning/credentials.
Управление Flutter через `claude-in-mobile` и видео входят в T16/T17,
а не в этот API-набор. Фактические результаты T15: [verification](../docs/verification/T15.md).

T19 research-memory: `go run ./integration/researchmemory --runtime "$PWD/artifacts/t19/new-check"`
uses a fresh disposable SQLite database, ephemeral HTTP ports, real account auth
and the installed Qwen3 Ollama model. The persisted report/analysis/chunk corpus
is synthetic. A forwarding loopback proxy injects only outage/race timing;
healthy inference stays real. No dotenv, DeepSeek, unit/widget tests or device
interaction. Contracts and exact results: `docs/research-memory.md`,
`docs/verification/T19.md`. Use a new runtime directory for each invocation.

T20 `agentmemory.py` is retained as a historical prose-provider fixture and shared
seed helpers. Its old CLI (including its `--live` branch) is **not compatible**
with the production grounded contract; do not use it as a current passing check.
T20 historical results remain in verification/T20.md.

Current production commands: `python3 -B integration/grounding.py` and
`python3 -B integration/history_replay.py`. The first covers exact references,
original reads, bounded repair/failure, cancel/budget, canonical accepted replay,
notes/corrections and memory-off. The second inspects actual fixture-provider
input after report-level correction/exclusion and note edit/clear, including
annotation failure, metadata budgets, the five-report bound and memory-off.
Both use production Go HTTP/auth/SQLite, real Qwen3 and clearly labelled synthetic
science/provider fixtures; no paid calls or unit/widget tests. They do not claim
that every historical T20 content-recall/capture-failure scenario was rerun.
