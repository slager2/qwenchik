# Chat / Deep Research: реализация и статус проверки

Итоговый отчёт по интеграции на 20 октября 2026 года, локальная дата. UTC-записи за 19 октября относятся к той же сессии проверки, а не к отдельной версии. Backend/test/deployment и restart-результаты ниже переданы main; визуальный read-only проход и frontend-проверки также выполнены на этом этапе. Документационная финализация не запускала повторно генерацию или рестарт.

**Подтверждены:** Go tests/vet/race, frontend build и 28 тестов, mocked Chrome, реальный Chat со Stop/Retry/Continue и reload, поиск/fetch с сохранением evidence, восстановление research после checkpoint. **Исследовательский синтез в проверенном HTTP/3 run завершился честным excerpt fallback, а не полной семантической верификацией claims.**

## 1. Исходный проект

Go-монолит, SQLite/WAL, один durable worker, React/TypeScript/Vite, локальный OpenAI-compatible llama.cpp. Существующие задачи, подтверждения, память, артефакты и CLI сохранены. Разговоры — новый основной интерфейс `#/chat`; рабочее пространство доступно по `#/workspace`.

Backend и frontend обслуживаются процессом приложения на `127.0.0.1:8080`. Vite dev UI: `127.0.0.1:5173`, модель: `127.0.0.1:8000/v1`, необязательный SearXNG: `127.0.0.1:8081`.

## 2. Поисковый сбой и подтверждённые границы

Исходный риск: полезные provider hits могли теряться при исчерпании общего срока последовательной URL-валидации, а частичный результат превращался в общую ошибку worker. Исправленная реализация разделяет сбор результатов и валидацию, дедуплицирует URL, сохраняет публичные наблюдения и typed outcomes. Исторические воспроизведения находятся в [research-repair-2026-10-17.md](research-repair-2026-10-17.md).

Новый live run `bf78c9dc-9638-4047-bf8c-3e2fb2868abb` реально выполнил 2 search и 2 fetch, сохранил 16 source records, 2 evidence и provider outcomes. Это подтверждает реальную поисковую цепочку; отдельно не утверждается, что именно этот run воспроизвёл все provider-timeout/SSRF/late-hit сценарии — для них есть автоматические регрессии.

Пределы в [search_validation.go](../internal/tools/search_validation.go): сбор до **12 с**, валидация до **5 с**, один URL до **1 с**, одновременно до **4** валидаторов. Родительский deadline может сократить каждый бюджет. `MAX_SEARCH_QUERIES=12` — отдельный лимит количества запросов. Эти секундные бюджеты и параллелизм сейчас являются константами, не `.env`-настройками.

## 3. Основные изменённые файлы

Основные группы реализации и регрессий:

Основные интеграционные модули в текущем дереве:

- UI: `frontend/src/App.tsx`, `ChatShell.tsx`, `ResearchPanel.tsx`, `MarkdownMessage.tsx`, `chat.css`, `api.ts`, `types.ts`, `conversation-state.*`, `research-state.*`, manifest/lockfile, `vite.config.ts` и `frontend/tests/{browser,live-browser}.mjs`.
- CRUD/API: `internal/domain/conversation.go`, `internal/app/conversations.go`, `internal/api/{server,conversations,event_projection,sources}.go`; conversation, persistence-audit, query/projection и doctor-guard tests.
- Persistence: `internal/storage/sqlite/{store,conversations,verification}.go`, соответствующие CRUD/persistence/verification tests и `internal/storage/sqlite/migrations/008_conversations.sql`.
- Worker lifecycle: `internal/orchestrator/{worker,chat,citations,search_diagnostics,research_intent}.go`; `chat_test.go`, citation, recovery, replanning, query и hit-observer регрессии.
- Модель: `internal/llm/{client,guard,probe}.go`, `internal/llm/openai/client.go`, guard/probe/client/reliability tests, `internal/cli/inference.go` и tests.
- Поиск/extraction: `internal/tools/{research,search_validation}.go`, query/validation tests; composite/outcome/HTML/SSRF провайдеры и их tests.
- Запуск и документация: `internal/cli/{root,runtime}.go` и tests, `README.md`, `.env.example`, `Makefile`, этот отчёт.

