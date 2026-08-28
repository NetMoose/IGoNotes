import { describe, expect, it, vi } from 'vitest'

import { openSettingsSafely, switchBaseSafely } from './app-transitions.js'

describe('app transitions', () => {
  it('flushes the active note before opening settings', async () => {
    const order = []
    const flush = vi.fn(async () => order.push('flush'))
    const open = vi.fn(() => order.push('open'))

    await openSettingsSafely({ flush, open })

    expect(order).toEqual(['flush', 'open'])
  })

  it('does not open settings when flushing fails', async () => {
    const error = new Error('save failed')
    const open = vi.fn()

    await expect(openSettingsSafely({
      flush: () => Promise.reject(error),
      open,
    })).rejects.toBe(error)
    expect(open).not.toHaveBeenCalled()
  })

  it('flushes, switches, and commits a base in order', async () => {
    const order = []
    const config = { current_base: 'work' }
    const flush = vi.fn(async () => order.push('flush'))
    const switchRequest = vi.fn(async (name) => {
      order.push(`switch:${name}`)
      return config
    })
    const commit = vi.fn((savedConfig) => order.push(`commit:${savedConfig.current_base}`))

    await expect(switchBaseSafely({
      name: 'work',
      flush,
      switchRequest,
      commit,
    })).resolves.toBeUndefined()

    expect(order).toEqual(['flush', 'switch:work', 'commit:work'])
  })

  it('stops switching when flushing fails', async () => {
    const error = new Error('save failed')
    const switchRequest = vi.fn()
    const commit = vi.fn()

    await expect(
      switchBaseSafely({
        name: 'work',
        flush: () => Promise.reject(error),
        switchRequest,
        commit,
      }),
    ).rejects.toBe(error)
    expect(switchRequest).not.toHaveBeenCalled()
    expect(commit).not.toHaveBeenCalled()
  })

  it('does not commit when switching fails', async () => {
    const error = new Error('switch failed')
    const commit = vi.fn()

    await expect(
      switchBaseSafely({
        name: 'work',
        flush: vi.fn(),
        switchRequest: () => Promise.reject(error),
        commit,
      }),
    ).rejects.toBe(error)
    expect(commit).not.toHaveBeenCalled()
  })
})
