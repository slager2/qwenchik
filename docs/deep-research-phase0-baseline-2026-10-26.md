# Deep Research reliability — Фаза 0: baseline и честный аудит

<!-- Hallmark · audit pre-emit critique: P4 H3 E3 S5 R4 V4; qualitative source review, not live visual measurement. -->

Дата: 2026-10-26. Исследованный HEAD: `6ca63799cf8a03b0753ce3d503bff1bc6b43a887`.
Это **новый** поэтапный roadmap, не продолжение старой нумерации laboratory phases.

**Вывод:** инфраструктура research/report/recovery существует и проходит scripted
tests. Сохранённые реальные отчёты существуют, но полезный roadmap, содержательная
полнота, многокруговой real-model research и длительная live acceptance пока
**Not established**. Больше шагов само по себе не исправит проверку результата.

## 1. Границы аудита и сохранность рабочего дерева

До изменений выполнены `git status --short`, `git log --oneline -12`, проверка HEAD
и ветки. Tracked-изменений пользователя не было; `main` совпадал с `origin/main`.
Существовали untracked `.idea/`, `internal/cli/data/`, `internal/orchestrator/data/`,
`internal/providers/research/data/`, `internal/tools/data/`, `repomix-ai.xml`.
Они не удалялись, не редактировались намеренно и не включаются в коммит отчёта.
Локальный ignored `.env` прочитан только по allowlist несекретных флагов.
В конце аудита `repomix-ai.xml` перестал присутствовать в workspace и не оказался
ignored; команды/правки этой фазы его не удаляли. Он не восстанавливался поверх
возможных внешних действий пользователя. Остальные исходные untracked entries
сохранились; новых tracked-изменений помимо отчёта нет.

Фаза 0 не меняет код, конфигурацию, migrations, checkpoints, evidence, артефакты,
routes или состояние сервисов. Единственное добавление — этот отчёт. Обязательная
frontend build пересоздала assets идентично HEAD: tracked diff остался пустым.
База исследовалась через `sqlite3 -readonly` с `PRAGMA query_only=ON`; новые задачи
и inference не запускались. `doctor`, который может инициировать inference,
не использовался. agentSearcher и дополнительные подсистемы не подключались.

Четыре независимых read-only агента работали параллельно:

| Аудит | Session | Проверка интегратором |
| --- | --- | --- |
| Backend/budget state machine | `ses_e87b48790ffejKjjE1oZzvgGW3` | Continuation transaction, нормализация, verifier, notes/report, профили |
| Runtime/model/context | `ses_e87b48777ffe1ONVwIpJ61fra5` | Context discovery, service units/state, снимки, input budgeting |
| Frontend/Hallmark audit | `ses_e87b48769ffe4a8xWWLVCHwGsz` | ChatRoute, Transcript, Inspector, CSS и существующие browser assertions |
| Acceptance/real-run evidence | `ses_e87b4875dffeT42Rh3Rpz75bpA` | Soak/benchmark code, read-only SQL и фактический Markdown артефакт |

Выводы агентов не принимались автоматически. Например, предположение о свободном
понижении ранее critical criterion до supplementary **не принято как подтверждённый
production defect**: `worker.go:2782–2810` сохраняет critical-классификацию и
незакрытые вопросы. Удалять такой frontier только ради PASS было бы неверно.
Утверждение backend-агента о невозможности самому запустить четыре агента относится
к его sandbox; интегратор действительно запустил четыре независимых аудита.

## 2. Свежий baseline

| Команда | Рабочий каталог | Результат |
| --- | --- | --- |
| `CGO_ENABLED=1 go test -tags sqlite_fts5 ./...` | root | **PASS**, все Go packages; часть результатов cached; orchestrator 147.416s |
| `go vet -tags sqlite_fts5 ./...` | root | **PASS** |
| `npm test -- --runInBand` | frontend | **PASS**, 77/77 |
| `npx tsc --noEmit --pretty false` | frontend | **PASS** |
| `npm run build` | frontend | **PASS**, 315 modules; assets идентичны HEAD |
| `git diff --check` | root | **PASS** |

Environmental и product test failures в этом baseline **не возникли**. Ничего не
skipped специально, assertions не удалялись. IPv6 listener и ACL/xattr workaround
не понадобились. При будущем environment failure следует отдельно доказать
недоступность IPv6 либо filesystem operation; для IPv4 listener допустим
`127.0.0.1:0`, но security failure нельзя переименовать в unsupported filesystem.