Существующие runtime/replanning-дополнения сохранены. Этот финальный docs-проход меняет только `README.md` и данный файл; код не изменялся, коммиты не создавались, пользовательские изменения не удалялись.

## 4. HTTP, storage и события

| Контракт | Назначение |
| --- | --- |
| `GET/POST /api/conversations` | Список и создание; создание принимает `Idempotency-Key` |
| `GET/PATCH/DELETE /api/conversations/:id` | Чтение, название/режим/настройки, soft delete |
| `GET/POST /api/conversations/:id/messages` | Каноническая история и отправка нового turn |
| `POST /api/runs` | Альтернативная отправка turn с conversation ID |
| `GET /api/runs/:id` | Каноническое состояние run |
| `POST /api/runs/:id/cancel`, `/retry`, `/continue` | Остановка и новые явные попытки |
| `GET /api/runs/:id/events`, `/events/stream?after=N` | История и SSE replay |
| `GET /api/runs/:id/plan`, `/sources`, `/evidence` | Независимые research details |

Отправка возвращает `{conversation_id,user_message,assistant_message,run}`. Повтор того же запроса использует тот же idempotency key; изменённый payload с прежним ключом конфликтует. Одна беседа имеет не более одного активного run. Retry/Continue относятся к последнему terminal run этой беседы.

`008_conversations.sql` добавляет `conversations`, `conversation_messages` и связь run с существующей таблицей `tasks`; отдельной таблицы `runs` нет. Сообщения хранят статус, родителя, stream/chunk и `last_event_seq`. Run сохраняет mode, operation, ошибки, deadline, heartbeat и ссылки на сообщения. Существующая task API остаётся доступной.

SSE сохраняет прежние имена `message.delta`, `message.created`, `task.completed/failed/cancelled` и envelope с `task_id`, `seq`, `schema_version`. Дельта содержит `stream_id`, `message_id`, `chunk_index`, `delta`; новая stream ID с index 0 заменяет provisional текст. `Last-Event-ID` и начальный `after` обеспечивают replay. Интегрированы публичные `search.hit` с вложенным `hit`, `query.updated`, `verification.updated` и step/tool events.

Штатный конец модельного SSE, усечение и сетевой обрыв различаются адаптером; malformed/empty/truncated/interrupted ответы не приравниваются к полноценному завершению. Backend-регрессии прошли, а реальное завершение Chat и Stop/reload подтверждены live run IDs ниже. Наличие HTTP 200 или `[DONE]` само по себе не используется как доказательство содержательного успеха.

## 5. Обычный Chat, Stop, Retry, Continue

- API mode — `chat`; запрос идёт локальной модели, без исследовательских tools и облачного fallback.
- User message и assistant placeholder принимаются атомарно с durable run. Браузер показывает optimistic отправку и сверяет её с сохранённой историей.
- Stop вызывает `/cancel`; уже записанные дельты остаются в assistant message. Если генерация успела завершиться до Stop, состояние читается из API, а отмена не выдаётся за проверенную.
- Retry создаёт новый run и assistant message для прежнего user message; история не стирается.
- Continue создаёт новый run, требует непустой предыдущий ответ и связывает его через parent/continue-from. Это не возобновление task `/resume` и не восстановление KV cache модели.
- Закрытие вкладки не отменяет run. После reconnect/refresh используются persisted messages, event sequence и polling; ошибка sources не скрывает чат.

Обычный Chat завершился в run `75a247f7-51d3-4138-a04c-0e4dcc4e7ab2`, conversation `c1183d8d-2854-4408-8702-cf38c30ccf53`: ответ о SQLite WAL в двух предложениях, **без tool executions**.

