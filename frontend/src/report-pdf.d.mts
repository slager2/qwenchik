export function reportPDFURL(runID: string): string
export function reportPDFEligible(
  message: { role: string; run_id: string; content: string; status: string; run_mode?: string } | null | undefined,
  run: { id: string; mode: string; status: string } | null | undefined,
): boolean
export function reportPDFAvailability(health: { report_pdf?: { available: boolean } } | null | undefined): { disabled: boolean; reason: string }
export function filenameFromDisposition(header: string | null | undefined, fallback?: string): string
export function obsidianExportMessage(result: { path?: string; notes?: number } | null | undefined): string
export function reportObsidianURL(runID: string): string
