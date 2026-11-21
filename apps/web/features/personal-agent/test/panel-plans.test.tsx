// MUL-13553 「待我决策」: the executor's plans for issues that need the owner, executed as them, edited first, or rejected
// with a reason; a merge asks for confirmation.
import { expect, it } from 'vitest'
import { zh } from '../client/locales'
import { withPanel, mount, click, text, field, type, at, all } from './panel'
import { server } from './panel-server'

withPanel()
it('lists pending plans with their conclusion, reason and actions, linked to the issue', async () => {
  at({ page: 'plans' })
  await mount()
  expect(text()).toContain('可以合并')
  expect(text()).toContain('review 通过、CI 绿')
  expect(all('.dt-plan-action').map(item => item.textContent)).toEqual(['评论 MUL-551：LGTM', '合并 MR mr-1'])
  expect(document.querySelector<HTMLAnchorElement>('.dt-plan a')?.getAttribute('href')).toBe('/acme/issues/MUL-551')
})

it('executes a plan with a merge only after confirmation', async () => {
  at({ page: 'plans' })
  await mount()
  await click(zh.planExecute)
  expect(server.requests.some(request => request.path === 'plans/p1/execute')).toBe(false)
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain(zh.planMergeConfirm)
  await click(zh.confirm)
  expect(server.requests.find(request => request.path === 'plans/p1/execute')).toMatchObject({ method: 'POST', body: { confirm: true } })
})

it('executes the owner\'s edited actions', async () => {
  at({ page: 'plans' })
  await mount()
  await click(zh.planEdit)
  await type(field<HTMLTextAreaElement>(zh.planActions), '[{"type":"comment","target":"MUL-551","body":"先别合"}]')
  await click(zh.planExecuteEdited)
  expect(server.requests.find(request => request.path === 'plans/p1/execute')).toMatchObject({ method: 'POST',
    body: { actions: [{ type: 'comment', target: 'MUL-551', body: '先别合' }], confirm: false } })
})

it('rejects a plan with a reason', async () => {
  at({ page: 'plans' })
  await mount()
  await click(zh.planReject)
  await type(field<HTMLTextAreaElement>(zh.planRejectReason), 'MR 还有冲突')
  await click(zh.confirm)
  expect(server.requests.find(request => request.path === 'plans/p1/reject')).toMatchObject({ method: 'POST', body: { reason: 'MR 还有冲突' } })
})