Отдельный live Chrome runner прошёл реальные send → Stop → reload, Retry → Stop и Continue → Stop. Run IDs: `efbae231-e3a3-41e0-b42d-0a475d6310a6`, `eaa99f0d-f6f8-4165-9fc3-4de8d6427037`, `a2878f80-0d97-4d15-800c-9cf017d4b799`. Runner работал только со своей беседой и выполнил soft-delete cleanup.

## 6. Deep Research, настройки и восстановление

API mode — `research`. План, поиск, URL validation, fetch/extraction, evidence, проверка и синтез выполняются durable worker. UI показывает действия, публичные queries, стадии и источники, не скрытые рассуждения модели.

`research_settings: {"web_enabled": false}` запрещает запуск web research: submit/retry отклоняются с `web_disabled`, а проверка на границе claim не даёт уже поставленному research run обойти изменённую политику. Это backend-ограничение, а не model prompt или подпись composer; оно покрыто прошедшими persistence/API регрессиями. Chat идёт по отдельной ветке без исследовательских tools.

Реальные bool-переключатели окружения: `SEARCH_ENABLE_DDG` и `LLM_ENABLE_THINKING`. SearXNG/Tavily подключаются через `SEARXNG_URL`/`TAVILY_API_KEY`; заполненная конфигурация не означает здоровый провайдер. **`model_profile.temperature`, `top_p`, `max_output_tokens` принимаются и сохраняются как metadata, но worker пока использует глобальную конфигурацию; per-conversation применение этих параметров не реализовано.** Вымышленные `FEATURE_*` или `WEB_ENABLED` env knobs не добавлены.

При старте под database worker lock незавершённые research/tasks переводятся в очередь и сверяются с checkpoint/events. Неопределённый результат non-idempotent действия требует review вместо автоматического повторения. Chat с уже сохранённым частичным ответом после рестарта получает `generation_interrupted`, failed run и interrupted message: пользователь явно выбирает Retry/Continue. Пустой прерванный chat может вернуться в очередь. Абсолютный deadline сохраняется и не начинается заново после рестарта.

Live restart smoke `/tmp/opencode/checkpoint_restart_smoke.py` — **PASS**. Conversation `a30d9892-1529-4a4f-b4bf-1b4c6ca37a7d`, run `41874c6a-507a-4eca-9385-da3a0ab89ec3`: чтение только `https://go.dev/doc/`, без discovery. До `systemctl --user restart qwenchik-app.service`: checkpoint seq **20**, `fetched_bytes=47874`, completed step `fetch_go_doc`. Других активных задач перед рестартом не было. После рестарта зарегистрирован `task.recovered`, затем run завершился; суммарно **fetch=1, search=0, evidence=1**, повторного fetch не было. Сохранённый абсолютный deadline пережил рестарт.

Исследовательские данные, conversation context и явно принятая user memory отделены от обучения весов; research не является fine-tuning.

## 7. Providers, evidence, citations и model guard

Search hit — только lead, не evidence. URL проходит политику/SSRF-проверку; пустой/challenge/неподдерживаемый документ не должен становиться доказательством. Source cards различают discovered/validated/rejected и этапы загрузки/извлечения; typed ошибки и частичные outcomes не должны стирать успешные результаты остальных провайдеров.

`query.updated` публикует только безопасный нормализованный query, максимум 500 Unicode code points. Для подозрительного текста остаётся hash, без исходного query. Эта новая функция и public projections проверены фикстурами; **HTTP/3 run был создан до добавления query events**, поэтому наличие его 16 sources не выдаётся за доказательство показа точных queries именно в том старом run.

