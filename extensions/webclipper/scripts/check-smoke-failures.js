import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { resolve } from 'node:path';

// Run under xvfb-run, like the normal smoke. No mocks or product entry changes.
for (const fault of ['hang-evaluate', 'close-fail', 'close-hang', 'launch-hang']) {
  const started = performance.now();
  const run = spawnSync(process.execPath, ['scripts/smoke.js'], {
    cwd: resolve(import.meta.dirname, '..'),
    env: { ...process.env, CLIPPER_SMOKE_FAULT: fault, CLIPPER_SMOKE_TIMEOUT_MS: '4000' },
    encoding: 'utf8',
    timeout: 12000,
    killSignal: 'SIGKILL',
  });
  assert.equal(run.error, undefined, `outer failsafe must not fire: ${run.error}`);
  assert.equal(run.status, 1, `${fault} must fail on its own:\n${run.stdout}\n${run.stderr}`);
  assert.ok(run.stdout.includes(`SMOKE_FAULT_REACHED ${fault}`), `fault was not reached: ${run.stdout}`);
  assert.match(run.stderr, fault === 'close-fail' ? /Injected browser.close failure/ : /Smoke deadline exceeded/);
  const stateLine = run.stdout.split('\n').filter((line) => line.startsWith('SMOKE_RESOURCES ')).at(-1);
  assert.ok(stateLine, `missing ownership evidence: ${run.stdout}`);
  const state = JSON.parse(stateLine.slice('SMOKE_RESOURCES '.length));
  assert.ok(state.browserPid > 0);
  if (fault !== 'launch-hang') assert.ok(state.workerPid > 0);
  assert.equal(existsSync(state.profile), false, 'temporary profile removed');
  for (const pid of [state.browserPid, state.workerPid].filter(Boolean)) {
    assert.throws(() => process.kill(-pid, 0), { code: 'ESRCH' }, `owned process group ${pid} is gone`);
  }
  await assert.rejects(fetch(state.fixtureURL, { signal: AbortSignal.timeout(1000) }), 'fixture listener closed');
  assert.match(run.stdout, /SMOKE_CLEANUP \{"profileRemoved":true,"serverClosed":true,"processesStopped":true\}/);
  const elapsedMs = Math.round(performance.now() - started);
  assert.ok(elapsedMs < 10000, `${fault} exceeded its deadline plus cleanup allowance: ${elapsedMs}ms`);
  console.log(JSON.stringify({ fault, result: 'PASS', exitCode: run.status, elapsedMs, profileRemoved: true, processGroupsGone: true, serverClosed: true }));
}
