# Жёсткий аудит и ремонт research pipeline

Проверка: 17–18 октября 2026. Базовый HEAD: `4e78f2d`. Снимок до изменений: `main`, единственный tracked diff — исправление отступа `use_default_settings` в `searxng-settings.yml`; `.idea/` — локальные данные пользователя. Этот YAML-фикс сохранён и включён в ремонт. `.env` и значения ключей не включались в отчёт, события или коммит.

## 1. Executive summary

Проблема была не одной ошибкой провайдера. На реальных задачах последовательно воспроизведены:

1. Поиск и fetch успешно выполнялись, но compaction выбрасывал целый tool result при недостатке контекстного бюджета. Verifier получал пустые результаты при наличии evidence в SQLite.
2. Небольшая модель путала ID и индексы критериев, пропускала search-оценки и выдумывала дополнительные обязательные требования.
3. Verifier пытался проверить существование финального отчёта до запуска finalizer; корректные tool counts также могли быть отвергнуты как «недоказанные».
4. Простой planner schema необходим, но одного упрощения недостаточно: Qwen нарушает соотношение search/fetch шагов и `max_steps`. Нужен безопасный backend normalizer для read-only workflow.
5. DDG действительно возвращает challenge, а SearXNG может вернуть HTTP 200 с отказами upstream engines. Это нельзя считать валидным пустым поиском.
6. CPU-only runtime выдавал около 5,5 token/s и не завершал planner за 120 секунд. В этой среде есть локальная Vulkan-сборка и RTX 3050; GPU runtime устранил этот конкретный bottleneck без увеличения timeout.
7. Same-status SQLite transitions не сохраняли смену stage. Отдельный recovery-путь терял bytes неудачного извлечения.

После ремонта подтверждён полный live-путь для физических библиотек Астаны, а не только fixture tests: задача `046a29ff-9ceb-455c-908f-07d9c8f39598` завершилась `completed`, сохранив страницы Национальной академической библиотеки и каталога библиотек Астаны. Отдельный ценовой smoke завершился честным сообщением об отсутствии подтверждённых цен в извлечённом тексте — именно такой вариант был разрешён запросом.

Это не доказательство, что произвольный exhaustive research всегда завершится успешно или что каждое предложение модели формально следует из источника. Неизвестные данные, отказ внешней страницы и ограничения модели остаются наблюдаемыми результатами, а не поводом придумывать факты.

## 2. Reproduction до изменений

Выполнены:

```sh
git status --short --branch
git log -5 --oneline
git diff --check
go run -tags sqlite_fts5 ./cmd/qagent providers --json
go run -tags sqlite_fts5 ./cmd/qagent config --json
timeout 65s go run -tags sqlite_fts5 ./cmd/qagent doctor --json
go test -tags sqlite_fts5 ./...
go vet -tags sqlite_fts5 ./...
npm --prefix frontend run typecheck
npm --prefix frontend run build
```

Baseline unit/type/build checks прошли, но live research не был успешным. В тот момент:

- `qagent` и CPU `llama-server` были запущены;
- API отвечал на `127.0.0.1:8080`, модель — на `127.0.0.1:8000`;
- Docker Desktop отвечал, но не сообщал rootless mode;
- `.env` содержал SearXNG URL и Tavily key, их значения не печатались;
- CLI doctor возвращал общий `ok` несмотря на warning checks;
- provider diagnostics различали в основном configuration, не фактический outcome.

### Исходная пользовательская задача

`6645fb75-9361-4e59-82df-5ce8d5f6ff1d`, «универы айти»:

| Наблюдение | Сохранённый факт |
|---|---|
| План | Один `search`, один `fetch` вместо обязательного research workflow |
| Поиск | Реальные `tool.started` / `tool.completed` |
| Dead source | Первая страница не использована; fallback перешёл к следующей |
| Evidence | Страница `swissuniversities.ch`, один сохранённый evidence ID |
| Checkpoint | Два tool result с JSON-размерами около 5211 и 4565 символов |
| Failure | `verification_incomplete`; verifier сообщил `tool_results is empty` |

