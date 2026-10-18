# Диалоги, run и история — T07

Миграция `T07_runs.sql` добавляет `conversations`, `runs`, `messages` и
`run_events`. Связи `(conversation_id, user_id)` и
`(run_id, conversation_id, user_id)` проверяются SQLite; нельзя прикрепить
запуск или сообщение к чужому диалогу. Пользователь HTTP определяется только
через `RequireAuth` / `UserFromContext`. Чужой и отсутствующий ID дают одинаковый
404, включая создание run, чтение событий и отмену.

## HTTP

- `POST /api/v1/conversations`: `{"title":"..."}`, 201 с диалогом.
  Непустой title ограничен 200 Unicode characters.
- `GET /api/v1/conversations?cursor=...&limit=50`: `items`, `next_cursor`.
  Порядок по непрозрачному ID, cursor — последний ID предыдущей страницы;
  пустой `next_cursor` означает конец. Фильтр владельца применяется до LIMIT.
- `GET /api/v1/conversations/{id}?after_seq=0&limit=50`: `conversation`,
  `messages`, `next_after_seq`, `runs`. Сообщения идут по возрастающему `seq`;
  `next_after_seq=null` означает конец текущей страницы истории. `runs` —
  последние 100 запусков, от новых к старым; более старые ID также остаются
  в сообщениях своей страницы и доступны через `/runs/{id}`.
- `POST /api/v1/conversations/{id}/runs`:
  `{"message":"...","client_request_id":"..."}`, 202 с `run_id`, `status`,
  `progress_url`. Message — до 20000 Unicode characters, request ID — до 128.
  Body также ограничен общим лимитом 64 KiB; строки из одних пробелов запрещены.
- `GET /api/v1/runs/{id}`: статус, timestamps, `cancel_requested`, бюджет,
  usage, необязательный `error_code`, `report_id` (пока null).
- `GET /api/v1/runs/{id}/events?after_seq=0&limit=50`: `items`,
  `next_after_seq`, `has_more`. Cursor равен последнему возвращённому seq
  (либо входному cursor для пустой страницы); он пригоден для следующего polling.
- `POST /api/v1/runs/{id}/cancel`: без body/query, возвращает текущий run.

Максимум страницы — 100 элементов, минимум — 1. Неизвестные/повторяющиеся
query-поля, неверные cursors, неизвестные JSON-поля и назначение `user_id`
отклоняются. Все ответы имеют `Cache-Control: no-store`.

Создание run записывает run, пользовательское сообщение и `run_queued` одной
транзакцией. `client_request_id` уникален для владельца и диалога; повтор с тем
же точным message возвращает прежний run даже после terminal status или при
заполненном пуле. Другое message с тем же ID даёт 409 `idempotency_conflict`.
Другой request ID при активном run даёт 409 `active_run`. На сервере допускаются
два активных run, включая queued; третий получает 429 `run_capacity` с
`retryable=true`. Частичный unique index дополнительно обеспечивает один
активный run на диалог. Проверки глобального лимита выполняются внутри
транзакции через единственное writer connection текущего Go-процесса.

## Интерфейс для T09/T10/T12

`research.New(serverContext, db)` создаёт общий lifecycle service. Тот же service
передаётся в `httpapi.NewWithRuns`; `httpapi.New` сохраняется как удобный вариант
без фонового исполнителя. `Store.RecoverRuns` вызывается **один раз до начала
HTTP-обслуживания** entrypoint, после seed-ветки. Не вызывать recovery из worker
или при открытии дополнительного соединения к живой БД.

1. `Service.Create(ctx, owner, conversationID, requestID, message)` возвращает
   run и `created`. Только `created=true` может передаваться в новую работу.
   T07 сохраняет `queued`; модель и фоновая очередь исполнения появятся в
   T09/T12. Polling не изображает выполненный поиск или готовый отчёт.
2. `Service.Start(operationContext, owner, runID)` переводит `queued` в
   `running`, записывает `run_started` и возвращает execution context с
   15-минутным deadline от server context. Закрытие HTTP-запроса не отменяет его.
   Повторный Start и Start terminal run отвергаются. Не обходить service через
   низкоуровневый `Store.StartRun`, если worker должен получать отмену.
3. `Store.AppendMessage` сохраняет оригинальные поля `role`, `content`,
   `reasoning_content`, `tool_calls_json`, `tool_call_id`, `provider_json`.
   ID, conversation/run, timestamp и возрастающий conversation-wide `seq`
   назначает хранилище. `Store.Messages` возвращает всю приватную историю,
   включая незавершённые ходы. T09 обязан выбрать coherent completed turns
   для контекста, не подмешивая interrupted/failed ходы как успешные.
4. `Store.AppendEvent` записывает публичный progress payload и отдельный
   приватный payload, назначая возрастающий run-wide `seq`. Публичный payload
   конструируется сервером из безопасных полей прогресса; provider response,
   reasoning, headers и ошибки транспорта туда не копируются. T07 добавляет
   `run_queued`, `cancel_requested` к предусмотренным событиям.
5. `Service.Cancel` сначала сохраняет запрос отмены, затем вызывает context
   cancel worker. `queued` сразу становится `cancelled`; `running` остаётся
   активным до подтверждения worker, освобождение слота раньше его остановки
   недопустимо. Повтор не добавляет события. Terminal run остаётся неизменным.
6. Worker вызывает `Service.Finish` с terminal status (`completed`, `partial`,
   `failed`, `cancelled`), безопасным кодом ошибки и агрегированным usage.
   После отмены execution context для записи нужен **отдельный живой context**
   с коротким timeout. Уже сохранённая отмена имеет приоритет над completion.
   Статус, usage и `run_finished` записываются одной транзакцией; повтор terminal
   операции не изменяет историю. Пустой usage `{}` означает отсутствие данных,
   не нулевую стоимость; неизвестные usage надо сохранять явно. Сохранение
   отчёта и окончательного результата в общей транзакции относится к T12.

AppendMessage/AppendEvent принимают только running run; поздние записи в
terminal run отвергаются. БД сохраняет приватную историю, но HTTP выбирает
только user/assistant text без provider fields и tool results. Ключи и headers
не должны попадать даже в приватные provider records.

`Service.Close` и server context отменяют execution contexts. Оставшиеся
`queued`/`running` при следующем старте становятся `interrupted` с
`error_code=run_interrupted`, timestamp и единственным terminal event.
Готовые состояния и сообщения не меняются; автоматического replay нет.
Это локальное приложение с одним Go-процессом на БД, без межпроцессного
планировщика/автовосстановления оплаченных запросов.

## Проверка

```sh
python3 integration/runs.py
```

Сценарий собирает реальный сервер и companion `integration/runs` с `-race`,
использует временные БД, настоящие auth/HTTP/store/research, loopback port 0
и SIGKILL/restart. Companion явно добавляет сгенерированные protocol records
для проверки хранения; это не live DeepSeek/tool execution. Общий dotenv,
внешние модели и UI не требуются для T07. Все build caches и временные
файлы находятся под `.bb/runtime/t07` этого worktree.
