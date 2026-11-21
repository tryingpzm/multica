// The Execution status, a Ticket, and the Execution settings (SA tests/panel-execution.spec.tsx), with annotations on a
// Ticket and the Router and Worker agents.
import { expect, it } from 'vitest'
import { en, zh } from '../client/locales'
import { withPanel, mount, remount, all, find, tab, click, type, text, field, at } from './panel'
import { server, ticketSummary, threadSummary } from './panel-server'

withPanel()
it('lists Tickets under Execution by who they wait on, with the Executor state and its pause as an action', async () => {
  await mount()
  await click(zh['tab.execution'])
  expect(server.requests.some(request => request.path === 'tickets?state=open&limit=20')).toBe(true)
  expect(text()).toContain(zh.executorRunning)
  await click(zh.idleLabel)
  expect(server.requests.some(request => request.path === 'tickets?state=idle&limit=20')).toBe(true)
  await click(zh['ticket.ended'])
  expect(text()).toContain('旧工单')
  expect(text()).toContain(zh['outcome.stopped'])
  expect(all('.dt-status-line .dt-lv').map(item => item.textContent)).toEqual(['执行中 2', '排队 0'])
  await click(zh.pauseProcessing)
  await click(zh.confirm)
  expect(server.requests.find(request => request.path === 'executor/pause')).toMatchObject({ method: 'POST', body: { paused: true } })
  expect(text()).toContain(zh.processingPaused)
})

it('stops an open Ticket from its page after confirmation', async () => {
  await mount()
  await click('修复回归')
  await click(zh.stop)
  expect(server.requests.some(request => request.path.endsWith('/stop'))).toBe(false)
  expect(document.querySelector('[role="dialog"]')?.textContent).toContain('停止“修复回归”')
  await click(zh.confirm)
  expect(server.requests.some(request => request.path === 'tickets/t1/stop' && request.method === 'POST')).toBe(true)
})

it('shows a Ticket with its stage, the Trigger and Decision behind it, and its Threads, Channels and Multica issue under technical details', async () => {
  await mount()
  await click(all('.dt-open').find(button => button.textContent === '修复回归')!)
  expect(document.querySelector('.dt-page-title h2')?.textContent).toBe('修复回归')
  expect(all('.dt-section-title').map(title => title.querySelector('.dt-section-name')?.textContent)).toEqual(['批注', '个人助理的判断', '进展时间线',
    '未送达的消息'])
  expect(document.querySelector('.dt-status-line .dt-stage')?.textContent).toBe(zh['stage.working'])
  expect(all('.dt-status-line .dt-lv').map(item => item.textContent)).toEqual([expect.stringMatching(/^更新 /)])
  for (const expected of ['修复登录页回归', '名单里的人明确请求，由个人助理接单', '未送达：等待派单运行', 'Alice：顺便改标题', '先修登录页'])
    expect(text()).toContain(expected)
  await click(zh.techDetails)
  expect(text()).toContain('怎么找你@ 智能体')
  const links = () => all('.dt-disclosure-body a').map(link => [link.textContent, link.getAttribute('href')])
  expect(links()).toContainEqual(['MUL-42', '/acme/issues/MUL-42'])
  expect(links()).toContainEqual(['MUL-7', '/acme/issues/MUL-7'])
  // The worker's run is on the ticket's Multica issue.
  await click(find(zh.openSession, '.dt-page-head button'))
  expect(server.opened).toEqual(['issue-t1'])
  await click(document.querySelector<HTMLElement>('.dt-trigger-link')!)
  expect(tab(zh['tab.trigger']).getAttribute('aria-selected')).toBe('true')
})

