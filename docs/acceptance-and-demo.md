# Приёмка и видео дня 35

Итоговая карта доказательств на 2026-10-18. Реальный проход выполнен в T16,
новый проход и запись — в T17; T18 собирает сдачу и повторяет доступные проверки.
Это результат этапа implement, а не отметка слияния DAG: два ревью, упрощение
и итоговый контроль остаются у координатора. Дни 33 и 34 в приёмку не входят.

## Обязательные требования дня 35

| Требование | Фактический результат | Доказательство / видео |
| --- | --- | --- |
| Реальная задача | Исследование долговременной памяти AI-агентов на примере MemoryBank, arXiv `2305.10250v1` | [README](../README.md); видео 0:00, 0:55–1:22 |
| Использование AI | DeepSeek выбирает tools, читает текст, отвечает и уточняет вывод по исходным passages | [T17](verification/T17.md): 12 requests, 56161 tokens, unknown usage=0; 1:22–3:49 |
| Реальное окружение | Go на ноутбуке, Flutter на API 35, официальный arXiv/DeepSeek, настоящая Qwen3 в Ollama | [T16](verification/T16.md), [T17](verification/T17.md); новый fetch и новый run, fixtures выключены |
| Описание задачи | Задача исследователя, участие AI, границы и результат описаны в README и титрах | [README](../README.md); 0:00–0:30 |
| Показать участие AI | Вопрос → поиск → полный анализ → отчёт → проверка §2.3 → hybrid follow-up | 0:55–3:49; точные run/evidence IDs в [T17](verification/T17.md) |
| Видео и код | Реализованные Go/Flutter, runbook и проверенный MP4 | [Код](../cmd/paper-scout/main.go), [клиент](../client/lib), [runbook](local-development.md), [видео](../artifacts/day35-demo.mp4) |

## Обязательства проекта

| ID | Результат | Граница доказательства |
| --- | --- | --- |
| P01 | Два аккаунта: отдельные профили, история, библиотека; чужие IDs получают 404 | UI T16/T17 + authenticated HTTP до/после рестарта: conversation, run, events, report, paper, evidence, PATCH note |
| P02 | Полный анализ извлечённого HTML MemoryBank: **39757/39757**, 4 последовательных диапазона и synthesis | T17: complete, SHA-256 сохранённых входов, индекс 37/37; 1:22–2:37. Полнота индекса отдельно от чтения |
| P03 | Blank/corrupt/unavailable text и annotation-only не выдаются за полный разбор | T15 integration: внешние source/provider fixtures + настоящие ingestion/Analyzer/SQLite/Poppler. Это не сбой, разыгранный в видео |
| P04 | Собственный AgentRunner/registry/history и один DeepSeekClient | [internal/agent](../internal/agent), [internal/deepseek](../internal/deepseek); агентных библиотек нет |
| P05 | Реальные Qwen3 embeddings, BM25/dense ranks, RRF top-8; evidence использован в ответе | T17 follow-up: 20/20 candidates, coverage=1; 2:50–3:49 и 4:45–4:53. T16: exact/paraphrase/русский размеченный набор |
| P06 | Reports/history/profile/library/saved/read/note пережили OS-рестарт Go и Flutter | T16/T17: снимки до/после, UI 4:55–6:07; новых model requests нет |
| P07 | Отмена, лимиты, errors, interrupted без replay | T15: HTTP/SQLite и production OS-процесс с provider fixture, SIGKILL, два рестарта; повторено T18. Технические проверки не изображены в MP4 |
| P08 | Основные действия доступны в Android Flutter | T16/T17: все UI-действия через `claude-in-mobile`; adb только APK/reverse/recording/артефакты |
| P09 | Допустимые проверки | Сборки, analyze/vet и интеграции; unit/widget tests не создавались и не запускались; T18 проверяет отслеживаемые файлы |

## Видео

