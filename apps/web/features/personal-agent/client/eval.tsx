/** Eval status view (Decision log) and Eval settings group (Policy rules, per-Source admission, and assessor settings). */
import { useState } from 'react'
import { ADMISSION_DEFAULTS, ADMISSION_OPTIONS, type Admission, type AdmissionCell } from '../admission'
import type { AdmissionRowView } from '../sources/kind'
import { LIMITS } from '../limits'
import { DECISION_FILTERS, DECISION_KINDS, usePoll, type DecisionFilter, type Settings, type SourceView } from './api'
import { Button, DisclosureRow, Icon, InfoTip, Input, Pill, SegmentedControl, Switch } from './controls'
import { SaveBar } from './draft'
import { useAction, usePanel } from './provider'
import { DecisionLine } from './rows'
import { Field, Fields, SourceField, TargetField, numberOf, useAgents, useBlockDraft, useSettings } from './settings-kit'
import { localized, valueLabel, when } from './text'
import { Hint, LabelValue, LoadMore, LoadState, Section, StatusHead } from './widgets'

export function EvalStatus({ filter, window, paused }: { filter: DecisionFilter; window: '24h' | undefined; paused: boolean }) {
  const { t, call, navigate } = usePanel()
  const [limit, setLimit] = useState(30)
  const { data, error } = usePoll(() => call('GET decisions', { query: { limit, kind: DECISION_KINDS[filter],
    ...window ? { window } : {} } }), [filter, limit, window])
  const show = (key: DecisionFilter, last24h: boolean) => navigate({ page: 'eval', query: { ...key === 'work' ? {} : { filter: key },
    ...last24h ? { window: '24h' } : {} } }, true)
  return <>
    <StatusHead hint={t('evalStatusIntro')} settings="eval" />
    {paused && <p className="dt-banner">
      {t('evalRecordsPaused')} <button type="button" className="dt-textlink"
        onClick={() => navigate({ page: 'execution' })}>{t('resumeWhere')}</button>
    </p>}
    <div className="dt-filters">
      {DECISION_FILTERS.map(key => <Pill key={key} active={filter === key} onClick={() => { setLimit(30); show(key, !!window) }}>
        {t(`decisionFilter.${key}`)}
      </Pill>)}
      {window && <><span className="dt-filter-gap" /><Pill active onClick={() => show(filter, false)}>{t('lastDayOnly')}</Pill></>}
    </div>
    <LoadState error={error} loading={!data} />
    {data && (data.items.length ? <ul className="dt-list">{data.items.map(decision => <DecisionLine key={decision.id}
      decision={decision} />)}</ul> : <Hint>{t('none')}</Hint>)}
    {data?.next_offset != null && <LoadMore onMore={() => setLimit(limit + 30)} />}
  </>
}

type Rule = Settings['policy']['rules'][number]
function RuleRow({ rule, expanded, onToggle, onChange, onDuplicate, onRemove, agents }: {
  rule: Rule; expanded: boolean; onToggle: () => void; onChange: (rule: Rule) => void; onDuplicate: () => void; onRemove: () => void
  agents: ReturnType<typeof useAgents>
}) {
  const { t } = usePanel()
  const name = rule.name.trim() || t('newRule')
  return <li className="dt-rule">
    <DisclosureRow icon={<span className="dt-rule-dot" data-enabled={rule.enabled
      || undefined} />} title={<>{name}{rule.origin && <span className="dt-rule-origin" data-origin={rule.origin}>
        {t(`ruleOrigin.${rule.origin}`)}</span>}{rule.target_origin && <span className="dt-rule-origin" data-target-origin={rule.target_origin}>
        {t(`targetOrigin.${rule.target_origin}`)}</span>}</>} open={expanded} onToggle={onToggle}
      collapsedContent={<span className="dt-rule-preview">{rule.instruction}</span>}>
      <div className="dt-rule-body">
        <Field label={t('ruleName')}><Input value={rule.name} required maxLength={LIMITS.ruleName} onChange={event => onChange({ ...rule,
          name: event.target.value })} /></Field>
        <Field label={t('ruleInstruction')}>
          <textarea className="dt-textarea" rows={4} required maxLength={LIMITS.ruleInstruction} value={rule.instruction}
            placeholder={t('ruleInstructionPlaceholder')}
            onChange={event => onChange({ ...rule, instruction: event.target.value })} />
        </Field>
        <Field label={t('ruleAgent')} hint={t('ruleAgentHint')}>
          <TargetField value={rule.target} list={agents} unset={t('ruleAgentDefault')} onChange={target => {
            const { target: _, ...rest } = rule
            onChange(target ? { ...rest, target } : rest)
          }} />
        </Field>
        <div className="dt-actions">
          <Button variant="ghost" size="sm" icon={<Icon name="copy" />} onClick={onDuplicate}>{t('duplicate')}</Button>
          <Button variant="ghost" size="sm" icon={<Icon name="trash" />} onClick={onRemove}>{t('remove')}</Button>
        </div>
      </div>
    </DisclosureRow>
    <Switch className="dt-rule-switch" checked={rule.enabled} onChange={enabled => onChange({ ...rule, enabled })} label={t('ruleEnable',
      { name })} />
  </li>
}

