const empty = () => ({ loading: false, error: '', data: undefined })

export function createRunEvidenceCache(loadEvidence, options = {}) {
  const maxEntries = Number.isSafeInteger(options.maxEntries) && options.maxEntries > 0 ? options.maxEntries : 24
  const maxConcurrent = Number.isSafeInteger(options.maxConcurrent) && options.maxConcurrent > 0 ? options.maxConcurrent : 2
  const entries = new Map()
  const listeners = new Map()
  const queue = []
  let active = 0
  const notify = (runID) => { for (const listener of listeners.get(runID) ?? []) listener() }
  const touch = (runID, entry) => { entries.delete(runID); entries.set(runID, entry) }
  const evict = () => {
    if (entries.size <= maxEntries) return
    for (const [runID, entry] of entries) {
      // Never evict an actively observed or in-flight entry. It may still be
      // evicted on a later insertion after its request settles.
      if (entry.pending || (listeners.get(runID)?.size ?? 0) > 0) continue
      entries.delete(runID)
      if (entries.size <= maxEntries) return
    }
  }
  const pump = () => {
    while (active < maxConcurrent && queue.length) {
      const job = queue.shift()
      if (!job) return
      active++
      Promise.resolve().then(() => loadEvidence(job.runID)).then((result) => {
        const data = Array.isArray(result) ? result.filter((item) => item && item.task_id === job.runID) : []
        Object.assign(job.entry, { loading: false, error: '', data, pending: undefined })
        return job.entry
      }).catch(() => {
        Object.assign(job.entry, { loading: false, error: 'Ссылки этого ответа временно недоступны.', pending: undefined })
        return job.entry
      }).then((result) => {
        active--
        touch(job.runID, job.entry)
        evict()
        notify(job.runID)
        job.resolve(result)
        pump()
      })
    }
  }
  return {
    get(runID) {
      const entry = entries.get(runID)
      if (!entry) return empty()
      touch(runID, entry)
      return entry
    },
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
      touch(runID, entry)
      notify(runID)
      entry.pending = new Promise((resolve) => queue.push({ runID, entry, resolve }))
      evict()
      pump()
      return entry.pending
    },
  }
}
