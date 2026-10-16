# Диагностика поиска и отчёт по тестам

**Дата проверки:** 2026-10-17
**Репозиторий:** `main`, HEAD `dae42c9` (`feat: stream answers and isolate PDF extraction`)
**Адрес приложения во время проверки:** <http://127.0.0.1:8080>

## Краткий вывод

Поиск не является постоянно работающей службой внутри `qagent`: поисковый инструмент регистрируется только при настроенном SearXNG и/или Tavily. Во время этой проверки оба провайдера были отключены, SearXNG на `127.0.0.1:8081` не отвечал, а Docker daemon был недоступен. Поэтому новая задача, действительно распознанная как веб-исследование, завершится `search_unavailable` до планирования.

У последней задачи пользователя обнаружена и отдельная проблема: запрос **«библиотеки в астане»** не распознался как веб-поиск. Модель создала `analysis`-шаг и вызвала `text.inspect`; `tool.started` при этом показывал текст, будто поиск уже выполнен. Реального события `tool: search` не было. Верификатор не принял ответ без списка библиотек и завершил задачу с `verification_incomplete`. Это объясняет, почему задача выглядела как поиск, но фактически не обращалась к поисковому провайдеру.

Тесты поискового контура проходят, но это преимущественно детерминированные тесты с HTTP-серверами `httptest`, stub-провайдерами и scripted LLM. Они доказывают поведение кода и его отказоустойчивость, но **не доказывают, что у текущего приложения настроен рабочий интернет-поиск**.

## Фактическое состояние во время проверки

Проверены `/api/providers`, `/api/doctor`, локальные процессы и адреса провайдеров.

| Компонент | Наблюдение |
|---|---|
| Сайт и API | `http://127.0.0.1:8080` отвечает HTTP 200 |
| SQLite и embedded frontend | `ok` в `/api/doctor` |
| Локальная Qwen | `ok`: строгий JSON Schema smoke-запрос прошёл на локальном `llama-server` |
| Search provider | `disabled`; `SEARXNG_URL` пустой, Tavily не настроен |
| SearXNG | `127.0.0.1:8081` не отвечает |
| Docker | Docker Desktop socket и default socket недоступны; в выводе `docker info` нет признака rootless режима |
| Sandbox / PDF | недоступны без rootless Docker и локальных образов |

Важно: общий `status` в `/api/doctor` был `ok`, хотя проверки search и sandbox имели статус `warning`. При этом `/api/providers` явно показывал search `disabled`. Общий `ok` не означает, что поиск настроен.

### Последняя задача «библиотеки в астане»

Задача `8ee2d088-1446-474d-a06c-67f655df746c` была создана `2026-10-16 18:54:43 UTC` и завершилась ошибкой в `18:55:48 UTC`.

Сохранённый план состоял из одного шага:

```text
id: {task_id}
kind: analysis
description: «Использовать поиск для получения списка библиотек в Астане»
success_criteria: «Получен список библиотек»
```

Хотя в описании шага было слово «поиск», его `kind` — `analysis`. Исполнен `text.inspect`; инструмента `search` в истории задачи нет. Summary события `tool.started` утверждал, что найден список из 12 учреждений, но верификатор установил, что результат содержит только метаданные/статистику, а списка библиотек нет. Итоговый `task.failed` имел `error_code: verification_incomplete` и указал отсутствующий список как unmet requirement.

Дополнительно: планировщик выбрал placeholder ID `{task_id}`, который доменная проверка сейчас допускает. В результате сообщения об ошибках верификатора ссылаются на несовпадающие ID (`step_0`, `{task_id}`). Это не первопричина отсутствия поиска, но затрудняет диагностику.

### Предыдущая задача «рестораны»

Для задачи `4879f1f4-9d55-4f7d-afe0-be619523591c` сохранены разные исходы разных запусков:

- В запуске `2026-10-16 07:31 UTC` были реальные события `tool: search`, затем `tool: fetch_url`; search event сохранил 8 результатов от `searxng` и `tavily` на 7 hostnames, после чего задача дошла до verification и завершилась `verification_incomplete`, потому что извлечённые сведения не покрыли требования пользователя.
- В четырёх последующих попытках около `18:54 UTC` задача останавливалась ещё на planning с `search_unavailable`. К этому времени search providers были отключены.

Это показывает две разные ситуации: **поиск работал, но evidence не хватило** в первом запуске; позже **поиск вообще не был доступен**.

