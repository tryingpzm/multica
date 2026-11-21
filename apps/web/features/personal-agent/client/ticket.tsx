/* eslint-disable react/jsx-key -- KeyValues rows are [label, value] tuples, not rendered lists. */
/** Ticket detail page: stage and actions, Mandate details, undelivered/delivered Inputs, subscribed Threads, and Assignments. */
import { useState, type ReactNode } from 'react'
import { AnnotateBox } from './annotate'
import { ApiError, usePoll, type TicketPageView } from './api'
import { Dot, Tag } from './controls'
import { decisionMark, reportDot, ticketMark } from './marks'
import { usePanel } from './provider'
import { settingsRoute } from './route'
import { PlanRow, plansOf, usePlans } from './plans'
import { QuestionBox } from './question'
import { AnswerFacts, Earlier, Fold, ThreadLine, automatedRuns } from './rows'
import { actorName, localized, readableMarkers, reasonLabel, valueLabel, when } from './text'
import { ActionLink, ConfirmButton, ExternalLink, Hint, KeyValues, LabelValue, LoadState, MarkdownLite, PageHead, Section, SessionButton,
  StageMark, When } from './widgets'

type Input = TicketPageView['inputs'][number]
function InputLine({ input, earlier = [] }: { input: Input; earlier?: { id: string; at: string; preview: string }[] }) {
  const { t, navigate } = usePanel()
  const trigger = input.kind === 'trigger' ? input.trigger : undefined
  return <li className="dt-row">
    <Dot state={input.delivered ? 'idle' : 'warning'} label={input.delivered ? t('inputDelivered') : t('inputUndelivered')} />
    <div className="dt-row-main">
      {trigger ? <div className="dt-row-title"><button type="button" className="dt-open" onClick={() => navigate({ page: 'trigger',
        id: trigger.thread, query: { trigger: trigger.id } })}>
        {actorName(t, trigger.actor)}{t('labelSeparator')}{readableMarkers(t, trigger.preview) || t('empty')}
      </button></div> : <div className="dt-row-title">{valueLabel(t, 'input', input.kind)}</div>}
      <div className="dt-row-meta">{input.kind === 'trigger' && <span>{valueLabel(t, 'input', input.kind)}</span>}<When
        at={input.at} /></div>
      {(input.kind === 'text' || input.kind === 'note') && input.text && <div className="dt-row-quote"><MarkdownLite text={input.text} /></div>}
      {trigger?.handback && <Hint>{t('inputHandedBack', { reason: trigger.handback.reason })}</Hint>}
      <Earlier items={earlier} />
    </div>
  </li>
}
function InputList({ inputs }: { inputs: Input[] }) {
  const sender = (input: Input) => input.kind === 'trigger' && input.trigger && !input.trigger.human
    ? `${input.trigger.actor.source}:${input.trigger.actor.id}` : undefined
  const shown = (input: Input) => ({ id: input.id, at: input.at, preview: input.kind === 'trigger' ? input.trigger?.preview ?? '' : '' })
  return <ul className="dt-list">{automatedRuns(inputs, sender).map(run => <InputLine key={run.item.id} input={run.item}
    earlier={run.earlier.map(shown)} />)}</ul>
}