[artifacts/day35-demo.mp4](../artifacts/day35-demo.mp4): **414.267 с (6:54)**,
H.264, 720×1780, 30 fps, 12428 кадров, 15838091 байт, без аудио.
SHA-256: `e7aa4af10074cced77b6e9ec3fcd4564fb8782f57f4cc74704f7a48df6449c36`.
Снято в T17 с API 35 `emulator-5582`; весь проход ускорен в 1,5 раза,
скорость обозначена постоянно. Пояснения добавлены в отдельное поле;
последние 8 секунд — удержание финального кадра. Сцены не вырезались,
готовые ответы не подставлялись. Звука нет, задачу и стек поясняют титры.

| Время | Эпизод |
| --- | --- |
| 0:00–0:55 | Вход, задача, профиль A |
| 0:55–1:22 | Новый диалог и исследовательский вопрос |
| 1:22–2:09 | Поиск, анализ, complete и отчёт |
| 2:09–2:37 | Исходный §2.3, координаты и URL |
| 2:50–3:49 | Follow-up, ответ и evidence |
| 3:51–4:25 | Сохранение статьи и личной заметки |
| 4:45–4:53 | Реальные BM25/dense/RRF ranks |
| 4:55–6:07 | Повторный вход A после рестарта: история, отчёт, библиотека, профиль |
| 6:08–6:46 | Вход B: отдельные пустые история, библиотека и профиль |
| 6:46–6:54 | Итоговый титр и стек |

T17 проверил воспроизведение в Android Google Photos через mobile UI,
несколько кадров и полное декодирование. T18 повторил SHA-256, ffprobe и
полное декодирование локальной копии; новых действий на устройстве не было.
OS-рестарт Go и проверки HTTP 404 подтверждены отдельным журналом T17,
а не кадрами терминала внутри видео.

## Содержательная проверка и известные ограничения

В T17 три утверждения сверены с исходным §2.3: начальное S=1; recall повышает
S на 1 и сбрасывает t; модель упрощена до трёх принципов и исключает
эффекты overlearning/meaningful material. Оба прочитанных evidence IDs точно
использованы в follow-up. Coverage `complete` создан последовательным анализом,
не retrieval. В T16 отдельно сверены три числовых факта таблицы 2/метрик и
сохранён исправляющий follow-up; исходные неудачи не скрыты.

- HTML имеет `html_images_and_math_not_interpreted`; OCR нет, формулы могут
  терять форматирование. `complete` относится ко всему извлечённому тексту.
- В T16 были partial, неверный ID и неточная подпись диапазона метрики.
  Поэтому наличие completed run само по себе не означает принятую научную
  точность. В T17 два completed run прошли без неудачных AI-запусков.
- Один источник и три сверенных утверждения не доказывают надёжность любого
  ответа. Dense top-k для нерелевантного вопроса не является доказательством.
- Chrome получил правильный HTTPS URL, но чтение всей страницы в браузере не
  подтверждалось; содержание сверялось по сохранённым исходным passages.
- Mobile input не передал кириллицу в первом вводе; вопросы/профиль введены
  на английском, ответы — на русском. Этот эпизод оставлен в видео.
- Live UI использовал bounded `integration/researchtools` с продуктовыми
  модулями; production `cmd/paper-scout` отдельно проверен интеграционным
  OS-process набором. Полный live UI-проход через этот entrypoint не заявляется.
- T18 не повторял платные arXiv/DeepSeek-проходы, Flutter build/analyze или
  UI-запись; основание этих утверждений — T16/T17, а не новый результат T18.

## Состав локальной сдачи

| Файл | Происхождение |
| --- | --- |
| [day35-demo.mp4](../artifacts/day35-demo.mp4) | Проверенная копия финального видео T17 |
| [live-run.json](../artifacts/live-run.json) | Sanitized T17: runs/reports/coverage/segments/retrieval и результаты isolation |
| [retrieval-evaluation.json](../artifacts/retrieval-evaluation.json) | Sanitized T16: заранее выбранный evidence и фактические top-8/ветки для размеченных вопросов |
| [verification/T18.md](verification/T18.md) | Коммитируемый итог: команды, результаты, SHA-256 и ограничения |

