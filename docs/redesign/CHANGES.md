# Qwenchik UI redesign changelog

## Реализованные фазы

- **Phase 0 — baseline:** read-only capture и документирование текущих экранов в `phase-0/before/`.
- **Phase 1 — foundation:** IBM Plex Sans/Mono, локальные tokens/base/primitives, rem-типографика и безопасная focus-система.
- **Phase 2 — shell:** единая навигация Chat / Projects / Tasks / Memory, collapsible sidebar, mobile drawer и command palette.
- **Phase 3 — chat:** writing-first composer, переключатель Chat/Исследование, privacy/context notices, copy action и пустое состояние.
- **Phase 4 — research:** пятишаговый progress, понятные labels для источников/подтверждений, сохранённые technical disclosures и scoped inspector.
- **Phase 5 — tasks:** приоритет approval-карточек, «Нужно ваше решение», адаптивная доска и пользовательские статусы вместо служебного жаргона.
- **Phase 6 — projects:** Projects встроен в общий shell; чаты, проверенные материалы и исследования остаются в одном durable context.
- **Phase 7 — polish:** сохраняемые тема оформления и размер текста, mobile overflow guard, 44px touch targets, единый command palette trigger и regression QA.

## Контракты

Сохранены `api.ts`, `types.ts`, state-модули `*.mjs`, ChatRoute hooks/SSE, `data-message-id`, `tabIndex=-1`, `ContextNotice role=alert`, видимые labels browser-тестов и существующие backend semantics. Изменения этого цикла — presentation-only.

## Проверки

Проверки запускаются последовательно из-за thermal budget ноутбука:

```text
npm --prefix frontend run typecheck
npm --prefix frontend test
npm --prefix frontend run test:browser
npm --prefix frontend run build
```

На последнем QA: typecheck — pass, unit — 121/121, mocked Chrome browser — pass по 320/375/414/768/1024/1280/1440px.

## Известные границы

Memory и Settings остаются route-level интеграциями legacy backend; новый shell не добавляет сетевые запросы и не меняет их состояние. Production service использует обновлённый `internal/api/web/dist` после build; запуск Go-сервиса и systemd restart выполняются отдельно от frontend QA.