function Assignments({ ticket }: { ticket: TicketPageView }) {
  const { t } = usePanel()
  const report = (state: string) => t('assignmentReportShort', { state: valueLabel(t, 'report', state) })
  return <ul className="dt-list">{ticket.assignments.slice().reverse().map((item, index) => <li key={item.id} className="dt-row">
    <Dot state={item.state === 'running' ? reportDot(item.report?.state) : 'idle'} label={valueLabel(t, 'assignment', item.state)} />
    <div className="dt-row-main">
      <div className="dt-row-title">{t('assignmentNumber', { number: ticket.assignments.length - index })}</div>
      <div className="dt-row-meta">
        {/* A running Assignment whose worker reported a non-progress state has ended its turn; show that report as its tag and end its span there. */}
        {item.turnEndedAt && item.report ? <Tag tone="neutral">{report(item.report.state)}</Tag> : <Tag tone={item.state === 'running'
          ? 'info'
          : 'neutral'}>{valueLabel(t, 'assignment', item.state)}</Tag>}
        {item.state !== 'running' && item.report && <span>{report(item.report.state)}</span>}
        {item.report?.unreported && <Tag tone="warning">{t('assignmentUnreported')}</Tag>}
        <span>{t('assignmentSpan', { from: when(item.startedAt ?? item.createdAt, t('lang')), to: when(item.endedAt ?? item.turnEndedAt,
          t('lang')) || t('now') })}</span>
        {item.endedReason && <span>{reasonLabel(t, item.endedReason)}</span>}
      </div>
    </div>
    {item.ref && item.ref !== ticket.sessionId && <div className="dt-row-actions"><SessionButton variant="outline"
      sessionId={item.ref} /></div>}
  </li>)}</ul>
}
/** Why the Router took the Ticket: the message, the rules it matched, its reason, and what it told the worker to do. */
function RouterJudgement({ ticket }: { ticket: TicketPageView }) {
  const { t, navigate } = usePanel()
  const { decision } = ticket
  const direction = decision?.direction
  const matched = decision?.rules.filter(rule => rule.matched) ?? []
  const rows: [string, ReactNode][] = [
    ...decision ? [[t('judgeTrigger'), <button type="button" className="dt-trigger-link" onClick={() => navigate({ page: 'trigger',
      id: decision.thread, query: { trigger: decision.triggerIds[0] ?? '' } })}>
      <span className="dt-trigger-text">{readableMarkers(t, decision.preview) || t('empty')}</span>
      <span className="dt-row-meta"><When at={decision.at} /></span>
    </button>] as [string, ReactNode]] : [],
    ...matched.length ? [[t('judgeRules'), <div className="dt-kv-stack">{matched.map(rule => <span key={rule.id}>{rule.name}{rule.reason
      ? `${t('labelSeparator')}${rule.reason}` : ''}</span>)}</div>] as [string, ReactNode]] : [],
    ...decision ? [[t('judgeWhy'), <div className="dt-kv-stack">
      <span>{decisionMark(t, decision.outcome).label}{decision.reason.length ? `${t('labelSeparator')}${reasonLabel(t, decision.reason)}` : ''}</span>
      {direction && <Hint>{t('directionLine', { intent: t(direction.implementation_requested ? 'intentYes' : 'intentNo'),
        basis: valueLabel(t, 'targetBasis', direction.target_basis),
        design: t(direction.requires_design_decision ? 'designNeeded' : 'designNotNeeded') })} {t('directionEvidence',
          { evidence: direction.evidence })}</Hint>}
    </div>] as [string, ReactNode]] : [],
    ...planNote(ticket.plan) ? [[t('judgePlan'), <MarkdownLite text={planNote(ticket.plan)} />] as [string, ReactNode]] : [],
  ]
  if (!rows.length) return null
  return <Section title={t('routerJudgement')} icon="decision"><KeyValues rows={rows} /></Section>
}

/** The Router's own note on how to go about it; the rule text and engine boilerplate of the brief stay in technical details. */
export function planNote(plan: string | undefined): string {
  if (!plan) return ''
  const note = /Routing note[^:\n]*:\s*(.+)/.exec(plan)?.[1]
  if (note) return note.trim()
  const head = plan.split(/\n\s*(?:Matched rules|- )/)[0]!.trim()
  return /[\u4e00-\u9fff]/.test(head) ? head : ''
}

type Moment = NonNullable<TicketPageView['timeline']>[number]
function momentText(t: Parameters<typeof reasonLabel>[0], item: Moment): string {
  switch (item.kind) {
    case 'trigger': return t('timeline.trigger', { actor: actorName(t, item.actor), text: readableMarkers(t, item.text ?? '') || t('empty') })
    case 'decision': return t('timeline.decision', { outcome: decisionMark(t, item.outcome ?? 'accept').label })
    case 'assign': return t('timeline.assign', { executor: item.executor || t('defaultWorker') })
    case 'report': return t('timeline.report', { text: item.text || valueLabel(t, 'report', item.state) })
    default: return t('timeline.reply', { source: valueLabel(t, 'source', item.source), text: item.text ?? '' })
  }
}
/** Trigger, judgement, dispatch, reports and replies in time order, one line each; a line with more text opens it. */
function Timeline({ items }: { items: Moment[] }) {
  const { t } = usePanel()
  const [open, setOpen] = useState<number | undefined>()
  return <ul className="dt-plain-list dt-timeline">{items.map((item, index) => {
    const more = item.detail && item.detail.trim() !== (item.text ?? '').trim()
    return <li key={index}>
      <When at={item.at} /> <span className="dt-timeline-text">{momentText(t, item)}</span>
      {item.kind === 'decision' && item.reason?.length ? <Hint>{reasonLabel(t, item.reason)}</Hint> : null}
      {more && <button type="button" className="dt-textlink" aria-expanded={open === index} onClick={() => setOpen(open === index
        ? undefined : index)}>{open === index ? t('collapse') : t('expand')}</button>}
      {more && open === index && <div className="dt-row-quote"><MarkdownLite text={item.detail!} /></div>}
    </li>
  })}</ul>
}

