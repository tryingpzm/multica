// The Eval status and settings (SA tests/panel-eval.spec.tsx, on Multica's Sources).
import { act } from 'react'
import { expect, it } from 'vitest'
import { zh } from '../client/locales'
import { withPanel, mount, remount, all, find, tab, click, type, text, field } from './panel'
import { server } from './panel-server'

withPanel()
const valueLabelOf = (language: string) => zh[`language.${language}` as keyof typeof zh] as string
const openAdvanced = () => click(find(zh.advanced, '.dt-part [data-disclosure-row]'))

it('opens the Decisions under Policy from the flow line with their reasons, Decisions that start work first, filterable by kind',
  async () => {
  await mount()
  await click(all('.dt-stat button')[1]!)
  expect(tab(zh['tab.eval']).getAttribute('aria-selected')).toBe('true')
  expect(server.requests.some(request => request.path === 'decisions?limit=30&kind=accept,reopen&window=24h')).toBe(true)
  expect(text()).toContain(zh['reason.explicitRouted'])
  await click(zh.stop)
  expect(server.requests.some(request => request.path === 'decisions?limit=30&kind=stop&window=24h')).toBe(true)
  expect(text()).toContain('@alice 修一下')
  await click(zh['decision.ignore'])
  expect(server.requests.some(request => request.path === 'decisions?limit=30&kind=ignore&window=24h')).toBe(true)
  expect(text()).toContain(zh.none)
  await click(zh.lastDayOnly)
  expect(server.requests.some(request => request.path === 'decisions?limit=30&kind=ignore')).toBe(true)
})

it('keeps a Policy draft across remounts, and saves each Source\'s people in that Source\'s own identities', async () => {
  await mount()
  await click(zh['tab.eval'])
  await click(find(zh['tab.settings'], '.dt-status-head button'))
  expect(all('.dt-subtab').map(tab => tab.textContent)).toEqual(['GitHub', 'Multica'])
  expect(text()).toContain('任何人在群里 @ 你，都是明确的请求。')
  // MUL-13343: the lists are optional, kept under 高级.
  expect(all('.dt-part .dt-rows-item')).toEqual([])
  await openAdvanced()
  // A saved person shows as their id; only a row being added is a field.
  expect(all('.dt-part .dt-rows-item .dt-row-label').map(label => label.textContent)).toEqual(['alice'])
  await click(all('.dt-part button').filter(button => button.textContent === '添加')[0]!)
  const row = all('.dt-part .dt-rows-item').at(-1)!
  await type(row.querySelector<HTMLInputElement>('input[aria-label="GitHub 用户名"]')!, 'bob')
  await type(row.querySelector<HTMLInputElement>('input[aria-label="名字"]')!, 'Bob')
  expect(text()).toContain(zh.draftKept)
  await remount()
  expect(text()).toContain(zh.draftRestored)
  await click(zh.save)
  expect(server.settings.policy.sources['github']!.allowed).toEqual([{ id: 'alice', name: 'Alice' }, { id: 'bob', name: 'Bob' }])
  expect(server.requests.find(request => request.path === 'settings/policy')).toMatchObject({ method: 'PUT', body: { revision: 3 } })
  await click(find('Multica', '.dt-subtab'))
  expect(text()).toContain('任何人在 issue 评论里 @ 你，都是明确的请求。')
})

it('shows the owner on an allowed list as 你自己, without a way to remove them', async () => {
  server.self = { multica: 'u-alice' }
  await mount()
  await click(zh['tab.eval'])
  await click(find(zh['tab.settings'], '.dt-status-head button'))
  await click(find('Multica', '.dt-subtab'))
  await openAdvanced()
  const items = all('.dt-part .dt-rows-item')
  expect(items.map(item => item.textContent)).toEqual([zh.yourself])
  expect(items[0]!.querySelector('button')).toBeNull()
})

it('opens the rule list for an invalid field inside it only', async () => {
  await mount()
  await click(zh['tab.eval'])
  await click(find(zh['tab.settings'], '.dt-status-head button'))
  const openRows = () => all('.dt-disclosure[data-open]').length
  await act(async () => { field(zh.evalTimeout).dispatchEvent(new Event('invalid', { bubbles: false })) })
  expect(openRows()).toBe(0)
  await click(all('.dt-rule [data-disclosure-row]')[0]!)
  await act(async () => { all('.dt-rule input')[0]!.dispatchEvent(new Event('invalid')) })
  expect(openRows()).toBe(all('.dt-rule').length)
})

