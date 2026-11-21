/** Shared list rows for Tickets, Threads, and Decisions, plus Source fact chips and automated-message run folding. */
import { useState, type ReactNode } from 'react'
import type { PlanView } from '../app/api'
import { PlanRow } from './plans'
import { QuestionBox } from './question'
import type { DecisionRow, ThreadFacts, ThreadRow, TicketRow } from './api'
import { DisclosureRow, Icon, Tag, type IconName } from './controls'
import { decisionMark, threadMark, ticketMark } from './marks'
import { usePanel } from './provider'
import { actorName, askedBy, localized, readableMarkers, reasonLabel, valueLabel } from './text'
import { ActionLink, ConfirmButton, LabelValue, SessionButton, StageMark, When } from './widgets'

type Answerable = Pick<TicketRow, 'stage' | 'ended' | 'actor' | 'scope' | 'executor' | 'issue' | 'note' | 'summary'> & { action?: string }
/** The four answers a Ticket gives the owner: who asked, what for, who it went to, and how it stands. */
export function AnswerFacts({ ticket }: { ticket: Answerable }) {
  const { t } = usePanel()
  const status = ticketMark(t, ticket as Parameters<typeof ticketMark>[1])
  const progress = ticket.note?.text ? readableMarkers(t, ticket.note.text) : ticket.summary.split('\n')[0]?.trim()
  const to = ticket.executor?.name || t('defaultWorker')
  const facts: [string, ReactNode][] = [
    ...ticket.actor ? [[t('cardFrom'), askedBy(t, ticket.actor)] as [string, ReactNode]] : [],
    ...ticket.scope ? [[t('cardScope'), ticket.scope] as [string, ReactNode]] : [],
    [t('cardTo'), <>{to}{ticket.issue && <> · <a className="dt-inline-link" href={ticket.issue.url} target="_blank" rel="noopener noreferrer"
      title={ticket.issue.title}>{ticket.issue.identifier}</a></>}</>],
    [t('cardNow'), progress ? `${status.label} · ${progress}` : status.label],
  ]
  return <div className="dt-card-facts">{facts.map(([label, value]) => <span key={label} className="dt-card-fact"><span
    className="dt-card-label">{label}</span><span className="dt-card-value">{value}</span></span>)}</div>
}

export function TicketLine({ ticket, question, onChanged, context, plans = [] }: {
  ticket: TicketRow; question?: boolean; onChanged?: () => Promise<void>; context?: boolean; plans?: PlanView[]
}) {
  const { t, call, navigate } = usePanel()
  const status = ticketMark(t, ticket)
  const after = async (request: Promise<unknown>) => { await request; await onChanged?.() }
  // Only what needs the owner gets a button: answer or decide where the conversation is, or retry what stalled.
  const link = ticket.stage === 'person' && (ticket.action === 'answer' || ticket.action === 'review') ? ticket.reply?.url
    || ticket.issue?.url : undefined
  return <li className="dt-row dt-link dt-ticket" data-state={question ? status.dot : undefined}>
    <div className="dt-ticket-stage"><StageMark state={status.dot} icon={status.icon} label={status.label} /></div>
    <div className="dt-ticket-body">
      <div className="dt-row-title"><button type="button" className="dt-open" onClick={() => navigate({ page: 'execution',
        id: ticket.id })}>{ticket.title}</button></div>
      <AnswerFacts ticket={ticket} />
      {ticket.question && <QuestionBox ticketId={ticket.id} question={ticket.question} onAnswered={onChanged} />}
      <div className="dt-row-meta dt-ticket-meta">
        {/* Threads on the same channel with nothing else to tell them apart show one chip. */}
        {ticket.threads.filter((thread, index, all) => thread.url || all.findIndex(other => !other.url && other.source === thread.source
          && JSON.stringify(other.facts) === JSON.stringify(thread.facts)) === index).map(thread => <ThreadChip key={thread.key} thread={thread} />)}
        {context && <ContextTag />}
        {ticket.stuck && <Tag tone="warning" title={t(`stuck.${ticket.stuck.kind}`)}>{t('ticketStuck')}</Tag>}
        {ticket.undelivered > 0 && <LabelValue label={t('ticketUndeliveredLabel')} value={ticket.undelivered} />}
        <When at={ticket.updatedAt} />
      </div>
      {/* 「等你决定」: the plan and its buttons right on the card. */}
      {onChanged && plans.map(plan => <PlanRow key={plan.id} plan={plan} reload={onChanged} embedded />)}
      <div className="dt-ticket-actions">{onChanged && <>
        {link && !(plans.length && ticket.stage === 'person' && ticket.action === 'review') && <ActionLink href={link} icon="message">{ticket.stage === 'person' && ticket.action === 'review' ? t('goDecide') : ticket.reply?.url
          ? t('answerIn', { source: valueLabel(t, 'source', ticket.reply.source) }) : t('goAnswer')}</ActionLink>}
        {ticket.actions.retry && <ConfirmButton variant="outline" icon="refresh" label={t('retry')} title={t('retryTitle')}
          message={t('retryConfirm', { title: ticket.title })}
          onConfirm={() => after(call('POST tickets/:id/retry', { id: ticket.id }))} />}
        {ticket.actions.retry && ticket.sessionId && <SessionButton variant="outline" sessionId={ticket.sessionId} />}
      </>}</div>
    </div>
  </li>
}

/** A collapsed group, such as the Tickets that finished their turn or a Ticket's technical details. */
export function Fold({ icon, title, open = false, children }: { icon: IconName; title: ReactNode; open?: boolean; children: ReactNode }) {
  const [shown, setShown] = useState(open)
  return <DisclosureRow icon={<Icon name={icon} />} title={title} open={shown} onToggle={() => setShown(!shown)}>
    {shown && <div className="dt-disclosure-body">{children}</div>}
  </DisclosureRow>
}

