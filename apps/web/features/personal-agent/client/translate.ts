/** A translator over one of the panel's dictionaries (SA's DSH locale binding; tests/fixtures/panel.tsx's translator). */
import type { T } from './text'

export const translator = (dictionary: Record<string, string>): T => ((key: string, params?: Record<string, unknown>) => {
  const template = dictionary[key] ?? key
  return params ? template.replace(/\{(\w+)\}/g, (match, name: string) => name in params ? String(params[name]) : match) : template
}) as T
