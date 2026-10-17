import type { ReactElement } from 'react'
import type { ProviderStatus, Evidence } from './types'

export function ProviderCard(props: { name: string; provider: ProviderStatus; checkedLabel: string }): ReactElement
export function TaskFailureCard(props: { payload?: Record<string, unknown>; evidenceCount: number }): ReactElement
export function SourceList(props: { sources: Evidence[]; formatTime: (time: string) => string }): ReactElement
