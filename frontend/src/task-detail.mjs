export function createTaskDetailTracker() {
  let selectedID = ''
  let observed = false
  let observedStatus
  let request = 0

  function select(id) {
    if (id === selectedID) return
    selectedID = id
    observed = false
    request += 1
  }

  return {
    select,
    observe(id, status) {
      select(id)
      if (!id) return false
      const refresh = !observed || (['completed', 'failed', 'cancelled'].includes(status) && status !== observedStatus)
      observed = true
      observedStatus = status
      return refresh
    },
    beginRequest(id) {
      if (id !== selectedID) return () => false
      const current = ++request
      return () => id === selectedID && current === request
    },
  }
}
