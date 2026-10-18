# Go foundation — T01

Из корня worktree, с локальным Go 1.26 или новее:

```sh
go build -o bin/paper-scout ./cmd/paper-scout
HTTP_ADDR=127.0.0.1:8080 ./bin/paper-scout
```

Без dotenv используются `HTTP_ADDR=127.0.0.1:8080`,
`SQLITE_PATH=data/paper-scout.sqlite`, `CACHE_DIR=data/cache`.
Каталоги создаются автоматически. Для подключения приватной конфигурации
передать только путь, например:

```sh
./bin/paper-scout -env-file /home/kotdath/omp/personal/ai/paper-scout/.env
```

Программа загружает dotenv библиотекой, не исполняет shell; переменные окружения
имеют приоритет. Содержимое файла и структура config не логируются, ошибки
парсера dotenv обезличены. Приложение принимает только loopback IP; порт `0`
выбирает свободный порт, фактический адрес появляется в JSON-событии
`server listening`. SIGTERM/SIGINT выполняет graceful HTTP shutdown до 10 секунд.

- `GET /healthz`: `200`, `{"status":"ok"}` — жив HTTP-процесс.
- `GET /readyz`: `200`/`503`, `status` и boolean `checks` для
  `database_fts5`, `pdftotext`, `pdfinfo`. Проверка БД выполняет транзакционный
  FTS5 insert/MATCH/BM25 через Go-драйвер и проверяет PRAGMA. Проверка Poppler
  подтверждает наличие исполняемых файлов в PATH; извлечение статьи относится
  к T05. Внешние модели и каталог не вызываются этими маршрутами.

SQLite использует `database/sql` + `modernc.org/sqlite`, один connection,
`foreign_keys=ON`, `journal_mode=WAL`, `busy_timeout=5000`. PRAGMA передаются
в DSN и применяются при открытии каждого нового connection. Новая БД создаётся
с правами `0600`, новые каталоги — `0700`. Существующие права не изменяются.
Не держать SQL rows/транзакции во время другой операции с этим же connection
или сетевого запроса.

Миграции автоматически применяются при старте из
`internal/store/migrations/*.sql`, в порядке имён, одной транзакцией.
Добавлять отдельные файлы вида `T02_accounts.sql`, `T05_documents.sql`;
не менять уже применённые файлы. Ledger `schema_migrations` хранит имя,
SHA-256 и UTC-время. Несовпадение checksum останавливает запуск.
T01 создаёт только ledger; таблицы предметной области создают их DAG-ноды.
Production-процесс использует один экземпляр сервера на БД.

Проверки без unit tests:

```sh
go vet ./...
python3 integration/foundation.py
```

Интеграционный скрипт собирает и запускает реальный бинарный файл, использует
временную SQLite БД и случайный порт. Все его файлы и Go build/module caches
находятся внутри `.bb/runtime/t01/` этого worktree. Он не читает приватный
dotenv, не вызывает внешние API и не использует моки.

Аккаунты, provider preflight, извлечение текста, hybrid retrieval и Flutter
не входят в T01; общий целевой runbook остаётся в `local-development.md`.