type Part = Record<string, unknown>
function SourcePart({ source, part, saved, onChange }: {
  source: SourceView; part: Part; saved: Part; onChange: (next: Part) => void
}) {
  const { t } = usePanel()
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const policy = source.policy!
  // anyone who @s the owner asks explicitly, so the people and bot lists and the admission table are optional.
  return <div className="dt-part">
    <div className="dt-field"><span className="dt-field-head">{t('explicitRequest')}</span><span>{localized(t,
      policy.explicit)}</span></div>
    {(policy.fields.length > 0 || policy.rows) && <DisclosureRow icon={<Icon name="settings" />} title={t('advanced')} open={advancedOpen}
      onToggle={() => setAdvancedOpen(!advancedOpen)}>
      <div className="dt-disclosure-body">
        {policy.fields.map(field => <SourceField key={field.key} field={field} value={part[field.key]} saved={saved[field.key]}
          fact={undefined} labels={source.labels} self={field.key === 'allowed' ? source.self : undefined}
          onChange={next => onChange({ ...part, [field.key]: next })} />)}
        {policy.rows && <AdmissionTable rows={policy.rows} labels={source.labels} table={(part.admission ?? {}) as AdmissionTable}
          explicit={localized(t, policy.explicit)}
          onChange={admission => onChange({ ...part, admission })} />}
      </div>
    </DisclosureRow>}
  </div>
}

type AdmissionTable = Record<string, Partial<Record<AdmissionCell, Admission>>>
const CELLS: readonly AdmissionCell[] = ['listedUnaddressed', 'unlistedAddressed', 'unlistedUnaddressed']
function AdmissionTable({ rows, labels, table, explicit, onChange }: {
  rows: AdmissionRowView[]; labels: Record<string,
    string>; table: AdmissionTable; explicit: string; onChange: (table: AdmissionTable) => void
}) {
  const { t } = usePanel()
  const name = (row: AdmissionRowView) => labels[row.id] ?? localized(t, row.label)
  const set = (row: string, cell: AdmissionCell, value: Admission) => onChange({ ...table, [row]: { ...table[row], [cell]: value } })
  const select = (row: AdmissionRowView, cell: AdmissionCell) => {
    const value = table[row.id]?.[cell] ?? ADMISSION_DEFAULTS[cell]
    // Reserve a fixed-width slot for the warning icon so selecting a value never shifts column widths.
    return <td key={cell}><span className="dt-admission-cell">
      <select className="dt-select" aria-label={`${name(row)} · ${t(`admissionCell.${cell}`)}`} value={value} onChange={event => set(row.id,
        cell, event.target.value as Admission)}>
        {ADMISSION_OPTIONS[cell].map(option => <option key={option} value={option}>{t(`admissionValue.${option}`)}</option>)}
      </select>
    </span></td>
  }
  return <Section title={t('admission')} hint={t('admissionHint')}>
    <Hint>{explicit}</Hint>
    <table className="dt-table dt-admission">
      <thead>
        <tr><th rowSpan={2}>{t('admissionScope')}</th><th>{t('admissionListed')}</th><th colSpan={2}>{t('admissionUnlisted')}</th></tr>
        <tr><th>{t('admissionUnaddressed')}</th><th>{t('admissionAddressed')}</th><th>{t('admissionUnaddressed')}</th></tr>
      </thead>
      <tbody>{rows.map(row => <tr key={row.id}>
        <td className="dt-admission-scope">{name(row)}<small>{localized(t, row.note)}</small></td>
        {CELLS.map(cell => row.cells[cell] ? select(row, cell) : <td key={cell} className="dt-admission-na"
          aria-label={t('admissionNotReceived')}>—</td>)}
      </tr>)}</tbody>
    </table>
    <Hint>{t('admissionFootnote')}</Hint>
  </Section>
}