/** Engine internals the owner rarely needs: policy version, how the sender was addressed, authority, Threads and ids. */
function TechDetails({ ticket }: { ticket: TicketPageView }) {
  const { t, navigate } = usePanel()
  const { mandate, decision } = ticket
  const fact = (item: TicketPageView['basis']['facts'][number]): string => {
    if (item.name === 'legacyAuthority') return valueLabel(t, 'legacy', item.value)
    if (item.name === 'mode' || item.name === 'addressed') return valueLabel(t, item.name, item.value)
    if (item.name === 'admission') return valueLabel(t, 'admissionCell', item.value)
    if (item.name === 'rules') return item.value.map(rule => rule.name).join(t('listSeparator'))
    return item.name === 'sender' ? item.value : t('yes')
  }
  const { legacy } = ticket.basis
  const version = <button type="button" className="dt-textlink dt-mono" onClick={() => navigate(settingsRoute('eval',
    { version: mandate.policyVersion }))}>{mandate.policyVersion.slice(0, 8)}</button>
  const undelivered = ticket.inputs.filter(input => !input.delivered).length
  return <Fold icon="hash" title={t('techDetails')}>
    <KeyValues rows={[
      ...legacy ? [] : [[t('policyVersion'), version] as [string, ReactNode]],
      [t('techStrength'), valueLabel(t, 'strength', mandate.strength)],
      ...ticket.basis.facts.map((item): [string, string] => [valueLabel(t, 'basis', item.name), fact(item)]),
      ...decision?.rules.filter(rule => !rule.matched).map((rule): [string, string] => [t('judgeRules'), t('ruleUnmatched',
        { rule: rule.name, reason: rule.reason })]) ?? [],
      ...decision?.triggerIds.length ? [[t('triggerIdLabel'), <span className="dt-mono">{decision.triggerIds.join(t('listSeparator'))}</span>] as [string, ReactNode]] : [],
      [t('ticketId'), <span className="dt-mono">{ticket.id}</span>],
      ...ticket.plan ? [[t('planBrief'), <div className="dt-clamp"><MarkdownLite text={ticket.plan} /></div>] as [string, ReactNode]] : [],
      ...ticket.issue ? [[t('ticketIssue'), <ExternalLink key="issue" href={ticket.issue.url} icon="message">{ticket.issue.identifier}</ExternalLink>] as [string, ReactNode]] : [],
      ...legacy && mandate.scope ? [[t('legacyAssignment'), <span className="dt-mono dt-clamp">{mandate.scope}</span>] as [string, ReactNode]] : [],
    ]} />
    {ticket.threads.length > 0 && <Section title={t('subscribedThreads')} count={ticket.threads.length}>
      <ul className="dt-list">{ticket.threads.map(thread => <ThreadLine key={thread.key} thread={thread} ticketId={ticket.id}
        context={thread.context} />)}</ul>
    </Section>}
    {ticket.assignments.length > 1 && <Section title={t('term.assignment')} count={ticket.assignments.length}><Assignments ticket={ticket} /></Section>}
    <Section title={t('term.channel')}>
      <ul className="dt-plain-list">{ticket.channels.map((item, index) => <li key={index}>
        {item.url ? <ExternalLink href={item.url} icon="message">{localized(t, item.label)}</ExternalLink>
          : <span>{localized(t, item.label)}</span>}
      </li>)}</ul>
    </Section>
    {ticket.inputs.length > undelivered && <Section title={t('deliveredInputs', { count: ticket.inputs.length - undelivered })}>
      <InputList inputs={ticket.inputs.filter(input => input.delivered)} />
    </Section>}
  </Fold>
}

