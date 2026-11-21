/**
 * Typed fake of the personal agent panel API (SA tests/fixtures/panel-server.ts, on Multica's Sources). Implements every
 * route of app/api.ts and records an undeclared query parameter or a body mismatch in `server.misuse`, which
 * `withPanelServer` asserts is empty after each test.
 */
import { afterEach, beforeEach, expect, vi } from 'vitest'
import type { AgentOption, Api, ApiResponse, RouteParams } from '../app/api'
import type { DroppedReply, PendingReply, Problem, SettingsView, SourceStatusRow, StateView, ThreadPageView, ThreadRow, TicketPageView,
  TicketRow, TicketStage, TriggerRow } from '../client/api'
import { API_BASE } from '../client/api'

export const MEMBER = 'alice'
const ALLOWED: Record<string, { id: string; name?: string }[]> = { 'github': [{ id: MEMBER, name: 'Alice' }],
  multica: [{ id: 'u-alice', name: 'Alice' }] }
type Actor = TriggerRow['actor']
export function actor(source: string, id: string): Actor {
  if (!id) return { source, id }
  const entry = ALLOWED[source]?.find(item => item.id === id)
  const name = entry ? entry.name || entry.id : id
  return { source, id, name: { zh: name, en: name } }
}
const GROUP = 'github:g1:m_root'
type TicketFields = Omit<TicketRow, 'stage' | 'action'>
const TICKET = {
  id: 't1', title: '修复回归', state: 'open', actions: { retry: false, stop: true }, strength: 'explicit', ended: undefined,
  attention: undefined, assignments: 1, inputs: 2, undelivered: 0, deliveryError: undefined, sessionId: 'issue-t1', directory: undefined,
  summary: 'working on it', createdAt: '2026-09-22T00:00:00Z', updatedAt: '2026-09-23T00:00:00Z',
  actor: actor('github', MEMBER), scope: '修复登录页回归', executor: { type: 'agent', id: 'agent-w', name: '前端小队员' },
  issue: { id: 'issue-t1', identifier: 'MUL-7', title: '登录页回归', url: '/acme/issues/MUL-7' },
  threads: [{ key: 'multica:issue-42', source: 'multica', title: 'MUL-42 修复登录', url: '/acme/issues/MUL-42',
    facts: { number: { value: 'MUL-42' } } }],
} satisfies TicketFields
export const ticketSummary = (fields: Partial<TicketFields> = {}, stage: TicketStage = { stage: 'working' }): TicketRow =>
  ({ ...TICKET, ...fields, ...stage }) as TicketRow
export const threadSummary = (fields: Partial<ThreadRow> = {}): ThreadRow => ({
  key: GROUP, source: 'github', title: '这个文案好奇怪', stage: 'subscribed', latest: undefined, lastAt: '2026-09-23T00:00:00Z',
  createdAt: '2026-09-22T00:00:00Z', subscriptions: 1, tickets: [{ id: 't1', title: '修复回归', state: 'open' }], evaluating: undefined,
  pending: 0, pollError: undefined, url: undefined, facts: { chat: { value: '团队群' } }, ...fields,
})
type TriggerFields = Partial<Omit<TriggerRow, 'disposition' | 'actions'>> & { disposition?: Partial<TriggerRow['disposition']> }
export function trigger(id: string, at: string, { disposition, ...fields }: TriggerFields = {}): TriggerRow {
  const row = { id, source: 'github', actor: actor('github', MEMBER), human: true, at, text: `message ${id}`,
    preview: `message ${id}`, url: undefined, threads: [GROUP], ...fields,
    disposition: { queued: false, evaluating: false, delivered: false, filtered: undefined, fanout: [], handbacks: [], decision: undefined,
      ...disposition } }
  return { ...row, actions: { requeue: requeueable(row) } }
}
const requeueable = ({ human, disposition: d }: Pick<TriggerRow, 'human' | 'disposition'>) => human && !d.queued && !d.evaluating
  && (!!d.filtered || d.decision?.outcome === 'unavailable' || d.delivered || (d.fanout.length > 0 && !d.decision))