Browser tests/live browser/screenshot/contrast tests в Фазе 0 не выполнялись:
изучено их существующее покрытие. Это не новый полный race-suite и не live soak.

## 3. Capability/proof matrix

Термины: **Implemented** — код существует; **Scripted-test verified** — текущий
baseline выполняет соответствующие fixtures; **Real-model verified** — есть
сохранённое свидетельство реального локального inference для конкретного свойства;
**Live acceptance verified** — пройден конкретный содержательный live-сценарий;
**Not established** — достаточного доказательства нет. Исторический запуск не
становится новым экспериментом от повторного чтения его артефакта.

| Возможность | Есть в коде | Работает scripted | Работает на real model | Проверено live |
| --- | --- | --- | --- | --- |
| Adaptive state/frontier | Implemented | Scripted-test verified: два раунда и reopen | Флаги/state есть в трёх реальных runs, но только один раунд | Многокруговая полезная адаптация: Not established |
| Section reports | Implemented | Correction/fallback/coverage/section+artifact recovery verified | Real-model verified: JSON/Markdown и supported/fallback sections сохранены | Полезный полный roadmap: Not established |
| Source notes | Implemented | Ownership/hash-metadata/MIME/foreign evidence tests verified | По две notes в checkpoint каждого из трёх runs | Содержательная релевантность notes: Not established |
| Evidence Ledger/provenance | Evidence/artifact/events и criterion links есть | Ownership, hashes, replay/reservations verified | По два evidence в каждом из трёх runs | Наличие evidence подтверждено; factual truth/полнота не доказаны |
| Completion continuation | Implemented, append в общий plan | Fixed/adaptive transaction, duplicate/stale/reopen tests verified | В трёх исследованных новых real runs продолжений не было | Новая real multi-round acceptance: Not established |
| Context compaction/pins | Implemented | Protected compaction и pinned recovery verified | В каждом run четыре `context.compacted` события | Full-window нагрузка/качество после compaction: Not established |
| Model snapshots | Implemented | Stored endpoint/context/sampling и recovery checks verified | В реальных runs 32768 context, 4096 output, temperature 0.2 | Не fingerprint GGUF/tokenizer; соответствие весов не доказано |
| Runtime context | Startup discovery/capping Implemented | Metadata/shrink fixtures verified | Исторические runs имеют snapshot 32K | Сейчас сервер недоступен; ladder Not established |
| Research Inspector | Implemented | State/render/replay unit tests verified | Обрабатывает данные реальных runs через существующий API | Новый live UX/a11y/report lifecycle smoke не выполнен |
| Privacy modes | Три режима Implemented | Routing, corpus pinning, offline pipeline verified | Исследованные real reports: external-search mode | Реальный offline benchmark: Not established |
| Report artifacts | Implemented, fenced persistence | Content-addressed artifact/reopen tests verified | Real-model verified: по JSON+Markdown на run | Полезность не следует из существования download |
| Restart/recovery | Leases/journal/checkpoints Implemented | Adaptive/report/fetch fault fixtures verified | Исторический bounded supplied-URL recovery записан | Новая real adaptive/report crash recovery: Not established |
| Offline benchmark | 12 tasks, 8 development + 4 held-out | Manifest/scoring/cleanup/compare fixtures verified | Реальная baseline/adaptive пара не найдена | Not established |
| 15m/60m/4h observation | CLI Implemented | Virtual-time rejection tests verified | Сохранённые live-stage receipts не установлены | Not established; predicate недостаточен для useful workload |

Evidence Ledger здесь означает существующие evidence/artifact records, hashes,
events, quote spans и assessment links. `tools.URLLedger` — отдельный механизм
разрешённых/использованных URL. Ни тот ни другой не следует объявлять независимым
fact-checker или полноценным реестром claim-resolution без соответствующего кода.

## 4. Фактические функции и точки контроля

