export function createTaskDetailTracker() {
  let selectedID = ''
  let observed = false
  let observedStatus
  let request = 0
  let resourceRequests = new Map()

  function select(id) {
    if (id === selectedID) return
    selectedID = id
    observed = false
    request += 1
    resourceRequests = new Map()
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
    beginRequest(id, resource = '*') {
      if (id !== selectedID) return () => false
      if (resource !== '*') {
        const current = (resourceRequests.get(resource) ?? 0) + 1
        resourceRequests.set(resource, current)
        return () => id === selectedID && resourceRequests.get(resource) === current
      }
      const current = ++request
      return () => id === selectedID && current === request
    },
  }
}
