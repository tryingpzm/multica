// The Source status and the Source settings (SA tests/panel-source.spec.tsx, on Multica's Sources), with the switch
// of the whole personal agent.
import { expect, it } from 'vitest'
import { zh } from '../client/locales'
import { withPanel, mount, all, find, click, type, text, field } from './panel'
import { server, droppedReply } from './panel-server'

withPanel()
it('shows each Source\'s state and counts on the Source status, and opens the settings from it', async () => {
  server.settings.sources.multica = { enabled: false, config: {} }
  await mount()
  await click(zh['tab.source'])
  expect(all('.dt-row-name').map(name => name.textContent)).toEqual(['GitHub', 'Multica'])
  expect(all('.dt-status-text').map(state => state.textContent)).toEqual(['运行中', '已关闭'])
  expect(text()).toContain(`${zh.sourceReceivedLabel} 12`)
  await click(find(zh['tab.settings'], '.dt-status-head button'))
  expect(document.querySelector('.dt-settings-link[aria-current="page"]')?.textContent).toBe(zh['settingsGroup.source'])
})

it('renders each Source\'s settings from its kind\'s fields and saves one Source against the settings revision', async () => {
  await mount()
  await click(find(zh['tab.settings'], '[role="tab"]'))
  expect(all('.dt-card-title').map(title => title.textContent)).toEqual([zh.enableTitle, 'GitHub', 'Multica'])
  expect(text()).toContain('接收的群')
  await click(all('#dt-source-github button').find(button => button.textContent === '添加')!)
  const row = all('#dt-source-github .dt-rows-item').at(-1)!
  await type(row.querySelector<HTMLInputElement>('input[aria-label="群"]')!, 'g2')
  await type(row.querySelector<HTMLSelectElement>('select[aria-label="接收"]')!, 'all')
  await type(field<HTMLInputElement>('你的 GitHub 用户名'), 'alice2')
  await click(find(zh.save, '#dt-source-github button'))
  expect(server.requests.find(request => request.path === 'settings/source:github')?.body).toEqual({ revision: 3, value: { enabled: true,
    config: { rows: [{ row: 'g1', listen: 'mention' }, { row: 'g2', listen: 'all' }], userId: 'alice2' } } })
})

it('offers the Router\'s own instructions under the switch while the personal agent is on', async () => {
  await mount()
  await click(find(zh['tab.settings'], '[role="tab"]'))
  const link = () => document.querySelector<HTMLAnchorElement>('.dt-card a.dt-router-link')
  expect([link()?.textContent, link()?.getAttribute('href')]).toEqual([zh.routerAppend, '/acme/agents/agent-router'])
  expect(text()).toContain(zh.routerAppendHint)
  await click(find(zh.enableTitle, '[role="switch"]'))
  expect(link()).toBeNull()
})

it('switches the whole personal agent off and on from the Source settings', async () => {
  await mount()
  await click(find(zh['tab.settings'], '[role="tab"]'))
  await click(find(zh.enableTitle, '[role="switch"]'))
  expect(server.requests.find(request => request.path === 'enable')).toMatchObject({ method: 'POST', body: { enabled: false } })
  expect(find(zh.enableTitle, '[role="switch"]').getAttribute('aria-checked')).toBe('false')
  await click(find(zh.enableTitle, '[role="switch"]'))
  expect(server.settings.enabled).toBe(true)
})

it('lists the messages a Source did not send in the last day under it, with why and where they belong', async () => {
  server.dropped = [droppedReply('m2', 1, 'github', 'channelGone'), droppedReply('m0', 30, 'github', 'channelGone')]
  await mount()
  await click(zh['tab.source'])
  await click(find(`${zh.sourceDroppedTitle} 1`))
  expect(text()).toContain('Reply m2')
  expect(text()).not.toContain('Reply m0')
  await click(find(zh['term.thread'], '.dt-disclosure-body button'))
  expect(server.requests.some(request => request.path.startsWith('threads/github:g1:m_root'))).toBe(true)
})

it('takes no space for messages that were all sent', async () => {
  await mount()
  await click(zh['tab.source'])
  expect(text()).not.toContain(zh.sourceDroppedTitle)
})