const groupChannel = { source: 'github', thread: GROUP, address: { chatId: 'g1', msgId: 'm_root' } }
export const droppedReply = (id: string, hoursAgo: number, source: 'github' | 'multica', reason: string): DroppedReply => ({
  id, preview: `Reply ${id}`, kind: 'reply',
  channel: source === 'github' ? groupChannel : { source: 'multica', thread: 'multica:issue-42', address: { issueId: 'issue-42' } },
  dropped: { at: new Date(Date.now() - hoursAgo * 3_600_000).toISOString(), reason },
  thread: source === 'github' ? GROUP : 'multica:issue-42',
})
export const pendingReply = (id: string, text: string): PendingReply => ({ id, kind: 'reply', channel: groupChannel, text,
  at: '2026-09-23T00:00:00Z', link: { source: 'github', url: '', label: { zh: 'GitHub', en: 'GitHub' } },
  ticket: { id: 't1', title: '修复回归' }, thread: GROUP,
  to: { actor: actor('github', MEMBER), preview: '这个文案好奇怪' },
  where: { source: 'multica', place: 'MUL-587', url: '/acme/issues/MUL-587', comment: true } })
const dayAgo = () => new Date(Date.now() - 86_400_000).toISOString()

type Settings = { paused: boolean; enabled: boolean } & { [B in keyof SettingsView['blocks']]: SettingsView['blocks'][B]['value'] }
  & { sources: Record<string, { enabled: boolean; config: Record<string, unknown> }> }
export interface PanelServer {
  settings: Settings; revision: number
  /** The owner's id on each Source's allowed list. */
  self?: Record<string, string>
  tickets: { open: TicketRow[]; ended: TicketRow[] }; ticketDetails: Record<string, TicketPageView>
  threads: ThreadRow[]
  threadPages: (key: string, query: { before?: string; around?: string }) => ThreadPageView | undefined
  person: TicketRow[]; confirm: PendingReply[]
  search: Pick<ApiResponse<'GET search'>, 'items' | 'threads'>
  requests: { path: string; method: string; body?: unknown }[]
  misuse: string[]
  queued: Set<string>
  down: boolean
  pending: number
  opened: string[]
  dropped: DroppedReply[]
  /** The workspace's agents; null when they cannot be listed. */
  agents: AgentOption[] | null
}
export let server: PanelServer

