export default function PrivacyNotice({ mode }: { mode?: string }) {
  const text = mode === 'offline' ? 'Offline: поиск и чтение только из локального корпуса; сетевые источники отключены.'
    : mode === 'local_searxng' ? 'Локальная модель · локальный SearXNG. Его публичные движки и открываемые сайты получают сетевые запросы.'
      : mode === 'local_inference_external_search' ? 'Локальная модель · внешний поиск. Поисковые запросы и обращения к сайтам выходят в сеть.' : 'Privacy mode: данные конфигурации пока недоступны.'
  return <p className="chat-muted" role="note">{text}</p>
}
