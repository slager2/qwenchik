# Qwenchik: мастер-план реализации после hard review

Дата фиксации: 2026-10-30

Этот документ — не задание на повторный аудит. Подтверждённые проблемы уже перечислены ниже. Следующий исполнитель должен сначала проверить незакоммиченный WIP, затем последовательно реализовывать этапы, измеряя эффект каждого изменения.

## Готовый промпт для следующего захода

Работай в проекте /home/slager/Рабочий стол/ai.

Цель: довести Qwenchik до устойчивой локальной работы на текущем ноутбуке, уменьшить дисковую и RAM-нагрузку, сократить число LLM-вызовов и убрать деградацию интерфейса на длинных сессиях. Не начинай новый обзор с нуля: используй подтверждённые данные и этапы из этого документа.

Правила работы:

1. Сначала прочитай этот план, текущий git diff, README, Makefile и локальные инструкции проекта. Рабочее дерево грязное: не удаляй и не перезаписывай чужие изменения.
2. До новой реализации проверь уже существующий незакоммиченный WIP из раздела «Текущее состояние WIP». Не считай его завершённым без тестов и замеров.
3. Выполняй только один этап за раз. Для каждого этапа:
   - зафиксируй исходный показатель;
   - реализуй минимальное изменение;
   - запусти целевые тесты и регрессионную проверку;
   - повтори тот же замер;
   - запиши before/after, риски и оставшиеся ограничения.
4. Не запускай параллельно тяжёлые Go-тесты, сборку frontend и модельный benchmark. Если CPU выше 85 °C или датчик RAM уже в высокой зоне, отложи тяжёлую нагрузку. При 95 °C немедленно останови нагрузочный тест.
5. Не изменяй реальный systemd runtime, не переписывай production-базу, не запускай VACUUM или массовый backfill без отдельного подтверждения пользователя и проверяемой резервной копии.
6. Для спорных решений используй Exa, но выводы основывай прежде всего на официальной документации, исходном коде проекта или научной первичной публикации.
7. Не делай commit, push или destructive cleanup без прямой просьбы пользователя.
8. В конце каждого этапа дай короткий отчёт: что изменено, какие тесты прошли, численные результаты, известные риски и что делать следующим.

## Подтверждённый baseline

- SQLite DB: около 418.5 MB.
- Таблица checkpoints: около 402 MB, то есть примерно 96% базы.
- Всего checkpoints: 4102.
- Самый тяжёлый run: 308 checkpoints и около 158.8 MB.
- Исторические checkpoints практически не сжаты; около 312.6 MB plain payload размером от 64 KiB. На выборке gzip давал примерно 3.86x.
- LatestCheckpoint исторически перечитывал и проверял всю цепочку перед каждым role/LLM-вызовом. При полном snapshot это создаёт повторное чтение гигабайт и O(N²)-подобный рост работы.
- 24 из 25 paused tasks остановились по high_model_rss. Старый предел 3 GiB ниже наблюдавшегося RSS модели, максимум был примерно 4.44 GB.
- В одном реальном run: 43 LLM-вызова, 243078 prompt tokens, 11327 output tokens.
- Report phase сделал 23 вызова; все 8 секций закончили fallback.
- Actor phase сделал 16 вызовов, хотя значимая часть действий была механической.
- Кэш llama.cpp был фактически выключен: cache-ram 0, ctx-checkpoints 0, no-cache-idle-slots. Доля cached tokens была около 2.9%.
- При одновременных тяжёлых проверках температура CPU доходила примерно до 99.5 °C, датчик RAM — примерно до 62.8 °C.
- Frontend Workspace допускал продвижение cursor до проверки delta, повторно парсил растущий Markdown и заранее загружал evidence для всей истории.

## Текущее состояние WIP — проверить, а не считать готовым

До уточнения задачи уже были внесены незакоммиченные изменения:

- хвостовой cache для checkpoint history и перевод checkpoint writers на единый save path;
- корректировка invalidation trigger для checkpoint generation;
- ограничение распаковки checkpoint до 16 MiB;
- повышение model RSS default с 3 GiB до 5 GiB;
- CPU temperature guard с recovery hysteresis;
- runtime flags cache-ram 512 и ctx-checkpoints 4;
- проверка Workspace SSE delta до продвижения cursor;
- React memo для MessageContent;
- соответствующие целевые тесты и обновления конфигурации/README.