it('annotates a Ticket from its page as an instruction on the Thread it was accepted from', async () => {
  at({ page: 'execution', id: 't1' })
  await mount()
  await type(document.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${zh.annotate}"]`)!, '改完先别合并')
  await click(zh.annotateSubmit)
  expect(server.requests.find(request => request.path === 'tickets/t1/annotate')).toEqual({ path: 'tickets/t1/annotate', method: 'POST',
    body: { text: '改完先别合并' } })
  expect(server.requests.filter(request => request.path === 'tickets/t1').length).toBeGreaterThan(1)
})

it('marks a stuck Ticket on its page with the Watchdog finding', async () => {
  server.ticketDetails.t1 = { ...server.ticketDetails.t1!, stuck: { kind: 'no_run', since: '2026-09-22T00:00:00Z' } }
  await mount()
  await click(all('.dt-open').find(button => button.textContent === '修复回归')!)
  expect(document.querySelector('.dt-status-line .dt-tag')?.textContent).toBe('卡住')
  expect(document.querySelector('.dt-status-line .dt-tag')?.getAttribute('title')).toBe('派单后一直没有运行')
})

it('reports a missing Ticket instead of rendering an empty page', async () => {
  at({ page: 'execution', id: 'missing' })
  await mount()
  expect(document.querySelector('.dt-hint')?.textContent).toBe(zh.ticketNotFound)
})

it('says why an Input was not delivered on its Ticket\'s page, in either language', async () => {
  const t1 = server.ticketDetails.t1!
  server.ticketDetails.t1 = { ...t1, stage: 'person', action: 'delivery', deliveryError: 'github: message deleted' }
  at({ page: 'execution', id: 't1' })
  await mount()
  expect(document.querySelector('.dt-status-line .dt-stage')?.textContent).toBe(zh['stage.delivery'])
  expect(document.querySelector('.dt-banner.dt-danger')?.textContent).toBe(`${zh.deliveryFailedPrefix}github: message deleted`)
  expect(text()).toContain(zh.inputRetryHint)
  await remount(en)
  expect(document.querySelector('.dt-status-line .dt-stage')?.textContent).toBe(en['stage.delivery'])
  expect(text()).toContain(en.inputRetryHint)
})

it('tells a Ticket or Thread that does not exist from a server that is not answering', async () => {
  at({ page: 'execution', id: 't9' })
  await mount()
  expect(text()).toContain(zh.ticketNotFound)
  at({ page: 'execution', id: 't1' })
  server.down = true
  await remount()
  expect(text()).not.toContain(zh.ticketNotFound)
  expect(text()).toContain(zh.hostNotRunning)
})

it('gives a Ticket a bot\'s message started its basis, marks a Thread it follows for context, and an Input it handed back', async () => {
  const t1 = server.ticketDetails.t1!
  server.ticketDetails.t1 = { ...t1, mandate: { ...t1.mandate, strength: 'inferred', basis: { via: 'bot', sender: '评审机器人',
    observation: { mode: 'auto' } } },
  basis: { facts: [{ name: 'sender', value: '评审机器人' }, { name: 'bot' }, { name: 'mode', value: 'auto' }], legacy: false },
  threads: [threadSummary(), { ...threadSummary({ key: 'multica:issue-7', title: 'MUL-7 相关 issue' }), context: true }],
  inputs: [{ id: 'i1', kind: 'trigger', triggerId: 'e2', at: '2026-09-22T00:03:00Z', delivered: true, trigger: { id: 'e2',
    actor: { source: 'github', id: 'alice', name: { zh: 'Alice', en: 'Alice' } }, human: true, preview: '顺便加一个页面',
    thread: 'github:g1:m_root', handback: { reason: 'Asks for a new page', at: '2026-09-22T00:05:00Z' } } }] }
  at({ page: 'execution', id: 't1' })
  await mount()
  await click(zh.techDetails)
  const facts = () => all('.dt-kv > div').map(row => [row.querySelector('dt')?.textContent, row.querySelector('dd')?.textContent])
  expect(facts()).toEqual(expect.arrayContaining([[zh['basis.sender'], '评审机器人'], [zh['basis.bot'], zh.yes], [zh['basis.mode'], zh['mode.auto']]]))
  expect(all('.dt-list .dt-ticket').map(row => row.querySelector('.dt-tag')?.textContent)).toEqual([undefined, zh.contextOnly])
  expect(text()).toContain('已交回研判：Asks for a new page')
})

it('words an ended Ticket by its outcome, on its row and its page', async () => {
  const fields = { id: 't3', title: '已合并的工单', state: 'ended' as const, actions: { retry: false, stop: false },
    ended: { at: '2026-09-21T00:00:00Z', by: 'executor', outcome: 'merged' as const } }
  server.tickets.ended = [ticketSummary(fields, { stage: 'ended' }), ...server.tickets.ended]
  server.ticketDetails.t3 = { ...server.ticketDetails.t1!, ...fields, stage: 'ended' } as never
  await mount()
  await click(zh['tab.execution'])
  await click(zh['ticket.ended'])
  expect(all('.dt-ticket-stage').map(stage => stage.textContent)).toEqual([zh['outcome.merged'], zh['outcome.stopped']])
  await click('已合并的工单')
  expect(document.querySelector('.dt-status-line .dt-stage')?.textContent).toBe(zh['outcome.merged'])
})

it('saves the Router agent on the Execution settings against the settings revision, with no Worker agent to choose', async () => {
  await mount()
  await click(zh['tab.execution'])
  await click(find(zh['tab.settings'], '.dt-status-head button'))
  expect(document.querySelector('.dt-settings-link[aria-current="page"]')?.textContent).toBe(zh['settingsGroup.execution'])
  expect(text()).not.toContain('Worker 智能体')
  expect(all('.dt-block select').length).toBe(1)
  const router = field<HTMLSelectElement>(zh.routerAgent)
  expect(router.value).toBe('agent-router')
  await type(router, 'agent-other')
  await click(zh.save)
  expect(server.requests.find(request => request.path === 'settings/executor')?.body).toEqual({ value: { routerAgentId: 'agent-other' },
    revision: 3 })
  expect(server.settings.executor).toEqual({ routerAgentId: 'agent-other' })
})

it('links the owner\'s own Router copy to its own instructions, and any other Router agent to its page', async () => {
  at({ page: 'settings', query: { group: 'execution' } })
  await mount()
  const link = () => document.querySelector<HTMLAnchorElement>('.dt-block a.dt-router-link')
  expect([link()?.textContent, link()?.getAttribute('href')]).toEqual([zh.routerAppend, '/acme/agents/agent-router'])
  await type(field<HTMLSelectElement>(zh.routerAgent), 'agent-other')
  expect([link()?.textContent, link()?.getAttribute('href')]).toEqual([zh.routerOpen, '/acme/agents/agent-other'])
  await type(field<HTMLSelectElement>(zh.routerAgent), '')
  expect(link()).toBeNull()
})

it('shows the saved Router agent even when the agent list cannot be read', async () => {
  server.agents = null
  at({ page: 'settings', query: { group: 'execution' } })
  await mount()
  const router = field<HTMLSelectElement>(zh.routerAgent)
  expect(router.value).toBe('agent-router')
  expect(router.selectedOptions[0]?.textContent).not.toBe(zh.agentUnset)
})

it('says when the Router agent has no runtime to run on', async () => {
  at({ page: 'settings', query: { group: 'execution' } })
  await mount()
  expect(text()).not.toContain(zh.routerNoRuntime)
  server.agents = server.agents!.map(agent => agent.id === 'agent-router' ? { ...agent, runtime: '' } : agent)
  await remount()
  expect(text()).toContain(zh.routerNoRuntime)
})

it('asks for the Router agent while it is unset, and lists it under the overview problems', async () => {
  server.settings.executor = { routerAgentId: '' }
  await mount()
  expect(text()).toContain(zh.problemNoExecutor)
  await click(zh.actionChooseExecutor)
  expect(text()).toContain(zh.chooseExecutor)
})
it('links the owner\'s executor to its own instructions in Execution settings', async () => {
  at({ page: 'settings', query: { group: 'execution' } })
  await mount()
  const link = document.querySelector<HTMLAnchorElement>('.dt-block a.dt-executor-link')
  expect([link?.textContent, link?.getAttribute('href')]).toEqual([zh.routerAppend, '/acme/agents/agent-executor'])
})