## Как проходит поиск в приложении

```text
Создать задачу (inbox)
  → отдельно запустить / queue task
  → qagent serve worker забирает очередь
  → эвристика requiresWebResearch решает, обязателен ли web search
  → проверка, что search tool зарегистрирован
  → план (search → условный retry → fetch)
  → SearXNG и/или Tavily
  → фильтрация/дедупликация URL и SSRF-проверка
  → fetch URL → извлечение текста → artifact + task-owned evidence
  → проверка source floor и критериев/evidence ID
  → synthesis и citation validation
  → task.completed
```

### 1. Создание и запуск — разные действия

UI сначала создаёт задачу в статусе `inbox`; нужно отдельно нажать «Запустить». CLI `qagent task run ID` тоже только ставит задачу в очередь. Выполняет её один worker внутри процесса `qagent serve`. Если `serve` не работает, queued-задача останется без worker failure.

Основные места: `internal/app/service.go`, `internal/api/server.go` (`POST /api/tasks/{id}/run`), `internal/cli/root.go`, `internal/orchestrator/worker.go` (`Run`, `runTask`, `execute`).

### 2. Распознавание намерения — эвристическое

`internal/orchestrator/research_intent.go` содержит `requiresWebResearch`. Она проверяет маркеры вроде `research`, `latest information`, `актуальн`, `поищи`, а location-вопрос определяет через ограниченный шаблон, требующий `where` или `где` плюс предлог места.

Фраза **«библиотеки в астане»** не содержит `где`, `актуальн`, `поищи` и других маркеров, поэтому не сработала. Когда эвристика возвращает false, backend не требует `search`; планировщик может выбрать локальный `analysis`/`text.inspect`, как и произошло.

Существующий тест `TestRequiresWebResearchRecognizesRussianAndEnglishRequests` проверяет вопросы наподобие «Где находится…?», но не проверяет noun phrase «библиотеки в Астане» или «рестораны в Астане» без слова «где».

### 3. Провайдеры должны быть настроены при старте

`internal/cli/root.go:configuredSearchProvider` строит composite из настроенных источников:

- **SearXNG:** задаётся через `SEARXNG_URL`; URL должен вести на loopback. В репозитории есть `compose.searxng.yml`, но для запуска нужен доступный Docker daemon.
- **Tavily:** задаётся через `TAVILY_API_KEY`; ключ используется только в Authorization header и не выводится в diagnostics.

`internal/tools/research.go:RegisterResearchTools` регистрирует `search` только если передан search provider. Если `requiresWebResearch` сработал, а инструмента нет, `execute` возвращает `search_unavailable` до вызова planner/action модели. Настройки читаются при старте процесса: после изменения `SEARXNG_URL` или `TAVILY_API_KEY` нужен перезапуск `qagent serve`.

### 4. Search: успешные пустые результаты и provider error — разные случаи

`CompositeSearch` (`internal/providers/research/composite.go`) вызывает настроенные провайдеры параллельно:

- Если один provider падает, результаты второго сохраняются.
- Если все провайдеры падают, возвращается `search_unavailable` (либо ошибка oversized response).
- Валидный пустой ответ одного провайдера — не ошибка; composite сохраняет его и возвращает успешный пустой результат, если все успешные ответы пусты.

После удачного, но пустого/недостаточного ответа worker может исполнить **один** `search_retry_1` с более широким запросом. Если сам provider недоступен и tool возвращает ошибку, этот retry не запускается: задача заканчивается `search_unavailable`. Retry в коде — это повтор запроса после успешного пустого/недостаточного результата, не повтор сетевого сбоя.

### 5. Search results — кандидаты, а не evidence

`search` фильтрует результаты по `ALLOWED_DOMAINS`, проверяет URL fetch policy и запоминает только безопасные task-known ссылки. Сниппет сам по себе не становится доказательством. `fetch_url` повторно проверяет URL, SSRF/DNS/redirect, limits и MIME, сохраняет raw/text artifacts и создаёт evidence с provenance.

Для веб-исследования требуется evidence как минимум с **двух разных registrable domains**. Это проверка различия сайтов, а не автоматическая оценка их надёжности/независимости. Проверка цитат гарантирует, что ID существует и принадлежит нужной задаче/шагу; она не доказывает, что текст источника логически подтверждает каждый тезис.

### 6. Верификация и то, что видно в UI