function ContextTag() {
  const { t } = usePanel()
  return <Tag tone="quiet" title={t('contextOnlyHint')}>{t('contextOnly')}</Tag>
}
/** Thread list row; on a Ticket page, `ticketId` omits that Ticket from fanout targets and `context` marks context-only threads. */
export function ThreadLine({ thread, ticketId, context }: { thread: ThreadRow; ticketId?: string; context?: boolean }) {
  const { t, navigate } = usePanel()
  const live = thread.tickets.filter(ticket => ticket.state === 'open' && ticket.id !== ticketId)
  const { dot, icon, label } = threadMark(t, thread)
  const reaches = live.length > 0 && `${live[0]!.title}${live.length > 1 ? t('andMore', { count: live.length - 1 }) : ''}`
  return <li className="dt-row dt-link dt-ticket">
    <div className="dt-ticket-stage"><StageMark state={dot} icon={icon} label={label} /></div>
    <div className="dt-ticket-body">
      <div className="dt-row-title">
        <button type="button" className="dt-open" title={thread.pollError} onClick={() => navigate({ page: 'trigger',
          id: thread.key })}>{readableMarkers(t, thread.title)}</button>
      </div>
      <div className="dt-row-quote dt-line">
        {thread.latest && <>{actorName(t, thread.latest.actor)}{t('labelSeparator')}{readableMarkers(t, thread.latest.preview)
          || t('empty')}</>}
      </div>
      <div className="dt-row-meta dt-line">
        <ThreadChip thread={thread} />
        {context && <ContextTag />}
        {thread.pollError ? <span className="dt-line-text dt-error-text">{thread.pollError}</span> : reaches && <LabelValue
          label={t('fanoutTo')} value={reaches} />}
        <When at={thread.lastAt} />
      </div>
    </div>
  </li>
}

export function DecisionLine({ decision }: { decision: DecisionRow }) {
  const { t, navigate } = usePanel()
  const matched = decision.rules.filter(rule => rule.matched).map(rule => rule.name)
  const mark = decisionMark(t, decision.outcome)
  const preview = readableMarkers(t, decision.preview)
  const reason = decision.reason ? reasonLabel(t, decision.reason) : ''
  const open = () => decision.ticketId ? navigate({ page: 'execution', id: decision.ticketId })
    : navigate({ page: 'trigger', id: decision.thread, query: { trigger: decision.triggerIds[0] ?? '' } })
  return <li className="dt-row dt-link dt-ticket">
    <div className="dt-ticket-stage"><StageMark state={mark.dot} icon={mark.icon} label={mark.label} /></div>
    <div className="dt-ticket-body">
      <div className="dt-row-title">
        <button type="button" className="dt-open" title={reason || undefined} onClick={open}>
          {decision.ticketTitle ?? (decision.threadTitle ? readableMarkers(t, decision.threadTitle) : preview || t('empty'))}
        </button>
      </div>
      <div className="dt-row-quote dt-line">{preview}</div>
      <div className="dt-row-meta dt-line">
        {reason && <span className="dt-line-text">{reason}</span>}
        {matched.length > 0 && <LabelValue label={t('ruleLabel')} value={matched.join(t('listSeparator'))} />}
        <When at={decision.at} />
      </div>
    </div>
  </li>
}

function ThreadChip({ thread }: { thread: Pick<ThreadFacts, 'source' | 'facts' | 'url' | 'title'> }) {
  const { t } = usePanel()
  const source = valueLabel(t, 'source', thread.source)
  // A fact that already names the channel, makes the source label redundant.
  const repeats = Object.values(thread.facts).some(fact => localized(t, fact.value).includes(source))
  const cells = <>
    {!repeats && <span className="dt-chip-source">{source}</span>}
    {Object.entries(thread.facts).map(([id, fact]) => <span key={id} className={fact.problem ? 'dt-chip-fact dt-chip-problem'
      : 'dt-chip-fact'}>
      {fact.label && <span className="dt-chip-label">{localized(t, fact.label)}</span>}{localized(t, fact.value)}
    </span>)}
  </>
  return thread.url ? <a className="dt-chip dt-chip-link" href={thread.url} target="_blank" rel="noopener noreferrer" title={thread.title}>
    {cells}<span className="dt-chip-open"><Icon name="external" size={11} /></span>
  </a> : <span className="dt-chip">{cells}</span>
}

type Run<I> = { item: I; earlier: I[] }
/** Group consecutive automated items from the same sender so only the newest is shown by default. */
export function automatedRuns<I>(items: I[], sender: (item: I) => string | undefined): Run<I>[] {
  const runs: Run<I>[] = []
  for (const item of items) {
    const last = runs.at(-1), from = sender(item)
    if (last && from !== undefined && sender(last.item) === from) last.earlier.push(item)
    else runs.push({ item, earlier: [] })
  }
  return runs
}
export function Earlier({ items }: { items: { id: string; at: string; preview: string }[] }) {
  const { t } = usePanel()
  const [open, setOpen] = useState(false)
  if (!items.length) return null
  return <div>
    <button type="button" className="dt-textlink" aria-expanded={open} onClick={() => setOpen(!open)}>
      {open ? t('collapse') : t('automatedEarlier', { count: items.length })}
    </button>
    {open && <ul className="dt-plain-list dt-hint">{items.map(item => <li key={item.id}><When
      at={item.at} /> · {readableMarkers(t, item.preview) || t('empty')}</li>)}</ul>}
  </div>
}