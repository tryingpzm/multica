/**
 * Replies waiting for their owner's confirmation (Q4/Q5): outside auto mode every reply the Router writes is
 * held in the Outbox until the owner sends, edits or drops it here. Each card says whom it answers, where it goes, the
 * exact text that will be sent, and the ticket it comes from.
 */
import { useState, type ReactNode } from 'react'
import type { PendingReply } from './api'
import { Button, Icon } from './controls'
import { useAction, usePanel } from './provider'
import { actorName, readableMarkers, valueLabel } from './text'
import { ConfirmButton, Hint, MarkdownLite, Section, When } from './widgets'

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return <div className="dt-card-fact"><span className="dt-card-label">{label}</span><span className="dt-card-value">{children}</span></div>
}

function ReplyLine({ reply, onChanged }: { reply: PendingReply; onChanged: () => Promise<void> }) {
  const { t, call, navigate, notify } = usePanel()
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState<string | undefined>()
  const send = useAction(async () => {
    await call('POST outbox/:id/confirm', { id: reply.id, body: draft === undefined ? {} : { text: draft } })
    notify(t('confirmSent'))
  }, { reload: onChanged })
  const where = reply.where
  const source = valueLabel(t, 'source', where?.source ?? reply.channel.source)
  const place = where?.place ? where.source === 'multica' ? t(where.comment ? 'replyComment' : 'replyUnder', { place: where.place })
    : where.place : ''
  const spot = [source, place, where?.post && where.source !== 'multica' ? `${where.post} · ${t('replyPost')}` : ''].filter(Boolean)
    .join(' · ')
  const long = reply.text.length > 160 || reply.text.includes('\n')
  return <li className="dt-row dt-link dt-reply">
    <div className="dt-row-main">
      <Fact label={t('replyTo')}>{reply.to ? `${actorName(t, reply.to.actor)}${t('labelSeparator')}${readableMarkers(t, reply.to.preview)}`
        : t('replyUnknown')}</Fact>
      <Fact label={t('replyWhere')}>{where?.url ? <a className="dt-inline-link" href={where.url} target="_blank"
        rel="noopener noreferrer">{spot}</a> : spot}</Fact>
      <Fact label={t('replyText')}>{draft !== undefined
        ? <textarea className="dt-textarea" aria-label={t('replyText')} rows={6} value={draft} onChange={event => setDraft(event.target.value)} />
        : <div className={open || !long ? 'dt-reply-full' : 'dt-reply-full dt-clamp'}><MarkdownLite text={reply.text} />
          {long && <button type="button" className="dt-textlink" aria-expanded={open} onClick={() => setOpen(!open)}>
            {open ? t('collapse') : t('expand')}</button>}</div>}</Fact>
      <Fact label={t('replyWhy')}>{reply.ticket ? <button type="button" className="dt-textlink" onClick={() => navigate({ page: 'execution',
        id: reply.ticket!.id })}>{t('replyFromTicket', { title: reply.ticket.title })}</button>
        : reply.thread ? <button type="button" className="dt-textlink" onClick={() => navigate({ page: 'trigger', id: reply.thread! })}>
          {t('term.thread')}</button> : t('replyUnknown')}</Fact>
      {reply.error && <p className="dt-error dt-reply-error">{t('confirmFailed', { error: reply.error })}</p>}
      <div className="dt-row-meta dt-row-buttons">
        <Button variant="primary" size="sm" icon={<Icon name="message" size={14} />} disabled={send.busy || draft?.trim() === ''}
          onClick={() => { void send.run() }}>{t(draft === undefined ? 'confirmSend' : 'replyEditSend')}</Button>
        {draft === undefined ? <Button variant="outline" size="sm" onClick={() => setDraft(reply.text)}>{t('replyEdit')}</Button>
          : <Button variant="outline" size="sm" onClick={() => setDraft(undefined)}>{t('replyCancel')}</Button>}
        <ConfirmButton variant="outline" icon="dismiss" label={t('confirmWithdraw')} title={t('confirmWithdraw')}
          message={t('confirmWithdrawConfirm')}
          onConfirm={async () => { await call('POST outbox/:id/withdraw', { id: reply.id }); notify(t('confirmWithdrawn')); await onChanged() }} />
        <When at={reply.at} />
      </div>
    </div>
  </li>
}

export function ConfirmQueue({ replies, onChanged }: { replies: PendingReply[]; onChanged: () => Promise<void> }) {
  const { t } = usePanel()
  if (!replies.length) return null
  return <Section title={t('confirmReplies')} count={replies.length}>
    <Hint>{t('confirmRepliesHint')}</Hint>
    <ul className="dt-list dt-cards">{replies.map(reply => <ReplyLine key={reply.id} reply={reply} onChanged={onChanged} />)}</ul>
  </Section>
}
