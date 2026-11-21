/** Shared settings context, block draft hook, form controls, Source schema field renderer, and configuration history. */
import { cloneElement, createContext, useContext, useEffect, useId, useLayoutEffect, useRef, useState, type ReactElement,
  type ReactNode } from 'react'
import type { AgentOption, RuleTarget } from '../app/api'
import type { FieldSpec, Localized } from '../sources/kind'
import { usePoll, type SettingsView } from './api'
import { Button, Icon, InfoTip, Input, SegmentedControl, Switch } from './controls'
import { useDraft } from './draft'
import { useAction, usePanel, usePanelData } from './provider'
import { settingsRoute } from './route'
import { errorText, localized } from './text'
import { LoadState } from './widgets'

/** Parse a numeric input string, returning `null` when empty so the server rejects blank required numbers instead of coercing to 0. */
export const numberOf = (text: string): number | null => text.trim() ? Number(text) : null
interface SettingsData { settings: SettingsView; reload: () => Promise<void> }
const SettingsContext = createContext<SettingsData | undefined>(undefined)
export function useSettings(): SettingsData {
  const value = useContext(SettingsContext)
  if (!value) throw new Error('A settings block rendered outside the settings')
  return value
}
export function SettingsProvider({ children }: { children: ReactNode }) {
  const { call } = usePanel()
  const { restarts } = usePanelData()
  const { data, error, reload } = usePoll(() => call('GET settings'), [call, restarts])
  if (!data) return <LoadState error={error} loading />
  return <SettingsContext.Provider value={{ settings: data, reload }}>
    {error && <LoadState error={error} loading={false} />}
    {children}
  </SettingsContext.Provider>
}
export function useBlockDraft<V>(block: string, saved: V, revision: number, toValue: (draft: V) => unknown = draft => draft) {
  const { t, call, notify } = usePanel()
  const { reload } = useSettings()
  return useDraft({ key: `multica-personal-agent/draft/${block}`, saved, revision, reload, save: async (value, from) => {
    const result = await call('PUT settings/:block', { block, body: { value: toValue(value), revision: from } })
    notify(t(result.restarting ? 'savedRestarting' : 'saved'))
    return result
  } })
}

/** Fieldset wrapper that restores focus to the previously active input after a save completes and re-enables the form. */
export function Fields({ disabled, children }: { disabled: boolean; children: ReactNode }) {
  const set = useRef<HTMLFieldSetElement>(null)
  const focused = useRef<HTMLElement | null>(null)
  useLayoutEffect(() => {
    const active = document.activeElement
    if (disabled) focused.current = active instanceof HTMLElement && set.current?.contains(active) ? active : null
    else if (focused.current && (!active
      || active === document.body)) { focused.current.focus({ preventScroll: true }); focused.current = null }
  }, [disabled])
  return <fieldset ref={set} className="dt-fieldset" disabled={disabled}>{children}</fieldset>
}
export function Field({ label, hint, note,
  children }: { label: string; hint?: string | undefined; note?: ReactNode; children: ReactElement<{ id?: string }> }) {
  const id = useId()
  return <div className="dt-field">
    <span className="dt-field-head"><label htmlFor={id}>{label}</label>{hint && <InfoTip text={hint} />}</span>{cloneElement(children,
      { id })}{note && <small>{note}</small>}
  </div>
}
function SwitchRow({ label, hint, checked,
  onChange }: { label: string; hint?: string | undefined; checked: boolean; onChange: (next: boolean) => void }) {
  return <div className="dt-switch-row">
    <div className="dt-field-head">{label}{hint && <InfoTip text={hint} />}</div><Switch checked={checked} onChange={onChange}
      label={label} />
  </div>
}
/** Picks the Multica agent that plays a role; its runtime decides where the role runs. `Field` gives it its `id`. */
/** The workspace's agents for an AgentField, undefined until listed. */
export function useAgents(): { agents: AgentOption[] | undefined; error: string | undefined } {
  const { call } = usePanel()
  const [agents, setAgents] = useState<AgentOption[]>()
  const [error, setError] = useState<string>()
  useEffect(() => { call('GET agents').then(setAgents, failure => setError(errorText(failure))) }, [call])
  return { agents, error }
}
export function AgentField({ id, value, onChange, list, unset }: { id?: string; value: string; onChange: (next: string) => void
  list: { agents: AgentOption[] | undefined; error: string | undefined }; unset?: string }) {
  const { t } = usePanel()
  const { agents, error } = list
  // A saved agent the list does not hold (yet) keeps its own option, so it never reads as unset.
  return <select id={id} className="dt-select" value={value} onChange={event => onChange(event.target.value)}>
    <option value="">{unset ?? t('agentUnset')}</option>
    {error && <option value="" disabled>{t('agentListError', { error })}</option>}
    {value && !agents?.some(agent => agent.id === value) && <option value={value}>{value}</option>}
    {agents?.filter(agent => agent.type !== 'squad').map(agent => <option key={agent.id} value={agent.id}>{agent.runtime
      ? `${agent.name} · ${agent.runtime}` : agent.name}</option>)}
  </select>
}
/** Links the Router agent's page: "add my instructions" on the owner's own template copy, "open agent" otherwise. */
export function RouterLink({ agent, className = 'dt-router-link' }: { agent: AgentOption | undefined; className?: string }) {
  const { t } = usePanel()
  if (!agent?.url) return null
  return <a className={`${className} dt-btn dt-btn-outline dt-btn-sm`} href={agent.url} target="_blank" rel="noopener noreferrer">
    {t(agent.personal ? 'routerAppend' : 'routerOpen')}</a>
}
/** Picks a rule's executor: an agent or a squad, grouped. */
export function TargetField({ id, value, onChange, list, unset }: { id?: string; value: RuleTarget | undefined
  onChange: (next: RuleTarget | undefined) => void; list: { agents: AgentOption[] | undefined; error: string | undefined }; unset?: string }) {
  const { t } = usePanel()
  const { agents, error } = list
  const key = value ? `${value.type}:${value.id}` : ''
  const of = (type: 'agent' | 'squad') => agents?.filter(agent => (agent.type ?? 'agent') === type) ?? []
  return <select id={id} className="dt-select" value={key} onChange={event => {
    const [type, ...rest] = event.target.value.split(':')
    onChange(type ? { type: type as RuleTarget['type'], id: rest.join(':') } : undefined)
  }}>
    <option value="">{unset ?? t('agentUnset')}</option>
    {error && <option value="" disabled>{t('agentListError', { error })}</option>}
    {value && !agents?.some(agent => (agent.type ?? 'agent') === value.type && agent.id === value.id)
      && <option value={key}>{value.id}</option>}
    {(['agent', 'squad'] as const).map(type => of(type).length > 0 && <optgroup key={type} label={t(`targetGroup.${type}`)}>
      {of(type).map(agent => <option key={agent.id} value={`${type}:${agent.id}`}>{agent.runtime
        ? `${agent.name} · ${agent.runtime}` : agent.name}</option>)}
    </optgroup>)}
  </select>
}