В [citations.go](../internal/orchestrator/citations.go) conversation research ограничен **8 источниками и 8 короткими claim-абзацами**. Каждая единица синтеза имеет точный evidence UUID. Локальный critic проверяет все утверждения абзаца и возвращает поддерживающую цитату, которая должна буквально содержаться в том же excerpt. Неверное соответствие, неподтверждённый claim, недостаток бюджета/материалов или недоступность финализации приводят к явно обозначенному **fallback дословных выдержек**, а не к выдуманному связному выводу. Verification сохраняется отдельно; frontend связывает маркеры только с реальными evidence URL.

Ограничение: проверка тем же локальным LLM и совпадение выдержки не доказывают внешнюю истинность, актуальность или полноту. Legacy task source footer также не равен claim-level verification. Сохранять оговорки fallback/partial в ответе и метаданных.

Реальный HTTP/3 research: conversation `02a336ed-0956-4e9b-926c-5f14800b1a4b`, run `bf78c9dc-9638-4047-bf8c-3e2fb2868abb`, статус `completed`, verification **`fallback_excerpts`**, reason **`citation_mapping_invalid`**, method `verbatim_excerpt_fallback`. Это завершённое исследование с сохранёнными выдержками и явными ограничениями, **не успешная полная семантическая проверка синтезированного ответа**.

| Evidence ID | Реальный URL |
| --- | --- |
| `8bd70778-2fdd-4083-8841-76d73ca73902` | <https://http.dev/3> |
| `b453eb1d-08c4-445a-a7c2-41be18d773e3` | <https://vlanlab.com/ru/testing/http-version/> |

Все проверенные числовые UI-ссылки совпали с этими evidence URL; неизвестные маркеры не превращались в выдуманные ссылки. Предупреждение fallback вынесено в research panel. Текст выдержек не переписывался ради косметики: встречающиеся navigation-фрагменты/escaped entities отражают качество исходного excerpt, а не подтверждённый синтез.

Worker и API doctor используют один process-local `GuardedClient`. `Complete`/`Stream` занимают один слот; диагностический `TryComplete` при занятости откладывается, busy не кэшируется как ошибка модели. CLI doctor делегирует живому app; при отсутствии app перед inference берёт database worker lock. Guard не блокирует другие процессы, другие БД, чужие клиенты или прямой доступ к llama.cpp. Guard/stream regressions включены в прошедший полный Go/race прогон; live Stop/Retry/Continue не оставили слот заблокированным.

## 8. Запуск и диагностика

Команды выполняются из корня репозитория; системные требования и полная установка описаны в [README](../README.md).

```sh
cp -n .env.example .env
make build
./qagent serve
```

Перед `serve` запустить llama.cpp на `8000` с контекстом из `LLM_CONTEXT_LENGTH`. Для SearXNG сохранить `SEARXNG_SECRET` только локально, затем выполнить `docker compose -f compose.searxng.yml up -d` и задать `SEARXNG_URL=http://127.0.0.1:8081`. SearXNG обращается к внешним поисковикам. Не помещать реальные ключи в примеры, units или отчёт.

Постоянный login-user runtime после `make build`:

```sh
make runtime-install MODEL_BIN="/absolute/path/llama-server" MODEL_PATH="/absolute/path/model.gguf" ENABLE=1
make runtime-start
make runtime-status
systemctl --user status qwenchik-model.service qwenchik-app.service --no-pager
journalctl --user -u qwenchik-app.service -n 100 --no-pager
make doctor
make providers
```

Units не содержат credentials; app читает локальную `.env`. Не запускать второй `serve` поверх systemd app с той же БД. `make providers` читает наблюдавшиеся outcomes, не запускает поисковый запрос. `make search-smoke` проверяет локальный SearXNG, но его движки могут обращаться наружу. Worker встроен в app, отдельный worker service не требуется.

Main развернул свежий Go executable с пересобранными embedded CSS/fonts. После запуска `/api/health` подтвердил готовность app/БД, свежий worker heartbeat и модель в состоянии `configured`; отдельный model health ответил успешно. Docker и SearXNG запущены. Это не утверждение о rootless sandbox/PDF-контексте или результате полного CLI/API doctor: такие проверки различаются.

