// The Trigger status and a Thread (SA tests/panel-trigger.spec.tsx), with annotations on a Thread.
import { expect, it } from 'vitest'
import { zh } from '../client/locales'
import { withPanel, mount, all, find, click, type, text } from './panel'
import { server, trigger, threadSummary } from './panel-server'
import type { DecisionView } from '../app/api'

withPanel()
it('walks a Thread timeline newest first with dispositions as tags and its Decisions, requeues a filtered Trigger, pages backwards and returns', async () => {
  await mount()
  await click(zh['tab.trigger'])
  expect(document.querySelector('.dt-ticket .dt-stage')?.textContent).toBe(zh.threadSubscribed)
  expect(text()).toContain('同步给 修复回归')
  await click('这个文案好奇怪')
  expect(document.querySelector('.dt-page-head .dt-btn')?.textContent).toBe(zh['term.trigger'])
  expect(text()).toContain('已过滤：发送者不在这个来源的名单里')
  expect(text()).toContain(zh['term.correlation'])
  expect(all('.dt-event .dt-event-text').map(element => element.textContent)).toEqual(['message e4', 'message e3'])
  await click(zh.requeue)
  expect(server.requests.some(request => request.path === 'triggers/e4/requeue')).toBe(true)
  expect(text()).toContain(zh.dispositionQueued)
  await click(zh.loadEarlier)
  expect(all('.dt-event .dt-event-text').map(element => element.textContent)).toEqual(['message e4', 'message e3', 'message e1'])
  await click(document.querySelector<HTMLElement>('.dt-page-head .dt-btn')!)
  expect(find(zh['threadFilter.subscribed'], '.dt-pill').getAttribute('aria-pressed')).toBe('true')
})

it('offers to send a person\'s message that reached a ticket to Eval, for one its worker neither did nor handed back', async () => {
  await mount()
  await click(zh['tab.trigger'])
  await click('这个文案好奇怪')
  const event = all('.dt-event').find(element => element.textContent?.includes('message e3'))!
  await click([...event.querySelectorAll<HTMLElement>('button')].find(button => button.textContent === zh.requeue)!)
  expect(server.requests.some(request => request.path === 'triggers/e3/requeue' && request.method === 'POST')).toBe(true)
})

it('filters the Thread list by activity, and unsubscribes from a Thread page after confirmation', async () => {
  await mount()
  await click(zh['tab.trigger'])
  await click(zh['threadFilter.recent'])
  expect(server.requests.some(request => request.path === 'threads?filter=recent&limit=20')).toBe(true)
  await click('这个文案好奇怪')
  await click(zh.unsubscribe)
  await click(zh.confirm)
  expect(server.requests.some(request => request.path === 'threads/github:g1:m_root/unsubscribe')).toBe(true)
})

it('folds consecutive automated messages from one sender on a Thread, showing the newest with its text', async () => {
  const bot = (id: string, at: string) => trigger(id, at, { actor: { source: 'multica', id: '' }, human: false,
    text: `run of MUL-42: ${id === 'b3' ? 0 : 1} running.`, preview: `run ${id}` })
  server.threadPages = () => ({ ...threadSummary(), tickets: [], correlated: [], decisions: [], earlier: null, context: [],
    timeline: [bot('b1', '2026-09-23T00:00:00Z'), bot('b2', '2026-09-23T00:01:00Z'), trigger('h1', '2026-09-23T00:02:00Z'), bot('b3',
      '2026-09-23T00:03:00Z'), bot('b4', '2026-09-23T00:04:00Z')] })
  await mount()
  await click(zh['tab.trigger'])
  await click('这个文案好奇怪')
  expect(all('.dt-event .dt-event-text').map(element => element.textContent)).toEqual(['run of MUL-42: 1 running.', 'message h1',
    'run of MUL-42: 1 running.'])
  await click(find('同一发送者之前还有 1 条自动消息'))
  expect(text()).toContain('run b3')
})

