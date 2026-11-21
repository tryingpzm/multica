/**
 * Type-only management API contract between the personal agent panel and the server (SA src/app/api.ts). SA derives
 * the view types from its host's view functions; the server's are Go (server/internal/personalagent/views), so they are
 * written out here, field for field.
 */
import type { AcceptMode, AdmissionCell, Attention, ChannelRef, DecisionKind, MandateBasis, Strength, ThreadFacts,
  TicketInput, TicketOutcome } from '../core/model'
import type { Reason } from '../core/reasons'
import type { TicketStage } from '../core/stage'
import type { SetupProblem } from '../setup'
import type { Health } from '../sources/host'
import type { AdmissionRowView, FieldSpec, Localized } from '../sources/kind'

export const TICKET_FILTERS = ['open', 'person', 'working', 'idle', 'ended'] as const
export const THREAD_FILTERS = ['subscribed', 'evaluating', 'recent', 'all'] as const
export type TicketFilter = typeof TICKET_FILTERS[number]
export type ThreadFilter = typeof THREAD_FILTERS[number]
interface Paged { offset?: number; limit?: number }
type Delivery = 'injected' | 'pending'
export interface Page<T> { items: T[]; total: number; next_offset: number | null }

export interface ActorView { source: string; id: string; name?: Localized }
export interface SetupView {
  problem: SetupProblem | null; feishuProblem: SetupProblem | null
  accounts: { feishu: unknown } | null
}
export interface StateView {
  setup: SetupView
  load: { running: number; queued: number; maxWorkers: number }
  /** The Router's chat session; empty until its first Eval. */
  masterId: string
  paused: boolean
  /** Whether the person switched their personal agent on. */
  enabled: boolean
  health: Record<string, Health>
}
export interface ThreadFactsView { key: string; source: string; title: string; url?: string; facts: ThreadFacts }
export type TicketSummaryView = TicketStage & TicketBase
interface TicketBase {
  id: string; title: string; state: 'open' | 'ended'; note?: { text: string; error: boolean }
  actions: { retry: boolean; stop: boolean }; strength: Strength
  ended: { at: string; by: string; outcome: TicketOutcome; reason?: string } | undefined
  attention: Attention | undefined
  assignments: number; inputs: number; undelivered: number; deliveryError: string | undefined
  /** The ticket's Multica issue, which its worker runs on; empty before its first run. */
  sessionId: string
  directory: string | undefined; summary: string; createdAt: string; updatedAt: string
  /** who asked, what for, the agent or squad it went to (no name: the default worker), and its Multica issue. */
  actor?: ActorView; scope?: string; executor?: { type: 'agent' | 'squad'; id: string; name: string }; issue?: IssueLinkView
}
export interface IssueLinkView { id: string; identifier: string; title?: string; url: string }
/** One line of a Ticket's story; detail is its full text. */
export interface TimelineItemView {
  kind: 'trigger' | 'decision' | 'assign' | 'report' | 'reply'; at: string; actor?: ActorView; text?: string; detail?: string
  outcome?: DecisionOutcome; reason?: Reason[]; executor?: string; state?: string; source?: string
}
export interface ChannelLinkView { source: string; url: string; label: Localized }
/** A Watchdog finding: the latest Assignment has had no run, or its run is overdue. */
export interface StuckView { kind: 'run_overdue' | 'no_run'; since: string }
/** What a ticket waiting for the owner asks, and why. */
export type QuestionView = { text: string; reason: string }
export type TicketRowView = TicketSummaryView & { threads: ThreadFactsView[]; reply?: ChannelLinkView; stuck?: StuckView; question?: QuestionView }
export type ThreadStage = 'error' | 'evaluating' | 'queued' | 'subscribed' | 'dormant' | 'unsubscribed'
export interface ThreadSummaryView {
  key: string; source: string; title: string; stage: ThreadStage
  latest: { actor: ActorView; human: boolean; at: string; preview: string } | undefined
  lastAt: string; createdAt: string; subscriptions: number; tickets: { id: string; title: string; state: string }[]
  evaluating: { id: string; strength: Strength; triggers: number; startedAt: string; deadline: string } | undefined
  pending: number; pollError: string | undefined; url: string | undefined; facts: ThreadFacts
}
export type DecisionOutcome = DecisionKind | 'wouldAccept'
export interface DecisionView {
  id: string; kind: DecisionKind; outcome: DecisionOutcome; reason: Reason[]; at: string; strength: Strength
  ticketId: string | undefined; triggerIds: string[]; thread: string
  rules: { id: string; name: string; matched: boolean; reason: string }[]
  direction?: { implementation_requested: boolean; target_basis: string; requires_design_decision: boolean; evidence: string }
  threadTitle?: string; preview: string; ticketTitle: string | undefined
}
export interface TriggerView {
  id: string; source: string; actor: ActorView; human: boolean; at: string; text: string; preview: string; url: string | undefined
  threads: string[]; actions: { requeue: boolean }
  disposition: { queued: boolean; evaluating: boolean; delivered: boolean; filtered: Reason | undefined; fanout: string[]
    handbacks: { ticketId: string; reason: string; at: string; title: string }[]
    decision: { id: string; outcome: DecisionOutcome; reason: Reason[]; ticketId: string | undefined } | undefined }
}
export interface ThreadDetailView extends Omit<ThreadSummaryView, 'tickets'> {
  context: string[]; correlated: { key: string; title: string }[]; tickets: TicketRowView[]; decisions: DecisionView[]
  timeline: TriggerView[]; earlier: string | null
}
export interface SearchItemView extends Omit<TriggerView, 'text'> { threadTitle: string | undefined; filtered?: Reason | string }
export interface SearchView { tickets: TicketSummaryView[]; items: SearchItemView[]; threads: ThreadSummaryView[] }
export interface DecisionListView { items: DecisionView[]; next_offset: number | null }
export interface OutgoingView { id: string; channel: ChannelRef; text: string; attempts: number; error: string }
export type Problem =
  | ({ kind: 'setup' } & SetupProblem)
  | { kind: 'no-source' } | { kind: 'no-executor' } | { kind: 'executor-paused' }
  | { kind: 'rule-no-target'; rule: { id: string; name: string } }
  | { kind: 'source-error'; source: { id: string; title: Localized; health: Health & { name: string } } }
  | { kind: 'runtime-error'; name: string; detail: string }
  | { kind: 'eval-overdue'; thread: ThreadSummaryView }
  | { kind: 'stuck'; ticket: TicketRowView }
  | { kind: 'eval-unavailable'; decisionId: string; thread: ThreadSummaryView; reason: Reason[]; triggerId: string }
  | { kind: 'outgoing'; outgoing: OutgoingView }