| Область | Файл и функции | Подтверждённое поведение |
| --- | --- | --- |
| План | `internal/orchestrator/plan.go:18–207`, `normalizeResearchPlanWithinLimit` | Lossless bounded facets; поиск/fetch criteria заменяются execution criteria на 184/190 |
| Continuation | `internal/orchestrator/completion.go:113–147`, `requestCompletionContinuation`; `internal/storage/sqlite/completion.go:14–146`, `RequestResearchContinuation` | Две search+две fetch добавляются к тому же plan; transaction повторяет admission |
| Adaptive | `internal/storage/sqlite/adaptive_research.go`, `assessAdaptiveRound`, `StopAdaptiveResearch`, `adaptiveAssessmentHash` | Typed rounds/frontier, stagnation, counters, assessment identity |
| Reservations | `internal/storage/sqlite/invocations.go`, `BeginInvocation`; `research_sessions.go:365–452`, `reserveResearchOperationTx` | Invocation/reservation фиксируются до dispatch; failed attempts не возвращают расход |
| Stale state | `research_sessions.go:534–623`, `prepareResearchCheckpoint` | Ordinary stale save не заменяет canonical adaptive state; counters монотонны |
| Evidence | `internal/tools/research.go`, `RegisterResearchTools`; `internal/storage/sqlite/research_source.go`, `SaveResearchSource` | Fetch/extract, raw/text artifacts и task-owned evidence |
| Notes | `internal/orchestrator/source_notes.go:17–103`, `buildSourceNotes`, `refreshSourceNotes`, `compactSourceNotes` | До 32 notes; компактно первые восемь по ID; excerpt/metadata, не raw HTML/PDF |
| Verification | `internal/orchestrator/verification.go:43–53`, `verificationCatalog`; `worker.go`, `verify` | Catalog строится из plan step criteria; skipped steps исключены |
| Reports | `internal/orchestrator/research_report.go`, `reportJSON`, `writeReportSection`, `writeResearchReport`, `reportExecutionCriterion` | Outline → отдельные writer/critic calls → одна correction → fallback → backend assembly |
| Artifact storage | `internal/storage/sqlite/research_report.go`, `SaveResearchReportArtifact` | Fenced metadata commit без дополнительного evidence slot |
| Context | `internal/cli/model_context.go:14–52`, `effectiveModelContext`; `root.go:890–903`, `maxInputBytes`; `worker.go:3293–3435`, compaction/admission helpers | Startup min(config, reported positive context), byte estimate и bounded extractive compaction |
| Snapshots | `internal/config/research.go:233–239`, `ResearchModelSnapshot`; `internal/orchestrator/research_run.go:33–166`, `prepare`, `researchExecutor`, `modelExecutor` | Immutable run settings; compatibility сравнивается со startup-held runtime value |
| Inference slot | `internal/llm/guard.go`, `GuardedClient.Complete/Stream/TryComplete` | Один shared slot внутри app; stream удерживает slot до upstream close |
| Resource pause | `internal/orchestrator/runtime_guard.go`; `internal/storage/sqlite/resource_pause.go` | Pause/checkpoint/lease release, bounded probes/backoff, абсолютный deadline |
| UI | `frontend/src/ChatRoute.tsx`, `send`, `changeMode`, `ConversationView`; `ResearchInspector`, `ChatTranscript`, `MarkdownMessage` | Существующие routes/SSE/history/inline Markdown/evidence/citations; новая IA не реализована |
| Acceptance | `internal/cli/lab.go`, `soakTracker.sample/finish`; `lab_benchmark.go`, `runLabCase`, `scoreLabCase`, `compareLabResults` | Реальные интервалы CLI и fixture metrics, но ограничения доказательной силы ниже |

## 5. Defaults, локальные flags и реально достижимые бюджеты

Source defaults: `internal/config/config.go`, `internal/config/research.go:15–40`,
`internal/domain/research_defaults.go:5–38`. Локальные override и service units
прочитаны без изменений.

| Настройка | Source default | Текущее локальное свидетельство |
| --- | --- | --- |
| Adaptive | false | `.env:3` true; сохранённые новые runs true |
| Section report | false | `.env:4` true; сохранённые новые runs true |
| Profile | standard | Меняется conversation settings; старый snapshot не hot-swapped |
| Privacy | local_inference_external_search | Все три real reports сохраняют именно этот режим |
| Search routing | local_primary | Не означает offline |
| DDG | true | App service явно задаёт `SEARCH_ENABLE_DDG=false` |
| Model concurrency | 1, другое запрещено | Installed model unit `--parallel 1` |
| App context | 131072 | App service override 32768; имеющиеся session snapshots 32768 |
| Output cap | 4096 | Три изученных real runs: 4096 |
| Unsandboxed PDF | false | Разрешение не включалось этим аудитом |
| Resource guard | enabled | Defaults: RAM 512 MiB, VRAM 256 MiB, GPU 85°C, app RSS 2 GiB, bounded probes |

