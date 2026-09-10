import { afterEach, describe, expect, it, vi } from 'vitest';
import { createGitStatusPoller } from './git-status-poller.js';

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((promiseResolve, promiseReject) => {
    resolve = promiseResolve;
    reject = promiseReject;
  });
  return { promise, resolve, reject };
}

describe('createGitStatusPoller', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('loads immediately and continues at an exact two-second cadence', async () => {
    vi.useFakeTimers();
    const statuses = [{ base: 'notes', ahead: 1 }];
    const load = vi.fn().mockResolvedValue({ statuses });
    const onStatuses = vi.fn();
    const onError = vi.fn();
    const schedule = vi.fn((callback, delay) => setTimeout(callback, delay));
    const poller = createGitStatusPoller({ load, onStatuses, onError, schedule });

    poller.start();
    await vi.advanceTimersByTimeAsync(0);

    expect(load).toHaveBeenCalledTimes(1);
    expect(onStatuses).toHaveBeenCalledWith(statuses);
    expect(onError).toHaveBeenCalledWith(null);
    expect(schedule).toHaveBeenLastCalledWith(expect.any(Function), 2000);

    await vi.advanceTimersByTimeAsync(1999);
    expect(load).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(1);
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('reports an error without statuses and recovers on the next interval', async () => {
    vi.useFakeTimers();
    const error = new Error('offline');
    const recovered = [{ base: 'notes', ahead: 2 }];
    const load = vi
      .fn()
      .mockRejectedValueOnce(error)
      .mockResolvedValueOnce({ statuses: recovered });
    const onStatuses = vi.fn();
    const onError = vi.fn();
    const poller = createGitStatusPoller({ load, onStatuses, onError });

    poller.start();
    await vi.advanceTimersByTimeAsync(0);

    expect(onStatuses).not.toHaveBeenCalled();
    expect(onError).toHaveBeenCalledWith(error);

    await vi.advanceTimersByTimeAsync(2000);

    expect(onStatuses).toHaveBeenCalledTimes(1);
    expect(onStatuses).toHaveBeenCalledWith(recovered);
    expect(onError).toHaveBeenLastCalledWith(null);
  });

  it('refresh invalidates an older deferred response', async () => {
    vi.useFakeTimers();
    const older = deferred();
    const current = [{ base: 'notes', ahead: 3 }];
    const load = vi
      .fn()
      .mockReturnValueOnce(older.promise)
      .mockResolvedValueOnce({ statuses: current });
    const onStatuses = vi.fn();
    const onError = vi.fn();
    const poller = createGitStatusPoller({ load, onStatuses, onError });

    poller.start();
    const refresh = poller.refresh();
    await expect(refresh).resolves.toEqual(current);

    older.resolve({ statuses: [{ base: 'notes', ahead: 1 }] });
    await vi.advanceTimersByTimeAsync(0);

    expect(onStatuses).toHaveBeenCalledTimes(1);
    expect(onStatuses).toHaveBeenCalledWith(current);
    expect(onError).toHaveBeenCalledTimes(1);
  });

  it('stop cancels a timer and ignores a late deferred response', async () => {
    const late = deferred();
    const scheduled = [];
    const timer = { id: 'timer' };
    const schedule = vi.fn((callback) => {
      scheduled.push(callback);
      return timer;
    });
    const cancel = vi.fn();
    const load = vi
      .fn()
      .mockResolvedValueOnce({ statuses: [{ base: 'notes', ahead: 1 }] })
      .mockReturnValueOnce(late.promise);
    const onStatuses = vi.fn();
    const onError = vi.fn();
    const poller = createGitStatusPoller({
      load,
      onStatuses,
      onError,
      schedule,
      cancel,
    });

    poller.start();
    await Promise.resolve();
    expect(scheduled).toHaveLength(1);

    poller.stop();
    expect(cancel).toHaveBeenCalledWith(timer);

    scheduled[0]();
    late.resolve({ statuses: [{ base: 'notes', ahead: 4 }] });
    await Promise.resolve();

    expect(onStatuses).toHaveBeenCalledTimes(1);
    expect(onError).toHaveBeenCalledTimes(1);
    expect(schedule).toHaveBeenCalledTimes(1);
  });
});