/** A reply waiting for its owner to confirm it before it is sent (accept mode other than auto). */
export interface PendingReplyView {
  id: string; kind: string; channel: ChannelRef; link: ChannelLinkView | undefined; text: string; at: string
  ticket?: { id: string; title: string }; thread?: string
  /** Why the last send failed; confirming sends it again. */
  error?: string
  /** the message it answers, and where it lands named by issue, channel and post. */
  to?: { actor: ActorView; preview: string }
  where?: { source: string; place?: string; post?: string; url?: string; comment?: boolean }
}
export interface OverviewView {
  problems: Problem[]
  person: TicketRowView[]
  /** Replies held for confirmation, oldest first. */
  confirm: PendingReplyView[]
  executor: { running: number; queued: number; maxWorkers: number; idle: number }
  flow: { received: number; filtered: number; triggers: number; judged: number; accepted: number; evaluating: number; dropped: number }
  policyVersion: string
}
export interface DroppedReplyView {
  id: string; kind: string; channel: ChannelRef; preview: string; dropped: { at: string; reason: string }
  ticket?: { id: string; title: string }; thread?: string
}
export type SourceState = 'missing' | 'off' | 'error' | 'setup' | 'starting' | 'paused' | 'ready'
export interface SourceStatusView {
  id: string; title: Localized; installed: boolean; enabled: boolean; state: SourceState; health: (Health & { name: string })[]
  problem?: SetupProblem; stats: { received: number; filtered: number; lastAt: string }
  filtered: { id: string; at: string; actor: ActorView; preview: string; reason: Reason | undefined }[]
  dropped: DroppedReplyView[]
}
/** A rule's target is the agent or squad its tickets are assigned to; without one they go to the default worker agent. */
export interface RuleTarget { type: 'agent' | 'squad'; id: string }
export interface Rule { id: string; name: string; enabled: boolean; instruction: string; target?: RuleTarget
  /** Whether the rule is still the Router template's or the owner's own; set by the server, not saved. */
  origin?: 'template' | 'customized'; target_origin?: 'template' | 'owner' | 'fallback' }