// MUL-13210 Q4/Q5: an annotation is an instruction, which enters Eval as the owner's explicit request.
it('annotates a Thread from its page, sending the text once and clearing the box', async () => {
  await mount()
  await click(zh['tab.trigger'])
  await click('这个文案好奇怪')
  expect(text()).toContain(zh.annotateHint)
  const box = document.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${zh.annotate}"]`)!
  expect(find(zh.annotateSubmit).hasAttribute('disabled')).toBe(true)
  await type(box, '  这个按产品文档改  ')
  await click(zh.annotateSubmit)
  expect(server.requests.filter(request => request.path.endsWith('/annotate'))).toEqual([{ path: 'threads/github:g1:m_root/annotate',
    method: 'POST', body: { text: '这个按产品文档改' } }])
  expect(document.querySelector('.dt-toast')?.textContent).toBe(zh.annotateSent)
  expect(document.querySelector<HTMLTextAreaElement>(`textarea[aria-label="${zh.annotate}"]`)!.value).toBe('')
})
it('opens a channel Thread whose key carries "/" and "+" and shows its messages', async () => {
  const key = 'feishu:CiDFMOld+3pMuk17RAB:CiDTMZtm1v/3vsZl'
  const group = threadSummary().key
  const pages = server.threadPages
  server.threads = [threadSummary({ key, source: 'feishu', title: '频道里的帖子' })]
  server.threadPages = (asked, query) => asked === key ? pages(group, query) : undefined
  await mount()
  await click(zh['tab.trigger'])
  await click('频道里的帖子')
  expect(all('.dt-event .dt-event-text').map(element => element.textContent)).toEqual(['message e4', 'message e3'])
})
it('styles a Decision\'s Trigger as a link only when it opens a Ticket, and shows @{name} as @name', async () => {
  const decision = (id: string, ticketId: string | undefined): DecisionView => ({
    id, kind: 'accept', outcome: 'accept', reason: [{ code: 'explicitRouted' }], at: '2026-11-17T09:00:00Z', strength: 'explicit', triggerIds: ['e4'],
    thread: 'github:g1:m_root', rules: [], preview: '@{bob} 看下首页改版', ticketId, ticketTitle: ticketId ? '工单' : undefined })
  server.threadPages = () => ({ ...threadSummary(), tickets: [], correlated: [], earlier: null, context: [], timeline: [trigger('e4', '2026-11-17T08:00:00Z')],
    decisions: [decision('d2', 'k1'), decision('d1', undefined)] })
  await mount()
  await click(zh['tab.trigger'])
  await click('这个文案好奇怪')
  const [linked, plain] = all('.dt-thread-decision').map(row => row.querySelector<HTMLElement>('.dt-decision-trigger')!)
  expect(linked!.tagName).toBe('BUTTON')
  expect(linked!.classList.contains('dt-textlink')).toBe(true)
  expect(plain!.tagName).not.toBe('BUTTON')
  expect(plain!.classList.contains('dt-textlink')).toBe(false)
  expect(plain!.textContent).toBe('@bob 看下首页改版')
})
it('lists every Decision on a Thread with its outcome, reason, time and Trigger, each expanding to its rule assessments', async () => {
  const decision = (id: string, kind: DecisionView['kind'], reason: DecisionView['reason'], rules: DecisionView['rules']): DecisionView => ({
    id, kind, outcome: kind, reason, at: '2026-11-17T09:00:00Z', strength: 'explicit', triggerIds: ['e4'], thread: 'github:g1:m_root',
    rules, preview: 'message e4', ticketId: undefined, ticketTitle: undefined })
  server.threadPages = () => ({ ...threadSummary(), tickets: [], correlated: [], earlier: null, context: [], timeline: [trigger('e4', '2026-11-17T08:00:00Z')],
    decisions: [
      decision('d3', 'accept', [{ code: 'explicitRouted' }], [{ id: 'status', name: '状态询问', matched: true, reason: '在问需求是否发布正式' }]),
      decision('d2', 'ignore', [{ code: 'said', values: { text: '没有命中规则' } }], [{ id: 'copy', name: '文案修正', matched: false, reason: '不是改文案' }]),
      decision('d1', 'unavailable', [{ code: 'timeout' }], []),
    ] })
  await mount()
  await click(zh['tab.trigger'])
  await click('这个文案好奇怪')
  const rows = all('.dt-thread-decision')
  expect(rows.map(row => row.querySelector('.dt-decision-reason')?.textContent)).toEqual([zh['reason.explicitRouted'], '没有命中规则', zh['reason.timeout']])
  expect(rows.every(row => row.querySelector('.dt-decision-trigger')?.textContent === 'message e4')).toBe(true)
  await click(rows[0]!.querySelector<HTMLElement>('[data-disclosure-row]')!)
  expect(text()).toContain('状态询问')
  expect(text()).toContain('在问需求是否发布正式')
})