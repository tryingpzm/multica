/**
 * The panel's one glossary: the engine's own words and the plain words the owner sees for them. Every Chinese
 * string of the panel passes through it, so a concept is named the same on every page. Order matters: longer terms first.
 */
export const HUMAN_TERMS: readonly (readonly [string, string])[] = [
  ['dsh-superagent', '个人助理服务'],
  ['个人 Agent 的', '个人助理的'],
  ['个人 Agent', '个人助理'],
  ['执行器 Session ', '执行助手'],
  ['执行器 Session', '执行助手'],
  ['主 Session ', '个人助理'],
  ['主 Session', '个人助理'],
  [' Session', '会话'],
  ['Session', '会话'],
  ['Router 智能体', '个人助理智能体'],
  ['Router 的', '个人助理的'],
  ['Router ', '个人助理'],
  ['Router', '个人助理'],
  ['Worker', '执行助手'],
  ['执行器', '执行助手'],
  ['点名智能体', '@ 你'],
  ['点名方式', '怎么找你'],
  ['策略版本', '规则版本'],
  ['策略', '规则'],
  ['扇出给', '同步给'],
  ['扇出', '同步'],
  ['触发', '消息'],
  ['线程', '对话'],
  ['评估', '研判'],
  ['派工', '派单'],
  ['授权', '接单依据'],
  ['准入', '接收规则'],
  ['订阅', '关注'],
  ['输入未送达', '消息未送达'],
  ['的输入', '的消息'],
  ['仅观察', '只观察'],
  ['自动接单', '自动处理'],
]

/** A Chinese string in the glossary's words. */
export const humanize = (text: string): string => HUMAN_TERMS.reduce((out, [from, to]) => out.split(from).join(to), text)