const people = (zh: string, en: string) => {
  const columns = [{ key: 'id', type: 'text', label: { zh, en } }, { key: 'name', type: 'text', optional: true, label: { zh: '名字', en: 'Name' } }]
  return [{ key: 'allowed', type: 'rows', label: { zh: '能发起工作的人', en: 'People who may originate work' }, columns },
    { key: 'bots', type: 'rows', label: { zh: '机器人名单', en: 'Bots' }, columns }]
}
const KINDS = [
  { id: 'github', title: { zh: 'GitHub', en: 'GitHubs' }, health: ['github'],
    fields: [{ key: 'rows', type: 'rows', label: { zh: '接收的群', en: 'Groups' }, columns: [{ key: 'row', type: 'text', label: { zh: '群',
      en: 'Group' } }, { key: 'listen', type: 'select', label: { zh: '接收', en: 'Receive' }, options: [{ value: 'mention',
      label: { zh: '仅 @', en: '@ only' } }, { value: 'all', label: { zh: '全部消息', en: 'All' } }] }] },
    { key: 'userId', type: 'text', label: { zh: '你的 GitHub 用户名', en: 'Your GitHub username' } }],
    explicit: { zh: '任何人在群里 @ 你，都是明确的请求。', en: 'Anyone who mentions you in a group makes an explicit request.' } },
  { id: 'multica', title: { zh: 'Multica', en: 'Multica' }, health: ['multica'], fields: [],
    explicit: { zh: '任何人在 issue 评论里 @ 你，都是明确的请求。', en: 'Anyone who mentions you on an issue makes an explicit request.' } },
] as const
function sourceSettings(): SettingsView['sources'] {
  return KINDS.map(kind => {
    const value = server.settings.sources[kind.id] ?? { enabled: true, config: {} }
    const rows = Array.isArray(value.config.rows) ? value.config.rows as { row: string; listen: string }[] : []
    return { id: kind.id, kind: kind.id, title: kind.title, description: { zh: '', en: '' }, installed: true, value, revision: server.revision,
      restarts: [], fields: kind.fields as unknown as SettingsView['sources'][number]['fields'], facts: {}, labels: { g1: '团队群' }, self: server.self?.[kind.id],
      policy: { fields: (kind.id === 'github' ? people('GitHub 用户名', 'GitHub username') : people('Multica 用户 id', 'Multica user id')) as never,
        explicit: kind.explicit, ...kind.id === 'github' ? { rows: [...rows.map(row => ({ id: row.row, label: row.row,
        note: { zh: row.listen === 'all' ? '全部消息' : '仅 @', en: row.listen }, cells: { listedUnaddressed: row.listen === 'all',
          unlistedAddressed: true, unlistedUnaddressed: row.listen === 'all' } })), { id: 'other', label: '其他群', note: { zh: '仅 @', en: '@ only' },
        cells: { listedUnaddressed: false, unlistedAddressed: true, unlistedUnaddressed: false } }] } : {} } }
  })
}
function sourceStatuses(): SourceStatusRow[] {
  return KINDS.map(kind => {
    const enabled = server.settings.sources[kind.id]?.enabled ?? true
    const group = kind.id === 'github'
    return { id: kind.id, title: kind.title, installed: true, enabled, state: enabled ? 'ready' : 'off',
      health: kind.health.map(name => ({ name, state: 'ready' as const, detail: '' })),
      stats: { received: group ? 12 : 0, filtered: group ? 2 : 0, lastAt: '2026-09-23T00:00:00Z' },
      filtered: group ? [] : [{ id: 'e9', at: '2026-09-23T00:02:00Z', actor: actor('multica', 'someone'), preview: 'looks wrong',
        reason: { code: 'source_disabled' } }],
      dropped: server.dropped.filter(item => item.channel.source === kind.id && item.dropped.at >= dayAgo()) }
  })
}
const stateView = (): StateView => ({ setup: { problem: null, feishuProblem: null, accounts: null },
  load: { running: 2, queued: 0, maxWorkers: 0 }, masterId: 'router-chat', paused: server.settings.paused, enabled: server.settings.enabled,
  health: {} })
const settingsView = (): SettingsView => ({ policyVersion: 'abcdef1234567890',
  blocks: { policy: { value: server.settings.policy, revision: server.revision }, eval: { value: server.settings.eval, revision: server.revision },
    executor: { value: server.settings.executor, revision: server.revision } }, sources: sourceSettings() })
const overviewView = (): ApiResponse<'GET overview'> => ({
  problems: [
    ...KINDS.some(kind => server.settings.sources[kind.id]?.enabled !== false) ? [] : [{ kind: 'no-source' as const }],
    ...server.threads.filter(thread => thread.evaluating && thread.evaluating.deadline < new Date().toISOString())
      .map(thread => ({ kind: 'eval-overdue' as const, thread })),
    ...[...server.tickets.open, ...server.person].filter(ticket => ticket.stuck).map(ticket => ({ kind: 'stuck' as const, ticket })),
    { kind: 'eval-unavailable' as const, decisionId: 'u1', thread: threadSummary(), reason: [{ code: 'timeout' }], triggerId: 'e3' },
    ...server.settings.paused ? [{ kind: 'executor-paused' as const }] : [],
    ...server.settings.executor.routerAgentId ? [] : [{ kind: 'no-executor' as const }],
  ] satisfies Problem[],
  person: server.person, confirm: server.confirm, executor: { running: 2, queued: 0, maxWorkers: 0, idle: 1 },
  flow: { received: 12, filtered: 2, triggers: 10, judged: 5, accepted: 1, evaluating: 0,
    dropped: server.dropped.filter(item => item.dropped.at >= dayAgo()).length },
  policyVersion: 'abcdef1234567890',
})
export const acceptDecision = (): NonNullable<TicketPageView['decision']> => ({ id: 'd1', kind: 'accept', outcome: 'accept',
  reason: [{ code: 'explicitRouted' }], at: '2026-09-22T00:00:00Z', strength: 'explicit', ticketId: undefined,
  triggerIds: ['e1'], thread: GROUP, rules: [], preview: '@alice 修一下', ticketTitle: undefined })