`ResearchProfile` применяет `min(profile.MaxSteps, MAX_STEPS)` и
`min(profile.MaxSearchQueries, MAX_SEARCH_QUERIES)`; defaults обоих env caps — 12.

| Profile | Deadline, sec | Номинальные rounds | Queries nominal → default effective | Fetches | LLM calls | Evidence | Questions created/accepted |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| quick | 300 | 1 | 4 → 4 | 4 | 10 | 4 | 6 / 2 |
| standard | 1800 | 3 | 8 → 8 | 8 | 24 | 8 | 16 / 6 |
| deep_laptop | 7200 | 5 | 16 → 12 | 12 | 40 | 12 | 30 / 10 |
| long_laptop | 14400 | 8 | 32 → 12 | 24 | 72 | 24 | 60 / 20 |
| overnight_laptop | 21600 | 10 | 48 → 12 | 36 | 96 | 32 | 80 / 24 |

**12 steps — cumulative**, не per round. `Config.validateResearchLimits`,
`ValidateResearchPolicy`, domain plan validation, worker admission и storage
transaction удерживают этот потолок. `RequestResearchContinuation:77–113` проверяет
старую длину +4 и append-ит steps в главный plan. Для initial plan из S шагов
step-only upper bound: `1 + floor((12-S)/4)`, дополнительно ограниченный профилем.
S=4 даёт максимум три rounds; S=6 — два. 5/8/10 rounds сейчас недостижимы обычным
adaptive extension; большой wall-time — только deadline, не обещанный workload.

Остальные фактические ограничения:

- Fixed mode: максимум одно completion continuation вне quick; adaptive — до
  `MaxRounds-1`, но admission остаётся step/query/fetch/LLM/evidence bounded.
- Adaptive continuation требует headroom +2 search/+2 fetch/+2 evidence/+7 LLM.
- До 2 HTTP model attempts на logical-call budget unit с отдельными reservations.
- Citation sources: quick 4, прочие 8; claims 8; sections hard cap 4.
- Markdown report до 512 KiB; JSON/report storage до 1 MiB. Отдельных typed
  `MAX_REPORT_SECTIONS/PARAGRAPHS/BYTES` profile budgets нет; paragraph budget не
  выделен независимо от claims. Sections делят тот же восьми-claim allowance.
- Source notes: 32 stored, 8 compact; excerpts 1500/1000 runes. Selection по ID,
  не по максимальному покрытию пользовательских требований.
- Cumulative fetch 128 MiB, source 16 MiB, PDF 64 MiB; PDF 20 страниц/50000 runes,
  download/extract timeout 120/30 sec; PDF fallback не включается автоматически.

Отдельных initial/round/total plan budgets и durable bounded round plans сейчас
нет. Typed fetch/LLM/evidence/question/citation budgets уже существуют, но не все
являются самостоятельными env settings. Их нельзя описывать как полностью
отсутствующую reservation architecture.

## 6. Реальные сохранённые результаты: производство отчёта ≠ полезный ответ

Проверены SQL metadata/checkpoints/artifact metadata и фактический Markdown первого
run. Все три: profile `standard` (12 steps / 8 search / 24 LLM calls), `local-qwen`,
32768 context, 4096 output cap, temperature 0.2,
adaptive/report=true, external-search privacy. В каждой последней state: две
source notes, четыре `context.compacted` события. Это не saturation benchmark.
Сохранённые `inference.summary` имеют source `llama.cpp`, соответственно 12/15/23
записи; их наличие не равно независимой оценке качества или полному token accounting.

| Run ID | Wall sec | Calls / search / fetch / evidence | Rounds | Outcome | Report |
| --- | ---: | --- | ---: | --- | --- |
| `3933e38e-b51c-4695-9cfb-da8022ec4f7c` | 119.8 | 12 / 2 / 2 / 2 | 1 | completed | 2 supported sections; Markdown 2946 B; JSON 10456 B |
| `30f3fb12-5585-4f8b-b9b3-63783431f733` | 148.1 | 15 / 2 / 3 / 2 | 1 | partial | fallback + 3 supported sections; Markdown 4651 B; JSON 12469 B |
| `007d765e-b8f5-4fe3-a336-e9a69124d7c5` | 271.8 | 23 / 2 / 3 / 2 | 1 | partial | 4 fallback sections; Markdown 12653 B; JSON 30590 B |

