// The overview (SA tests/panel-overview.spec.tsx, on Multica's Sources), with the replies to confirm and the switch.
import { expect, it } from 'vitest'
import { en, zh } from '../client/locales'
import { withPanel, mount, remount, all, find, tab, click, text } from './panel'
import { server, droppedReply, pendingReply, threadSummary } from './panel-server'

withPanel()
it('leads the overview with the pending Tickets, then problems, the last day and the Executor, each linking to its stage', async () => {
  await mount()
  expect(tab(zh['tab.overview']).getAttribute('aria-selected')).toBe('true')
  expect(all('[role="tab"]').map(tab => tab.textContent)).toEqual(['概览', '来源', '消息', '研判', '执行', '待我决策', '设置'])
  expect(all('.dt-section-title').map(title => title.textContent)).toEqual(['待处理工单2', '系统问题1', '过去 24 小时详情 ›', '执行助手详情 ›'])
  expect(all('.dt-cards .dt-ticket-stage').map(item => item.textContent)).toEqual(['等你回答', '失败了'])
  expect(all('.dt-cards .dt-att-what').map(item => item.textContent)).toEqual(['研判未完成：这个文案好奇怪'])
  expect(all('.dt-ticket').every(row => row.querySelector('.dt-ticket-stage button, .dt-ticket-stage a') === null
    && [...row.querySelectorAll('.dt-ticket-actions button, .dt-ticket-actions a')].every(action => action.querySelector('svg')))).toBe(true)
  expect(text()).toContain('需要确认按钮颜色')
  // A Worker runs on Multica runtimes without a slot limit, so the Executor counts what runs without a maximum.
  expect(all('.dt-stat').map(stat => stat.textContent)).toEqual(['到达12', '被过滤2', '已研判5', '接单或续做1', '执行中2', '排队0', '已完成1'])
  expect(all('.dt-chip').map(chip => chip.textContent)[0]).toBe('MulticaMUL-42')
  await click(all('.dt-more-link').at(-1)!)
  expect(tab(zh['tab.execution']).getAttribute('aria-selected')).toBe('true')
})

it('marks a Ticket the Watchdog found stuck and lists it as a problem', async () => {
  server.person[1] = { ...server.person[1]!, stuck: { kind: 'run_overdue', since: '2026-09-22T00:00:00Z' } }
  await mount()
  expect(all('.dt-cards .dt-att-what').map(item => item.textContent)).toContain('工单卡住：截图工具')
  expect(all('.dt-ticket .dt-tag').map(tag => tag.textContent)).toContain('卡住')
  await click('工单卡住：截图工具')
  expect(server.requests.some(request => request.path === 'tickets/t4')).toBe(true)
})

it('re-evaluates or dismisses an unjudged Eval from the problem list', async () => {
  await mount()
  await click(zh.requeue)
  expect(server.requests.some(request => request.path === 'triggers/e3/requeue' && request.method === 'POST')).toBe(true)
  await click(zh.dismiss)
  expect(server.requests.some(request => request.path === 'decisions/u1/dismiss' && request.method === 'POST')).toBe(true)
  expect(server.requests.filter(request => request.path === 'overview').length).toBeGreaterThan(2)
})

it('opens the Thread of an unjudged Eval at its Trigger from the problem list', async () => {
  await mount()
  await click('研判未完成：这个文案好奇怪')
  expect(server.requests.some(request => request.path.startsWith('threads/github:g1:m_root'))).toBe(true)
  expect(document.querySelector('.dt-highlight')?.textContent).toContain('message e3')
})

it('follows the last day\'s counts to the status of each stage', async () => {
  await mount()
  expect(all('.dt-stat button').map(button => button.textContent)).toEqual(['2', '1'])
  await click(all('.dt-stat button')[0]!)
  expect(tab(zh['tab.source']).getAttribute('aria-selected')).toBe('true')
  await click(zh.sourceFilteredTitle)
  expect(text()).toContain('looks wrong')
  expect(text()).toContain(zh['reason.source_disabled'])
})

it('offers each Ticket that needs a person only the actions it needs on the overview: answer in its Channel, retry or open a failed worker',
  async () => {
  await mount()
  expect(all('.dt-ticket-actions a').map(link => [link.textContent, link.getAttribute('href')])).toEqual([['去GitHub回答',
    'https://github.com/']])
  expect(all('.dt-card-fact').map(fact => fact.textContent)).toContain('现在怎样失败了 · LlmError: no adapter registered for provider "provider-a"')
  await click(zh.retry)
  await click(zh.confirm)
  expect(server.requests.some(request => request.path === 'tickets/t4/retry' && request.method === 'POST')).toBe(true)
  // A failed worker's run is on its ticket's Multica issue.
  await click(all('.dt-ticket-actions button').find(button => button.textContent === zh.openSession)!)
  expect(server.opened).toEqual(['issue-t4'])
  // Stopping a Ticket is on its page, not an action the owner is asked for.
  expect(all('.dt-ticket-actions button').some(button => button.textContent === zh.stop)).toBe(false)
})

it('shows paused processing as a system problem with its resume action, and says so on the Policy records', async () => {
  server.settings = { ...server.settings, paused: true }
  await mount()
  expect(text()).toContain(zh.problemPaused)
  await click(zh['tab.eval'])
  expect(text()).toContain(zh.evalRecordsPaused)
  await click(zh.resumeWhere)
  expect(tab(zh['tab.execution']).getAttribute('aria-selected')).toBe('true')
  await click(zh['tab.overview'])
  await click(find(zh.resumeProcessing, '.dt-att button'))
  await click(zh.confirm)
  expect(server.requests.find(request => request.path === 'executor/pause')).toMatchObject({ method: 'POST', body: { paused: false } })
})