До verification/synthesis пользователь может видеть статусы шагов и events, но не поток токенов: `message.delta` начинается только после успешной verification. Затем provisional chunks стримятся через SSE; canonical assistant message появляется только после citation validation и `CompleteTask`.

Ошибки worker асинхронны: `POST .../run` отвечает accepted/queued, а результат приходит позже как task status/event. Для `verification_incomplete` payload содержит verification summary и `missing`; для `search_unavailable` — пользовательское сообщение об отсутствии search provider. `tool.failed` для fetch имеет audit visibility и не отправляется через публичный SSE allowlist.

UI отображает task events/timeline, но у frontend сейчас нет автоматизированного React test runner. Поэтому формат/логика отображения ошибок не покрыты полноценным UI-тестом.

## Search/research test inventory

Все названные ниже тесты прошли в последнем полном tagged запуске.

### Composite provider

- `TestCompositeSearchDeduplicatesFiltersAndCapsRoundRobinResults` — URL dedup, allowlist, result cap и round-robin merge.
- `TestCompositeSearchMovesPreferredDomainsForwardWithoutDroppingOtherHits` — предпочтение доменов не превращается в жёсткий фильтр.
- `TestCompositeSearchKeepsPartialResultsAndEmptySuccess` — ошибка одного provider не теряет другой успешный ответ; корректный empty success сохраняется.
- `TestCompositeSearchReturnsUnavailableOrOversizedWhenAllProvidersFail` — классификация полной недоступности и oversized result.
- `TestCompositeSearchHonorsCanceledContextAndRejectsInvalidLimit` — context cancellation и неверный лимит.

### SearXNG

- `TestSearXNGSearchNormalizesAndFiltersHits` — формат запроса, нормализация и фильтрация fixture-ответа.
- `TestSearXNGRequiresLoopbackConfiguration` — удалённый endpoint запрещён.
- `TestSearXNGClassifiesProviderHTTPFailure` — HTTP failure становится `ErrSearchUnavailable`.
- `TestSearXNGDistinguishesEmptyResultsFromMalformedResponse` — различие valid `results: []` и malformed/null/missing.
- `TestSearXNGClassifiesOversizedResponse` — ограничение размера.

### Tavily

- `TestTavilySearchUsesBearerAuthAndNormalizesResults` — POST payload, bearer test key, нормализация и таint.
- `TestTavilyPrefersOfficialDomainsWithoutHardFiltering` — preferred domains не отбрасывают остальные валидные результаты.
- `TestTavilyValidatesQueryAndResultLimitBeforeRequest` — неверные параметры блокируются до HTTP запроса.
- `TestTavilyBoundsClientTimeout` — верхняя граница HTTP timeout.
- `TestTavilyDoesNotExposeAPIKeyInErrors` — key redaction для HTTP/transport failures.
- `TestTavilyHandlesMalformedAndOversizedResponses` — malformed и oversized output.
- `TestNewTavilyRequiresKey` — пустой key отклоняется.

Эти provider-тесты используют `httptest`/fake transport; они не авторизуются на реальном Tavily и не проверяют live SearXNG.

### SSRF-safe fetch, extraction и provenance

- `TestValidateURLRejectsLocalAndPrivateTargets` — локальные/private/reserved IP, схемы, порты, credentials и allowlist.
- `TestDialContextBlocksDNSRebindingOnConnection` — повторная DNS-проверка перед соединением.
- `TestFetchBoundsBodyAndBlocksPrivateRedirect` — response bytes, MIME sniffing и redirect policy.
- `TestPDFFetchHasFiveMegabyteCeilingEvenWhenTaskBudgetIsLarger` — отдельный PDF fetch cap.
- `TestFetchRequestFailureIsRetryableButDoesNotHidePolicyOrCancellation` — transport failure retry marker не скрывает cancellation/policy error.
- `TestCanonicalURLAndSearchDomainFilter` — suffix/domain filtering.
- `TestResearchToolsRequireKnownURLAndPersistEvidence` — task-known URL, повторный fetch, fetch byte budget, artifacts/evidence persistence.
- `TestFetchToolReturnsFourThousandRuneEvidenceExcerpt` — excerpt cap и хранение evidence.
- `TestFailedPDFExtractionPersistsNoArtifactOrEvidence` — ошибки PDF не оставляют частичные artifacts/evidence.
- `TestFetchToolIncludesPageCountWhenExtractorProvidesIt` — page count передаётся в tool result.
- `TestURLLedgerTracksOnlyTaskKnownSources` и `TestRegistryTracksRequestedAndFinalFetchURLsAcrossRecovery` — task-scoped URL ledger/recovery.
- `TestBuildEvidenceSelectsContiguousQueryRelevantExcerpt`, `TestBuildEvidenceRetainsLargeMultilingualExcerptWithRuneOffsets`, `TestBuildEvidenceCarriesHashTaintQuotesAndArtifactReferences` — excerpt relevance, Unicode offsets и provenance metadata.

