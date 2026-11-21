// The tooltip and toast are painted with Multica's theme tokens: their text colour must differ from their background,
// or a hover shows a blank bubble.
import { expect, it } from 'vitest'
import { STYLES } from '../client/styles'

const token = (name: string) => STYLES.match(new RegExp(`${name}:\\s*([^;]+);`))?.[1]?.trim()
const rule = (selector: string, property: string) =>
  STYLES.match(new RegExp(`^\\${selector} \\{[^}]*?\\b${property}:\\s*var\\((--[\\w-]+)\\)`, 'm'))?.[1]

it.each(['.dt-tooltip', '.dt-toast'])('paints %s text in a different colour from its background', selector => {
  const background = token(rule(selector, 'background')!)
  const color = token(rule(selector, 'color')!)
  expect(background).toBeTruthy()
  expect(color).toBeTruthy()
  expect(color).not.toBe(background)
  expect([background, color].sort()).toEqual(['var(--background)', 'var(--foreground)'])
})