Новый smoke `fe74447b-cab9-4274-ba4b-56b72a929063` на старой сборке остановился ещё до плана с `context_budget_exceeded`. После первых исправлений smoke `66be2385-01d7-47e2-a0c3-57d6a82db350` на CPU остановился по HTTP client timeout при планировании. Лог модели показывал около 5,5 token/s. Это разные причины, а не универсальный `search_unavailable`.

## 3. Root causes и точки исправления

Номера относятся к исходникам ремонтируемого checkout; функции указаны для устойчивой навигации.

| Приоритет | Причина | Файл / точка |
|---|---|---|
| P0 | Целиком отбрасывались результаты, не помещавшиеся в бюджет | `internal/orchestrator/worker.go`, `compactToolResultsWithinBudget` (около 2608) |
| P0 | Configured context мог превышать runtime context модели | `internal/cli/model_context.go:14`, `effectiveModelContext`; startup в `internal/cli/root.go` |
| P0 | Пустые upstream failures маскировались как нулевой поиск | `internal/providers/research/searxng.go`, `Search`; `composite.go:64` |
| P1 | Intent не включал обычные noun/entity inquiries | `internal/orchestrator/research_intent.go:26` |
| P1 | Сложные planner schema и ошибочные counts от небольшой модели | `internal/orchestrator/schemas.go:17`; `plan.go:22` |
| P1 | Произвольные step IDs / criterion indices выбирала модель | `internal/orchestrator/verification.go:32`, `verificationCatalog` |
| P1 | Придуманные планом факты становились требованиями пользователя | `plan.go`, нормализация execution criteria и subject-bound descriptions |
| P1 | Проверялась готовность уже написанного отчёта, а не evidence readiness | `worker.go`, `verify`; `schemas.go`, `ready_to_answer` |
| P1 | Сырой finalizer мог забыть UUID citations или вернуть JSON-обёртку в UI | `answer_stream.go:24`; `worker.go`, `synthesize` |
| P1 | Failed extraction bytes могли теряться после рестарта | `worker.go`, `recordFetchCandidateFailure` / `reconcileResearchState`; `recovery_test.go` |
| P2 | Stage не менялся при `running → running` | `internal/storage/sqlite/store.go:196` |
| P2 | Polling мог закрыть SSE до загрузки финальной ошибки | `frontend/src/App.tsx`; `observability.mjs` |

## 4. Implemented changes и контракты

### Провайдеры

`internal/providers/research/types.go` / `outcomes.go`:

- `ProviderOutcome`: provider, state, safe code/message, retryability, HTTP status, latency, checked time, usable result/domain counts, bounded body size, content type, parser version;
- `SearchReport`: hits + outcomes;
- `DiagnosticSearchProvider.SearchWithDiagnostics` сохраняет совместимость с прежним `Search`;
- `SearchFailure` и typed classification заменяют разбор произвольного body/error text;
- `CompositeSearch.HealthSnapshot` возвращает копию последнего наблюдения под mutex;
- query, authorization и response body не входят в outcomes.

Composite ограничивает fanout и общий deadline. Полезные hits переживают частичный отказ. Empty + failed без usable hits — не валидный zero-result. Genuine all-valid-empty допускает один broadened query. Полная недоступность, oversized response и policy filtering имеют отдельные коды.

### Интеграция

`internal/tools/research.go` передаёт outcomes через context observer. Worker сохраняет sanitized `search.provider` events с task/step context, query hash, временами и latency. Ошибка сохранения события отменяет исполнение и не превращается в успешный результат. Cancellation во время URL validation не маскируется под allowlist filtering.

`SEARCH_ENABLE_DDG=false` отключает внешний DDG fallback. Default остаётся `true` ради удобства и явно обозначен в README, `.env.example` и UI. Для финальных диагностических запусков DDG был отключён в окружении приложения; challenge проверен отдельно opt-in probe.

### Извлечение

`smart_html.go` добавляет bounded JSON-LD `@graph`, article/product/FAQ properties, strict-JSON Next/Nuxt hydration, metadata и scoped semantic DOM. JavaScript не исполняется. Неподдерживаемый script остаётся недоверенными данными и не исполняется ради текста. PDF сохраняет прежние Docker/pure-Go limits; browser/OCR не добавлены.

