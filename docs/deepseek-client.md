# DeepSeekClient — T03

`internal/deepseek` — единственная реализация Chat Completions. Она не
создаёт сессии, не хранит историю, не исполняет tools и не обращается к Ollama.
T09/T10 передают сообщения и получают исходное assistant message для
приватного хранения. Агентных SDK/библиотек нет, зависимости не добавлены.

После `config.Load(envFile)` создать клиент через
`deepseek.New(deepseek.ConfigFromEnv())`. Конструктор не делает сетевых вызовов.
Ключ, URL и модель находятся только в серверном процессе. Настройки:

| Переменная | Поведение |
| --- | --- |
| `DEEPSEEK_BASE_URL` | По умолчанию `https://api.deepseek.com`; допускается `/v1` |
| `DEEPSEEK_MODEL` | Обязательная явная модель, без alias по умолчанию |
| `DEEPSEEK_API_KEY` | Обязательный секрет; не логировать config |
| `DEEPSEEK_THINKING` | `disabled` по умолчанию; допускается `enabled` |

`Complete(ctx, deepseek.Request{Messages: ..., Tools: ..., MaxTokens: ...})`
отправляет один POST на `<base>/chat/completions`, `stream=false`, явный
`thinking.type`, обязательный `max_tokens` от 1 до 4096 и `tool_choice=auto`
при наличии tools. При отсутствии tools поле `tool_choice` не отправляется.
`deepseek.Text(role, content)` создаёт текстовое сообщение. Ответ содержит
`Message`, `FinishReason`, `ID`, `Model`, `Usage`, `Attempted`.

Assistant `content` может быть `null`; `tool_calls`, строка JSON arguments и
`reasoning_content` сохраняются отдельно. Чтобы продолжить обмен, добавить
`response.Message` в историю и затем сообщения `role=tool` с исходными
`tool_call_id`. Не преобразовывать сообщение в один текстовый ответ.
Аргументы и доступ к инструменту проверяет harness; клиент не исполняет их.

## Бюджет и ошибки

Один `Complete` делает ровно одну HTTP-попытку. Автоматических retries нет.
Harness считает `Attempted=true` в бюджет, сохраняет usage даже при ошибке
завершения ответа и сам принимает решение об ограниченном повторе. Для 429 и
временных HTTP 500/502/503/504 возвращаются `Retryable=true` и `RetryAfter`
(секунды либо HTTP date); транспортный сбой также помечен retryable.
Повтор требует доступного бюджета, задержки и действующего контекста run.
После перезапуска платный replay не выполняется клиентом.

`Usage=nil` означает неизвестный расход. Ошибки транспорта, HTTP и чтения
тела не превращаются в нулевые токены. Успешный ответ требует присутствия
prompt/completion/total usage и согласованной суммы. При `finish_reason=length`
возвращаются ответ с известным usage и ошибка `output_limit`: инструменты
исполнять нельзя. `content_filter`, неизвестные finish reasons и невалидная
структура также возвращают ошибку.

Тип `*deepseek.Error` содержит только стабильный `Code`, `HTTPStatus`,
`Retryable`, `RetryAfter`, `UsageUnknown`. Provider error body, произвольные
headers и transport diagnostics не попадают в ошибки. Cancellation/deadline
сохраняют совместимость с `errors.Is`. Каждая попытка ограничена 120 секундами
или меньшим deadline вызывающего кода; `Config.Timeout` позволяет уменьшить
предел. Ответ ограничен 8 MiB. Redirects не выполняются. Удалённый endpoint
требует HTTPS; HTTP разрешён только на literal loopback для интеграции.

Сообщения/ответы содержат приватный текст и reasoning: не логировать их и не
возвращать напрямую из публичного API. Пользовательскую проекцию создаёт
владелец истории в последующих нодах.

## Проверки

Из этого worktree:

```sh
python3 integration/deepseek.py
```

Скрипт собирает исполняемый Go integration runner, использует реальные HTTP
запросы к локальному provider fixture на случайных портах и удаляет временный
бинарник. Go caches и временные файлы остаются внутри `.bb/runtime/t03/`.
Fixture доказывает HTTP-контракт и ошибки, но не доступность DeepSeek.
Unit tests и моки внутренних соседних модулей не используются.

Платный preflight запускается явно, загружает dotenv только внутри Go-процесса
и выполняет до трёх вызовов без retries, суммарный предел output — 416 tokens:

```sh
DEEPSEEK_MODEL=deepseek-flash DEEPSEEK_THINKING=disabled \
  python3 integration/deepseek.py --live \
  --env-file /home/kotdath/omp/personal/ai/paper-scout/.env
```

`deepseek-flash` — явно выбранная модель проверки, не fallback клиента.
Live runner принимает только официальный HTTPS endpoint, проверяет маркер
обычного ответа и выполняет запрошенное моделью умножение 6 × 7 локально,
передаёт результат с `tool_call_id` и проверяет итог `42`. В stdout идут только
статусы и usage; ключ, тела запросов/ответов и reasoning не печатаются.
Без доступных credentials/model проверка завершается ошибкой.

Контракт сверён с официальной документацией 2026-10-18:
[thinking и replay reasoning](https://api-docs.deepseek.com/guides/thinking_mode/),
[HTTP ошибки](https://api-docs.deepseek.com/quick_start/error_codes/),
[актуальная модель](https://api-docs.deepseek.com/quick_start/pricing/).
Live результаты и ограничения — в [verification/T03.md](verification/T03.md).