const ticketDetail = (): TicketPageView => ({
  ...ticketSummary(), paused: false, decision: acceptDecision(), threads: [threadSummary()],
  mandate: { strength: 'explicit', policyVersion: 'abcdef1234567890', basis: { via: 'addressed', sender: 'Alice', addressed: 'mention' },
    actor: actor('github', MEMBER), scope: '修复登录页回归' },
  basis: { facts: [{ name: 'sender', value: 'Alice' }, { name: 'addressed', value: 'mention' }], legacy: false },
  channels: [{ source: 'github', url: '', label: { zh: 'GitHub', en: 'GitHub' } },
    { source: 'multica', url: '/acme/issues/MUL-42', label: { zh: 'MUL-42', en: 'MUL-42' } }],
  assignments: [{ id: 'a1', state: 'running', createdAt: '2026-09-22T00:00:00Z', startedAt: '2026-09-22T00:01:00Z', ref: 'issue-t1',
    report: { state: 'progress', summary: 'working', at: '2026-09-22T00:02:00Z' } }],
  inputs: [{ id: 'i1', kind: 'trigger', triggerId: 'e2', at: '2026-09-22T00:03:00Z', delivered: false,
    trigger: { id: 'e2', actor: actor('github', MEMBER), human: true, preview: '顺便改标题', thread: GROUP } },
  { id: 'i2', kind: 'policy-changed', at: '2026-09-22T00:04:00Z', delivered: true, from: 'aaaaaaaaaaaaaaaa', to: 'bbbbbbbbbbbbbbbb' }],
  annotations: [{ id: 'n1', text: '先修登录页', at: '2026-09-22T00:05:00Z', triggerId: 'panel:n1' }],
  plan: '先复现再修',
  timeline: [{ kind: 'trigger', at: '2026-09-22T00:00:00Z', actor: actor('github', MEMBER), text: '修一下登录页', detail: '修一下登录页' },
    { kind: 'decision', at: '2026-09-22T00:00:10Z', outcome: 'accept', reason: [] },
    { kind: 'assign', at: '2026-09-22T00:00:20Z', executor: '前端小队员' },
    { kind: 'report', at: '2026-09-22T00:02:00Z', state: 'progress', text: 'working', detail: 'working' }],
} as TicketPageView)
function threadPage(key: string, { before, around }: { before?: string; around?: string }): ThreadPageView | undefined {
  if (key !== GROUP) return undefined
  const base = { ...threadSummary(), tickets: [ticketSummary()], correlated: [{ key: 'multica:issue-42', title: 'MUL-42' }], decisions: [],
    url: '', context: [] }
  const decision = { id: 'd1', outcome: 'accept' as const, reason: [{ code: 'said', values: { text: 'x' } }], ticketId: 't1' }
  if (before === 'cursor-1' || around === 'e1') return { ...base, timeline: [trigger('e1', '2026-09-20T00:00:00Z', { disposition: { decision } })],
    earlier: null }
  return { ...base, earlier: 'cursor-1', timeline: [trigger('e3', '2026-09-23T00:00:00Z', { disposition: { fanout: ['t1'] } }),
    trigger('e4', '2026-09-23T00:01:00Z', { disposition: { filtered: { code: 'senderNotMember' } } })] }
}
function initial(): PanelServer {
  const ended = ticketSummary({ id: 't0', title: '旧工单', state: 'ended', actions: { retry: false, stop: false },
    ended: { at: '2026-09-21T00:00:00Z', by: 'human', reason: 'Human asked to stop', outcome: 'stopped' } }, { stage: 'ended' })
  const reply = { source: 'github', url: 'https://github.com/', label: { zh: 'GitHub', en: 'GitHub' } }
  return {
    agents: [{ id: 'agent-router', name: 'Router 助理', runtime: 'Mac', personal: true, url: '/acme/agents/agent-router' },
      { id: 'agent-worker', name: '开发助手', runtime: 'Mac', url: '/acme/agents/agent-worker' },
      { id: 'agent-executor', name: '我的执行助手', runtime: 'Mac', personal: true, executor: true, url: '/acme/agents/agent-executor' },
      { id: 'agent-other', name: '另一个智能体', runtime: '', url: '/acme/agents/agent-other' }, { type: 'squad', id: 'squad-bugfix', name: 'bug 维修小队', runtime: '' }],
    down: false, pending: 0, opened: [], requests: [], misuse: [], queued: new Set(), dropped: [], confirm: [],
    settings: { paused: false, enabled: true,
      policy: { rules: [{ id: 'copy', name: '文案修改', enabled: true, instruction: '修正明确的文案错误' }],
        sources: { 'github': { allowed: [{ id: MEMBER, name: 'Alice' }], admission: {} }, multica: { allowed: [{ id: 'u-alice', name: 'Alice' }],
          admission: {} } } },
      eval: { mode: 'observe', timeoutSeconds: 120, model: null },
      executor: { routerAgentId: 'agent-router' },
      sources: { 'github': { enabled: true, config: { rows: [{ row: 'g1', listen: 'mention' }], userId: 'alice' } },
        multica: { enabled: true, config: {} } } },
    revision: 3,
    tickets: { open: [ticketSummary()], ended: [ended] }, ticketDetails: { t1: ticketDetail() },
    threads: [threadSummary()], threadPages: threadPage,
    person: [ticketSummary({ attention: { id: 'q1', question: '需要确认按钮颜色' }, note: { text: '需要确认按钮颜色', error: false }, reply },
      { stage: 'person', action: 'answer' }),
      ticketSummary({ id: 't4', title: '截图工具', actions: { retry: true, stop: true }, sessionId: 'issue-t4',
        summary: 'LlmError: no adapter registered for provider "provider-a"',
        note: { text: 'LlmError: no adapter registered for provider "provider-a"', error: true } }, { stage: 'person', action: 'failed' })],
    search: { items: [], threads: [] },
  }
}

