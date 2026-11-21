/** 「待我决策」(): plans for issues that need the owner — execute as them, edit first, or reject with a reason. */
import { useState } from 'react'
import type { PlanAction, PlanView } from '../app/api'
import { usePoll } from './api'
import { Button, Modal } from './controls'
import { useAction, usePanel } from './provider'
import { ActionLink, Hint, LoadState, Section } from './widgets'

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
function actionLabel(t: (key: string) => string, a: PlanAction, issue: string) {
  const name = t(`planAction.${a.type}`)
  // An id target is named by the plan's issue; a question's comment id names nothing the owner reads.
  const target = a.type === 'clarify_answer' ? '' : UUID.test(a.target) ? issue : a.target
  const head = target ? `${name} ${target}` : name
  return a.body ? `${head}：${a.body}` : head
}

/** One plan with its actions and its execute, edit-then-execute and reject buttons; on a ticket card it is embedded. */
export function PlanRow({ plan, reload, embedded }: { plan: PlanView; reload: () => Promise<void>; embedded?: boolean }) {
  const { t, call } = usePanel()
  const [confirming, setConfirming] = useState<PlanAction[] | null>(null)
  const [editing, setEditing] = useState<string | null>(null)
  const [rejecting, setRejecting] = useState<string | null>(null)
  const execute = useAction(async (actions: PlanAction[] | undefined, confirm: boolean) => {
    await call('POST plans/:id/execute', { id: plan.id, body: { ...(actions ? { actions } : {}), confirm } })
  }, { reload })
  const reject = useAction(async (reason: string) => { await call('POST plans/:id/reject', { id: plan.id, body: { reason } }) }, { reload })
  const run = (actions?: PlanAction[]) => {
    if ((actions ?? plan.actions).some(a => a.type === 'merge_mr')) { setConfirming(actions ?? plan.actions); return }
    void execute.run(actions, false)
  }
  const edited = (): PlanAction[] | undefined => { try { return JSON.parse(editing ?? '') as PlanAction[] } catch { return undefined } }
  const issue = plan.issue_key ?? plan.issue_id
  const Tag = embedded ? 'div' : 'li'
  return <Tag className={embedded ? 'dt-plan dt-plan-embedded' : 'dt-row dt-plan'}>
    <div className="dt-row-main">
      {embedded && <div className="dt-card-label">{t('planForYou')}</div>}
      <div className="dt-row-title">{plan.conclusion}</div>
      <p className="dt-hint">{plan.reason}</p>
      <ul>{plan.actions.map((a, i) => <li key={i} className="dt-plan-action">{actionLabel(t, a, issue)}</li>)}</ul>
      <div className="dt-row-meta"><ActionLink href={plan.issue_url}>{issue} · {plan.issue_status}</ActionLink></div>
      {editing !== null && <textarea aria-label={t('planActions')} value={editing} onChange={event => setEditing(event.target.value)} rows={6} />}
      <div className="dt-row-meta dt-row-buttons">
        {editing === null
          ? <>
            <Button variant="primary" size="sm" disabled={execute.busy} onClick={() => run()}>{t('planExecute')}</Button>
            <Button variant="outline" size="sm" onClick={() => setEditing(JSON.stringify(plan.actions, null, 2))}>{t('planEdit')}</Button>
          </>
          : <Button variant="primary" size="sm" disabled={execute.busy || !edited()} onClick={() => run(edited())}>{t('planExecuteEdited')}</Button>}
        <Button variant="outline" size="sm" onClick={() => setRejecting('')}>{t('planReject')}</Button>
      </div>
    </div>
    <Modal open={confirming !== null} onClose={() => setConfirming(null)} title={t('planExecute')} closeLabel={t('close')}
      description={t('planMergeConfirm')} footer={<>
        <Button variant="ghost" onClick={() => setConfirming(null)}>{t('cancel')}</Button>
        <Button variant="primary" disabled={execute.busy} onClick={() => {
          const actions = confirming === plan.actions ? undefined : confirming ?? undefined
          void execute.run(actions, true).then(() => setConfirming(null))
        }}>{t('confirm')}</Button>
      </>} />
    <Modal open={rejecting !== null} onClose={() => setRejecting(null)} title={t('planReject')} closeLabel={t('close')}
      footer={<>
        <Button variant="ghost" onClick={() => setRejecting(null)}>{t('cancel')}</Button>
        <Button variant="primary" disabled={reject.busy || !rejecting?.trim()}
          onClick={() => { void reject.run(rejecting!.trim()).then(() => setRejecting(null)) }}>{t('confirm')}</Button>
      </>}>
      <textarea aria-label={t('planRejectReason')} value={rejecting ?? ''} onChange={event => setRejecting(event.target.value)} rows={3} />
    </Modal>
  </Tag>
}

/** The owner's pending plans, polled once for a list of tickets or a ticket page. */
export function usePlans() {
  const { call } = usePanel()
  return usePoll(() => call('GET plans'), [])
}
export const plansOf = (plans: PlanView[] | undefined, ticketId: string) => plans?.filter(plan => plan.ticket_ids?.includes(ticketId)) ?? []

export function PlansPage() {
  const { t, call } = usePanel()
  const { data: value, error, reload } = usePoll(() => call('GET plans'), [])
  return <Section title={t('tab.plans')} count={value?.length}>
    <LoadState error={error} loading={!value} />
    {value && !value.length && <Hint>{t('planNone')}</Hint>}
    {value && <ul className="dt-list">{value.map((plan: PlanView) => <PlanRow key={plan.id} plan={plan} reload={reload} />)}</ul>}
  </Section>
}