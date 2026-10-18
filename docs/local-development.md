# Локальный runbook Paper Scout

Команды выполняются из корня **текущей worktree**, для T22 —
`/home/kotdath/omp/personal/ai/paper-scout-worktrees/T22`, ветка `dag/t22`.
Текущая попытка production live приёмки **blocked**: [T22](verification/T22.md).
Историческая сдача: [T18](verification/T18.md).
`make` targets в проекте отсутствуют; ниже используются существующие entrypoints.

## Подготовка

На машине T16/T17 использовались Go `1.26.2-X:nodwarf5`, Flutter `3.41.4` /
Dart `3.11.1`, Android API 35 `emulator-5582`, Poppler и установленная Ollama.
Обычный Flutter находится в `/home/kotdath/flutter/bin/flutter`, Android SDK —
`/home/kotdath/Android/Sdk`; Aurora SDK для этого клиента не нужен.
Нужны `go`, `flutter`, `dart`, `adb`, `pdfinfo`, `pdftotext`, `python3`;
для видео — `ffmpeg`, `ffprobe`; для race-сборок — C toolchain.

Для изолированных сборок, БД и кэшей в этой worktree:

```sh
export SCOUT_RUNTIME="$PWD/.bb/runtime/local"
mkdir -p "$SCOUT_RUNTIME"/{bin,state,source-cache,gocache,gomod,gopath,tmp,pub,gradle,android}
export GOCACHE="$SCOUT_RUNTIME/gocache" GOMODCACHE="$SCOUT_RUNTIME/gomod"
export GOPATH="$SCOUT_RUNTIME/gopath" TMPDIR="$SCOUT_RUNTIME/tmp"
export PUB_CACHE="$SCOUT_RUNTIME/pub" GRADLE_USER_HOME="$SCOUT_RUNTIME/gradle"
export ANDROID_USER_HOME="$SCOUT_RUNTIME/android" GOTELEMETRY=off
export PATH="/home/kotdath/flutter/bin:/home/kotdath/Android/Sdk/platform-tools:$PATH"
export SCOUT_ENV=/home/kotdath/omp/personal/ai/paper-scout/.env
export HTTP_ADDR=127.0.0.1:0
export SQLITE_PATH="$SCOUT_RUNTIME/state/paper-scout.sqlite"
export CACHE_DIR="$SCOUT_RUNTIME/source-cache"
export DEEPSEEK_MODEL=deepseek-flash DEEPSEEK_THINKING=disabled
```

Команды рассчитаны на Bash. Существующие SDK и dependency caches можно
использовать как входы; новые выходы оставляйте в своей worktree. При пустом
module/pub/Gradle cache нужен доступ к сети; `GOPROXY=off` и
`flutter pub get --offline` подходят только после заполнения локального кэша.
Не используйте shared hardlinks для writable caches.

Приватный dotenv должен быть подготовлен владельцем. Агент не читает и не
печатает его; **только Go-процесс** загружает файл по `-env-file` / `--env-file`.
Не выполнять `source`, `cat`, shell tracing или вывод окружения с секретами.
Окружение процесса имеет приоритет над dotenv. Серверу нужны:

| Настройка | Значение / назначение |
| --- | --- |
| `HTTP_ADDR` | Loopback IP и порт; `127.0.0.1:0` выбирает свободный порт |
| `SQLITE_PATH`, `CACHE_DIR` | Абсолютные локальные пути из команд выше |
| `DEEPSEEK_BASE_URL` | По умолчанию `https://api.deepseek.com` |
| `DEEPSEEK_MODEL`, `DEEPSEEK_THINKING` | В T16/T17 фактически `deepseek-flash`, `disabled`; доступность модели зависит от аккаунта |
| `DEEPSEEK_API_KEY` | Секрет только сервера, обязателен для live-исследования |
| `OLLAMA_BASE_URL` | По умолчанию `http://127.0.0.1:11434` |
| `EMBEDDING_MODEL`, `EMBEDDING_DIMENSIONS` | `qwen3-embedding:0.6b`, `1024` |
| `EMBEDDING_MODEL_DIGEST` | Необязательный SHA-256 pin установленной модели |
| `SEED_USER_1_LOGIN`, `SEED_USER_2_LOGIN` | Два разных логина, нужны команде seed |
| `SEED_USER_1_PASSWORD`, `SEED_USER_2_PASSWORD` | Пароли 12–72 UTF-8 bytes; нужны seed, не аргументы CLI |

Ollama должна уже работать с этой моделью; runbook её не скачивает и не
перезапускает общий сервис. Startup проверяет модель/digest; реальное encoding
проверяет интеграционная группа `retrieval-live`. Digest T16/T17:
`ac6da0dfba84a81fdbfbaf330198c33cd77c4cdfc53e8bc50eb581914a15621d`.

