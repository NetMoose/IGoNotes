import { afterEach, it, expect, vi } from 'vitest';

afterEach(() => { vi.unstubAllGlobals(); vi.resetModules(); });
it('restricts storage on every worker start and on install/browser startup without reading secrets', async () => {
  const installed = vi.fn(); const startup = vi.fn(); const setAccessLevel = vi.fn(async () => {});
  const get = vi.fn();
  vi.stubGlobal('chrome', { storage: { local: { setAccessLevel, get } }, runtime: {
    onInstalled: { addListener: installed }, onStartup: { addListener: startup },
  } });
  await import('../src/worker.js');
  expect(setAccessLevel).toHaveBeenCalledWith({ accessLevel: 'TRUSTED_CONTEXTS' });
  expect(installed).toHaveBeenCalledOnce(); expect(startup).toHaveBeenCalledOnce();
  await installed.mock.calls[0][0](); await startup.mock.calls[0][0]();
  expect(setAccessLevel).toHaveBeenCalledTimes(3);
  expect(get).not.toHaveBeenCalled();
});
