import type { Run, RunRuntime } from './types'

const outcomes: Record<string, string> = { completed: 'Завершено', partial: 'Частичный результат', blocked: 'Заблокировано проверкой', budget_exhausted: 'Бюджет исчерпан', failed: 'Ошибка запуска', cancelled: 'Отменено' }
const reasons: Record<string, string> = { low_ram: 'Недостаточно свободной RAM', low_vram: 'Недостаточно свободной VRAM', high_rss: 'Превышен лимит памяти процесса', gpu_temperature: 'Высокая температура GPU', llama_unhealthy: 'Локальная модель недоступна', model_oom: 'Модели не хватило памяти', model_timeout: 'Превышено время ожидания модели', llama_timeout: 'Превышено время ожидания модели', recovery_required: 'Нужно проверить неизвестный исход вызова', manual: 'Пауза пользователя' }
const verdicts: Record<string, string> = { PASS: 'Evidence достаточно для подготовки ответа', PARTIAL: 'Критические критерии выполнены; дополнительные не подтверждены', RESEARCH: 'Нужно дополнительное исследование', REPAIR: 'Нужно исправление', UNCERTAIN: 'Есть неразрешённые противоречия', BLOCK: 'Проверка блокирует завершение' }
const duration = (value?: number) => Number.isFinite(value) ? `${Math.floor(value! / 60)} мин ${Math.floor(value! % 60)} с` : 'неизвестно'
const bytes = (value?: number | null) => typeof value === 'number' && Number.isFinite(value) ? `${Math.round(value / 1048576)} MiB` : 'неизвестно'

export default function RunStatus({ run, data, busy, onPause, onResume }: { run: Run | null; data?: RunRuntime | null; busy?: boolean; onPause?: () => void; onResume?: () => void }) {
  if (!run) return null
  const current = data?.task_id === run.id ? data : null
  const resources = current?.runtime?.resources
  const sleep = current?.runtime?.sleep
  const keepAwakeRequested = current?.keep_awake_requested ?? current?.runtime?.keep_awake
  const warnings = current?.runtime_warnings ?? resources?.warnings ?? []
  const reason = current?.resource_reason || current?.pause_reason || run.resource_reason || run.pause_reason || ''
  const outcome = current?.terminal_outcome || run.terminal_outcome || ''
  const paused = run.status === 'paused'
  return <section className="chat-run-status" aria-label="Состояние запуска">
    <h3>Состояние запуска</h3>
    {outcomes[outcome] && <p role="status"><strong>{outcomes[outcome]}</strong></p>}
    {paused && <p role="status">{reasons[reason] || 'Запуск на паузе'}. Состояние и остаток бюджета сохранены.</p>}
    {current?.resume_after && paused && <p>Следующая проверка ресурса: <time dateTime={current.resume_after}>{new Date(current.resume_after).toLocaleTimeString('ru-RU')}</time> · попыток: {current.resource_probe_attempts ?? 0}</p>}
    <div className="chat-run-actions">
      {['planning', 'running', 'verifying'].includes(run.status) && onPause && <button type="button" disabled={busy} onClick={onPause}>Пауза</button>}
      {paused && onResume && <button type="button" disabled={busy || current?.pause_reason === 'resource_guard' || current?.pause_reason === 'recovery_required'} onClick={onResume}>Возобновить</button>}
    </div>
    {paused && current?.pause_reason === 'resource_guard' && <p>Возобновление разрешит backend после успешной проверки ресурсов. Можно остановить запуск.</p>}
    {current && <>
	  {current.runtime?.enabled === false && <p className="chat-partial-note" role="status">Resource guard отключён в конфигурации runtime.</p>}
      <dl className="chat-runtime-grid">
        <dt>Профиль</dt><dd>{current.profile || 'Chat'}</dd>
        <dt>Текущий шаг</dt><dd>{current.step || 'Между шагами'} · выполнено {current.completed_steps ?? 0}</dd>
        <dt>Прошло / осталось</dt><dd>{duration(current.elapsed_seconds)} / {duration(current.remaining_seconds)}</dd>
        <dt>Вызовы модели</dt><dd>{current.counters?.llm_calls ?? '—'} / {current.budget?.max_llm_calls ?? '—'}</dd>
        <dt>Поиски / загрузки</dt><dd>{current.counters?.search_queries ?? '—'} / {current.budget?.max_search_queries ?? '—'} · {current.counters?.fetches ?? '—'} / {current.budget?.max_fetches ?? '—'}</dd>
        <dt>Остаток загрузок</dt><dd>{bytes(current.budget_remaining?.fetched_bytes)}</dd>
        <dt>Продолжения / compaction</dt><dd>{current.completion?.continuations ?? 0} / {current.max_continuations ?? 0} · {current.compactions ?? 0} сжатий</dd>
        <dt>Свободная RAM / RSS</dt><dd>{bytes(resources?.free_ram_bytes)} / {bytes(resources?.rss_bytes)}</dd>
        <dt>VRAM / температура GPU</dt><dd>{resources?.gpu_free_vram_mb == null ? 'неизвестно' : `${resources.gpu_free_vram_mb} MiB`} / {resources?.gpu_temp_c == null ? 'неизвестно' : `${resources.gpu_temp_c} °C`}</dd>
        <dt>Удержание от сна</dt><dd>{sleep?.active ? 'Активно' : keepAwakeRequested == null ? 'Неизвестно' : keepAwakeRequested ? 'Запрошено, не активно' : 'Не запрошено'} · {sleep?.provider || 'провайдер неизвестен'}</dd>
        <dt>Питание</dt><dd>{resources?.power_source === 'AC' ? 'От сети' : resources?.power_source === 'battery' ? 'От батареи' : 'неизвестно'}</dd>
      </dl>
      {keepAwakeRequested && !sleep?.active && <p className="chat-partial-note" role="status">Блокировка сна не подтверждена{sleep?.available === false || sleep?.failure_reason ? ': провайдер недоступен' : ''}. Закрытие крышки регулируется системой.</p>}
      {warnings.map((warning) => <p className="chat-muted" key={warning}>{warning}</p>)}
      {current.verdict && <p>Проверка готовности: {verdicts[current.verdict] || 'Статус неизвестен'}</p>}
      {!!current.contradictions?.length && <div className="chat-partial-note" role="status"><h4>Неразрешённые противоречия</h4><ul>{current.contradictions.map((item, index) => <li key={index}>{item}</li>)}</ul></div>}
      {current.context_snapshot_hash && <details><summary>Durable context · версия {current.context_snapshot_number}</summary><code>{current.context_snapshot_hash}</code></details>}
	  {current.bundle_hash && <details><summary>AgentBundle · версия {current.bundle_version}</summary><code>{current.bundle_hash}</code></details>}
      {current.notification && <p>Уведомление: {({ pending: 'в очереди', attempted: 'доставка начата', failed: 'повтор доставки запланирован', delivered: 'принято провайдером', abandoned: 'доставка недоступна' } as Record<string, string>)[current.notification.status] || 'неизвестно'}{current.notification.channel ? ` · ${current.notification.channel}` : ''}</p>}
    </>}
  </section>
}