class Refusal { constructor(readonly status: 400 | 404 | 409 | 500, readonly body: Record<string, unknown>) {} }
const refuse = (status: Refusal['status'], error: string, more: Record<string, unknown> = {}) => new Refusal(status, { error, ...more })
type Query<K extends keyof Api> = Api[K] extends { query?: infer Q } ? keyof NonNullable<Q> & string : never
type Body<K extends keyof Api> = Api[K] extends { body: infer B } ? B : undefined
interface Request<K extends keyof Api> { params: RouteParams<K>; query: Partial<Record<Query<K>, string>>; body: Body<K> }
type Answer<K extends keyof Api> = ApiResponse<K> | Refusal
type Route<K extends keyof Api> = { query?: readonly Query<K>[]; answer: (request: Request<K>) => Answer<K> | Promise<Answer<K>> }
  & (Api[K] extends { body: unknown } ? { body: true } : { body?: never })
const page = <T>(items: T[]) => ({ items, total: items.length, next_offset: null })
const ticketOf = (id: string) =>
  [...server.tickets.open, ...server.person, ...server.tickets.ended].find(ticket => ticket.id === id) ?? server.ticketDetails[id]
const saved = (revision: number) => {
  if (revision !== server.revision) return refuse(409, 'Settings changed. Reload before saving.')
  server.revision++
  return undefined
}