Первый run просил знания, этапы и время обучения биоинформатике/химии/информатике.
Артефакт `data/artifacts/3933e38e-b51c-4695-9cfb-da8022ec4f7c/` +
`b7e5c5328cc239cd8be7719d1669422c15d955aeae816158396f382781f67c07`:
в строках 18 и 24 лишь два английских абзаца о сроке вузовского обучения и
факультете МГУ. Строка 45 объявляет ноль missing criteria и `completed`.
Event seq 136 сохраняет `criteria_covered`, seq 137 — `PASS`.

Это конкретный acceptance counterexample: sources/citation IDs/artifacts есть,
но requested roadmap отсутствует. Нельзя объявлять этот результат полезным
полным исследованием на основании внутренних labels. Independent factual accuracy
не измерена; её значение остаётся **null**, а не 0/1.

Read-only SQL также подтвердил 12 сохранённых research sessions, все с context
32768. Это snapshot evidence, не доказательство реального заполнения окна 32K.
В новых реальных отчётах многокруговая адаптация не продемонстрирована. Старый
bounded supplied-URL restart из `docs/chat-research-implementation.md` не доказывает
новую adaptive/report crash recovery.

## 7. Runtime/context: шесть разных величин

На 2026-10-26 18:08:47 +05 app service active, PID 35579; model service inactive,
PID 0. Независимые GET `/v1/models` и `/health` на 8000 завершились connection
refused. Причина остановки не устанавливалась, сервисы не перезапускались.
Go metadata running app содержит VCS revision `6ca6379…`, `vcs.modified=true`:
revision совпадает, но это не byte-exact reproducible build attestation.

| Слой | Что установлено |
| --- | --- |
| Training context | Старый аудит записывает `n_ctx_train=262144`; не новая проверка GGUF и не runtime guarantee |
| Server configured runtime | Installed unit: Qwen3.5-4B-Q4_K_M.gguf, `-c 32768`, Vulkan1, `-ngl 99`, q8_0 K/V, flash attention, reasoning off |
| Server reported runtime | Сейчас unavailable: сервер остановлен; нельзя выдать configured 32768 за свежий reported value |
| Application configured context | В service environment 32768; source default вне service — 131072 |
| Session snapshot context | Все исследованные sessions 32768; snapshots не fingerprint весов/tokenizer |
| Available input/output | При context 32768/output 4096: `32768-4096-2048=26624` estimated input tokens; код превращает это в 106496 bytes |

`effectiveModelContext` читает `meta.n_ctx`, не training context. Он ограничивает
configured value меньшим положительным reported value только на старте. На network,
status/JSON/metadata failure возвращает configured context. При успешном понижении
serve пишет warning; при невозможности discovery отдельного mismatch state нет.
Recovery сравнивается со startup-held `RuntimeModelContext`, не с новым discovery.

`requestInputBytes/requestFits` учитывают system/user/schema bytes и framing;
это не точная tokenizer admission. `assembleChatContext:189` делит уже вычисленный
byte allowance ещё на четыре: history selection и research budgeting используют
разные фактические объёмы. Report roles ограничены output cap до 4096, critic
legacy path — 2048/30sec; больший context сам не увеличит output/report budgets.

Context ladder **32K/64K/96K/128K NOT RUN**. Нет нового сравнения throughput,
VRAM/RAM peak, температуры, OOM/timeout, citation quality и compactions при
сопоставимой нагрузке. Ни 128K, ни оптимальность 32K этим baseline не доказаны.

## 8. Ранжированные backend/runtime/acceptance findings

Severity: P1 — блокирует цель или достоверность acceptance; P2 — материальный
defect/risk измерений; P3 — диагностическая неполнота. Новых P0 не установлено.
Предложения ниже — **только последующие фазы**, без реализации в Фазе 0.

