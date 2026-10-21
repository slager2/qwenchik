# Autonomous research harness: Phase 0 audit

Дата: 21 октября 2026. Baseline checkout: `a53bd43` (`feat: add durable chat and bounded research runtime`). Этот коммит фиксирует ранее накопленную реализацию, а не выполнение всех фаз нового harness brief.

## 1. Граница фазы

Прочитан канонический `QWENCHIK_HARNESS_AUTONOMOUS_RESEARCH_PROMPT.md`. Выполнены три параллельных read-only аудита: durable recovery, inference/evidence и context/UI/SSE. Основной агент выполнил baseline и runtime/hardware checks.

Production-код, конфигурация, схема и пользовательские задачи в этой фазе не изменялись. Единственное изменение после baseline-коммита — этот отчёт. Новые фазы требуют явного `Proceed to Phase N`. Старая реализация из `research-phase1.md` относится к предыдущему roadmap: это snapshots/budgets, а не завершённая canonical Phase 1 с resource guard.

## 2. Фактическая архитектура

```text
React/Vite Chat + legacy workspace
  -> loopback Go API / committed-event SSE
  -> SQLite/WAL: tasks-as-runs, conversations, messages,
     events, checkpoints, approvals, artifacts, evidence,
     memories/FTS, research_sessions
  -> one synchronous durable worker
  -> shared capacity-one GuardedClient -> local llama.cpp
  -> typed tools/policy -> search -> validated fetch -> extraction -> evidence
```

Существующая `tasks` хранит попытку/run. Повторы связаны conversation/user-message ссылками; отдельной конкурирующей `runs` таблицы нет. Реально применены migrations `001_initial.sql`–`009_research_sessions.sql`; migration registry использует `name/applied_at`, не числовое поле `version`. Live `PRAGMA journal_mode` вернул `wal`. Из таблиц с префиксом `research_` существует только `research_sessions`.

Не найдены реализованные AgentBundle/hash/promotion, canonical invocation ledger, ContextSnapshot, completion governor, notification outbox, refinement/CandidatePatch или resource/sleep guard. Имена в новом brief не являются существующими capabilities.

## 3. Baseline-проверки

Все команды завершились успешно:

```sh
CGO_ENABLED=1 go test -tags sqlite_fts5 ./...
go vet -tags sqlite_fts5 ./...
CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./...
npm --prefix frontend test
npm --prefix frontend run typecheck
npm --prefix frontend run build -- --outDir /tmp/opencode/qwenchik-harness-phase0-dist
npm --prefix frontend run test:browser
go build -tags sqlite_fts5 -o /tmp/opencode/qwenchik-harness-phase0-qagent ./cmd/qagent
git diff --check
```

Go tests/race использовали допустимые cache hits для неизменённого checkout; это не новый длительный fault-injection/soak run. Frontend suite: **34/34**. Chrome smoke использовал mocked API/SSE, включая replay, cancellation, independent panels и widths 320/375/414/768/1440. Он не доказывает реальный overnight research. Frontend и executable собраны во внешнюю `/tmp/opencode`, deployed assets/binary не заменялись.

Существуют локальные fixtures для DNS/source fallback, PDF oversize/decompression/cleanup, budgets, immutable snapshots и recovery. Отсутствующие canonical acceptance cases перечислены ниже; зелёный baseline не означает их прохождение.

## 4. Измеренные runtime capabilities

Значения — отдельные моментальные измерения, не гарантии будущей доступности или температуры.

| Проверка | Наблюдение |
|---|---|
| App/model user units | `active/running`, `NRestarts=0` в момент проверки; это не lifetime OOM/restart history |
| App `/api/health` | `status=ok`, database `ok`, attached/idle worker, fresh heartbeat |
| llama.cpp `/health` | HTTP success, `status=ok` |
| `/v1/models` | alias `local-qwen`, 4,205,751,296 parameters, `Q4_K - Medium`, runtime context **32768**, training context 262144 |
| Physical RAM | 15,949,737,984 bytes; available 9,171,025,920 bytes при `free -b` |
| GPU | NVIDIA RTX 3050 6GB Laptop: total 6144 MiB, used 3352 MiB, free 2455 MiB, temperature 52 C, utilization 0% по `nvidia-smi` |
| Process RSS | `qagent` 20,404 KiB; `llama-server` 835,488 KiB по отдельному `ps` snapshot |
| Unit memory | App 632,897,536 bytes, model 3,053,146,112 bytes по systemd; эти значения не равны process RSS или Go heap |
| Power | UPower `OnBattery=false`, то есть mains по этому provider |
| Inhibitor | `/usr/bin/systemd-inhibit` доступен; `--list` успешно перечислил 6 inhibitors, без Qwenchik |
| User manager | `Linger=no`: нет подтверждения работы после последнего logout или до login |
| Docker Desktop | `inactive/dead`; Docker socket отсутствует, `docker info` не смог подключиться |
| PDF isolation | Doctor: `isolated_available=false`, `fallback_allowed=false` |