const ROUTES: { [K in keyof Api]: Route<K> } = {
  'GET state': { answer: stateView },
  'GET overview': { answer: overviewView },
  'GET search': { query: ['q'], answer: ({ query }) => ({ tickets: [], threads: server.search.threads,
    items: server.search.items.filter(item => item.preview.includes(query.q ?? '')) }) },
  'GET sources': { answer: sourceStatuses },
  'GET settings': { answer: settingsView },
  'GET agents': { answer: () => server.agents ?? refuse(500, 'agents unavailable') },
  'GET threads': { query: ['filter', 'offset', 'limit'], answer: () => page(server.threads) },
  'GET threads/:key': { query: ['limit', 'before', 'around'], answer: ({ params, query }) => {
    const found = server.threadPages(params.key, query)
    if (!found) return refuse(404, 'Thread not found')
    const queued = (item: TriggerRow) => ({ ...item, disposition: { ...item.disposition, queued: true }, actions: { requeue: false } })
    return { ...found, timeline: found.timeline.map(item => server.queued.has(item.id) ? queued(item) : item) }
  } },
  'POST threads/:key/unsubscribe': { answer: ({ params }) => {
    const removed = server.threads.length > 0
    server.threads = []
    return { removed, thread: params.key, context_delivery: 'injected' }
  } },
  'POST threads/:key/annotate': { body: true, answer: ({ params }) => params.key === GROUP ? { triggerId: 'panel:n2' }
    : refuse(404, 'Thread not found') },
  'POST triggers/:id/requeue': { answer: ({ params }) => { server.queued.add(params.id); return { queued: true } } },
  'GET decisions': { query: ['kind', 'window', 'offset', 'limit'], answer: ({ query }) =>
    ({ items: query.kind === 'ignore' ? [] : [acceptDecision()], next_offset: null }) },
  'GET plans': { answer: () => [{ id: 'p1', issue_id: 'MUL-551', issue_status: 'in_review', issue_url: '/acme/issues/MUL-551',
    conclusion: '可以合并', reason: 'review 通过、CI 绿', state: 'pending', issue_key: 'MUL-551', ticket_ids: ['t1'],
    actions: [{ type: 'comment', target: 'MUL-551', body: 'LGTM' }, { type: 'merge_mr', target: 'mr-1' }] }] },
  'POST plans/:id/execute': { body: true, answer: ({ params }) => ({ id: params.id, issue_id: 'MUL-551', issue_status: 'in_review', issue_url: '',
    conclusion: '', reason: '', actions: [], state: 'executed' }) },
  'POST plans/:id/reject': { body: true, answer: () => ({ rejected: true as const }) },
  'POST decisions/:id/dismiss': { answer: ({ params }) => params.id === 'u1' ? { dismissed: true } : refuse(404, 'Unavailable Eval not found') },
  'GET tickets': { query: ['state', 'offset', 'limit'], answer: ({ query }) => page(server.tickets[query.state === 'ended' ? 'ended' : 'open']) },
  'GET tickets/:id': { answer: ({ params }) => server.ticketDetails[params.id] ?? refuse(404, 'Ticket not found') },
  'POST tickets/:id/retry': { answer: ({ params }) => {
    const ticket = ticketOf(params.id)
    if (!ticket) return refuse(404, 'Ticket not found')
    return ticket.actions.retry ? { retried: true } : refuse(400, 'Only an open ticket can be retried, and only when its worker last reported failed or blocked and no person has sent a message since, or when delivering an Input to its worker failed',
      { code: 'notRetryable', values: {} })
  } },
  'POST tickets/:id/stop': { answer: ({ params }) => {
    if (!ticketOf(params.id)) return refuse(404, 'Ticket not found')
    server.tickets.open = []; server.person = server.person.filter(ticket => ticket.id !== params.id)
    return { stopped: true }
  } },
  'POST tickets/:id/annotate': { body: true, answer: ({ params }) => server.ticketDetails[params.id] ? { triggerId: 'panel:n3' }
    : refuse(404, 'Ticket not found') },
  'POST outbox/:id/confirm': { body: true, answer: ({ params }) => {
    const before = server.confirm.length
    server.confirm = server.confirm.filter(item => item.id !== params.id)
    return server.confirm.length < before ? { confirmed: true } : refuse(404, 'No reply waits for confirmation under this id')
  } },
  'POST outbox/:id/withdraw': { answer: ({ params }) => { server.confirm = server.confirm.filter(item => item.id !== params.id); return { withdrawn: true } } },
  'POST executor/pause': { body: true, answer: ({ body }) => { server.settings = { ...server.settings, paused: body.paused }; return { paused: body.paused } } },
  'POST sources/:id/labels': { answer: () => ({ added: [] }) },
  'POST enable': { body: true, answer: ({ body }) => { server.settings = { ...server.settings, enabled: body.enabled }; return { enabled: body.enabled } } },
  'PUT settings/:block': { body: true, answer: ({ params: { block }, body }) => {
    const source = block.startsWith('source:') ? KINDS.find(kind => `source:${kind.id}` === block) : undefined
    if (!['policy', 'eval', 'executor'].includes(block) && !source) return block.startsWith('source:') ? refuse(404, 'Source not found')
      : refuse(400, 'Invalid settings: block')
    const conflict = saved(body.revision)
    if (conflict) return conflict
    const value = body.value as never
    if (source) server.settings = { ...server.settings, sources: { ...server.settings.sources, [source.id]: value } }
    else server.settings = { ...server.settings, [block]: value }
    return { restarting: false, revision: server.revision }
  } },
}

