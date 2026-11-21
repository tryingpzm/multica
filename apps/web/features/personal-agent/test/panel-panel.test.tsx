// Panel search, glossary, route persistence, and the server-down fallback (SA tests/panel-panel.spec.tsx).
import { act } from 'react'
import { expect, it } from 'vitest'
import { en, zh } from '../client/locales'
import { withPanel, mount, remount, settle, all, click, type, text, field } from './panel'
import { server, trigger } from './panel-server'

withPanel()
const found = (id: string, at: string, fields: Parameters<typeof trigger>[2] & { threadTitle?: string; filtered?: string } = {}) => {
  const { threadTitle, filtered, ...rest } = fields
  const { text: _text, ...row } = trigger(id, at, rest)
  return filtered === undefined ? { ...row, threadTitle } : { ...row, threadTitle: undefined, filtered }
}
const search = async (query: string) => {
  await type(field<HTMLInputElement>(zh.searchLabel), query)
  await act(async () => { field<HTMLInputElement>(zh.searchLabel).closest('form')!.requestSubmit() }); await settle()
}

it('states why a filtered search result has no Thread instead of opening an empty one', async () => {
  server.search = { items: [found('e9', '2026-09-20T00:00:00Z', { source: 'multica', actor: { source: 'multica', id: 'someone' },
    preview: 'issue text', threads: ['multica:issue-7'], filtered: 'source_disabled' })], threads: [] }
  await mount()
  await search('issue')
  expect(text()).toContain('被来源过滤：来源已停用')
  expect(all('.dt-search-results button').some(button => button.textContent === 'issue text')).toBe(false)
})

it('opens a search result older than the newest page around its Trigger', async () => {
  server.search = { items: [found('e1', '2026-09-20T00:00:00Z')], threads: [] }
  await mount()
  await search('message')
  expect(text()).toContain('找到 0 个工单、0 个对话、1 条消息')
  await click(all('.dt-search-results button').find(button => button.textContent === 'message e1')!)
  expect(server.requests.some(request => request.path.includes('around=e1'))).toBe(true)
  expect(document.querySelector('.dt-highlight')?.textContent).toContain('message e1')
})

it('explains what can be searched when nothing matches', async () => {
  await mount()
  await search('nothing')
  expect(text()).toContain(zh.searchNone)
})

it('returns to the exact page after a remount of the panel', async () => {
  await mount()
  await click('修复回归')
  await remount()
  expect(document.querySelector('.dt-page-title h2')?.textContent).toBe('修复回归')
})

it('shows domain terms in the interface language only and lists both languages in the (?) glossary', async () => {
  await mount(en)
  expect(all('[role="tab"]').map(tab => tab.textContent)).toEqual(['Overview', 'Source', 'Trigger', 'Eval', 'Execution', 'For me to decide', 'Settings'])
  await click('Glossary')
  const dialog = document.querySelector('[role="dialog"]')!
  const row = [...dialog.querySelectorAll('tr')].find(item => item.textContent?.startsWith('Ticket'))!
  expect([...row.querySelectorAll('td')].map(cell => cell.textContent)).toEqual(['Ticket', '工单', 'An authorized unit of work'])
})

it('says the server is not answering when its routes are gone', async () => {
  server.down = true
  await mount()
  expect(text()).toContain(zh.hostNotRunning)
  expect(text()).not.toContain('404')
})

it('opens the Router\'s chat from the header', async () => {
  await mount()
  await click(zh.openMaster)
  expect(server.opened).toEqual(['router-chat'])
})