| ID / severity | Подтверждённый факт и функции | Предложение | Доказывающий тест/эксперимент |
| --- | --- | --- | --- |
| B1 / P1 | `plan.go:184,190`, `verificationCatalog`, `reportExecutionCriterion`: acquisition criteria заменяют deliverable coverage; real run выше получает PASS без roadmap | Сохранять независимые user-deliverable criteria через normalization/skips/rounds/report; не использовать acquisition как полноту ответа | Два корректных admission-focused sources не дают полного roadmap PASS; сохраняются partial и явные missing dimensions; затем реальный rubric review |
| B2 / P1 | `RequestResearchContinuation:77–113`: append +4 в общий plan и cumulative 12; профили дополнительно clamp queries до 12 | Отдельные initial/round plans, frontier/history и достижимые явные total budgets, не просто `MAX_STEPS=48` | 1/2/3/5/10 rounds, каждый budget stop, deadline, restart, stale/duplicate continuation, useful partial без лишнего inference |
| R1 / P1 observed blocker | Model inactive, metadata/health refused; app active | В разрешённой runtime фазе диагностировать остановку и явно показывать model-unhealthy; не считать app active моделью ready | Исчезновение модели после здорового старта даёт durable pause/ограниченные probes, не пустой успешный ответ |
| R2 / P2 | `effectiveModelContext`, `researchExecutor`: unverified fallback и startup-only comparison; 4 bytes/token — оценка | Явная provenance/mismatch диагностика и проверяемая token/input admission; revalidation на recovery | Missing/malformed metadata, server shrink, multilingual/token-dense prompts; zero inference после admission reject |
| R3 / P2 | `assembleChatContext:189` вторично делит byte budget; snapshot не содержит GGUF/tokenizer/thinking identity | Согласовать units; сохранить материальные generation/model identities | Максимально fitting chat branch, same-alias replacement/thinking drift, chat/research/retry/continue/restart |
| B3 / P2 | `adaptiveAssessmentHash:18–21` не включает summary и supplementary-missing, но они сохраняются `CommitVerification:220–222` | Полная canonical assessment identity либо явный conflict на различающиеся persisted поля | Exact retry создаёт один event; изменение только summary/supplementary не silently ignored |
| B4 / P2 | `compactSourceNotes:93–103`, `citationCatalogue`: stable ID order + первые 8, не criterion relevance | Общий bounded evidence selector с приоритетом необходимых критериев | Девятый по ID единственный relevant source остаётся в пределах восьми выбранных |
| B5 / P3 | `completion.go:136` event.new_steps содержит лишь первые два из четырёх adaptive steps | Формировать payload из полного appended slice | Four adaptive / two fixed IDs совпадают с task_steps и порядком |
| A1 / P1 | `soakTracker.sample:165–173` считает любое изменение reserved counters прогрессом, включая уменьшение; допускает paused/queued | Различить successful durable work, reservations, in-flight и waiting; требовать монотонный полезный прогресс | Reservation-only/decreasing counters/failed attempts/long waits не проходят; затем реальные elapsed stages |
| A2 / P2 | `runLabCase:308–312`: отсутствующий report становится zero sections/zero unresolved и scoring_complete | Nullable report coverage, artifact identity/hash и collection status | Missing report/artifact/evidence не равен измеренному zero missing; completed collection отделена от run status |
| A3 / P2 | `scoreLabCase`, `compareLabResults`: lexical substring/owned-marker proxies; arbitrary subsets и отсутствующие numeric fields не доказывают full experiment | Versioned experiment schema, metric presence/ranges, fixed selected-case set, coverage/usefulness rubric | Keyword dump/negation/irrelevant owned source не получают substantive coverage; неполный declared 12-case experiment явно incomplete |
| A4 / P2 | Soak receipt/sample не сохраняет все predicate inputs; 4h stage не требует predecessor | Replayable manifest/samples и связь прошедших stages | Независимый replay receipt; пропуск/подмена samples и failed/missing predecessors отклоняются |

Источник RSS benchmark — app process, не полный llama-server footprint. Total
output tokens runner не собирает: null честнее нуля. Наличие `inference.summary`
в истории не даёт total без reconciliation по invocation ID/retries/missing data.

## 9. Frontend — Hallmark audit, Cobalt сохраняется

Pre-flight: `frontend/src/style.css:1–48` — Cobalt, semantic OKLCH tokens,
Space Grotesk display + Inter body + JetBrains Mono, существующая spacing scale.
`main.tsx` импортирует локальные fonts. Motion restrained; reduced-motion CSS есть.
Существующие routes, ownership/state, SSE/replay, mobile dialog и tokens должны
сохраняться. Новая theme/design system не выбиралась; visual edits отсутствуют.

