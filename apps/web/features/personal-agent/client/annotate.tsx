/**
 * Annotations (Q4/Q5): a note the owner writes on a Thread or Ticket is an instruction to their personal agent,
 * and the server turns it into an explicit Trigger from the owner on that Thread.
 */
import { useState, type FormEvent } from 'react'
import type { AnnotationView } from '../app/api'
import { Button, Icon } from './controls'
import { useAction, usePanel } from './provider'
import { Hint, Section, When } from './widgets'

export function AnnotateBox({ send, annotations = [] }: { send: (text: string) => Promise<unknown>; annotations?: AnnotationView[] }) {
  const { t, notify } = usePanel()
  const [text, setText] = useState('')
  const action = useAction(async (value: string) => { await send(value); setText(''); notify(t('annotateSent')) })
  const submit = (event: FormEvent) => { event.preventDefault(); if (text.trim()) void action.run(text.trim()) }
  return <Section title={t('annotate')} icon="message">
    <Hint>{t('annotateHint')}</Hint>
    <form className="dt-form dt-annotate" onSubmit={submit}>
      <textarea className="dt-textarea" rows={3} aria-label={t('annotate')} placeholder={t('annotatePlaceholder')} value={text}
        disabled={action.busy} onChange={event => setText(event.target.value)} />
      <div className="dt-actions">
        <Button type="submit" variant="primary" size="sm" icon={<Icon name="message" size={14} />} disabled={action.busy || !text.trim()}>
          {t('annotateSubmit')}</Button>
      </div>
    </form>
    {annotations.length > 0 && <ul className="dt-plain-list dt-annotations" aria-label={t('annotations')}>
      {annotations.map(item => <li key={item.id}><span className="dt-annotation-text">{item.text}</span> <When at={item.at} /></li>)}
    </ul>}
  </Section>
}