export interface PolicyConfig {
  rules: Rule[]
  sources: Record<string, { allowed: { id: string; name?: string }[]; bots?: { id: string; name?: string }[]
    admission?: Record<string, Partial<Record<AdmissionCell, string>>> } & Record<string, unknown>>
}
/** language is what the Router writes reasons in. */
export interface EvalConfig { mode: AcceptMode; timeoutSeconds: number; language?: 'zh' | 'en'; model: null }
/** The Multica agent that runs the Router's chat; its runtime decides where it runs. Tickets go to their rules' agents. */
export interface ExecutorConfig { routerAgentId: string }
export interface SourceSettingsView {
  id: string; kind: string; title: Localized; description: Localized; installed: boolean
  value: { enabled: boolean; config: Record<string, unknown> }; revision: number; restarts: string[]; fields: FieldSpec[]
  facts: Record<string, Localized | string>; labels: Record<string, string>; problem?: SetupProblem
  /** The owner's id on this Source's allowed list, shown as the owner and not removable; empty when unknown (unbound). */
  self?: string
  policy?: { fields: FieldSpec[]; explicit: Localized; rows?: AdmissionRowView[]
    sync?: { label: Localized; ready: boolean; health: Health | undefined } }
}
export interface SettingsView {
  policyVersion: string
  blocks: { policy: { value: PolicyConfig; revision: number }; eval: { value: EvalConfig; revision: number }
    executor: { value: ExecutorConfig; revision: number } }
  sources: SourceSettingsView[]
}
/** An agent or, since , a squad (type squad, no runtime) a rule may target. */
/** personal marks the owner's own template copy, whose own instructions they edit on its page (url). */
export interface AgentOption { id: string; name: string; runtime: string; type?: 'agent' | 'squad'; personal?: boolean; url?: string; executor?: boolean }
export type BasisFact =
  | { name: 'addressed'; value: 'mention' | 'direct' } | { name: 'followup' } | { name: 'admission'; value: AdmissionCell }
  | { name: 'observation' } | { name: 'mode'; value: AcceptMode } | { name: 'rules'; value: { id: string; name: string }[] }
  | { name: 'sender'; value: string } | { name: 'legacyAuthority'; value: string } | { name: 'bot' }
