export function createGitStatusPoller({
  load,
  onStatuses,
  onError,
  interval = 2000,
  schedule = setTimeout,
  cancel = clearTimeout,
}) {
  let active = false;
  let generation = 0;
  let timer = null;

  function clearTimer() {
    if (timer !== null) {
      cancel(timer);
      timer = null;
    }
  }

  async function run(current) {
    if (!active || current !== generation) {
      return null;
    }

    try {
      const payload = await load();
      if (!active || current !== generation) {
        return null;
      }

      onStatuses(payload.statuses);
      if (!active || current !== generation) {
        return null;
      }

      onError(null);
      return payload.statuses;
    } catch (error) {
      if (active && current === generation) {
        onError(error);
      }
      return null;
    } finally {
      if (active && current === generation) {
        let scheduledTimer;
        scheduledTimer = schedule(() => {
          if (timer === scheduledTimer) {
            timer = null;
          }
          void run(current);
        }, interval);
        timer = scheduledTimer;
      }
    }
  }

  return {
    start() {
      clearTimer();
      active = true;
      generation += 1;
      void run(generation);
    },

    refresh() {
      clearTimer();
      if (!active) {
        return Promise.resolve(null);
      }

      generation += 1;
      return run(generation);
    },

    stop() {
      active = false;
      generation += 1;
      clearTimer();
    },
  };
}