type Row = Record<string, string>
type Column = Extract<FieldSpec, { type: 'rows' }>['columns'][number]
const ROWS_SHOWN = 6
const byName = ([, a]: [string, string], [, b]: [string, string]) => a.localeCompare(b)
function RowsField({ field, value, saved, onChange, labels, refreshLabels, self }: {
  field: Extract<FieldSpec, { type: 'rows' }>; value: unknown; saved: unknown; onChange: (next: unknown) => void; labels: Record<string,
    string>
  refreshLabels: (() => Promise<string[]>) | undefined
  /** The owner's id: their row reads 你自己 and cannot be edited or removed. */
  self?: string
}) {
  const { t, notify } = usePanel()
  const [open, setOpen] = useState(false)
  const refresh = useAction(async () => {
    try {
      const added = await refreshLabels!()
      notify(added.length ? t('labelsAdded', { names: added.join(t('listSeparator')) }) : t('labelsCurrent'))
    } catch (failure) { notify(t('labelsFailed', { error: errorText(failure) })) }
  })
  const label = localized(t, field.label), hint = localized(t, field.hint)
  const rows = Array.isArray(value) ? value as Row[] : []
  const set = (index: number, row: Row) => onChange(rows.map((item, position) => position === index ? row : item))
  const key = field.columns[0]!
  const savedKeys = new Set((Array.isArray(saved) ? saved as Row[] : []).map(row => row[key.key]))
  const choose = key.type === 'text' && key.choose && Object.keys(labels).length ? localized(t, key.choose) : ''
  // Exclude options already selected in another row.
  const choices = (id: string) => Object.entries(labels).filter(([option]) => option === id
    || !rows.some(other => other[key.key] === option)).sort(byName)
  const blank = Object.fromEntries(field.columns.map(column => [column.key, column.type === 'select' ? column.options[0]!.value : '']))
  // Summarize a row by its second column only when that column is free text (e.g., a display name), not a select option.
  const named = field.columns[1]?.type === 'text' ? field.columns[1] : undefined
  const rowName = (row: Row) => (named && row[named.key]) || labels[row[key.key] ?? ''] || row[key.key] || ''
  const cell = (row: Row, index: number, column: Column) => {
    const id = row[key.key] ?? '', change = (next: string) => set(index, { ...row, [column.key]: next })
    if (column === key && (savedKeys.has(id) || (!choose && labels[id]))) {
      return <span key={column.key} className={labels[id] ? 'dt-row-label' : 'dt-row-label dt-mono'} title={id}>{labels[id] ?? id}</span>
    }
    if (column === key && choose) {
      return <select key={column.key} className="dt-select" aria-label={choose} required value={id}
        onChange={event => change(event.target.value)}>
        <option value="" disabled>{choose}</option>
        {choices(id).map(([option, name]) => <option key={option} value={option}>{name}</option>)}
      </select>
    }
    if (column.type === 'select') {
      return <select key={column.key} className="dt-select" aria-label={localized(t, column.label)} value={row[column.key] ?? ''}
        onChange={event => change(event.target.value)}>
        {column.options.map(option => <option key={option.value} value={option.value}>{localized(t, option.label)}</option>)}
      </select>
    }
    return <Input key={column.key} className={column.optional ? undefined : 'dt-mono'} aria-label={localized(t, column.label)}
      placeholder={localized(t, column.label)}
      required={!column.optional} pattern={column.pattern} spellCheck={false} value={row[column.key] ?? ''}
        onChange={event => change(event.target.value)} />
  }
  return <div className="dt-field">
    <div className="dt-subhead">
      <span className="dt-field-head">{label}{hint && <InfoTip text={hint} />}<span className="dt-count">{rows.length || ''}</span></span>
      <span className="dt-subhead-actions">
        {key.type === 'text' && key.refresh && refreshLabels && <Button variant="ghost" size="sm" className={refresh.busy ? 'dt-btn-busy'
          : undefined} icon={<Icon name="refresh" />}
          disabled={refresh.busy} aria-busy={refresh.busy} onClick={() => { void refresh.run() }}>{localized(t, key.refresh)}</Button>}
        <Button variant="ghost" size="sm" icon={<Icon name="plus" />} onClick={() => { setOpen(true); onChange([...rows,
          blank]) }}>{t('addRow')}</Button>
      </span>
    </div>
    {rows.length > ROWS_SHOWN && <button type="button" className="dt-textlink dt-rows-summary" aria-expanded={open}
      onClick={() => setOpen(!open)}>
      {open ? t('collapse') : rows.map(rowName).join(t('listSeparator'))}
    </button>}
    {(rows.length <= ROWS_SHOWN || open) && <ul className="dt-rows"
      style={{ ['--dt-columns' as string]: field.columns.length }}>{rows.map((row, index) => self && row[key.key] === self
      ? <li key={index} className="dt-rows-item"><span className="dt-row-label" title={self}>{t('yourself')}</span></li>
      : <li key={index} className="dt-rows-item">
      {field.columns.map(column => cell(row, index, column))}
      <Button variant="ghost" size="sm" icon={<Icon name="trash" />} aria-label={t('removeRow', { name: labels[row[key.key] ?? '']
        || row[key.key] || label })}
        onClick={() => onChange(rows.filter((_, position) => position !== index))} />
    </li>)}</ul>}
  </div>
}
export function SourceField({ field, value, saved, fact, onChange, labels, refreshLabels, self }: {
  field: FieldSpec; value: unknown; saved: unknown; fact: Localized | string | undefined; onChange: (next: unknown) => void
  labels: Record<string, string>
  refreshLabels?: () => Promise<string[]>
  self?: string
}) {
  const { t, navigate } = usePanel()
  const label = localized(t, field.label), hint = localized(t, field.hint)
  switch (field.type) {
    case 'switch': return <SwitchRow label={label} hint={hint} checked={value === true} onChange={onChange} />
    case 'select': return <Field label={label} hint={hint}>
      <SegmentedControl className="dt-segmented dt-field-segmented" label={label} value={String(value ?? field.options[0]!.value)}
        options={field.options.map(option => ({ value: option.value, label: localized(t, option.label) }))} onChange={onChange} />
    </Field>
    case 'number': return <Field label={label} hint={hint}>
      <Input type="number" min={field.min} max={field.max} required value={String(value ?? '')}
        onChange={event => onChange(event.target.value === '' ? '' : Number(event.target.value))} />
    </Field>
    case 'text': return <Field label={label} hint={hint} note={localized(t, fact)}>
      {field.choose && Object.keys(labels).length
        ? <select className="dt-select" value={String(value ?? '')} onChange={event => onChange(event.target.value)}>
          <option value="">{localized(t, field.choose)}</option>
          {value && !labels[String(value)] ? <option value={String(value)}>{String(value)}</option> : null}
          {Object.entries(labels).sort(byName).map(([option, name]) => <option key={option} value={option}>{name}</option>)}
        </select>
        : <Input value={String(value ?? '')} readOnly={field.readonly} pattern={field.pattern} spellCheck={false}
          onChange={event => onChange(event.target.value)} />}
    </Field>
    case 'fact': return <div className="dt-field">
      <span className="dt-field-head">{label}{hint && <InfoTip text={hint} />}</span>
      <span className="dt-mono">{localized(t, fact) || t('empty')}</span>
    </div>
    case 'rows': return <RowsField field={field} value={value} saved={saved} onChange={onChange} labels={labels}
      refreshLabels={refreshLabels} self={self} />
  }
}