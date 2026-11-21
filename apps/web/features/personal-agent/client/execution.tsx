/** Execution status view (active Tickets and pause control) and Execution settings group (Executor capacity and workspace environment). */
import { useState } from 'react'
import { TICKET_FILTERS, type TicketFilter } from '../app/api'
import { usePoll, type StateView, type TicketRow } from './api'
import { InfoTip, Pill } from './controls'
import { SaveBar } from './draft'
import { usePanel } from './provider'
import { plansOf, usePlans } from './plans'
import { Fold, TicketLine } from './rows'
import { AgentField, Field, RouterLink, Fields, useAgents, useBlockDraft, useSettings } from './settings-kit'
import { ConfirmButton, Hint, LabelValue, LoadMore, LoadState, StatusHead } from './widgets'

export function ExecutionStatus({ filter: current, state, reload }: {
  filter: TicketFilter; state: StateView; reload: () => Promise<void>
}) {
  const { t, call, navigate } = usePanel()
  const [limit, setLimit] = useState(20)
  const { data, error, reload: reloadTickets } = usePoll(() => call('GET tickets', { query: { state: current, limit } }), [current, limit])
  const { paused, load: { maxWorkers } } = state
  // Pausing uses its own block revision so toggling pause does not invalidate an open Execution settings draft.
  const pause = async () => { await call('POST executor/pause', { body: { paused: !paused } }); await reload() }
  const status = state.setup.problem ? { tone: 'warning', label: t('executorWaitingSetup') } : paused ? { tone: 'warning',
    label: t('processingPaused') }
    : { tone: 'ongoing', label: t('executorRunning') }
  const action = paused ? 'resumeProcessing' : 'pauseProcessing'
  return <>
    <StatusHead hint={t('executionStatusIntro')} settings="execution" />
    <div className="dt-row-meta dt-status-line">
      <span className="dt-status-text" data-state={status.tone}>{status.label}</span>
      {!state.setup.problem && <>
        <LabelValue label={t('executingLabel')}
          value={maxWorkers ? `${state.load.running} / ${maxWorkers}` : state.load.running} /><LabelValue label={t('queuedLabel')} value={state.load.queued} />
      </>}
      <ConfirmButton variant="outline" icon={paused ? 'play' : 'pause'} label={t(action)} title={t(action)} message={t(paused
        ? 'resumeProcessingConfirm' : 'pauseProcessingConfirm')}
        onConfirm={pause} />
    </div>
    <div className="dt-filters">
      {TICKET_FILTERS.map(value => <Pill key={value} active={current === value}
        onClick={() => { setLimit(20); navigate({ page: 'execution', query: value === 'open' ? {} : { filter: value } },
          true) }}>{t(`ticketFilter.${value}`)}</Pill>)}
    </div>
    {current === 'idle' && <Hint>{t('idleHint')}</Hint>}
    <LoadState error={error} loading={!data} />
    {data && (data.items.length ? <TicketList items={data.items} fold={current === 'open'} onChanged={reloadTickets} />
      : <Hint>{t('none')}</Hint>)}
    {data?.next_offset != null && <LoadMore onMore={() => setLimit(limit + 20)} />}
  </>
}

/** Tickets that need the owner or are underway; on the open list, the ones that finished their turn fold under recently done. */
function TicketList({ items, fold, onChanged }: { items: TicketRow[]; fold: boolean; onChanged: () => Promise<void> }) {
  const { t } = usePanel()
  const plans = usePlans()
  const changed = async () => { await onChanged(); await plans.reload() }
  const line = (ticket: TicketRow) => <TicketLine key={ticket.id} ticket={ticket} question={ticket.stage === 'person'}
    onChanged={changed} plans={plansOf(plans.data, ticket.id)} />
  const done = fold ? items.filter(ticket => ticket.stage === 'idle') : []
  const rest = items.filter(ticket => !(done as TicketRow[]).includes(ticket))
  return <>
    {rest.length > 0 && <ul className="dt-list">{rest.map(line)}</ul>}
    {done.length > 0 && <Fold icon="clock" title={t('recentDone', { count: done.length })}><ul className="dt-list">{done.map(line)}</ul></Fold>}
  </>
}

/** The Router agent; tickets go to the agents their rules name, or the deployment's default worker agent. */
export function ExecutionSettings() {
  const { t } = usePanel()
  const { blocks } = useSettings().settings
  const draft = useBlockDraft('executor', blocks.executor.value, blocks.executor.revision)
  const list = useAgents()
  const value = draft.value
  const router = list.agents?.find(agent => agent.id === value.routerAgentId)
  const executor = list.agents?.find(agent => agent.executor)
  const edit = (next: Partial<typeof value>) => draft.edit({ ...value, ...next })
  return <div className="dt-form">
    <form className="dt-form" onSubmit={event => { void draft.submit(event) }}>
      <Fields disabled={draft.busy}>
        <div className="dt-form dt-block">
          <h2 className="dt-block-title">{t('term.executor')}<InfoTip text={t('executorIntro')} /></h2>
          {!blocks.executor.value.routerAgentId && <Hint>{t('chooseExecutor')}</Hint>}
          <Field label={t('routerAgent')} hint={t('routerAgentHint')}>
            <AgentField value={value.routerAgentId} onChange={routerAgentId => edit({ routerAgentId })} list={list} />
          </Field>
          <RouterLink agent={router} />
          {router && !router.runtime && <Hint>{t('routerNoRuntime')}</Hint>}
          {executor && <Hint>{t('executorAgent')}：{executor.name}（{t('executorAgentHint')}）</Hint>}
          <RouterLink agent={executor} className="dt-executor-link" />
        </div>
      </Fields>
      <SaveBar {...draft} onDiscard={draft.discard} />
    </form>
  </div>
}