## 9. Выполненные проверки и финальная матрица

| Команда | Зафиксированный статус |
| --- | --- |
| `CGO_ENABLED=1 go test -tags sqlite_fts5 ./...` | PASS, все packages, после последних safe-query/frontend-CSP изменений; результат main |
| `go vet -tags sqlite_fts5 ./...` | PASS, все packages; результат main |
| `go test -race -tags sqlite_fts5 ./...` | PASS, все packages; результат main |
| `npm run typecheck` в `frontend/` | PASS на frontend-этапе |
| `npm test` в `frontend/` | PASS: **28** state/render tests |
| `npm run build` в `frontend/` | PASS: финальные embedded assets; main пересобрал и развернул Go executable |
| `npm run test:browser` в `frontend/` | PASS: system Chrome, **mocked API/SSE**, 320/375/414/768/1440 px |
| Live Chrome, extended-команда раздела 10 | PASS: реальные send/Stop/reload, Retry/Stop, Continue/Stop; cleanup своей беседы |
| `/tmp/opencode/checkpoint_restart_smoke.py` | PASS: durable checkpoint/restart, один fetch, ноль search, один evidence |
| Финальный read-only Chrome на `http://localhost:8080` после redeploy | PASS: обе сохранённые беседы при 1440/375 px, numeric evidence links, keyboard workspace/drawers, без горизонтального overflow, `pageerror=[]`, font CSP violations=0 |
| `make doctor` | Выполнен: общий `status=warning`, не скрытый green; local model прошёл bounded structured JSON smoke, SearXNG вернул usable hit с degraded upstream diagnostics |
| `make providers` | Выполнен: читает текущие наблюдения без нового Tavily/DDG запроса; после рестарта health search adapters честно `not_checked` |

Mock-browser проверяет reconnect/dedup, stop/reload, retry/continue, потерянный terminal event, independent panels, nested hits, plan stages, citation safety, IME/keyboard/drawers и legacy failure isolation. Это не подтверждение реального поиска, модели или восстановления после перезапуска.

Go и frontend результаты относятся к финальному прогону main после последних исправлений. Заключительный post-deploy CSP-browser проход также выполнен на реальном `localhost:8080`, не на preview. Полный doctor запущен отдельно через CLI: app, БД, worker, frontend и structured model smoke — `ok`; sandbox и isolated Poppler — `warning`, поскольку Docker daemon не rootless. Проверка поиска получила один usable SearXNG hit; upstream state остаётся `degraded`. Браузерный тест намеренно не подменял эти диагностические probes.

## 10. Реальные live-проверки, screenshots и границы подтверждения

Main выполнил следующую команду с результатом **PASS** на реальной модели/API:

```sh
RUN_LIVE_BROWSER=1 LIVE_EXTENDED=1 LIVE_TIMEOUT_MS=90000 BASE_URL=http://localhost:8080 npm --prefix frontend run test:browser:live
```

Run IDs и cleanup зафиксированы в разделе 5, отдельный research — в разделе 7, restart — в разделе 6. Runner не запрашивал external research; `/api/doctor` блокировался, чтобы не инициировать фоновые probes. Chat/SSE ответы не мокировались. `CHROME_BIN` заменяет `/usr/bin/google-chrome`. Если модель завершится до проверенного Stop, runner не выдаёт это за успешную отмену.

Скриншоты действительно сняты system Chrome и прочитаны при визуальном проходе:

| Артефакт | Что показывает |
| --- | --- |
| `/tmp/opencode/qwenchik-chat-desktop.png` | **Обновлённый capture последнего deployed build**, реальная research-беседа, 1440 px |
| `/tmp/opencode/qwenchik-chat-mobile.png` | **Обновлённый capture последнего deployed build**, реальный ordinary chat, 375 px |
| `/tmp/opencode/qwenchik-research-mobile.png` | Последний deployed build: research drawer, 375 px |
| `/tmp/opencode/qwenchik-ordinary-desktop.png` | Последний deployed build: ordinary chat, 1440 px |
| `/tmp/opencode/qwenchik-fixed-chat-desktop.png` | Fixed production **preview** с настоящими backend GET/API и действующей CSP |
| `/tmp/opencode/qwenchik-fixed-chat-mobile.png` | Fixed preview, 375 px; не выдаётся за capture последнего deployed executable |
| `/tmp/opencode/qwenchik-fixed-research-mobile.png` | Один заголовок drawer, fallback/partial disclosure, компактный план |
| `/tmp/opencode/qwenchik-fixed-sources-desktop.png` | Реальные source cards и честная метка неподтверждённой загрузки |

Финальный read-only проход после redeploy не создавал беседы, не отправлял mutations и не запускал модель. Для обеих бесед проверены 1440/375 px: document/body width совпали с viewport. `pageerror=[]`, `fontCspErrors=[]`, неожиданных mutating requests=0. Реальная CSP содержит `font-src 'self'`; после загрузки fonts нет CSP/font ошибок. Все 4 числовые citation links research-ответа совпали с двумя evidence URL из раздела 7. `/api/health` вернул `status=ok`, `database=ok`, model `configured`; UI показал «Backend: доступен», а Qwen осталась «не проверена», поскольку `/api/doctor` намеренно блокировался. Единственные 4 console resource errors — ожидаемые `ERR_BLOCKED_BY_CLIENT` для этих doctor-запросов, не JavaScript исключения.

Research/sidebar drawers выдержали по 15 Tab-переходов без выхода фокуса; Escape, return focus и переход в workspace с клавиатуры прошли. Новых визуальных ошибок не найдено, frontend дополнительно не менялся. Проверенные text contrast samples — не ниже 4.64:1. Исторические 6 font CSP violations раннего deployment исправлены `assetsInlineLimit: 0`; обновлённые пути screenshots выше теперь содержат реальные финальные deployed captures. Отдельный full doctor выполнен main и этим проходом не подменяется.

## 11. Оставшиеся ограничения

- HTTP/3 live run подтвердил transport/search/evidence и честный fallback, но не успешный claim-entailment синтез. Critic ограничен 8 sources/8 claims и не является формальным доказательством.
- `model_profile` пока metadata: worker использует глобальные generation defaults, per-conversation overrides не применяет.
- Новые safe-query events покрыты регрессиями; точные queries старого HTTP/3 run не демонстрировались задним числом.
- Запущенный Docker/SearXNG не подтверждает доступность rootless code sandbox и изолированного Poppler. Без соответствующего окружения code tools fail closed; bounded pure-Go PDF fallback имеет более слабую изоляцию.
- JS-rendering/browser research tools и OCR не зарегистрированы; недоступный или требующий их источник не считается прочитанным.
- Один inference guard внутри процесса не является глобальным lock всего model server.
- Приложение без LAN authentication остаётся loopback-only; секреты не передаются в браузер.

## 12. Итог и конкретные следующие действия

Подтверждён рабочий локальный Chat со streaming/Stop/Retry/Continue, durable conversations, реальный research с provenance и безопасным excerpt fallback, а также checkpoint recovery без повторного fetch в проверенном сценарии. Тесты, race и embedded build прошли. Это не универсальное утверждение «всё работает» для произвольных сайтов/моделей и не semantic-success claim для fallback run.

Финальный read-only CSP-browser проход текущего deployment завершён успешно; full doctor подтвердил работоспособность локальной модели и честно сообщил ограничения rootless runtime. При дальнейшем развитии per-conversation generation overrides нужно сначала реально применить в worker и проверить; до этого metadata не рекламируется как действующая настройка.
