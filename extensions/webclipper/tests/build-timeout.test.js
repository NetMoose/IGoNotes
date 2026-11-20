import { spawnSync } from 'node:child_process';
import { resolve } from 'node:path';
import { expect, test } from 'vitest';

test('the packaging test aborts its child build on its own timeout', () => {
  const started = performance.now();
  const result = spawnSync(process.execPath, ['node_modules/vitest/vitest.mjs', 'run', 'tests/package.test.js'], {
    cwd: resolve(import.meta.dirname, '..'),
    env: { ...process.env, CLIPPER_BUILD_TIMEOUT_MS: '1' },
    encoding: 'utf8',
    timeout: 10000,
    killSignal: 'SIGKILL',
  });
  // The outer bound is only a failsafe: the inner build timeout must cause the failure.
  expect(result.error).toBeUndefined();
  expect(result.status).toBe(1);
  expect(result.stdout + result.stderr).toContain('ETIMEDOUT');
  expect(performance.now() - started).toBeLessThan(8000);
});
