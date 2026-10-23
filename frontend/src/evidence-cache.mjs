const empty = () => ({ loading: false, error: '', data: undefined })

export function createRunEvidenceCache(loadEvidence) {
  const entries = new Map()
  const listeners = new Map()
  const notify = (runID) => { for (const listener of listeners.get(runID) ?? []) listener() }
  return {
    get(runID) { return entries.get(runID) ?? empty() },
    subscribe(runID, listener) {
      if (!listeners.has(runID)) listeners.set(runID, new Set())
      listeners.get(runID).add(listener)
      return () => { const group = listeners.get(runID); group?.delete(listener); if (!group?.size) listeners.delete(runID) }
    },
    load(runID, retry = false) {
      if (!runID) return Promise.resolve(empty())
      const previous = entries.get(runID)
      if (previous?.pending) return previous.pending
      if (previous && !retry) return Promise.resolve(previous)
      const entry = { loading: true, error: '', data: previous?.data, pending: undefined }
      entries.set(runID, entry)
      notify(runID)
      entry.pending = Promise.resolve().then(() => loadEvidence(runID)).then((result) => {
        const data = Array.isArray(result) ? result.filter((item) => item && item.task_id === runID) : []
        Object.assign(entry, { loading: false, error: '', data, pending: undefined })
        return entry
      }).catch(() => {
        Object.assign(entry, { loading: false, error: 'Ссылки этого ответа временно недоступны.', pending: undefined })
        return entry
      }).then((result) => { notify(runID); return result })
      return entry.pending
    },
  }
}
