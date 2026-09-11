export function createGitStatusPoller({
  load,
  onStatuses,
  onError,
  interval = 2000,
  schedule = setTimeout,
  cancel = clearTimeout,
}) {
  let active = false
  let generation = 0
  let timer = null
  let chain = Promise.resolve()
  let callbackGeneration = null

  function clearTimer() {
    if (timer !== null) {
      cancel(timer)
      timer = null
    }
  }

  async function report(current, error) {
    callbackGeneration = current
    try {
      await onError(error)
    } catch {
    } finally {
      callbackGeneration = null
    }
  }

  async function run(current) {
    if (!active || current !== generation) {
      return null
    }

    try {
      const payload = await load()
      if (!active || current !== generation) {
        return null
      }

      callbackGeneration = current
      try {
        await onStatuses(payload.statuses)
      } finally {
        callbackGeneration = null
      }
      if (!active || current !== generation) {
        return null
      }

      await report(current, null)
      if (!active || current !== generation) {
        return null
      }

      return payload.statuses
    } catch (error) {
      if (active && current === generation) {
        await report(current, error)
      }
      return null
    } finally {
      if (active && current === generation) {
        let scheduledTimer
        scheduledTimer = schedule(() => {
          if (timer === scheduledTimer) {
            timer = null
          }
          if (active && current === generation) {
            void enqueue(current)
          }
        }, interval)
        timer = scheduledTimer
      }
    }
  }

  function enqueue(current) {
    const queued = chain.then(() => run(current))
    chain = queued.catch(() => null)
    return queued
  }

  return {
    start() {
      clearTimer()
      active = true
      generation += 1
      void enqueue(generation)
    },

    refresh() {
      clearTimer()
      if (!active) {
        return Promise.resolve(null)
      }

      generation += 1
      const queued = enqueue(generation)
      return callbackGeneration === null ? queued : Promise.resolve(null)
    },

    stop() {
      active = false
      generation += 1
      clearTimer()
    },
  }
}
