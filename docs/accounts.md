# Аккаунты и ownership — T02

Сборка из корня worktree:

```sh
go build -o bin/paper-scout ./cmd/paper-scout
```

Два аккаунта создаются явным запуском, без HTTP-регистрации:

```sh
./bin/paper-scout -seed -env-file /home/kotdath/omp/personal/ai/paper-scout/.env
./bin/paper-scout -env-file /home/kotdath/omp/personal/ai/paper-scout/.env
```

Приложение само загружает файл по пути. Не читать его через shell и не передавать
пароли аргументами командной строки. `SEED_USER_1_LOGIN`, `SEED_USER_2_LOGIN`,
`SEED_USER_1_PASSWORD`, `SEED_USER_2_PASSWORD` должны быть заданы через окружение
или dotenv. Значения и содержимое файла не логируются. Логины чувствительны
к регистру, без пробелов и управляющих символов, длина 1–128 UTF-8 bytes;
пароли — 12–72 UTF-8 bytes. Верхняя граница соответствует
[контракту bcrypt](https://pkg.go.dev/golang.org/x/crypto/bcrypt).

Seed создаёт отсутствующих пользователей и их профили одной транзакцией.
Повтор не сбрасывает пароли, ID, время создания, интересы и действующие/отозванные
сессии. Новые значения паролей в окружении не изменяют существующие аккаунты.
Обычный старт сервера не требует seed-переменных и не создаёт аккаунты.

Реализованы контракты из [api-and-data.md](api-and-data.md):

- `POST /api/v1/auth/login`: JSON `login`, `password`; ответ `session_token`,
  `expires_at`, `user` с `id`, `login`, `created_at`.
- `POST /api/v1/auth/logout`: bearer обязателен, 204 без тела; отзывается только
  текущий токен. Повтор с отозванным токеном получает 401.
- `GET /api/v1/me`: текущий пользователь.
- `GET /api/v1/profile`: собственный профиль (`user_id`, `interests`,
  `preferred_language`, `updated_at`).
- `PUT /api/v1/profile`: JSON `interests` (до 10000 символов),
  `preferred_language` (`ru`/`en`), ответ — обновлённый собственный профиль.

JSON-body ограничено 64 KiB; требуется `Content-Type: application/json`.
Неизвестные поля, дополнительные JSON-значения и query-параметры отклоняются
с 400. `user_id` нельзя назначить через входные данные. У профиля нет публичного
маршрута по ID пользователя: доступен только собственный `/profile`.
Неизвестный API-ресурс с валидным bearer возвращает JSON 404, без bearer — 401.
Ошибки имеют `error.code/message/retryable`, `request_id`, ответы — `no-store`.

Сессии: случайный непрозрачный токен из 32 криптографических байт, base64url
без padding; сервер хранит только SHA-256 hex. Срок — 24 часа без продления,
UTC RFC 3339; истёкшие и отозванные сессии не проходят аутентификацию. Хеши
паролей — `golang.org/x/crypto/bcrypt`, DefaultCost 10. Logout одного токена
не завершает другие сессии. Хеши и токены не входят в объект `user`.

Для следующих нод: `httpapi.RequireAuth(auth.New(db), handler)` устанавливает
auth context; `httpapi.UserFromContext(r.Context())` возвращает владельца.
Маршруты, добавляемые в mux, должны сами подключать middleware: fallback
`/api/v1/` защищает неизвестные пути, но не заменяет middleware конкретного
маршрута. Параметры ID ресурса нужно объединять с владельцем в SQL, до LIMIT;
`store.ErrNotFound` означает одинаковый 404 для чужого и отсутствующего ресурса.
Будущие таблицы должны ссылаться на `users(id)`; T02 не создаёт диалоги,
run, библиотеку, отчёты или evidence. Проверки их ownership остаются за
соответствующими нодами.

Интеграционная проверка:

```sh
python3 integration/accounts.py
python3 integration/foundation.py
```

Оба сценария запускают реальный сервер на loopback-порту `0` с временной БД
и генерируемой конфигурацией внутри runtime этого worktree. Приватный dotenv
не читается; внешние модели и устройство не нужны для T02.