| Severity | Файл/функция и факт | Исправление следующей UI фазы | Доказательство |
| --- | --- | --- | --- |
| P1 | `ChatRoute.changeMode:280–286`, effect:210 и drawer:498: research автоматически открывает details; при ≤70rem это modal | Разделить mode selection и explicit Details; компактный Overview/Report в центре | Research select/send/reload не закрывает composer/transcript на 320/375/414/768/1024 |
| P1 | `chat.css:140,155,156`: triggers 36px, copy 28px вместо target минимум 44px | Увеличить interactive areas без декоративного раздувания | Computed bounding boxes ≥44×44, включая drawer close и source actions |
| P1 | `ChatRoute.send:249` pending; `ChatTranscript:28` сразу пишет «Не отправлено» | Различить sending и failed; сохранить idempotent retry | Delayed response показывает sending, reject — failure, accept убирает optimistic duplicate |
| P2 | `ResearchInspector:79–104` technical preamble до основной стадии/действий | Overview → Plan/Sources/Evidence/Report → collapsed Diagnostics | Первое окно с multi-round fixture сразу показывает outcome/current phase/actions |
| P2 | `TelemetryPanel` freshness преимущественно title/sr-only; lifecycle success не имеет ясного task announcement | Touch-visible freshness и один polite lifecycle announcer | Stale/estimated понятны без hover; screen-reader transitions, без озвучивания каждого токена |
| P2 risk | Long nowrap context CTA, 480px min-height shell, ограниченная width coverage | Проверить element bounds, short heights, text zoom/software keyboard | Все 7 ширин, reachable actions и отсутствие clipping, не только root overflow |
| P3 | Inspector shortcuts:106–108 делают scrollIntoView без focus transfer | Keyboard section navigation с destination focus | Enter/Space → destination focus → ожидаемый Tab order |

Inline Markdown уже существует: backend возвращает assembled report в answer,
`ChatTranscript/MarkdownMessage` отображают content в центре. Неверно утверждать,
что report доступен **только** download-ом. Но отдельные section navigation,
summary Overview и полноценный browser/live report-completion→reload сценарий
ещё не доказаны. Технические disclosure/provider/activity уже collapsed.

Существующие browser assertions покрывают 320/375/414/768/1440 и ряд
replay/partial/blocked/retry/continue/drawer сценариев. Нет отдельного 1024 layout
case; production runner 1280 проверяет другие свойства, не полный responsive
acceptance. Нет свежих screenshots, measured contrast, complete bidirectional
focus trap/restoration, zoom/software keyboard или live research report smoke.
Root overflow check с `overflow-x:clip` не доказывает отсутствие обрезанного child.

## 10. Устаревшая документация и неисполненные проверки

- `docs/research-lab-phase0-gap-audit.md` описывает HEAD `15deaf5`: отсутствие
  adaptive/notes/reports/privacy и rejection adaptive=true теперь исторические,
  не текущие утверждения.
- `docs/harness-phase4-completion.md` описывает максимум одно continuation и
  старую модель review/exhaustion; adaptive reviews/partial delivery теперь другие.
- `docs/research-phase1.md` и `docs/chat-research-implementation.md` содержат старые
  scope exclusions; утверждения о неиспользуемых model overrides не current inventory.
- `docs/research-lab-phase7-overnight.md` / phase9 status / README содержат
  pre-deployment statements. Сейчас deployed app имеет revision 6ca6379 и есть
  новые реальные section reports; старые записи не переписывались этим аудитом.
- Формулировку старого soak report «counters/completed work» нельзя принимать
  как доказательство successful work: часть counters — reservations.

**Не запускались в этой фазе:** новый real-model research, реальная offline
baseline/adaptive серия 12+12 задач, context ladder, staged 15m→60m→4h soak,
новый real adaptive/report restart, live browser/screen reader/screenshots,
rootless isolated PDF extraction, lid/logout/firmware/long thermal tests.
Отдельных retained ladder/soak receipts аудит не установил. Historical bounded
Chat/restart/notification/PDF-fallback доказательства не расширяются на эти claims.

## 11. Gate следующей фазы

Фаза 0 завершена отчётом и baseline, **без исправлений**. Следующая разрешаемая
работа — Phase 1: round-budget redesign, достижимые explicit limits, invariant
tests и устранение ложного completion по execution-only criteria. Повышать один
`MAX_STEPS` недостаточно. Safety/deadline/reservations, ledger, checkpoints и
существующие routes должны сохраняться.

Целевая формулировка после доказанного исправления:

> No artificial plan-step ceiling.
> Still bounded by explicit time, search, fetch, LLM, evidence and resource budgets.

**Остановка. Ожидается `Proceed to Phase 1`.**