### Планирование, retry и worker

- `TestRequiresWebResearchRecognizesRussianAndEnglishRequests` — текущий эвристический классификатор на выбранных fixture-примерах; формулировка пользователя «библиотеки в Астане» в список не входит.
- `TestGenericResearchDecomposesIntoBoundedSearchAndFetchSteps` — baseline plan: focused search, условный retry, два fetch.
- `TestResearchRequestWithUserURLKeepsURLFirstAndAddsDiscovery` — URL fetch перед discovery.
- `TestResearchQuestionsAndQueriesRejectDuplicatesGenerically` — duplicate question/query rejection.
- `TestBroadeningDuplicateRetryQueryDropsRestrictiveTerms` — deterministic fallback для дублирующего retry query.
- `TestResearchSourceCoverageUsesDistinctSites` и `TestSearchRetryDecisionCountsDistinctFetchableSites` — подсчёт distinct domains/candidates и исключение failed URL.
- `TestAvailableFetchURLsPreferUnfetchedDistinctSites`, `TestAvailableFetchURLsRetryFailedUserURLButExcludeFailedDiscoveryURL`, `TestAvailableFetchURLsSkipsAlreadyFetchedSourceSite` — candidate selection, retry исходного URL и anti-loop для найденных ссылок.
- `TestResearchTaskWithoutSearchFailsClearlyBeforeCallingModel` — при распознанном research-intent без search tool worker прекращает задачу до model planning.
- `TestPersistedPlanReplacementRequiresOnlyIdempotentUntouchedActions` и `TestWasExplicitFailedRetryDoesNotTreatPauseResumeAsRetry` — безопасная замена плана и явный retry.
- `TestResearchPlanSchemaRequiresConditionalSearchRetryAndTwoFetches`, `TestResearchPlanSchemaCanFetchSuppliedURLBeforeDiscovery`, `TestActionSchemaBindsArgumentsToSelectedTool` — структурные ограничения плана/action.
- `TestWorkerRunsGenericResearchAndStoresCitedEvidence` — scripted LLM + stub search/fetch/extractor, локальная SQLite evidence и citation flow.
- `TestWorkerBroadensOnceAfterAnEmptySearchResult` — scripted LLM + search stub возвращают empty, затем retry hits; проверяет ровно два запроса, не live provider.
- `TestWorkerRetriesTransientFailureForUserProvidedURL` — requeue fetch исходного URL после transient failure.
- `TestCitationValidationRejectsMissingAndInventedEvidence`, `TestVerifierCannotCompleteWithoutAssessingEveryPlanCriterion`, `TestExecutionCoverageRequiresTaskOwnedFetchEvidence` — локальные backend evidence/citation gates.
- `TestReconcileFetchEvidenceWrittenBeforeToolCompleted`, `TestReconcileFetchWithoutEvidenceFailsClosed`, `TestReconcileToolCompletedEventWithoutCheckpoint` — durable recovery state.

`TestStructuredOutputRepairKeepsSchemaAndRetriesOnce` относится к **ремонту формата JSON**, а не к search-query retry.

### API/configuration и модельная граница

- `TestProvidersReportsTavilyConfiguredWithoutKey` и `TestSearchProviderCommandsNeverExposeTavilyKey` — диагностика сообщает configured status и не раскрывает key.
- `TestDoctorDoesNotReportMalformedSearXNGResponseAsHealthy` — malformed SearXNG response не считается здоровым.
- `TestDoctorRequiresStrictStructuredModelSmoke` и `TestProbeStructuredOutputUsesBoundedStrictSchema` — smoke проверяет structured output локальной модели, но не интернет-поиск.
- `TestLoadTrimsOptionalTavilyAPIKey` и `TestValidateRejectsNonLoopbackBindAndLLM` — env/config и loopback security.
- `TestSSEReplaysOnlyUserVisibleEvents` — API replay/redaction; frontend failure rendering этим тестом не покрывается.

