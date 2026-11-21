// The Execution list and a Ticket's page answer the owner's questions in order: who asked, what for, who it
// went to, how it stands, and whether the owner has to act; the engine's internals stay folded under technical details.
import { expect, it } from 'vitest'
import { zh } from '../client/locales'
import { ticketMark } from '../client/marks'
import { planNote } from '../client/ticket'
import { translator } from '../client/translate'
import { withPanel, mount, all, click, field, find, text, type, at } from './panel'
import { server, pendingReply, ticketSummary } from './panel-server'

withPanel()
const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i
const t = translator(zh)

it('words each Ticket stage as what it means to the owner', () => {
  const label = (stage: Parameters<typeof ticketMark>[1]) => ticketMark(t, stage).label
  expect(label({ stage: 'working', ended: undefined })).toBe('处理中')
  expect(label({ stage: 'idle', ended: undefined })).toBe('已完成（看结果）')
  expect(label({ stage: 'person', action: 'answer', ended: undefined })).toBe('等你回答')
  expect(label({ stage: 'person', action: 'review', ended: undefined })).toBe('等你决定')
  expect(label({ stage: 'person', action: 'blocked', ended: undefined })).toBe('卡住了')
  expect(label({ stage: 'person', action: 'failed', ended: undefined })).toBe('失败了')
  expect(label({ stage: 'ended', ended: { at: '', by: '', outcome: 'stopped' } })).toBe('已停止')
  expect(label({ stage: 'ended', ended: { at: '', by: '', outcome: 'dropped' } })).toBe('已放弃')
})

it('answers who asked, what for, who it went to and how it stands on every Ticket card, with only the action it needs', async () => {
  server.tickets.open = [ticketSummary({ id: 't2', title: '选方案', note: { text: '用哪个方案？', error: false },
    reply: { source: 'github', url: 'https://github.com/x', label: { zh: 'GitHub', en: 'GitHub' } } }, { stage: 'person', action: 'answer' }),
  ticketSummary()]
  at({ page: 'execution' })
  await mount()
  const card = (title: string) => all('.dt-ticket').find(row => row.querySelector('.dt-open')?.textContent === title)!
  const facts = (title: string) => [...card(title).querySelectorAll('.dt-card-fact')].map(fact => fact.textContent)
  expect(facts('修复回归')).toEqual(['谁找的你Alice · GitHub', '要做什么修复登录页回归', '交给了谁前端小队员 · MUL-7', '现在怎样处理中 · working on it'])
  expect(card('修复回归').querySelector('.dt-ticket-actions')?.textContent).toBe('')
  expect(facts('选方案')[3]).toBe('现在怎样等你回答 · 用哪个方案？')
  expect(card('选方案').querySelector('.dt-ticket-actions a')?.textContent).toBe('去GitHub回答')
  expect(text()).not.toMatch(UUID)
})

it('shows a blocked ticket\'s question and reason on its card and sends the answer back as an annotation', async () => {
  server.tickets.open = [ticketSummary({ id: 't1', title: '排期', question: { text: '要排期哪个需求单？', reason: 'MUL-562 是咨询单，没有需求单。' } },
    { stage: 'person', action: 'answer' })]
  at({ page: 'execution' })
  await mount()
  const box = all('[data-testid="ticket-question"]')[0]!
  expect(box.textContent).toContain('要你回答：要排期哪个需求单？')
  expect(box.textContent).toContain('原因：MUL-562 是咨询单，没有需求单。')
  await click('去回答')
  await type(field<HTMLTextAreaElement>('去回答'), '排那张需求单')
  await click('发送批注')
  expect(server.requests.find(r => r.path.startsWith('/tickets/t1/annotate') || r.path.endsWith('tickets/t1/annotate'))?.body).toEqual({ text: '排那张需求单' })
})

it('folds what finished its turn under recently done, below what waits on the owner', async () => {
  server.tickets.open = [ticketSummary({ id: 't2', title: '待回答的' }, { stage: 'person', action: 'answer' }),
    ticketSummary({ id: 't3', title: '做完的' }, { stage: 'idle' })]
  at({ page: 'execution' })
  await mount()
  expect(all('.dt-open').map(item => item.textContent)).toEqual(['待回答的'])
  await click('最近完成 · 1')
  expect(all('.dt-open').map(item => item.textContent)).toEqual(['待回答的', '做完的'])
})

it('lays a Ticket out as the answers, the Router\'s judgement and its progress, with the technical details folded', async () => {
  server.ticketDetails.t1 = { ...server.ticketDetails.t1!, threads: [{ ...server.ticketDetails.t1!.threads[0]!,
    key: 'multica:0f8c7a52-1b2c-4d3e-8f90-0123456789ab', title: 'MUL-9 登录页回归' }] }
  at({ page: 'execution', id: 't1' })
  await mount()
  expect(all('.dt-card-fact').map(fact => fact.querySelector('.dt-card-label')?.textContent)).toEqual(['谁找的你', '要做什么', '交给了谁', '现在怎样'])
  expect(all('.dt-section-title .dt-section-name').map(name => name.textContent)).toEqual(['批注', '个人助理的判断', '进展时间线', '未送达的消息'])
  for (const expected of ['名单里的人明确请求，由个人助理接单', '先复现再修']) expect(text()).toContain(expected)
  expect(all('.dt-timeline > li .dt-timeline-text').map(item => item.textContent)).toEqual(['Alice 找你：修一下登录页',
    '个人助理研判：接单', '派给 前端小队员', '执行助手报告：working'])
  for (const hidden of ['abcdef12', '关注的对话', '怎么找你', 'multica:']) expect(text()).not.toContain(hidden)
  expect(text()).not.toMatch(UUID)
  await click('技术详情')
  for (const shown of ['abcdef12', '关注的对话', '怎么找你', 'MUL-9 登录页回归']) expect(text()).toContain(shown)
})