### План и verifier

Planner schema — обычный bounded массив шагов, без зависимости от `prefixItems` + combinatorial `anyOf`. Backend проверяет ID, известные зависимости, циклы, допустимые инструменты и budgets. Read-only research plan нормализуется: ограниченное число facets/fetches, нейтральные IDs, порядок search → fetch, execution criteria и descriptions, привязанные к исходному goal. Unknown IDs, циклы и side-effect workflow не исправляются угадыванием.

Verifier получает фиксированный каталог `c0`, `c1`, … и required-key object. Нельзя пропустить search criterion, сослаться на выдуманный step ID или указать несуществующий criterion index. Fetch evidence IDs ограничены ID своего результата; search не выдаёт snippets за evidence. `ready_to_answer` означает возможность честного ответа до запуска finalizer, включая явно разрешённые неизвестные детали, а не существование готового отчёта.

Finalizer стримит schema-bound `{ "answer": "..." }`. Incremental decoder публикует только текст answer и отклоняет truncated/wrong/fenced/array envelopes. Реальные источники добавляются backend-ом, если модель не поставила inline markers. Выдуманные citation IDs по-прежнему не принимаются. Раздел источников показывает, какие evidence были переданы finalizer, а не является формальным доказательством каждого предложения.

## 5. Provider matrix: fixture и live

| Provider | Configuration | Live observation | Failure semantics |
|---|---|---|---|
| SearXNG | Loopback URL, enabled | Наблюдались usable results и HTTP 200 с отказами upstream engines | Empty + upstream failures — classified failure; partial results — degraded с usable count |
| Tavily | Key загружен из `.env`, enabled | Явный opt-in probe вернул HTTP 200, 3 usable hits, `healthy`; task runs также вернули hits | 401/403/432/433 — nonretryable misconfiguration/quota; 429 — rate limit; 5xx/timeout — outage |
| DDG | Configurable; выключен в финальном app runtime | Opt-in probe получил HTTP 202 challenge, `blocked`, 0 hits | Challenge/consent/captcha/parser drift не считаются валидным empty |

Команда live provider probe:

```sh
./qagent search-smoke --allow-external --json
```

Она сначала напечатала предупреждение о внешнем и потенциально billable Tavily request. Результат: SearXNG degraded с 3 hits, Tavily healthy с 3 hits, DDG blocked. Итоговый composite сохранил 3 usable hits. Ни ключ, ни response body в вывод не попали. Это реальный внешний probe; HTTP fixture matrix — отдельная проверка, не его замена.

Без `--allow-external` `search-smoke` использует только локальный SearXNG endpoint и не вызывает Tavily/DDG. Сам SearXNG при этом обращается к публичным upstream engines.

## 6. Worker, stages, retry и recovery

- Русские location/current-info queries идут в research, self-contained text — без web.
- Обычный research требует минимум двух independent facets и двух fetched sites; пользовательский URL сначала fetch-ится.
- Все candidate URL авторизуются отдельно. Dead/blocked/unsupported page не останавливает попытку использовать следующий approved candidate.
- Unsafe URL не обходится и не отправляется повторно как «сетевой retry».
- Genuine initial zero-result получает максимум один bounded broadened retry. Block/key/transport failures его не запускают.
- `tool.failed` audit сохраняет безопасную byte charge; UI получает отдельную sanitized failure summary.
- Failed extraction bytes + post-checkpoint successful bytes восстанавливаются без double counting. Тест 3 MB failed + 4 MB success восстанавливает 7 MB.
- Search/fetch/extract/verification/finalization stages сохраняются; новый fetch после extraction возвращает stage к fetch.
- Неизвестные non-idempotent outcomes не повторяются автоматически.
- Compaction сначала резервирует структуру, evidence IDs, URL, title и минимум текста каждого результата, потом делит оставшийся budget. Если даже metadata не помещается, model call не выполняется.

## 7. API / SSE / UI

Существующие endpoints сохранены:

