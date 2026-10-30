export function headingID(prefix: string, offset: number): string
export function reportOutline(content: string, prefix: string): { id: string; text: string; depth: number }[]
export function focusReportHeading(container: HTMLElement | null, id: string): void