Известные прогоны до остановки работ:

- целевые frontend tests прошли;
- TypeScript noEmit прошёл;
- internal/storage/sqlite прошёл после перевода writers;
- config/resources/cli проходили целевые тесты;
- go vet проходил до самых последних изменений, поэтому его нужно повторить после охлаждения ноутбука;
- полный race/full repository suite после всех изменений не выполнялся.

## Этап 1. Стабилизировать и доказать корректность WIP

Проверить:

- все checkpoint writers обновляют cache только после успешной транзакции;
- rollback не оставляет cache новее базы;
- прямые INSERT, UPDATE и DELETE checkpoint корректно инвалидируют generation;
- изменение несвязанного event не сбрасывает cache;
- возвращаемый cached state нельзя мутировать через общий slice/map;
- LatestCheckpoint после первого чтения не сканирует всю историю;
- migration 013 совместима как с новой, так и с существующей базой;
- gzip bomb останавливается лимитом распаковки;
- CPU guard корректен на Linux и безопасно деградирует без доступного датчика;
- значения 5 GiB синхронизированы между config, policy, example env и документацией;
- cache flags поддерживаются установленной версией llama.cpp;
- SSE gap, duplicate и out-of-order delta приводят к resync, а не к повреждению draft.

Минимальная проверка:

    CGO_ENABLED=1 go test -tags sqlite_fts5 ./internal/storage/sqlite ./internal/runtime/resources ./internal/config ./internal/cli -count=1
    CGO_ENABLED=1 go test -race -tags sqlite_fts5 ./internal/storage/sqlite ./internal/runtime/resources -count=1
    go vet -tags sqlite_fts5 ./...
    npm test
    npx tsc --noEmit --incremental false

Добавить benchmark для 100, 500 и 1000 checkpoints. Критерий: повторные LatestCheckpoint после прогрева не перечитывают полный payload; рост времени близок к постоянному для cache hit, а построение цепочки не демонстрирует квадратичный рост.

## Этап 2. Безопасная офлайн-компактизация существующей базы

Сделать отдельную admin-команду, а не автоматическую миграцию при старте:

1. dry-run: число строк, ожидаемая экономия, свободное место, оценка времени;
2. создать резервную копию через SQLite backup API;
3. переписывать старые plain checkpoints в gz1 батчами с возобновляемым progress marker;
4. после каждого батча проверять decode, chain hash и generation;
5. выполнить integrity_check;
6. для физического уменьшения файла использовать VACUUM INTO в новый файл;
7. заменять исходную базу атомарно только после полной проверки и явного подтверждения.

Критерии приёмки:

- повторный запуск идемпотентен;
- прерывание питания не повреждает исходную базу;
- hash chain и восстановленный state совпадают до и после;
- ожидаемое уменьшение checkpoint storage — не менее 2x, целевой ориентир выборки около 3–4x;
- команда не запускается автоматически.

## Этап 3. Устранить O(N²) модель хранения

Перейти от полного snapshot на каждом шаге к схеме:

- content-addressed blobs для крупных повторяющихся данных;
- периодический полный anchor checkpoint;
- между anchors — компактные deterministic deltas;
- versioned decoder и обратная совместимость чтения старых записей;
- проверка hash chain и fail-closed при повреждении delta;
- политика compaction только как отдельная обслуживающая операция.

До миграции сравнить минимум два варианта: anchor плюс JSON/CBOR delta и immutable content-addressed state nodes. Выбрать по простоте восстановления, стоимости записи и возможности локального ремонта. Не вводить сложную event-sourcing платформу, если компактный versioned delta решает задачу.

Критерии: рост диска и write time на один новый шаг не зависит линейно от размера всей накопленной истории; восстановление последнего state остаётся ограниченным числом anchors/deltas.

## Этап 4. Сократить LLM-вызовы и prompt churn

### Детерминированные инструменты

- fetch_url, повторный fetch, извлечение метаданных и простые repair/retry выполнять обычным кодом;
- search action делать детерминированным, если planner уже выдал структурированные facet/query;
- small model оставлять для решений, где действительно нужна семантика;
- каждую замену LLM-вызова покрыть fixture-тестом.

### Report circuit breaker

