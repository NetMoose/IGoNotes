import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { resolve } from 'node:path';

// Run under xvfb-run, like the normal smoke. No mocks or product entry changes.
const repetitions = Number(process.argv[2] ?? 3);
assert.ok(Number.isSafeInteger(repetitions) && repetitions >= 1 && repetitions <= 5, 'repetitions must be 1–5');
const cases = ['hang-evaluate', 'close-fail', 'close-hang', 'launch-hang'].map(fault => ({ fault }));
// Prove the independent overall watchdog still wins if the phase clock is longer.
cases.push({ fault: 'launch-hang', overallTimeoutMs: 1000, faultTimeoutMs: 10000, watchdog: true });
for (let repetition = 1; repetition <= repetitions; repetition++) for (const { fault, overallTimeoutMs = 10000, faultTimeoutMs = 1000, watchdog = false } of cases) {
  const started = performance.now();
  const run = spawnSync(process.execPath, ['scripts/smoke.js'], {
    cwd: resolve(import.meta.dirname, '..'),
    env: { ...process.env, CLIPPER_SMOKE_FAULT: fault, CLIPPER_SMOKE_IMAGES: '0', CLIPPER_SMOKE_TIMEOUT_MS: String(overallTimeoutMs), CLIPPER_SMOKE_FAULT_TIMEOUT_MS: String(faultTimeoutMs) },
    encoding: 'utf8',
    timeout: 18000,
    killSignal: 'SIGKILL',
  });
  assert.equal(run.error, undefined, `outer failsafe must not fire: ${run.error}`);
  assert.equal(run.status, 1, `${fault} must fail on its own:\n${run.stdout}\n${run.stderr}`);
  assert.ok(run.stdout.includes(`SMOKE_FAULT_REACHED ${fault}`), `fault was not reached: ${run.stdout}`);
  const armedLine = run.stdout.split('\n').find(line => line.startsWith('SMOKE_FAULT_ARMED '));
  assert.ok(armedLine, `missing phase barrier: ${run.stdout}`);
  assert.ok(run.stdout.indexOf(armedLine) < run.stdout.indexOf(`SMOKE_FAULT_REACHED ${fault}`), 'parent watchdog was armed before fault execution');
  const armed = JSON.parse(armedLine.slice('SMOKE_FAULT_ARMED '.length));
  assert.equal(armed.fault, fault);
  assert.equal(armed.overallTimeoutMs, overallTimeoutMs);
  assert.equal(armed.faultTimeoutMs, faultTimeoutMs);
  assert.equal(armed.sessionReady, fault !== 'launch-hang');
  assert.ok(armed.elapsedMs < overallTimeoutMs, 'setup reached the phase before the overall deadline');
  if (fault !== 'launch-hang') assert.match(armed.browser, /^\d+\.\d+\.\d+\.\d+$/, 'real Chromium session version');
  if (fault === 'close-fail') {
    assert.match(run.stderr, /Injected browser.close failure/);
    assert.doesNotMatch(run.stderr, /deadline exceeded/, 'the close rejection itself must fail the worker');
  } else {
    assert.ok(run.stderr.includes(watchdog ? `Smoke deadline exceeded (${overallTimeoutMs}ms)` : `Smoke fault deadline exceeded (${faultTimeoutMs}ms)`), run.stderr);
    assert.doesNotMatch(run.stderr, watchdog ? /Smoke fault deadline exceeded/ : /Smoke deadline exceeded/, 'only the intended watchdog fired');
  }
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
  assert.ok(elapsedMs < overallTimeoutMs + 6000, `${fault} exceeded its overall deadline plus cleanup allowance: ${elapsedMs}ms`);
  assert.ok(elapsedMs - armed.elapsedMs < (watchdog ? overallTimeoutMs : faultTimeoutMs) + 5000, 'phase failure and independent cleanup remained bounded');
  console.log(JSON.stringify({ repetition, fault, watchdog, result: 'PASS', exitCode: run.status, elapsedMs, setupMs: armed.elapsedMs, overallTimeoutMs, faultTimeoutMs, browser: armed.browser, sandbox: process.env.CLIPPER_SMOKE_NO_SANDBOX !== '1', profileRemoved: true, processGroupsGone: true, serverClosed: true }));
}
