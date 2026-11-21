/** What a ticket waiting for the owner asks, why, and an inline answer sent back to the ticket as an annotation. */
import { useState, type FormEvent } from 'react'
import type { QuestionView } from '../app/api'
import { Button, Icon } from './controls'
import { useAction, usePanel } from './provider'

export function QuestionBox({ ticketId, question, onAnswered }: { ticketId: string; question: QuestionView; onAnswered?: () => Promise<void> }) {
  const { t, call, notify } = usePanel()
  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')
  const action = useAction(async (value: string) => {
    await call('POST tickets/:id/annotate', { id: ticketId, body: { text: value } })
    setText(''); setOpen(false); notify(t('annotateSent')); await onAnswered?.()
  })
  const submit = (event: FormEvent) => { event.preventDefault(); if (text.trim()) void action.run(text.trim()) }
  return <div className="dt-banner dt-question" data-testid="ticket-question">
    <span className="dt-banner-label">{t('questionPrefix')}</span><span className="dt-question-text">{question.text}</span>
    {question.reason && <div className="dt-row-meta">{t('questionReason')}{question.reason}</div>}
    {open ? <form className="dt-form dt-annotate" onSubmit={submit}>
      <textarea className="dt-textarea" rows={2} aria-label={t('goAnswer')} value={text} disabled={action.busy}
        onChange={event => setText(event.target.value)} />
      <div className="dt-actions">
        <Button type="submit" variant="primary" size="sm" icon={<Icon name="message" size={14} />} disabled={action.busy || !text.trim()}>
          {t('annotateSubmit')}</Button>
      </div>
    </form> : <div className="dt-banner-actions"><Button type="button" variant="outline" size="sm" onClick={() => setOpen(true)}>{t('goAnswer')}</Button></div>}
  </div>
}