- после 1–2 однотипных неудач секции прекращать повторять тот же prompt;
- делать extractive fallback из уже проверенных evidence;
- сократить отчёт с 8 до 3–4 смысловых секций;
- проверку citation entailment выполнять батчами;
- не считать self-critique независимой проверкой качества.

### Prompt cache A/B

Сравнить одинаковый workload минимум в двух режимах:

- cache disabled;
- cache-ram 512 MiB и ctx-checkpoints 4.

Замерять wall time, prompt eval time, cached tokens, RAM/RSS, GPU memory, температуру и качество результата. Не повышать cache до 8 GiB на этом ноутбуке.

Критерий этапа: минимум на 25% меньше LLM-вызовов на типичном run без ухудшения покрытия evidence/citations; report fallback перестаёт генерировать десятки одинаковых вызовов.

## Этап 5. SQLite, SSE и артефакты

- добавить метрики: checkpoint bytes, encode/decode time, cache hit/miss, chain rebuild count, transaction wait, WAL size и checkpoint latency;
- коалесцировать частые message delta writes в ограниченные батчи;
- заменить SQLite polling раз в 500 ms на in-process broadcast для активных клиентов, сохранив DB replay как fallback после reconnect;
- ввести backpressure и ограниченный буфер на SSE client;
- artifact download отдавать streaming reader, а не загружать до 64 MiB целиком в heap;
- добавить request body timeout и проверить SQLite file permissions.

Критерии: число DB reads не растёт как clients × 2 в секунду; медленный SSE client не раздувает память; artifact download имеет ограниченный memory footprint.

## Этап 6. Frontend на длинной истории

- один общий reducer/state machine для stream событий Conversation и Workspace;
- cursor изменяется только после полной валидации delta;
- memoize message rows и рендерить Markdown только для реально изменившегося сообщения;
- объединять delta UI updates через requestAnimationFrame;
- evidence загружать лениво при раскрытии/видимости, с LRU и ограничением concurrency;
- останавливать polling в hidden tab;
- добавить Workspace fallback, если SSE degraded;
- сделать единый exhaustive terminal_outcome mapper;
- отложить тяжёлые панели и редактор через code splitting;
- исправить focus trap/restore в modal и сделать destructive delete явным и отменяемым.

Критерии: append нового delta не вызывает повторный Markdown parse всей истории; первая загрузка не создаёт N+1 evidence requests; разрыв SSE восстанавливается без потери или дублирования текста.

## Этап 7. Защита ноутбука

- сохранить CPU temperature guard с hysteresis и cooldown;
- добавить учёт AC/battery и предупреждение при работе модели от батареи;
- различать observed warnings и enforced limits в UI/логах;
- кэшировать/ограничивать частоту nvidia-smi;
- сделать model RSS threshold baseline-aware: измерять стабильный idle/loaded RSS конкретной модели и добавлять безопасный запас;
- добавить предупреждение по sustained CPU utilization/power, если достоверный датчик доступен;
- не обещать автоматический RAM temperature guard там, где датчик не доступен приложению переносимо.

Критерии: guard не вызывает pause loop около порога, причина остановки понятна пользователю, после cooldown задача может безопасно продолжиться.

## Что не делать

- не увеличивать context до 64K/128K как способ исправить архитектуру;
- не повышать model concurrency выше 1 на текущем ноутбуке без отдельного thermal benchmark;
- не ставить cache-ram 8 GiB;
- не делать больше двух тяжёлых fetch одновременно;
- не отключать hash/integrity checks ради скорости;
- не запускать автоматический VACUUM или backfill при старте приложения;
- не считать self-critic независимым судьёй качества;
- не запускать несколько тяжёлых тестовых наборов параллельно.

## Приоритет выполнения

1. Этап 1: корректность и доказательство WIP.
2. Этап 4: убрать лишние LLM-вызовы и report loop.
3. Этап 2: безопасно уменьшить существующую DB.
4. Этап 3: исправить модель хранения на будущее.
5. Этап 5: SQLite/SSE/artifact memory.
6. Этап 6: frontend long-session performance.
7. Этап 7: расширить защиту ноутбука.

## Первичные источники для проверки решений