Видео/JSON находятся в ignored `artifacts/` worktree T18, не опубликованы и
не входят в Git. В новой копии репозитория переносите эти три файла отдельно,
проверяя хеши T18; иначе локальные ссылки на них работать не будут. Исходное
видео/кадры/монтажные файлы остаются в локальных артефактах T17, их пути и
хеши описаны в [T17](verification/T17.md). Файлы БД, credentials, provider
reasoning и PDF в сдачу Git не включаются.

Повторный запуск приложения, проверок и записи: [runbook](local-development.md).
Машиночитаемый DAG и отметку done после ревью/слияния обновляет координатор.

## Память исследований

T01–T18 закрывают предыдущую сдачу дня35. Новое требование пользователя — другой диалог после рестарта, автоматическое воспоминание исследования A и заметки, осмысленное сравнение с B и проверяемые ссылки. Предыдущее видео этого не доказывает. Критерии T22 и отдельный research-memory-demo.mp4 — в [плане](research-memory.md#приёмка--t22).

## T22 — полный production walkthrough

Implement и fix1 выполнены 2026-10-19; следующие review/simplify/final verify остаются
отдельными этапами координатора. Основной [paper-scout-full-demo.mp4](../artifacts/paper-scout-full-demo.mp4)
— **43:08,8**, отдельный [research-memory-demo.mp4](../artifacts/research-memory-demo.mp4)
— **15:24,6**. H.264, 720×1780, CFR30, без аудио; UI playback **2x**.
Это **семь записанных частей с явными joins**, не непрерывная capture.
Части 1/2 — реальные production исследования/lifecycle, 3 — подписанные
synthetic fallback fixtures, 4 — просмотр тех же reports/originals, 5 — latest
APK с фактическим usage существующего A без paid replay. Части 6/7 —
зафиксированные через claude-in-mobile same-dialogue correction/exclusion и
note edit/clear; Go c35ea1c/1cfcc7f, тот же APK c3a2e2e. Failed clear attempt
виден перед успешным повтором после diagnostic fix; первичный A/B не переснимался.
Memory clip также включает обе новые сцены. Старые финальные exports сохранены
как `*-before-fix1-630653a.mp4`. Вступление +8s сдвигает все прежние таймкоды. Первые 8s launcher
setup и verification interval part2 05:00–14:30 убраны явно; raw сохранены.
В пропущенном интервале повторно открывался уже готовый отчёт: outcome и текст
показаны по обе стороны, неудача не заменена успехом. Основной A/B снят на
d64b9ab; последующие contract fixes проверены адресно/live, UI usage — c3a2e2e.

Ниже **таймкоды full-версии**, сверенные с кадрами. Небольшие интервалы включают
навигацию/ожидание. Supporting artifacts — ignored локальные файлы; один лог
не засчитывается как video scene. Полные hashes/расход/научные границы:
[verification/T22.md](verification/T22.md). Старые blocked/failed записи и
прежний day35-demo сохранены; [архив попыток](verification/T22-attempt-history.md).

| Функция → действие | Ожидаемое доказательство / фактическая сцена | Таймкод full | Supporting artifact |
| --- | --- | --- | --- |
| Адрес API, неверный вход, вход, профиль | Настройка loopback origin, понятная ошибка неверного пароля, вход/интересы | 00:12–01:23 | full MP4, final contact0, public profile/history |
| Новый A, arXiv search/import/analysis/progress | Вопрос MemoryBank, реальные source/tool events, готовый отчёт; actual original query `ti:MemoryBank` раскрыт повторно в latest APK | 01:08–02:18; 34:16–34:22 | primary raw, usage raw, run A/public audit |
| Report, usage и coverage | Complete39757/39757; **8 requests / 43366 tokens / 4 tools** показаны UI на неизменённом A, не inferred cost | 01:43–02:18; 34:00–34:10 | full MP4, usage raw, run A.usage |
| Saved/read и личная note | Оба switches и точная содержательная note, server save | 02:23–02:38 | primary raw, primary-public library |
| Go/Flutter restart, сохранение истории/библиотеки/профиля | OS restart, повторный вход; сервер использует ту же DB | 02:33–03:28 | primary raw + process log; повторно 23:38–24:33 |
| Новый диалог B без A в question | Вопрос только MemGPT, автоматический recall A+note до model request; fresh complete52333/52333 | 03:08–04:28 | primary raw, report B, event seq3<4 / 27<28 |
| Scientific A/B contrast и note | Положительный контраст S/t updating vs queue eviction/recall storage, original excerpts; note применена к MSC5+1/personas/unknown duration | 04:28–06:13 | primary raw, primary-public evidence; broad absence claim не засчитан |
| Recalled/referenced, origin A, старый отчёт | Раздельные секции/typed cards, переход к A, report provenance; точная note в историческом snapshot после clear не объявляется текущей | 04:28–05:23; 32:08–33:06 | full MP4, memory API; последний просмотр тех же accepted reports |
| Полное чтение ≠ index ≠ retrieval | Extraction warning, coverage, document/index counts и coordinates отдельны; exact A excerpt13799–15297 и B queue8983–10724 / MSC18036–19010 | 01:43–02:18; 06:18–06:43; 07:12; 33:03; 33:18 | primary/provenance raw, public evidence |
| Переход к оригиналу | A abs page показана в основной части; HTML B первый раз blank body (gap сохранён), позднее arXiv abs B отрисован с title/authors/abstract | 02:08; 06:38–06:53; **33:25–33:29** | full MP4, provenance raw; URL2310.08560v2 |
| Настоящий библиотечный hybrid search | BM25+dense+RRF, индекс81/81, candidates20/20, ranks/scores, переход к MSC original | **07:07–07:14** | primary raw; real Ollama/SQLite HTTP retrieval |
| Настоящий same-dialogue follow-up | Existing sources, original queue thresholds, citations, без нового full analysis | 07:58–09:38 | primary raw, run_2XTL.../report |
| Negative recall | Вулканы нерелевантны: recalled3/referenced0, нет выдуманного сравнения/scientific facts | 09:48–10:38 | primary raw, negative live report |
| Semantic fault → original counterexample | Явно внедрён только derived wrong100years/real-users claim с valid ev; live original disproves it **до** user correction | 11:03–13:43 | primary raw, semantic-fault-setup, real fault review |
| Correction/disputed/provenance/revision/exclude/restore | User feedback требует проверки, не сообщает правильные числа; disputed rev2 → exclude3 → restore4; позже exclude5 | 13:23–14:48; 22:08–23:23 | primary/lifecycle raw, current Entry/API |
| Ответ после correction и edited note | Новая live question; original10days/15virtual; not_supported, correction/note2 дословно; inventory overclaim сохранён как limitation, catalog scope исправлен | 17:43–20:03; 20:23–22:03 | lifecycle raw, post-correction report |
| Note clear/rebuild/restart и новый ответ | UI edit2/clear, сохранённое exclusion5, OS restart, библиотека без note; следующий live run не получает старую note/fault | 18:08–18:33; 23:08–25:33 | lifecycle raw; verified-rebuild/post-rebuild-search/after-clear |
| Старый отчёт во время active follow-up, cancel/continue | **Локальный provider-delay fixture**, zero upstream; старый real report доступен; первый failed attempt сохранён, второй cancelled, input снова доступен | 25:43–28:06 | lifecycle raw, controlled-cancel-check; реальные status/report ownership |
| 401 и второй аккаунт | Явный disposable session-expiry fixture → настоящий401/logout; Bob: пустые история/библиотека/профиль, отдельная память | 28:08–28:41 | lifecycle raw; Bob empty both branches + foreign404 API audit |
| Abstract-only и partial | **Подписанные synthetic UI fixtures**, onlyabstract84/84 не fulltext; partial42/84 | 29:00–29:33 | fallback raw, fixture reports/documents |
| Pending/unavailable memory | Подписанные fixture reports, состояния не трактуются как отсутствие истории | 29:43–30:08 | fallback raw, HTTP fixture reasons |
| Lexical-only/hybrid-partial | Реальные UI подписи «dense недоступен» / «неполный индекс»; общая Ollama не выключалась | 30:11–30:32 | fallback raw, explicitly synthetic snapshots |
| Detail pending/unavailable/retry | Русские ошибки памяти; после снятия local fault повторная загрузка открывает actual entry | 30:41–30:56 | fallback raw, auth/owner checks остаются настоящими |
| Stale revision409/reload | Настоящее конкурентное PATCH fixture изменило revision; stale UI получает409, reload показываетrev2 | 30:57–31:08 | fallback raw, real SQLite/PATCH |
| Сеть и восстановление | Закрытый ephemeral local endpoint → понятная ошибка; восстановлен origin и вход в actual production | 31:20–31:51 | fallback raw, основной сервер не подменял scientific ответы |
| NOOP/rebuild bytes / memory-off control | Backend checks PASS, **не выдаются за отдельную UI-функцию/сцену**: NOOP1, immutable hashes, same B off snapshots0/null | UI state lifecycle выше; direct/HTTP checks отдельно | verified-rebuild, verified-memory-off, note-only guard |
| Fix1: старый B → correction/exclusion | Актуальная disputed/excluded rev3 и дословная correction рядом с immutable отчётом; правка не научный факт | 35:20–36:10 | fix1 replay raw, live-public, replay-verification |
| Fix1: историческая note и текущая правка | Сначала старый snapshot с прежней note; UI меняет текущую note на критерий «threshold examples ≠ deployment measurements» | 36:10–37:50 | fix1 replay raw; old report/snapshot SHA unchanged |
| Fix1: same-dialogue follow-up, current note | B dialogue без импорта/полного анализа: original reads, новый критерий применён, старый prose не источник | 38:15–39:25 | run_FO627...; 4 requests/49723 tokens; Go c35ea1c |
| Fix1: clear и честный failed attempt | Note очищена; первый clear answer отклонён после bounded repair, report не опубликован; ошибка видна | 39:25–41:32 | run_LE3QQ...; 3 requests/34696 tokens; failed raw сохранён |
| Fix1b: повтор после diagnostic fix | Тот же B dialogue/DB/question, Go 1cfcc7f; 3 requests/36854 tokens; новый completed answer | 41:40–42:50 | fix1b clear raw, run_DFAG...; тот же APK |
| Fix1b: original + нет текущего критерия + warning | Original eviction/recall storage, явное отсутствие активных личных критериев, correction/excluded3/cleared3 в server warning | 42:50–43:08 | fix1b raw, live-public + replay-verification; public history counts до model event |

Все UI-взаимодействия — claude-in-mobile. adb использовался только для
install/reverse/record/transfer. Проверены рост и finally-stop каждого recorder,
ffprobe, полное декодирование обоих конечных файлов, начало/конец и все ключевые
сцены. Raw/keyframes/contact sheets/manifest сохранены; private reasoning,
секретные dotenv и загруженные PDF не входят в Git.

Приёмка не обещает универсальную научную истинность prose или benchmark
качества. Засчитаны три сверенных source facts и узкое сравнение; неоднозначная
фраза «без модели забывания» не засчитана как отсутствие других механизмов.
Старые ошибки/затраты не удалены и не выданы за успешные результаты.