function PolicyBlock() {
  const { t } = usePanel()
  const { settings: { blocks, sources, policyVersion }, reload } = useSettings()
  const draft = useBlockDraft('policy', blocks.policy.value, blocks.policy.revision)
  const policy = draft.value
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const parts = sources.filter(source => source.installed && source.policy)
  const [tab, setTab] = useState<string>()
  const current = parts.find(source => source.id === tab) ?? parts[0]
  const edit = (next: Partial<typeof policy>) => draft.edit({ ...policy, ...next })
  const toggle = (id: string) => { const next = new Set(expanded); if (next.has(id)) next.delete(id); else next.add(id); setExpanded(next) }
  const insert = (rule: Rule, at: number) => {
    setExpanded(new Set([...expanded, rule.id]))
    edit({ rules: [...policy.rules.slice(0, at), rule, ...policy.rules.slice(at)] })
  }
  // Expand all rules when native form validation fails inside a collapsed rule.
  const reveal = (target: EventTarget) => { if (target instanceof Element
    && target.closest('.dt-rules')) setExpanded(new Set(policy.rules.map(rule => rule.id))) }
  const part = (id: string) => (policy.sources[id] ?? { allowed: [] }) as Part
  const agents = useAgents()
  const add = () => insert({ id: crypto.randomUUID(), name: '', enabled: true, instruction: '' }, policy.rules.length)
  return <form className="dt-form dt-block" onSubmit={event => { void draft.submit(event) }}
    onInvalidCapture={event => reveal(event.target)}>
    <Fields disabled={draft.busy}>
      <h2 className="dt-block-title">{t('term.policy')}<InfoTip text={t('policyIntro')} /></h2>
      <p className="dt-flow">
        <LabelValue label={t('policyVersion')} value={<span className="dt-mono">{policyVersion.slice(0, 8)}</span>} /><InfoTip
          text={t('policyVersionHint')} />
      </p>
      <Section title={t('rules')} hint={t('rulesHint')} action={<Button variant="ghost" size="sm" icon={<Icon name="plus" />}
        onClick={add}>{t('addRule')}</Button>}>
        {policy.rules.length ? <ul className="dt-rules">{policy.rules.map((rule, index) => <RuleRow key={rule.id} rule={rule} agents={agents}
          expanded={expanded.has(rule.id)}
          onToggle={() => toggle(rule.id)} onChange={next => edit({ rules: policy.rules.map((item, position) => position === index ? next
            : item) })}
          onDuplicate={() => insert({ ...rule, origin: undefined, id: crypto.randomUUID(), name: `${rule.name}${t('copySuffix')}`.slice(0, LIMITS.ruleName) },
            index + 1)}
          onRemove={() => edit({ rules: policy.rules.filter((_, position) => position !== index) })} />)}</ul>
            : <Hint>{t('rulesEmpty')}</Hint>}
      </Section>
      {current && <Section title={t('policyPerSource')} hint={t('policyPerSourceHint')}>
        {parts.length > 1 && <div className="dt-subtabs" role="tablist" aria-label={t('policyPerSource')}>
          {parts.map(source => <button key={source.id} type="button" role="tab" aria-selected={source === current} className="dt-subtab"
            onClick={() => setTab(source.id)}>
            {localized(t, source.title)}
          </button>)}
        </div>}
        <SourcePart key={current.id} source={current} part={part(current.id)}
          saved={(blocks.policy.value.sources[current.id] ?? {}) as Part} onChange={next => edit({ sources: { ...policy.sources,
            [current.id]: next as typeof policy.sources[string] } })} />
      </Section>}
    </Fields>
    <SaveBar {...draft} onDiscard={draft.discard} />
  </form>
}

function EvalBlock() {
  const { t } = usePanel()
  const block = useSettings().settings.blocks.eval
  const saved = { ...block.value, timeoutSeconds: String(block.value.timeoutSeconds) }
  const draft = useBlockDraft('eval', saved, block.revision, value => ({ ...value, timeoutSeconds: numberOf(value.timeoutSeconds) }))
  const value = draft.value
  const edit = (next: Partial<typeof value>) => draft.edit({ ...value, ...next })
  return <form className="dt-form dt-block" onSubmit={event => { void draft.submit(event) }}>
    <Fields disabled={draft.busy}>
      <h2 className="dt-block-title">{t('evalSettings')}<InfoTip text={t('evalIntro')} /></h2>
      <Section title={t('acceptMode')} hint={t(`modeOption.${value.mode}`)}>
        <SegmentedControl className="dt-segmented" label={t('acceptMode')} value={value.mode} onChange={mode => edit({ mode })}
          options={(['off', 'observe', 'auto'] as const).map(mode => ({ value: mode, label: valueLabel(t, 'mode', mode),
            title: t(`modeOption.${mode}`) }))} />
      </Section>
      <Section title={t('evalLanguage')} hint={t('evalLanguageHint')}>
        <SegmentedControl className="dt-segmented" label={t('evalLanguage')} value={value.language ?? 'zh'} onChange={language => edit({ language })}
          options={(['zh', 'en'] as const).map(language => ({ value: language, label: valueLabel(t, 'language', language) }))} />
      </Section>
      <Field label={t('evalTimeout')} hint={t('evalTimeoutHint')}>
        <Input type="number" min={LIMITS.evalTimeoutSeconds.min} max={LIMITS.evalTimeoutSeconds.max} required value={value.timeoutSeconds}
          onChange={event => edit({ timeoutSeconds: event.target.value })} />
      </Field>
    </Fields>
    <SaveBar {...draft} onDiscard={draft.discard} />
  </form>
}

export function EvalSettings() {
  return <div className="dt-form">
    <PolicyBlock />
    <EvalBlock />
  </div>
}