function match(method: string, segments: string[]) {
  for (const key of Object.keys(ROUTES) as (keyof Api)[]) {
    const [verb, path] = key.split(' ') as [string, string]
    const pattern = path.split('/')
    if (verb !== method || pattern.length !== segments.length || !pattern.every((part, index) => part.startsWith(':')
      || part === segments[index])) continue
    return { key, params: Object.fromEntries(pattern.flatMap((part, index) => part.startsWith(':') ? [[part.slice(1),
      decodeURIComponent(segments[index]!)]] : [])) }
  }
  return undefined
}
async function run<K extends keyof Api>(key: K, params: Record<string, string>, query: URLSearchParams, body: unknown): Promise<Answer<K>> {
  const route: Route<K> = ROUTES[key]
  const read: readonly string[] = route.query ?? []
  for (const name of query.keys()) if (!read.includes(name)) server.misuse.push(`${key} with the query parameter ${name}`)
  if ((body !== undefined) !== !!route.body) server.misuse.push(`${key} ${body === undefined ? 'without' : 'with'} a body`)
  const values: Partial<Record<Query<K>, string>> = {}
  for (const name of route.query ?? []) { const value = query.get(name); if (value !== null) values[name] = value }
  return route.answer({ params: params as RouteParams<K>, query: values, body: body as Body<K> })
}
class Reply extends Response {
  constructor(value: unknown, status: number) { super(JSON.stringify(value), { status, headers: { 'content-type': 'application/json' } }); server.pending++ }
  override async text() { server.pending--; return super.text() }
}

export function withPanelServer(cleanup: () => void) {
  beforeEach(() => {
    sessionStorage.clear()
    server = initial()
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit): Promise<Response> => {
      if (server.down) return new Response(null, { status: 404 })
      const address = new URL(url, 'http://127.0.0.1:8080')
      const path = address.pathname.replace(API_BASE, '')
      const method = init?.method ?? 'GET'
      const body: unknown = init?.body ? JSON.parse(String(init.body)) : undefined
      server.requests.push({ path: decodeURIComponent(`${path}${address.search}`), method, ...body === undefined ? {} : { body } })
      const route = match(method, path.split('/'))
      if (!route) return new Reply({ error: 'Unknown operation' }, 404)
      const answer = await run(route.key, route.params, address.searchParams, body)
      return answer instanceof Refusal ? new Reply(answer.body, answer.status) : new Reply(answer, 200)
    }))
  })
  afterEach(() => {
    try { expect(server.misuse).toEqual([]) }
    finally { cleanup(); vi.unstubAllGlobals() }
  })
}