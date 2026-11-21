/** The panel's part of the Source plugin contract (SA src/sources/kind.ts): the server's sources.FieldSpec as JSON. */
export interface Localized { zh: string; en: string }
interface FieldBase { key: string; label: Localized; hint?: Localized; advanced?: true }
export type FieldSpec =
  | FieldBase & { type: 'switch' }
  | FieldBase & { type: 'select'; options: { value: string; label: Localized }[] }
  | FieldBase & { type: 'number'; min: number; max: number }
  | FieldBase & { type: 'text'; pattern?: string; readonly?: boolean; choose?: Localized }
  | FieldBase & { type: 'fact'; settings?: 'deployment' }
  | FieldBase & { type: 'rows'; columns: ({ key: string; label: Localized; type: 'text'; pattern?: string; optional?: boolean
    choose?: Localized; refresh?: Localized } | { key: string; label: Localized; type: 'select'; options: { value: string
    label: Localized }[] })[] }
export interface AdmissionRowView { id: string; label: Localized | string; note: Localized; cells: { listedUnaddressed: boolean
  unlistedAddressed: boolean; unlistedUnaddressed: boolean } }
export interface ChannelLink { url: string; label: Localized; triggerId?: string }
