/**
 * Per-scope admission matrix classifying incoming messages as explicit requests, inferred candidates, or ignored.
 * A listed sender who addresses the agent always makes an explicit request (no table cell); an unaddressed message cannot be explicit.
 */
import type { AdmissionCell } from './core/model'

export type { AdmissionCell }
export type Admission = 'explicit' | 'inferred' | 'ignore'
export const ADMISSION_OPTIONS = { listedUnaddressed: ['inferred', 'ignore'], unlistedAddressed: ['explicit', 'inferred', 'ignore'],
  unlistedUnaddressed: ['inferred', 'ignore'] } as const satisfies Record<AdmissionCell, readonly Admission[]>
export type AdmissionRow = { [C in AdmissionCell]?: typeof ADMISSION_OPTIONS[C][number] }
/** A personal agent's default admission: anyone who @s the owner asks explicitly; unaddressed messages are inferred candidates. */
export const ADMISSION_DEFAULTS: Readonly<Record<AdmissionCell, Admission>> = { listedUnaddressed: 'inferred', unlistedAddressed: 'explicit', unlistedUnaddressed: 'inferred' }
