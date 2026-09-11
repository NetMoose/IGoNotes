import { afterEach, describe, expect, it, vi } from 'vitest'

import { createGitStatusPoller } from './git-status-poller.js'

function deferred() {
  let resolve
  let reject
  const promise = new Promise((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })
  return { promise, resolve, reject }
}

describe('createGitStatusPoller', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('awaits asynchronous status and success callbacks before scheduling', async () => {
    const statusesDone = deferred()
    const errorDone = deferred()
    const sequence = []
    const schedule = vi.fn()
    const poller = createGitStatusPoller({
      load: vi.fn(async () => {
        sequence.push('load')
        return { statuses: ['ready'] }
      }),
      onStatuses: vi.fn(async () => {
        sequence.push('statuses')
        await statusesDone.promise
      }),
      onError: vi.fn(async (error) => {
        sequence.push(`error:${error}`)
        await errorDone.promise
      }),
      schedule,
    })

    poller.start()
    await Promise.resolve()
    await Promise.resolve()

    expect(sequence).toEqual(['load', 'statuses'])
    expect(schedule).not.toHaveBeenCalled()

    statusesDone.resolve()
    await vi.waitFor(() => {
      expect(sequence).toEqual(['load', 'statuses', 'error:null'])
    })
    expect(schedule).not.toHaveBeenCalled()

    errorDone.resolve()
    await vi.waitFor(() => {
      expect(schedule).toHaveBeenCalledWith(expect.any(Function), 2000)
    })
  })

  it('serializes refresh behind an older load and discards the stale result', async () => {
    const older = deferred()
    const current = deferred()
    const onStatuses = vi.fn()
    const load = vi.fn()
      .mockReturnValueOnce(older.promise)
      .mockReturnValueOnce(current.promise)
    const poller = createGitStatusPoller({
      load,
      onStatuses,
      onError: vi.fn(),
      schedule: vi.fn(),
    })

    poller.start()
    await Promise.resolve()
    const refresh = poller.refresh()
    await Promise.resolve()
    expect(load).toHaveBeenCalledTimes(1)

    older.resolve({ statuses: ['old'] })
    await vi.waitFor(() => {
      expect(load).toHaveBeenCalledTimes(2)
    })
    expect(onStatuses).not.toHaveBeenCalled()

    current.resolve({ statuses: ['current'] })
    await expect(refresh).resolves.toEqual(['current'])
    expect(onStatuses).toHaveBeenCalledExactlyOnceWith(['current'])
  })

  it('reports load and callback errors without leaking rejected callbacks', async () => {
    const schedule = vi.fn()
    const loadError = new Error('offline')
    const poller = createGitStatusPoller({
      load: vi.fn().mockRejectedValueOnce(loadError).mockResolvedValueOnce({ statuses: [] }),
      onStatuses: vi.fn().mockRejectedValue(new Error('statuses consumer failed')),
      onError: vi.fn().mockRejectedValue(new Error('error consumer failed')),
      schedule,
    })

    poller.start()
    await vi.waitFor(() => {
      expect(schedule).toHaveBeenCalledTimes(1)
    })
    const next = schedule.mock.calls[0][0]
    next()
    await vi.waitFor(() => {
      expect(schedule).toHaveBeenCalledTimes(2)
    })
  })

  it('invalidates deferred work when stopped', async () => {
    const pending = deferred()
    const schedule = vi.fn()
    const onStatuses = vi.fn()
    const onError = vi.fn()
    const poller = createGitStatusPoller({
      load: vi.fn().mockReturnValue(pending.promise),
      onStatuses,
      onError,
      schedule,
      cancel: vi.fn(),
    })

    poller.start()
    await Promise.resolve()
    poller.stop()
    pending.resolve({ statuses: ['late'] })
    await Promise.resolve()
    await Promise.resolve()

    expect(onStatuses).not.toHaveBeenCalled()
    expect(onError).not.toHaveBeenCalled()
    expect(schedule).not.toHaveBeenCalled()
  })
})