it('adds, copies, renames, disables and deletes rules in Policy without touching Eval', async () => {
  await mount()
  await click(zh['tab.eval'])
  await click(find(zh['tab.settings'], '.dt-status-head button'))
  await click(zh.addRule)
  await type(field<HTMLInputElement>(zh.ruleName), '文档')
  await type(field<HTMLTextAreaElement>(zh.ruleInstruction), '修复失效链接')
  await click(zh.duplicate)
  expect(all('.dt-rule').map(rule => rule.querySelector('[data-disclosure-row]')!.textContent)).toEqual(expect.arrayContaining([
    expect.stringContaining('文档 副本')]))
  await click('启用规则：文案修改')
  const removeButtons = all('button').filter(button => button.textContent === '删除')
  await click(removeButtons.at(-1)!)
  await click(zh.save)
  expect(server.settings.policy.rules.map(rule => [rule.name, rule.enabled])).toEqual([['文案修改', false], ['文档', true]])
  expect(server.requests.filter(request => request.method === 'PUT').map(request => request.path)).toEqual(['settings/policy'])
})

it('sends a rule\'s tickets to the agent chosen on it, or the default worker agent when none is', async () => {
  await mount()
  await click(zh['tab.eval'])
  await click(find(zh['tab.settings'], '.dt-status-head button'))
  await click(all('.dt-rule [data-disclosure-row]')[0]!)
  const agent = field<HTMLSelectElement>(zh.ruleAgent)
  expect(agent.selectedOptions[0]?.textContent).toBe(zh.ruleAgentDefault)
  await type(agent, 'agent:agent-other')
  await click(zh.save)
  expect(server.settings.policy.rules.map(rule => [rule.id, rule.target])).toEqual([['copy', { type: 'agent', id: 'agent-other' }]])
})

it('offers agents and squads in groups and sends a rule\'s tickets to the squad chosen on it', async () => {
  await mount()
  await click(zh['tab.eval'])
  await click(find(zh['tab.settings'], '.dt-status-head button'))
  await click(all('.dt-rule [data-disclosure-row]')[0]!)
  const target = field<HTMLSelectElement>(zh.ruleAgent)
  expect([...target.querySelectorAll('optgroup')].map(group => group.label)).toEqual([zh['targetGroup.agent'], zh['targetGroup.squad']])
  await type(target, 'squad:squad-bugfix')
  await click(zh.save)
  expect(server.settings.policy.rules.map(rule => [rule.id, rule.target])).toEqual([['copy', { type: 'squad', id: 'squad-bugfix' }]])
})

it('marks each rule as from the template or customized', async () => {
  server.settings.policy.rules = [
    { id: 'status', name: '状态询问', enabled: true, instruction: '问进度', origin: 'template' },
    { id: 'mine', name: '我的', enabled: true, instruction: '我自己的', origin: 'customized' },
  ]
  await mount()
  await click(zh['tab.eval'])
  await click(find(zh['tab.settings'], '.dt-status-head button'))
  expect(all('.dt-rule .dt-rule-origin').map(badge => badge.textContent)).toEqual([zh['ruleOrigin.template'], zh['ruleOrigin.customized']])
})

it('sets the language the Router writes its reasons in, Simplified Chinese by default', async () => {
  await mount()
  await click(zh['tab.eval'])
  await click(find(zh['tab.settings'], '.dt-status-head button'))
  await click(find(valueLabelOf('en'), '.dt-segmented button'))
  await click(all('button').filter(button => button.textContent === zh.save).at(-1)!)
  expect(server.settings.eval.language).toBe('en')
})

it('sets the group admission table per receiving scope under 高级, offering only the cells a scope can receive, defaulting to the personal agent\'s cells',
  async () => {
  server.settings.sources['github'] = { enabled: true, config: { rows: [{ row: 'g1', listen: 'all' }, { row: 'g2', listen: 'mention' }] } }
  await mount()
  await click(tab(zh['tab.settings']))
  await click(find(zh['term.eval'], '.dt-settings-link'))
  expect(document.querySelector('.dt-admission')).toBeNull()
  await openAdvanced()
  const scopes = all('.dt-admission tbody tr')
  expect(scopes.map(row => row.querySelector('.dt-admission-scope')?.firstChild?.textContent)).toEqual(['团队群', 'g2', '其他群'])
  expect(scopes.map(row => row.querySelectorAll('select').length)).toEqual([3, 1, 1])
  const selects = [...scopes[0]!.querySelectorAll('select')] as HTMLSelectElement[]
  expect(selects.map(select => select.value)).toEqual(['inferred', 'explicit', 'inferred'])
  await type(selects[1]!, 'ignore')
  await click(zh.save)
  expect(server.settings.policy.sources['github']!.admission).toEqual({ g1: { unlistedAddressed: 'ignore' } })
})

it('saves the accept mode and the Eval timeout on the Eval settings', async () => {
  await mount()
  await click(tab(zh['tab.settings']))
  await click(find(zh['term.eval'], '.dt-settings-link'))
  await click(find(zh['mode.auto'], '[role="radio"], button'))
  await type(field<HTMLInputElement>(zh.evalTimeout), '90')
  await click(all('button').filter(button => button.textContent === zh.save).at(-1)!)
  expect(server.requests.find(request => request.path === 'settings/eval')?.body).toEqual({ value: { mode: 'auto', timeoutSeconds: 90,
    model: null }, revision: 3 })
})