- llama.cpp server options: https://github.com/ggml-org/llama.cpp/blob/HEAD/tools/server/README.md
- llama.cpp benchmark: https://github.com/ggml-org/llama.cpp/blob/master/tools/llama-bench/README.md
- SQLite WAL: https://sqlite.org/wal.html
- SQLite transactions: https://sqlite.org/lang_transaction.html
- ALCE, citation quality: https://arxiv.org/abs/2305.14627
- MemGPT, tiered context/memory: https://arxiv.org/abs/2310.08560

## Implementation log: первый проход

Реализовано и проверено в текущем заходе:

- checkpoint cache теперь публикуется только после успешного commit; transactional writers используют Store-aware путь; добавлены rollback/immutability tests и benchmarks для 100/500/1000 записей;
- gzip checkpoint decode ограничен, migration/generation invalidation сохранены;
- frontend защищает partial draft при SSE gap, сохраняет identity неизменившихся сообщений, ограничивает historical evidence LRU/concurrency и не polling-ит hidden tab;
- model RSS/host probes получили bounded cache и privacy-safe counters; runtime cache flags централизованы на cache-ram 512 и ctx-checkpoints 4;
- artifact HTTP download получил bounded-memory streaming path с проверкой hash/size до отправки ответа;
- HTTP server получил 30-секундный ReadTimeout для request body.

Проверки первого прохода:

- frontend npm test: 21/21 passed;
- frontend TypeScript noEmit: passed;
- targeted и extended Go tests с sqlite_fts5: passed;
- go vet по затронутым пакетам: passed;
- storage/runtime race tests: passed на targeted пакетах.

Не выполнено намеренно:

- полный repository race suite и production/model benchmark;
- smoke-проверка установленной версии llama-server через --help;
- offline DB compaction/VACUUM и content-addressed migration;
- измерение реального thermal behavior после изменений.

Текущий thermal stop: во время расширенного прогона CPU достиг около 91 °C, RAM sensor оставался в зоне high alarm. Тяжёлые проверки продолжать только после охлаждения.

## Implementation log: продолжение 2026-10-31

Реализовано в текущей волне:

- research prompt cache включён по умолчанию; явный per-request opt-out сохранён;
- post-commit checkpoint tail публикуется только после успешной транзакции и проверяет generation/latest id;
- deterministic search/fetch actor bypass добавлен только для канонизированных входов;
- report standard/quick ограничен четырьмя секциями, добавлен report-wide circuit breaker после повторяющихся content failures;
- `/props` и `/models` получили короткий TTL-cache с сериализацией cache miss; строгий fingerprint pin выполняется на первом research admission и для явно pinned/direct клиентов, а последующие research вызовы используют bounded TTL; `/apply-template` и `/tokenize` намеренно остались live;
- SSE event polling переведён с фиксированных 500 ms на bounded backoff 500 ms → 1 s → 2 s с reset при активности;
- `nvidia-smi` telemetry sample получил короткий bounded cache и hit/miss counters;
- generated llama runtime unit теперь ограничивает CPU-side work (`--threads 4`, `--threads-batch 4`) и отключает busy polling (`--poll 0`, `--poll-batch 0`), сохраняя single-slot и cache limits.

Проверки этой волны:

- `go test ./internal/llm/openai -count=1` — passed;
- targeted orchestrator tests — passed при `GOMAXPROCS=1`, `-p=1`;
- CLI runtime, API SSE/artifact, runtime/resources и SQLite checkpoint tests — passed; SQLite/API с `-tags sqlite_fts5`;
- `go vet -p=1 -tags sqlite_fts5` по затронутым backend-пакетам запускался последовательно;
- остановленный orchestrator compile при обычном режиме достигал 95.6 °C, поэтому дальнейшие тяжёлые suite намеренно не выполнялись параллельно.

Не выполнено намеренно:

- установленный systemd runtime не перезапускался и не переписывался автоматически;
- полный repository race suite, frontend browser capture, model benchmark, DB compaction/VACUUM и content-addressed migration требуют отдельного холодного окна и/или явного подтверждения;
- push выполняется только после финального git/release review.

## Формат финальной сдачи каждого этапа

    Этап: <номер и название>
    Изменено: <файлы и краткая суть>
    Baseline: <цифры до>
    Result: <цифры после>
    Tests: <точные команды и результат>
    Thermal: <максимальные температуры и длительность нагрузки>
    Risks: <что ещё не доказано>
    Next: <один следующий этап>
