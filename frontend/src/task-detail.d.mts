export function createTaskDetailTracker(): {
  select(id: string): void
  observe(id: string, status: string | undefined): boolean
  beginRequest(id: string, resource?: string): () => boolean
}