Факт наличия `systemd-inhibit` и доступного listing не доказывает, что приложение уже берёт lock или что acquisition разрешён в любом окружении. Новый inhibitor не приобретался. Desktop lid-switch inhibitor не является гарантией от firmware/desktop sleep policy.

Go heap, inference TTFT/duration/token telemetry, исторические kernel OOM и application-owned thermal/resource monitoring — **unknown / не реализовано**, а не нулевые значения. Короткий health/probe не доказывает устойчивость под многочасовым prefill.

## 5. Doctor и provenance сборки

`./qagent doctor --json` вернул **warning**. App/DB/frontend/worker/local structured smoke — `ok`; PDF isolation, sandbox и search readiness — warnings. Проверка не выполняла новый billable Tavily/DDG probe; локальный SearXNG сейчас не подтвердил readiness при остановленном Docker.

Running policy: task 128 MiB, ordinary source 16 MiB, PDF 64 MiB; 20 страниц, 50,000 runes, download 120 s/extraction 30 s, isolated memory 512 MiB/temp 64 MiB, CPU 1/PID 32, unsandboxed fallback false, fetch-temp quotas 128/256 MiB. Это policy values, не доказательство доступного PDF runtime. Новый real-PDF extraction в Phase 0 не выполнялся.

`go version -m ./qagent` показывает deployed binary stamp `ccfb791...`, `vcs.modified=true`, Go 1.26.5. Binary был собран из dirty tree до baseline-коммита; его embedded VCS stamp **не доказывает соответствие HEAD `a53bd43`**. Отдельная сборка текущего checkout в `/tmp/opencode` прошла, но не была развёрнута. Этот provenance gap надо закрыть при следующем согласованном deployment.

Structured smoke просит тот же JSON, который допускает schema. Успех означает cooperative structured response с локальной проверкой. Native grammar enforcement против schema-ignoring provider остаётся **unknown**; adapter может явно downgrade к JSON mode при неподдерживаемом schema format.

## 6. Recovery: контракт → проблема → минимальное продолжение

| Существующий контракт | Подтверждённая проблема | Инвариант следующего patch | Минимальная canonical Phase 1 работа | Failure behavior / test |
|---|---|---|---|---|
| Transactional queued→planning claim плюс process-wide `flock` | Нет SQLite lease owner/expiry/fencing; heartbeat не lease | Только актуальный owner может commit; paused task не владеет lease | Расширить existing tasks ownership metadata, сохранить `flock` как дополнительную защиту | Expiry/renew/reclaim/stale-writer fixtures; не создавать новый runs authority |
| Append-only checkpoints, atomic checkpoint event+row | Нет parent/state hash/general schema/pending invocation input hash | Recovery проверяет цепочку и identity; старый checkpoint переживает неудачную запись | Расширить existing checkpoint envelope с legacy compatibility | Corrupt hash/version/parent блокирует replay, не удаляя данные |
| Durable logical/HTTP/search/fetch reservations | Нет canonical invocation outcome; tool result, step status и checkpoint фиксируются раздельно | Intent до I/O; outcome/event/usage/checkpoint после I/O атомарны | Stable invocation identity в existing events/checkpoints и узкий repository commit helper | Crash injections на каждой границе; unknown non-idempotent outcome не повторяется |
| Reservation guard проверяет cancel, terminal и deadline | `paused` не отклоняется самим storage guard | После pause нельзя резервировать новую работу | Дополнить storage lifecycle guard; сохранить counters | Paused/queued/waiting-approval reservation tests и cancel/pause races |
| Model timeout имеет typed cause | Worker обычно переводит его в failed, не resource pause | Временная ресурсная проблема сохраняет checkpoint/partial и останавливает новые calls | Fakeable telemetry/health provider, atomic resource pause, durable bounded probe schedule | Low RAM/VRAM/temp/health timeout, restart paused, finite escalation, no busy loop |
| Hard caps и review errors имеют коды | Task enum не содержит canonical blocked/budget_exhausted/needs_review/partial | Завершение workflow не равно доказанному complete | Выбрать совместимый terminal outcome/stop-reason contract; statuses менять только согласованно со schema/API | Exhausted budget и unknown effect не становятся completed |

