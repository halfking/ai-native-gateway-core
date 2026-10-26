import { describe, expect, it } from 'vitest'
import { installSelectKeyboardSuppress } from './suppressSelectKeyboard'

describe('installSelectKeyboardSuppress', () => {
  it('blurs a focused text input before a select opens', () => {
    const root = document.createElement('div')
    document.body.appendChild(root)
    const input = document.createElement('input')
    input.type = 'text'
    const select = document.createElement('select')
    root.append(input, select)
    input.focus()
    expect(document.activeElement).toBe(input)

    const dispose = installSelectKeyboardSuppress(root)
    select.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    expect(document.activeElement === input).toBe(false)

    dispose()
    root.remove()
  })

  it('ignores non-select targets', () => {
    const root = document.createElement('div')
    document.body.appendChild(root)
    const input = document.createElement('input')
    root.append(input)
    input.focus()
    const dispose = installSelectKeyboardSuppress(root)
    input.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    expect(document.activeElement).toBe(input)
    dispose()
    root.remove()
  })
})