export interface AnnotationView { id: string; text: string; at: string; triggerId: string }
export type TicketPageView = TicketStage & Omit<TicketBase, 'assignments' | 'inputs'> & { reply?: ChannelLinkView; stuck?: StuckView; question?: QuestionView
  mandate: { strength: Strength; policyVersion: string; basis: MandateBasis; actor: ActorView; scope: string }
  basis: { facts: BasisFact[]; legacy: boolean }; paused: boolean; channels: ChannelLinkView[]
  decision: DecisionView | undefined
  assignments: { id: string; state: string; createdAt: string; startedAt?: string; endedAt?: string; endedReason?: string
    report?: { state: string; summary: string; at: string; unreported?: boolean }; ref?: string; turnEndedAt?: string }[]
  inputs: (TicketInput & { text?: string; delivered: boolean; trigger?: { id: string; actor: ActorView; human: boolean; preview: string
    thread: string; handback?: { reason: string; at: string } } })[]
  threads: (ThreadSummaryView & { context?: true })[]
  /** The person's annotations on this ticket, newest first. */
  annotations: AnnotationView[]
  /** replies withdrawn before sending, kept on record. */
  withdrawn?: { id: string; kind: string; text: string; at: string; status: 'discarded' }[]
  /** The Multica issue the ticket's worker runs on. */
  issue?: IssueLinkView
  /** What the Router told the worker to do. */
  plan?: string
  timeline?: TimelineItemView[]
}

export interface Api {
  'GET state': { response: StateView }
  'GET overview': { response: OverviewView }
  'GET search': { query: { q: string }; response: SearchView }
  'GET sources': { response: SourceStatusView[] }
  'GET settings': { response: SettingsView }
  'GET agents': { response: AgentOption[] }
  'GET threads': { query?: Paged & { filter?: ThreadFilter }; response: Page<ThreadSummaryView> }
  'GET threads/:key': { query?: { limit?: number; before?: string; around?: string }; response: ThreadDetailView }
  'POST threads/:key/unsubscribe': { response: { removed: boolean; thread: string; context_delivery: Delivery } }
  'POST threads/:key/annotate': { body: { text: string }; response: { triggerId: string } }
  'POST triggers/:id/requeue': { response: { queued: boolean } }
  'GET decisions': { query?: Paged & { kind?: string; window?: '24h' }; response: DecisionListView }
  'POST decisions/:id/dismiss': { response: { dismissed: true } }
  'GET plans': { response: PlanView[] }
  'POST plans/:id/execute': { body: { actions?: PlanAction[]; confirm: boolean }; response: PlanView }
  'POST plans/:id/reject': { body: { reason: string }; response: { rejected: true } }
  'GET tickets': { query?: Paged & { state?: TicketFilter }; response: Page<TicketRowView> }
  'GET tickets/:id': { response: TicketPageView }
  'POST tickets/:id/retry': { response: { retried: true } }
  'POST tickets/:id/stop': { response: { stopped: true } }
  'POST tickets/:id/annotate': { body: { text: string }; response: { triggerId: string } }
  /** text sends the reply as edited. */
  'POST outbox/:id/confirm': { body: { text?: string }; response: { confirmed: boolean } }
  'POST outbox/:id/withdraw': { response: { withdrawn: true } }
  'POST executor/pause': { body: { paused: boolean }; response: { paused: boolean } }
  'POST sources/:id/labels': { response: { added: string[] } }
  'POST enable': { body: { enabled: boolean }; response: { enabled: boolean } }
  'PUT settings/:block': { body: { value: unknown; revision: number }; response: { revision: number; restarting: boolean } }
}
type Path<K> = K extends `${string} ${infer P}` ? P : never
type Params<P> = P extends `${string}:${infer Name}/${infer Rest}` ? Record<Name, string> & Params<Rest>
  : P extends `${string}:${infer Name}` ? Record<Name, string> : unknown
export type RouteParams<K extends keyof Api> = Params<Path<K>>
export type ApiRequest<K extends keyof Api> = RouteParams<K> & Omit<Api[K], 'response'>
export type ApiResponse<K extends keyof Api> = Api[K]['response']
/** A plan the owner's executor proposes for an issue that needs them. */
export interface PlanAction { type: 'comment' | 'clarify_answer' | 'channel_reply' | 'status' | 'merge_mr' | string; target: string; body?: string }
export interface PlanView { id: string; issue_id: string; issue_status: string; issue_url: string; conclusion: string; reason: string
  actions: PlanAction[]; state: string
  /** the issue's number, and the open tickets about it, which show the plan on their cards. */
  issue_key?: string; ticket_ids?: string[] }