## Go API и два аккаунта

```sh
go build -mod=readonly -o "$SCOUT_RUNTIME/bin/paper-scout" ./cmd/paper-scout
"$SCOUT_RUNTIME/bin/paper-scout" -seed -env-file "$SCOUT_ENV"
"$SCOUT_RUNTIME/bin/paper-scout" -env-file "$SCOUT_ENV"
```

Seed идемпотентен: создаёт отсутствующих пользователей, не меняет существующие
пароли/профили/сессии. Если seed-конфигурация отсутствует, команда завершается
ошибкой; реальные аккаунты фиктивными не подменяются. Обычный старт не требует
seed-переменных. Миграции автоматические; checksum drift останавливает запуск.
Детали: [accounts](accounts.md), [foundation](foundation.md).

Процесс работает в foreground. JSON `server listening` сообщает фактический
адрес, например `127.0.0.1:42829`; число в примере не фиксированный порт.
Во втором терминале повторите блок подготовки с теми же путями (экспорты
не переходят между терминалами) и задайте адрес из текущего лога:

```sh
export SCOUT_PORT=42829
curl --fail --silent --show-error "http://127.0.0.1:$SCOUT_PORT/healthz"
curl --fail --silent --show-error "http://127.0.0.1:$SCOUT_PORT/readyz"
```

Health проверяет HTTP; ready — SQLite/FTS5 и наличие Poppler, **не** доступность
DeepSeek/arXiv/Ollama. Без корректных DeepSeek credentials сервер может работать,
но новый run завершится `provider_unavailable`. Это не успешный live-run.
При провале embedding preflight сервер явно работает lexical-only до рестарта.

Production entrypoint использует `agent.DefaultLimits`: 32 DeepSeek requests,
24 tools, 250000 tokens, 4096 output tokens, до 900 секунд и один retry;
анализ входит в общий бюджет. Для повторения записанного ограниченного прохода
используйте отдельную композицию ниже с меньшими пределами.

## Flutter на Android

Используйте доступный Android-эмулятор. В T16/T17 работал `emulator-5582`;
подключение и все действия интерфейса проверяются через **claude-in-mobile**.
Параметры запуска ранее созданного AVD: [client README](../client/README.md).
Не меняйте AVD чужой ноды. Отсутствующий эмулятор — незакрытая предпосылка
UI-проверки; API-проверки можно продолжать независимо.

Из корня worktree, после подготовки SDK и локальных caches:

```sh
flutter pub get --directory client
flutter analyze --no-pub client
(cd client && flutter build apk --debug --no-pub)
export SCOUT_DEVICE=emulator-5582
adb -s "$SCOUT_DEVICE" install -r client/build/app/outputs/flutter-apk/app-debug.apk
adb -s "$SCOUT_DEVICE" reverse "tcp:$SCOUT_PORT" "tcp:$SCOUT_PORT"
```

Запустите приложение `dev.paperscout.paper_scout` через claude-in-mobile и введите
на экране входа `http://127.0.0.1:<фактический-порт>`. Там же войдите одним из
seed-аккаунтов. Для compile-time адреса доступен
`flutter run -d "$SCOUT_DEVICE" --dart-define=API_BASE_URL="http://127.0.0.1:$SCOUT_PORT"`
из `client/`. Серверный dotenv и ключи клиенту не передавать.

Debug APK допускает loopback HTTP. Адрес, введённый в UI, и bearer-токен
хранятся только в памяти: после остановки Flutter ввод/вход повторяются.
Compile-time origin по умолчанию — `http://127.0.0.1:8080`.
Ошибка подписи при установке означает другой debug keystore: используйте тот же
локальный ключ либо удалите disposable приложение через Android Settings
средствами mobile UI и установите заново. Серверная БД при этом сохраняется.

## Повтор live-прохода T16/T17

Это исторический сценарий. Для нового сценария памяти обязательна production
команда из раздела «Go API и два аккаунта», а не этот integration entrypoint.
См. раздел о памяти ниже и [результат T22](verification/T22.md).

Это bounded UI-композиция с production HTTP/auth/SQLite/AgentRunner/tools,
Analyzer и retrieval. Флаг `--live` выключает внешние fixtures; официальный
DeepSeek, arXiv и установленная Ollama обязательны. Платных запросов до
действия пользователя в UI нет. Используются публичные **disposable** аккаунты
`tools-alice` / `tools-bob`, пароль `api-integration-password` из исходника
`integration/researchtools/flutter.go`. Не используйте их для личной БД.

Для нового прохода возьмите отдельный каталог; для проверки сохранения оставьте
тот же каталог при повторном запуске:

```sh
go build -race -mod=readonly -o "$SCOUT_RUNTIME/bin/researchtools" ./integration/researchtools
mkdir -p "$SCOUT_RUNTIME/demo-session"
"$SCOUT_RUNTIME/bin/researchtools" --runtime "$SCOUT_RUNTIME/demo-session" \
  --client-check ui --live --env-file "$SCOUT_ENV"
```

Используйте origin из строки `T14 UI API:`; он выбирается ОС на каждом старте.
Повторите `adb reverse` с этим портом и введите origin во Flutter. Команда
`python3 -B integration/flutter_research.py --ui --live --env-file "$SCOUT_ENV"`
также доступна, но удаляет свою временную БД при завершении; для OS-рестарта
с сохранением нужен прямой бинарник и постоянный `demo-session`, как выше.
Запуск без `--live` использует source/provider HTTP fixtures и не доказывает
живое исследование.

На run: максимум 12 requests / 10 tools / 180000 tokens / 2048 output tokens,
retries=0; Analyzer до 6 calls в том же бюджете. Во Flutter создайте диалог
о MemoryBank и попросите:

> Найди MemoryBank через `search_papers` с запросом `ti:MemoryBank`, импортируй
> `2305.10250v1`, проанализируй весь текст через `analyze_paper` с точным
> `paper_version_id` из импорта. Кратко объясни механизм памяти и ограничения;
> укажи фактическое coverage и точные evidence IDs.

Затем спросите, как recall меняет S и t в §2.3, с требованием `search_library`
и `read_passages`. Откройте отчёт, coverage и evidence; для `complete` проверьте
все успешные диапазоны, для hybrid — BM25/dense/RRF и использованные цитаты.
Сохраните заметку, проверьте рестарт и второй аккаунт. AI недетерминирован:
partial, неверный ID или усечение не считаются успешной приёмкой; повтор только
явный, с учётом дополнительного расхода. В T16/T17 mobile input не передал
кириллицу: вопросы вводились на английском, ответы получены на русском.
Доказательства уже снятого прохода: [T16](verification/T16.md), [T17](verification/T17.md).

## Проверки

### Память через production entrypoint (T22)

`RESEARCH_MEMORY_ENABLED=true` по умолчанию включает capture/automatic recall
в `cmd/paper-scout`. Границы остаются конечными: 32 provider requests, 24 tools,
250000 tokens, 900 секунд на run; recall до первого запроса и максимум один
после анализа, до 12000 Unicode-символов JSON вместе с provenance/annotations.
Memory write не вызывает DeepSeek. Недоступность памяти — явное ограничение,
а не отсутствие истории. Детали: [контракт](research-memory.md).

На новой disposable БД выполните production seed/start, войдите через Flutter,
создайте A и исследуйте одну работу (например, MemoryBank `2305.10250v1`).
Откройте coverage/evidence, сохраните содержательную личную note. Остановите
Go и приложение; запустите Go с **той же** SQLite/cache и port 0, обновите reverse
и origin, войдите снова. В **новом** диалоге спросите про другую статью
(например, MemGPT `2310.08560v2`) без имени/ID A и без готового сравнения.
Проверьте порядок `memory_recalled` перед `model_request_started`, method/result/
limitation cards, точный note snapshot, ссылки текущего run и original passages
обеих работ. Recalled не доказывает referenced/использование; три утверждения
нужно сверить вручную. В T22 первый ответ этот критерий **не прошёл**.

Для memory-off контроля сделайте SQLite backup в отдельный локальный каталог,
назначьте ему свой cache и порт 0, затем запустите тот же production executable
с `RESEARCH_MEMORY_ENABLED=false`. Отправьте тот же вопрос B в новом диалоге:
ожидаются отсутствие snapshots и `report.research_memory=null`. Не используйте
историю диалога A как контекст контроля. Это не общий benchmark качества.
Не перезапускайте общую Ollama ради outage-сцен.

Адресные интеграции с настоящей Ollama, но явно synthetic scientific/provider
fixtures: `go run ./integration/researchmemory --runtime "$SCOUT_RUNTIME/memory-check"`,
`python3 -B integration/agentmemory.py`, `python3 -B integration/flutter_memory.py`.
Последний launcher принудительно использует собственные caches под
`.bb/runtime/t21/` **текущей worktree**; при `GOPROXY=off` пустой cache не
работает. T22 вместо launcher собрал `./integration/flutter_memory` локальным
Go cache и запустил бинарник с `--runtime` в T22 (он сам вызывает Dart).
Это не замена production live научной приёмке.

### Остальные интеграции

```sh
go vet -mod=readonly ./...
go mod verify
python3 -B integration/check.py
```