- `/api/health`: process/database liveness и attached/absent worker;
- `/api/doctor`: app, worker, artifacts, DB, model endpoint, local search probe, sandbox и provider observations;
- `/api/providers`: enabled/configured отдельно от last health/outcome/time/latency;
- task events: `search.provider`, visible `tool.failed`, stages, evidence, failures, streamed text.

Doctor не запускает inference smoke одновременно с busy worker и не делает скрытые billable/external health requests. Локальный SearXNG probe имеет короткий deadline и различает usable hits и valid-empty. `configured` не становится `healthy` без наблюдения.

UI до запуска показывает provider cards и предупреждение о передаче query внешним сервисам. Timeline показывает actual provider outcomes. Failure card показывает error code, missing criteria и уже сохранённые источники. Terminal/reconnecting stream не называется `SSE live`. Poll-before-SSE race исправлен: terminal status отдельно инициирует один detail refresh; stale responses после смены selection отбрасываются.

## 8. Tests и результаты

Финальные обязательные команды:

```sh
go test -tags sqlite_fts5 ./...
go test -race -tags sqlite_fts5 ./...
go vet -tags sqlite_fts5 ./...
git diff --check
npm --prefix frontend run typecheck
npm --prefix frontend test
npm --prefix frontend run build
make test
make build
```

Все пакеты включены; environment-sensitive packages не исключались. Frontend suite содержит 12 deterministic helper/component-render tests, включая failure/source cards и terminal polling race. Это не тест реального браузера.

Ключевые regression suites:

| Файл | Что проверяет |
|---|---|
| `providers/research/outcomes_test.go` | Valid/empty/malformed, 401/403/429/5xx, Tavily quota, DDG challenge/parser drift, filtering, cancellation, health-copy races, metadata redaction |
| `providers/research/smart_html_test.go` | JSON-LD, Next/Nuxt JSON, semantic scope, noise exclusion, depth/size bounds, no JS execution |
| `orchestrator/reliability_test.go` | Сохранение всех tool results в тесном budget, recovered hit fields, fail-before-verifier при недостатке metadata budget |
| `orchestrator/verification_test.go` | Fixed criterion addressing, пропуски/unknown keys, сохранение смысла goal, read-plan normalization, cycle rejection |
| `orchestrator/answer_stream_test.go` | UTF-8/escape boundaries, surrogate pairs, malformed/truncated/wrong/fenced envelopes |
| `orchestrator/search_diagnostics_test.go` | Provider events, safe errors, storage failure cancellation, no broadening for blocked/misconfigured, exactly-once zero broadening |
| `orchestrator/recovery_test.go` | Restart, synthetic retry recovery, failed-byte budgets, orphan evidence, stage sequence |
| `orchestrator/http_integration_test.go` | `httptest` provider + fake LLM + real temporary SQLite/artifacts → complete/cited evidence; outage, invalid Tavily, DDG block, valid zero/broadening, policy filtering |
| `storage/sqlite/store_test.go` | Same-status stage transitions are durable and observable |
| `frontend/src/*.test.mjs` | Provider states, all-fail, missing evidence, React source cards, XSS escaping, SSE/terminal race |

Промежуточные failures не скрывались: baseline budget failure, CPU timeout, wrong verifier addressing, invented acceptance criteria, lost bytes/stage review findings и raw citation failures воспроизводились до соответствующих правок. Отдельно был выявлен `completed` с subject drift в software/IT; это не признано корректным результатом. После goal-bound descriptions/neutral IDs финальный live library run использовал физические библиотеки и соответствующие источники.

## 9. Runtime checklist

1. Запустить совместимый local model server. В этой среде используется уже установленный Vulkan `llama-server`, Qwen3.5-4B Q4_K_M, RTX 3050, один слот и runtime context 32768.
2. Проверить `/v1/models`, не путать `n_ctx` с `n_ctx_train`. Startup снижает declared context до reported runtime capacity, если сервер сообщает меньшую.
3. Поднять SearXNG через pinned Compose и корректный YAML; сохранить `SEARXNG_SECRET` локально.
4. Осознанно настроить Tavily и `SEARCH_ENABLE_DDG`; `.env` загружается автоматически, непустой environment имеет приоритет.
5. Запустить `qagent serve` и проверить `doctor --json`, `providers --live --json`.
6. Создать и отдельно запустить задачу. `queued` — не доказательство выполнения.
7. Проверить events → sources/evidence → final response, а не только зелёный HTTP endpoint.