it('names a panel annotation as the owner\'s note, never by user id', async () => {
  server.ticketDetails.t1 = { ...server.ticketDetails.t1!, actor: { source: 'panel', id: 'f3a1c2d4-1111-4222-8333-944455556666',
    name: { zh: 'f3a1c2d4-1111-4222-8333-944455556666', en: 'x' } } }
  at({ page: 'execution', id: 't1' })
  await mount()
  expect(all('.dt-card-fact')[0]?.textContent).toBe('谁找的你你在面板写的批注')
  expect(text()).not.toMatch(UUID)
})

it('offers to answer on the issue when the question\'s channel has no link, and shows one chip per channel', async () => {
  const chip = { key: 'feishu:c1:a', source: 'feishu', title: 'x', facts: {} }
  server.tickets.open = [ticketSummary({ id: 't2', title: '问入口', threads: [chip, { ...chip, key: 'feishu:c1:b' }],
    reply: { source: 'feishu', url: '', label: { zh: '飞书', en: 'Feishu' } } }, { stage: 'person', action: 'answer' })]
  at({ page: 'execution' })
  await mount()
  expect([...document.querySelectorAll('.dt-ticket-actions a')].map(link => [link.textContent, link.getAttribute('href')])).toEqual([['去回答', '/acme/issues/MUL-7']])
  expect(all('.dt-chip').map(item => item.textContent)).toEqual(['飞书'])
})

it('names a channel once when its source and its label repeat each other', async () => {
  server.tickets.open = [ticketSummary({ actor: { source: 'feishu', id: 'c1', name: { zh: 'Multica 飞书', en: 'Multica' } } })]
  at({ page: 'execution' })
  await mount()
  expect(all('.dt-card-fact')[0]?.textContent).toBe('谁找的你Multica 飞书')
})
it('lays out each reply to confirm as whom it answers, where it goes, the full message and why, with no ids', async () => {
  const long = '已修复，请验证。\n\n第二段细节\n\n—— alice 的个人助理'
  server.confirm = [{ ...pendingReply('m1', long), channel: { source: 'multica', thread: 'multica:01a1ee7c-5751-74ac-9a8c-16645a9fa59a',
    address: { issueId: '01a1ee7c-5751-74ac-9a8c-16645a9fa59a' } } }]
  await mount()
  expect(text()).toContain('等你确认的回复')
  expect(text()).toContain('个人助理准备替你发出的消息，确认后才会发出。')
  const rows = all('.dt-reply .dt-card-fact').map(fact => fact.textContent)
  expect(rows.slice(0, 2)).toEqual(['回复谁Alice：这个文案好奇怪', '发到哪Multica · MUL-587 的评论下'])
  expect(rows[2]).toContain('—— alice 的个人助理')
  expect(rows[3]).toBe('为什么发来自工单：修复回归')
  expect(text()).not.toMatch(UUID)
  expect(all('.dt-reply button').map(button => button.textContent)).toEqual(expect.arrayContaining(['确认发送', '修改后发送', '不发了']))
  await click(find('修改后发送', '.dt-reply button'))
  await type(document.querySelector<HTMLTextAreaElement>('.dt-reply textarea')!, '改过的回复')
  await click(find('发送修改后的内容', '.dt-reply button'))
  expect(server.requests.find(request => request.path === 'outbox/m1/confirm')?.body).toEqual({ text: '改过的回复' })
})
const JARGON = /\b(Trigger|Thread|Eval|Ticket|Decision|disposition|strength|observe|auto|explicit|inferred|idle|person|Session|Router|Worker|ignoredFromCard)\b|触发|线程|扇出|执行器|主 Session|策略版本|点名方式|授权/
it('keeps every main page free of ids and engine words', async () => {
  for (const page of ['overview', 'source', 'trigger', 'eval', 'execution', 'plans', 'settings']) {
    at({ page })
    await mount()
    const shown = text()
    expect(shown, page).not.toMatch(UUID)
    expect(shown.match(JARGON)?.[0], page).toBeUndefined()
  }
})
it('says how the Router means to go about a Ticket in its own note, leaving the rule text of its brief to technical details', () => {
  expect(planNote('Routing note (not the source request): 小范围修正文案\nMatched rules; follow their procedure:\n- 修正补充 (fix): ...')).toBe('小范围修正文案')
  expect(planNote('Matched rules; follow their procedure:\n- fix')).toBe('')
  expect(planNote('先复现再修')).toBe('先复现再修')
})
it('puts a plan waiting for the owner on its ticket\'s card and page, with execute, edit and reject right there', async () => {
  at({ page: 'execution' })
  await mount()
  const card = all('.dt-ticket').find(row => row.querySelector('.dt-open')?.textContent === '修复回归')!
  expect(card.querySelector('.dt-plan-embedded')?.textContent).toContain('可以合并')
  expect([...card.querySelectorAll('.dt-plan-embedded button')].map(button => button.textContent)).toEqual(['去执行', '修改后执行', '驳回'])
  at({ page: 'execution', id: 't1' })
  await mount()
  await click(find('驳回', '.dt-plan-embedded button'))
  await type(document.querySelector<HTMLTextAreaElement>(`textarea[aria-label="驳回原因"]`)!, '先别合')
  await click(zh.confirm)
  expect(server.requests.find(request => request.path === 'plans/p1/reject')?.body).toEqual({ reason: '先别合' })
})