it('reports no problem when no Source lists anyone, since anyone who @s the owner may originate work', async () => {
  server.settings = { ...server.settings, policy: { ...server.settings.policy, sources: { 'github': { allowed: [], admission: {} },
    multica: { allowed: [], admission: {} } } } }
  await mount()
  expect(text()).not.toContain('没有人能发起工作')
})

it('counts the messages not sent in the last day, linking to the Source status, and shows nothing when there are none', async () => {
  await mount()
  expect(all('.dt-stat-label').map(label => label.textContent)).not.toContain(zh.flowDroppedLabel)
  server.dropped = [droppedReply('m2', 1, 'github', 'channelGone'), droppedReply('m1', 2, 'multica', 'channelGone'),
    droppedReply('m0', 30, 'github', 'channelGone')]
  await remount()
  const stat = all('.dt-stat').find(item => item.querySelector('.dt-stat-label')?.textContent === zh.flowDroppedLabel)!
  expect(stat.textContent).toBe(`${zh.flowDroppedLabel}2`)
  await click(stat.querySelector('button')!)
  expect(tab(zh['tab.source'])?.getAttribute('aria-selected')).toBe('true')
})

it('words a refused retry in the interface language by its code', async () => {
  await mount()
  server.person = server.person.map(ticket => ticket.id === 't4' ? { ...ticket, actions: { ...ticket.actions, retry: false } } : ticket)
  await click(find(zh.retry, '.dt-ticket-actions button'))
  await click(zh.confirm)
  expect(document.querySelector('.dt-toast')?.textContent).toBe(zh['userError.notRetryable'])
})

it('shows the overview in English', async () => {
  await mount(en)
  expect(all('.dt-section-title').map(title => title.textContent)).toEqual([`${en.pendingTickets}2`, `${en.problems}1`,
    `${en.lastDay}${en.details}`, `${en['term.executor']}${en.details}`])
  expect(all('.dt-cards .dt-ticket-stage').map(item => item.textContent)).toEqual([en['stage.answer'], en['stage.failed']])
})

it('says when no Source is on or an Eval is past its deadline, each with the way to it', async () => {
  for (const kind of ['github', 'multica']) server.settings.sources[kind] = { ...server.settings.sources[kind]!, enabled: false }
  server.threads = [threadSummary({ key: 'multica:issue-9', title: 'MUL-9 迟迟未决', stage: 'evaluating',
    evaluating: { id: 'v9', triggers: 1, strength: 'inferred', startedAt: '2026-08-31T23:58:00Z', deadline: '2026-09-01T00:00:00Z' } })]
  await mount()
  expect(all('.dt-cards .dt-att-what').map(item => item.textContent)).toEqual([zh.problemNoSource, '研判超时未决定：MUL-9 迟迟未决',
    '研判未完成：这个文案好奇怪'])
  await click(zh.actionOpenSources)
  expect(document.querySelector('.dt-settings-link[aria-current="page"]')?.textContent).toBe(zh['settingsGroup.source'])
  await click(tab(zh['tab.overview']))
  await click('研判超时未决定：MUL-9 迟迟未决')
  expect(server.requests.some(request => request.path.startsWith('threads/multica:issue-9'))).toBe(true)
})

// MUL-13210 Q4/Q5: outside auto mode a reply waits for its owner.
it('shows why a reply failed to send on its confirmation card', async () => {
  server.confirm = [{ ...pendingReply('m1', '收到'), error: 'feishu: no sender' }]
  await mount()
  expect(document.querySelector('.dt-reply .dt-reply-error')?.textContent).toContain('feishu: no sender')
})
it('lists the replies waiting for confirmation on the overview, and sends or withdraws each', async () => {
  server.confirm = [pendingReply('m1', '已修复，请验证'), pendingReply('m2', '这个需要产品确认')]
  await mount()
  expect(all('.dt-section-title').map(title => title.textContent)).toContain(`${zh.confirmReplies}2`)
  expect(all('.dt-reply-full .dt-md').map(item => item.textContent)).toEqual(['已修复，请验证', '这个需要产品确认'])
  await click(all('.dt-reply button').find(button => button.textContent === zh.confirmSend)!)
  expect(server.requests.find(request => request.path === 'outbox/m1/confirm')).toMatchObject({ method: 'POST', body: {} })
  expect(document.querySelector('.dt-toast')?.textContent).toBe(zh.confirmSent)
  expect(all('.dt-reply-full .dt-md').map(item => item.textContent)).toEqual(['这个需要产品确认'])
  await click(find(zh.confirmWithdraw, '.dt-reply button'))
  expect(server.requests.some(request => request.path === 'outbox/m2/withdraw')).toBe(false)
  await click(zh.confirm)
  expect(server.requests.find(request => request.path === 'outbox/m2/withdraw')).toMatchObject({ method: 'POST' })
  expect(all('.dt-section-title').map(title => title.textContent)).not.toContain(`${zh.confirmReplies}1`)
  expect(document.querySelector('.dt-reply')).toBeNull()
})

it('says the personal agent is off and turns it on from the overview', async () => {
  server.settings = { ...server.settings, enabled: false }
  await mount()
  expect(text()).toContain(zh.problemDisabled)
  await click(zh.actionEnable)
  expect(server.requests.find(request => request.path === 'enable')).toMatchObject({ method: 'POST', body: { enabled: true } })
  expect(text()).not.toContain(zh.problemDisabled)
})