Общий набор сам направляет Go caches в `.bb/runtime/t15/` текущей worktree,
использует временные БД/loopback port 0, собирает `-race` и удаляет временные
бинарники/данные. Итог — `.bb/runtime/t15/summary.json`, 14 групп.
Нужны C toolchain, Poppler и реальная Qwen3. Обычный набор использует внешние
HTTP fixtures arXiv/DeepSeek, dotenv не читает и не является их live-проверкой.
Границы и выборочный запуск: [integration README](../integration/README.md).
Flutter transport/repository: `python3 -B integration/flutter_client.py` и
`python3 -B integration/flutter_research.py`. Unit/widget tests не запускать.

Отдельный ограниченный **платный** API live-набор:

```sh
python3 -B integration/check.py --live --env-file "$SCOUT_ENV"
```

Он проверяет `1301.3781v1`, полный анализ и hybrid follow-up; максимум два run
с пределами 12 requests/180000 tokens каждый, без retries. Он не управляет
Flutter и не заменяет видео. Не запускался повторно в T18.

## Остановка, восстановление и типовые сбои

- Остановите foreground Go через Ctrl-C либо SIGTERM конкретного процесса.
  Production shutdown ждёт HTTP до 10 секунд. Запускайте только один процесс
  на SQLite; параллельным проверкам назначайте отдельные каталоги и порт 0.
- Снова запустите с теми же `SQLITE_PATH` и `CACHE_DIR`. Production переводит
  незавершённые run в `interrupted`; готовые отчёты/диалоги/заметки сохраняются,
  автоматического платного replay нет. Порт может измениться: исправьте reverse
  и origin. Во Flutter войдите снова; для нового run нужно явное действие.
- Для резервной копии остановите сервер и сохраните весь каталог `state`
  (включая возможные WAL/SHM) вместе с `source-cache`. Не копируйте только
  открытую SQLite и не удаляйте cache: evidence связан с исходными документами.
  Для bounded композиции сохраните весь `demo-session` с `tools.sqlite` и `cache`.
  Локальные файлы содержат пользовательские данные и не входят в Git.
- `401`: выполните новый вход; после logout/истечения старый token недействителен.
  `404` для чужого ID — ожидаемая изоляция. `409` — активный run или конфликт
  `client_request_id`; `429` — занят серверный пул. Не создавайте дубликаты
  запросов при сетевой неопределённости: клиент сохраняет idempotency ID.
- `provider_unavailable`/rate limit: проверьте наличие серверной конфигурации,
  доступность выбранной модели и сеть без вывода ключа. Health не доказывает
  работоспособность провайдера. Лимиты/усечение требуют короткого запроса или
  нового диалога; compaction не реализована.
- `lexical_only`/`hybrid_partial`: проверьте Ollama, digest и coverage индекса.
  При startup failure восстановите Ollama и перезапустите Go; несовместимые
  векторы не смешиваются, требуется повторная индексация через импорт.
- `abstract_only`/`partial`: откройте причину извлечения/анализа. OCR нет;
  недоступность source и сбой embedding — разные ограничения.
- Migration checksum drift: восстановите соответствующий код/БД; не правьте
  применённые SQL и не удаляйте пользовательскую БД ради успешного старта.

При завершении удалите только собственный reverse:
`adb -s "$SCOUT_DEVICE" reverse --remove "tcp:$SCOUT_PORT"`.
Общий эмулятор и Ollama оставьте работающими, если они используются другими.

## Запись и перенос видео

Все UI-действия — **claude-in-mobile**; adb применяется для APK, reverse,
screen recording и переноса артефактов. Скрипт не управляет приложением:

```sh
bash scripts/record-demo.sh "$SCOUT_DEVICE" artifacts/new-demo-part1.mp4 180
```

Скрипт не перезаписывает существующий файл, останавливает только собственную
запись, переносит MP4 и проверяет stream/длительность. Ctrl-C завершает запись
с финализацией файла. Значение `0` допустимо только после проверки поддержки
на конкретном устройстве; T17 подтвердил её на своём API 35. Для других устройств
используйте части по 180 секунд, отмечая монтаж. Микрофон screenrecord не пишет.

```sh
ffprobe -v error -show_entries format=duration,size:stream=codec_name,width,height -of json artifacts/new-demo-part1.mp4
ffmpeg -v error -xerror -i artifacts/new-demo-part1.mp4 -map 0:v:0 -f null -
sha256sum artifacts/new-demo-part1.mp4
```

Дополнительно просмотрите кадры и воспроизведение через mobile UI. Для переноса
уже принятой сдачи нужны `artifacts/day35-demo.mp4`, `artifacts/live-run.json`,
`artifacts/retrieval-evaluation.json`; сверьте SHA-256 по [T18](verification/T18.md).
Эти ignored файлы не появятся в новом clone сами. В Git остаются код, runbook
и sanitized verification; видео/PDF/credentials/БД туда не добавлять.