export function TicketPage({ id }: { id: string }) {
  const { t, call } = usePanel()
  const { data, error, reload } = usePoll(() => call('GET tickets/:id', { id }), [id])
  const plans = usePlans()
  const after = async (request: Promise<unknown>) => { await request; await reload() }
  if (!data) return <><PageHead
    title={t('term.ticket')} />{error instanceof ApiError && error.missing ? <Hint>{t('ticketNotFound')}</Hint> : <LoadState
      error={error} loading />}</>
  const status = ticketMark(t, data)
  const { legacy } = data.basis
  const undelivered = data.inputs.filter(input => !input.delivered)
  const failedText = /^(?:\w+Error|Error)\b[:：]/.test(data.summary)
  const failed = data.stage === 'person' && data.action === 'failed'
  const opening = data.threads.find(thread => thread.url)
  const ended = data.ended && (() => {
    const outcome = valueLabel(t, 'outcome', data.ended.outcome), reason = reasonLabel(t, data.ended.reason), time = when(data.ended.at,
      t('lang'))
    return reason ? t('endedAt', { time, outcome, reason }) : t('endedAtShort', { time, outcome })
  })()
  const channel = data.channels[0]
  return <>
    <PageHead title={data.title}>
      {opening?.url && <ActionLink href={opening.url}>{t('openThread', { source: valueLabel(t, 'source', opening.source) })}</ActionLink>}
      {data.actions.retry && <ConfirmButton variant="outline" icon="refresh" label={t('retry')} title={t('retryTitle')}
        message={t('retryConfirm', { title: data.title })}
        onConfirm={() => after(call('POST tickets/:id/retry', { id }))} />}
      {data.sessionId && <SessionButton variant="outline" sessionId={data.sessionId} />}
    </PageHead>
    {error && <LoadState error={error} loading={false} />}
    <div className="dt-row-meta dt-status-line">
      <StageMark state={status.dot} icon={status.icon} label={status.label} />
      {data.stuck && <Tag tone="warning" title={t(`stuck.${data.stuck.kind}`)}>{t('ticketStuck')}</Tag>}
      <LabelValue label={t('ticketUpdatedLabel')} value={when(data.updatedAt, t('lang'))} />
    </div>
    <AnswerFacts ticket={{ ...data, actor: data.actor ?? data.mandate.actor, scope: data.scope ?? (legacy ? '' : data.mandate.scope) }} />
    {plansOf(plans.data, data.id).map(plan => <PlanRow key={plan.id} plan={plan} embedded
      reload={async () => { await reload(); await plans.reload() }} />)}
    {ended && <Hint>{ended}</Hint>}
    {data.question && <QuestionBox ticketId={id} question={data.question} onAnswered={reload} />}
    {!data.question && data.attention && <div className="dt-banner"><span className="dt-banner-label">{t('attentionPrefix')}</span><MarkdownLite
      text={data.attention.question} />
      {channel?.url && <div className="dt-banner-actions">
        <ActionLink href={channel.url} icon="message">{t('answerIn', { source: valueLabel(t, 'source', channel.source) })}</ActionLink>
      </div>}</div>}
    {data.actions.retry && data.summary && <p className={`dt-banner${failed ? ' dt-danger' : ''}${failedText ? ' dt-mono' : ''}`}>
      <span className="dt-banner-label">{t(failed ? 'failedPrefix' : 'blockedPrefix')}</span>{data.summary}
    </p>}
    {data.deliveryError && <p className="dt-banner dt-danger dt-mono">
      <span className="dt-banner-label">{t('deliveryFailedPrefix')}</span>{data.deliveryError}
    </p>}
    <AnnotateBox send={text => after(call('POST tickets/:id/annotate', { id, body: { text } }))} annotations={data.annotations} />
    <RouterJudgement ticket={data} />
    {(data.timeline?.length ?? 0) > 0 && <Section title={t('progressTimeline')} icon="queue"><Timeline items={data.timeline!} /></Section>}
    {undelivered.length > 0 && <Section title={t('undeliveredInputs')} count={undelivered.length}>
      <Hint>{data.deliveryError ? t('inputRetryHint') : data.paused ? t('inputPausedHint') : t('inputWaitingHint')}</Hint>
      <InputList inputs={undelivered} />
    </Section>}
    {(data.withdrawn?.length ?? 0) > 0 && <Section title={t('withdrawnReplies')} count={data.withdrawn!.length}>
      <ul className="dt-plain-list">{data.withdrawn!.map(item => <li key={item.id}><Tag>{t('confirmWithdrawn')}</Tag> <span>{item.text}</span> <When at={item.at} /></li>)}</ul>
    </Section>}
    <div className="dt-details"><TechDetails ticket={data} /></div>
    {data.actions.stop && <div className="dt-danger-zone">
      <span className="dt-hint">{t('stopHint')}</span>
      <ConfirmButton variant="outline" label={t('stop')} title={t('stopConfirmTitle')} message={t('stopConfirm', { title: data.title })}
        onConfirm={() => after(call('POST tickets/:id/stop', { id }))} />
    </div>}
  </>
}