Удобные команды: `make doctor`, `make search-smoke`. Последняя не включает billable/external adapters без явного флага. `.env` не нужно выводить в терминал или отчёт.

## 10. Remaining blockers и ограничения

- Docker Desktop в этой среде не rootless. Sandbox и isolated Poppler требуют настоящего rootless daemon и локальных images; соответствующая проверка остаётся warning. Для PDF доступен ранее реализованный process-bounded pure-Go fallback; это не OS sandbox.
- SearXNG upstream engines могут блокироваться независимо от локального HTTP 200. DDG live challenge действительно наблюдался. Сменить/восстановить внешний сервис — действие окружения, не скрытый retry.
- Tavily key был работоспособен в opt-in probe и task runs, но квоты/сеть могут измениться. Проверка не гарантирует будущую доступность.
- Страница может не содержать нужных данных в статическом/structured/hydration тексте. JS execution и OCR не добавлялись; неизвестные значения не придумываются.
- Citation ownership/IDs/provenance проверяются детерминированно. Полная semantic entailment каждого утверждения модельного ответа не доказана формально; «все университеты мира» нельзя считать exhaustively verified по двум страницам.
- Браузерное end-to-end управление мышью не выполнялось. Проверены HTTP/SSE и реальные React component renders, не установлен browser automation.

## 11. Manual smoke transcript

Реальные runs отделены от fixture tests:

| Запрос / task ID | Фактический результат |
|---|---|
| Исходный IT-вуз, `6645fb75-9361-4e59-82df-5ce8d5f6ff1d` | Baseline search/fetch/evidence существовали, verifier получил empty input → reproduced bug |
| Библиотеки на старой сборке, `fe74447b-cab9-4274-ba4b-56b72a929063` | `context_budget_exceeded` до planner |
| CPU smoke, `66be2385-01d7-47e2-a0c3-57d6a82db350` | Planner timeout; затем GPU run выявил criterion addressing failures |
| Рестораны, `694f2a53-715b-4ad1-aa70-20c74b83b7f8` | После нормализации завершился `completed` с источниками |
| HTTP/3, `db8af055-1819-4167-afdc-f17c05701aba` | `completed` после live search/fetch |
| Локальный текст, `16dd54c7-7ca3-4755-9010-1a3b96140c04` | `completed`, только `text.inspect`, без search |
| URL, `1e015547-f293-4b29-abc0-d543d75c24ad` | `completed`, первый вызов `fetch_url`, без discovery search |
| Библиотеки, `2bafef6d-6d54-4636-93fd-845e4fcba53c` | Local-SearXNG-only run получил upstream engine failures → classified `search_unavailable`, не fake zero |
| Цены с разрешённым unknown, `a62193d5-de01-455f-a6b2-9e4f48ea79e6` | `completed`: честно сообщил, что подтверждённых цен в извлечённом тексте нет; сохранил два sources |
| Финальная physical-location проверка, `046a29ff-9ceb-455c-908f-07d9c8f39598` | `completed`, два search + два fetch, evidence из `nabrk.kz/ru` и `astana.jsprav.ru/biblioteki/`, final text и source-reference IDs |

Финальный library transcript содержит query `библиотеки в Астане`, реальные provider outcomes, fetched source titles и два UUID evidence. Ответ содержит список физических библиотек и ссылки на сохранённые sources, а не IT-курсы. Это live-проверка planner/action/verifier/finalizer на локальной модели, а не только smoke `{ "ok": true }`.

Для ценового run source references: `sibdroid.ru/catalog/samsung_galaxy_a55/` и публичная страница второго сайта. Числа из snippets не были выданы за подтверждённые цены: final answer явно сообщил об отсутствии достаточных price values. Такой ограниченный ответ разрешён формулировкой smoke; он не доказывает найденную актуальную цену.

HTTP/JSON и fixture results не заменяются этим transcript, а дополняют его: внешняя доступность изменяется во времени, и сохранённые task events остаются источником фактов о каждой конкретной попытке.