Ключевые исходники: `worker.go:233–274,328–469`, `store.go:1482–1615`, `research_sessions.go:325–465,517–621`, `domain/task.go:23–67`, `workerlock/lock_unix.go:18–43`. Это подтверждённые code findings, не новые воспроизведения resource/OOM crash.

## 7. Что уже нужно сохранить

- Один shared `GuardedClient` worker/doctor: `cli/root.go`, `llm/guard.go`. Он instance-local, не глобальный endpoint lock для сторонних процессов.
- Immutable policy/model/deadline и monotonic counters: `research_sessions.go`, `research_run.go`. Logical calls и reserved model HTTP attempts не равны измеренному сетевому traffic; неизвестные попытки не возвращаются в budget.
- Source DNS failure отделён от invalid arguments и SSRF: `ssrf.go`, `registry.go`, `source_failures.go`; bounded next-source и required-URL/cancel restrictions покрыты fixtures.
- Scheme/credential/domain/DNS/dial/redirect controls, decoded-byte cap, file-backed PDF/hash/0600/quota/cleanup и fail-closed extraction.
- Fetched/extracted task-owned evidence, source artifacts и exact excerpt checks. Не превращать lead/snippet или verifier opinion в evidence.
- Unknown non-idempotent effect и ambiguous fetch не получают blind retry. Chat остаётся tool-free.

## 8. Evidence, provider и verifier gaps

SearXNG/Tavily сейчас запускаются как concurrent composite, когда настроены; это не canonical SearXNG-primary/Tavily-on-difficulty policy. Canonical URL dedup выбирает одну winning hit provenance, хотя provider outcomes сохраняются отдельно. Provider request counts не равны одному logical search counter.

У неудачной extraction есть source failure metadata, но полный raw artifact записывается только после успеха. Нельзя заявлять полный durable downloaded-snapshot ledger. Quote spans сейчас relative-to-excerpt; точная позиция excerpt в полном extracted text не хранится как такой locator.

Сильный citation path проверяет ownership, HTTP success, quality flags, excerpt membership и supporting quotes; semantic support решает тот же локальный critic. Fallback явно виден, но может закончиться task status `completed`. Это не canonical `partial` terminal outcome и не formal truth guarantee. Исправления этих границ относятся к согласованным следующим фазам evidence/governor, а не к Phase 0.

## 9. Context, UI и SSE gaps

Есть per-request compaction, сохраняющая evidence/URL/hash references и minimum text skeleton, accepted scoped FTS memory и task-scoped content-derived artifacts. Это не canonical durable ContextSnapshot. Research goal включает только последние четыре user updates; Chat может вытеснять старые turns; memories передаются короткими prefixes. Полное сохранение старых constraints не гарантировано.

Conversation finalizer получает отобранный evidence catalogue; citation critic получает claims/excerpts без user goal/done criteria. Support verification не равна task-compliance verification.

SSE имеет after/Last-Event-ID, replay, heartbeat, dedup и delta-chunk resync. Но snapshot/replay/live запускаются без общей transactional watermark; high live cursor может опередить replay. Public sequence gaps допустимы из-за отфильтрованных internal events: нельзя считать каждый такой gap потерей. Research panels независимы и сохраняют usable data при ошибке одной панели.

Notification outbox, ContextSnapshot hash, AgentBundle hash, progress fingerprint, productive continuation, runtime/sleep dashboard и held-out refinement отсутствуют. Не добавлять их заранее и не выдавать current prompt-version metadata за bundle resolver.

## 10. Миграции, ограничения и следующий шаг

В Phase 0 новых migrations/tables нет. Предыдущее ограничение минимальной persistence сохраняется: использовать tasks/events/checkpoints/research_sessions, не создавать конкурирующую runs или перечень speculative tables. Возможные owner/hash/outcome поля требуют отдельной migration fixture и rollback note в authorized Phase 1.

Внешние проекты в brief — reference, не зависимости. Их code/install scripts, mutable downloads, benchmarks и hosted/free-tier claims в этой фазе не использовались как production guarantee и отдельно не верифицировались. Cloud generation, teacher/REPL и внешние judges не включались.

Blockers для canonical overnight readiness: resource pause/backoff, owner/fencing и atomic pending/outcome recovery, sleep policy, truthful terminal outcomes, затем evidence/context/governor/UI согласно порядку фаз. Docker/PDF availability — отдельное ограничение окружения; тест download handling не доказывает parsing.

Изменён только этот audit report. Phase 0 завершена. **Ожидать явное `Proceed to Phase 1`**; production patches и более поздние фазы не запускать автоматически.