### Отдельно: локальный FTS5 search по памяти

`TestMemoryRetrievalRequiresAcceptanceAndHonorsScope`, `TestMemoryValidatesScopeAndEvidenceProvenance`, `TestMemoryFTS5FreshSchemaTriggerSynchronizationAndSafeQueries`, `TestMemoryFTS5MigrationBackfillsExistingMemories` проверяют FTS5 для локальных принятых memories. Это не тесты web search.

## Итог тестирования и границы доказательств

Последний полный успешный набор команд:

```sh
CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./...
make test
make vet
make build
```

Они подтвердили компиляцию, локальные unit/integration tests, race suite, фронтенд typecheck/build и embedded assets. `make test` запускает Go tests с `sqlite_fts5` и `npm run typecheck`; **он не запускает браузерные UI-тесты**, потому что тестового frontend runner в проекте нет.

Что этими тестами не подтверждено:

1. Реальный внешний search query к SearXNG/Tavily: provider-тесты используют local fake HTTP transport.
2. Рабочий Tavily credential: тестовый bearer key никуда не отправляется.
3. Поведение Qwen при реальном search task: orchestration-тесты используют scripted LLM; live `qagent doctor` подтверждает только bounded JSON Schema smoke.
4. React UI поведение при фактическом `search_unavailable`/`verification_incomplete`: есть API SSE tests и build/typecheck, но нет UI tests.
5. Надёжность/независимость источников и то, поддерживает ли excerpt конкретное фактическое утверждение: проверки гарантируют URL/evidence ownership и citation ID, но не entailment.
6. Живой PDF container: runtime тестируется fake Docker runner. В текущем окружении `docker info` не показывает rootless daemon, поэтому реальную обработку PDF не запускали.

## Диагноз и следующий шаг для включения web search

**Непосредственная причина задачи `8ee2d088-1446-474d-a06c-67f655df746c`:** её формулировка не активировала `requiresWebResearch`; planner создал `analysis`, а не `search`; вызванный `text.inspect` не мог получить библиотеки из интернета; verifier корректно не завершил задачу с выдуманным списком. **Текущее отдельное ограничение:** SearXNG и Tavily оба отключены, поэтому после расширения intent detection поисковая задача всё равно будет завершаться `search_unavailable`, пока не настроен provider.

Для настоящего web search нужно включить один из поддерживаемых провайдеров и перезапустить `qagent serve`:

- SearXNG: поднять Compose service в rootless Docker и экспортировать `SEARXNG_URL=http://127.0.0.1:8081`.
- Tavily: экспортировать `TAVILY_API_KEY` из локального secret store. В отчёт и shell transcript сам key не копировать.

До пользовательского тестирования стоит добавить regression case для «библиотеки в Астане»/«рестораны в Астане» без слова «где», backend-правило, запрещающее `analysis`-only plan при location-bound/current-info запросе, и автоматизированное UI-покрытие видимости `task.failed` с verification `missing`. Иначе строки вида «использовать поиск» в `description` сами по себе не заставят backend вызвать search tool.

## Addendum: fixes after the captured run

The previous sections record the deployed state and events observed before this mission's code changes. The follow-up changes add intent regressions for the reported Russian location/entity queries and informational lookups, ban analysis steps in recognized research plans, strictly validate ASCII step IDs, allow 2–3 independent search facets and 2–4 fetches, retry only a successful zero-hit initial query, and expand the candidate pools to eight model-visible hits and six fetch URLs. The API doctor now reports disabled search as a warning and propagates warnings to the overall status.

PDF extraction now prefers the isolated Poppler image and falls back to the Go `dslipak/pdf` parser in a re-executed helper process when the image or rootless Docker is unavailable. This is deliberately a child process rather than an in-process parser so a five-second timeout can terminate it; it still is not an OS-level sandbox, and the Go memory limit is a runtime guard rather than a hard cgroup limit. Both paths enforce the 5 MB input, 20-page, and 50,000-rune output limits.

After these fixes, the race suite, `make test`, `make vet`, and `make build` pass with the `sqlite_fts5` tag. The current running app still has no SearXNG or Tavily provider configured, so web-research requests will correctly stop with `search_unavailable` until a provider is enabled and `qagent serve` is restarted. Automated tests still do not substitute for a live provider test.
