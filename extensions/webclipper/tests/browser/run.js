import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { existsSync, writeFileSync } from 'node:fs';
import { tmpdir, release, arch } from 'node:os';
import { join, resolve } from 'node:path';
import { modeReport } from './report.js';
import { openOwnership, runOwned } from './ownership.js';

const root = resolve(import.meta.dirname, '../..'), repo = resolve(root, '../..');
const summary = { schema: 1, startedAt: new Date().toISOString(), os: { platform: process.platform, release: release(), arch: arch() }, modes: [], manualAcceptance: 'NOT RUN (task 9.2)' };
const start = performance.now();
let temporary, xvfb, ownership, phase = 'prerequisites';
const integer = (name, fallback, max) => {
  const n = Number(process.env[name] ?? fallback);
  assert.ok(Number.isSafeInteger(n) && n > 0 && n <= max, `${name} must be 1..${max}`); return n;
};
const suiteMs = integer('CLIPPER_BROWSER_TIMEOUT_MS', 600000, 900000);
const modeMs = integer('CLIPPER_BROWSER_MODE_TIMEOUT_MS', process.env.CLIPPER_SMOKE_TIMEOUT_MS ?? 120000, 180000);
const checkerMs = integer('CLIPPER_BROWSER_CHECKER_TIMEOUT_MS', 90000, 90000);
summary.deadlines = { suiteMs, modeMs, checkerMs };
const remaining = limit => Math.max(1, Math.min(limit, suiteMs - (performance.now() - start)));
async function run(command, args, env, limit) {
  assert.ok(remaining(suiteMs) > 8000, 'Suite deadline exhausted before command and independent cleanup');
  const timeout = Math.floor(Math.min(limit, remaining(suiteMs) - 8000));
  const child = await runOwned(command, args, { ownership, cwd: command === 'go' ? repo : root, env, timeout });
  assert.ok(!child.error && child.status === 0, `${phase} failed (${child.error?.code ?? child.status}); check prerequisites and rerun the documented standalone mode`);
  assert.deepEqual(child.cleanup.errors, [], `${phase}: owned resource cleanup failed`);
  return child.stdout;
}
try {
  assert.equal(process.platform, 'linux', 'test:browser requires Linux; use the separate manual matrix on other platforms');
  assert.equal(process.versions.node.split('.')[0], '24', 'Use Node.js 24');
  assert.ok(existsSync(join(root, 'dist/manifest.json')), 'Run npm --prefix extensions/webclipper run verify first');
  assert.ok(existsSync(join(repo, 'web/dist/index.html')), 'Build the embedded app first: Node.js 24; npm --prefix web ci; npm --prefix web run build');
  const parent = process.env.CLIPPER_BROWSER_TMP ?? process.env.CLIPPER_SMOKE_TMP ?? tmpdir();
  ownership = openOwnership(process.env, parent);
  temporary = ownership.temporary(parent, 'clipper-browser-');
  const env = { ...process.env, ...ownership.env, CLIPPER_SMOKE_TMP: temporary, CLIPPER_SMOKE_IMAGES: '0', CLIPPER_SMOKE_DELIVERY: '0', CLIPPER_SMOKE_RESTART: '0' };
  delete env.CLIPPER_SMOKE_FAULT;
  if (!env.DISPLAY) {
    phase = 'Xvfb (install Xvfb or provide DISPLAY)';
    xvfb = spawn('Xvfb', ['-displayfd', '3', '-screen', '0', '1280x900x24', '-nolisten', 'tcp'], { detached: true, env: { ...process.env, ...ownership.env }, stdio: ['ignore', 'ignore', 'ignore', 'pipe'] });
    ownership.recordProcess(xvfb, 'display');
    env.DISPLAY = await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(Error('Xvfb readiness timeout')), 5000);
      xvfb.once('error', e => { clearTimeout(timer); reject(e); });
      xvfb.once('exit', () => { clearTimeout(timer); reject(Error('Xvfb stopped')); });
      xvfb.stdio[3].once('data', bytes => { clearTimeout(timer); const display = bytes.toString().trim(); /^\d+$/.test(display) ? resolve(`:${display}`) : reject(Error('Invalid Xvfb display')); });
    });
  }
  summary.xvfbLaunched = Boolean(xvfb);
  const probe = env.CLIPPER_BROWSER_FAILSAFE_PROBE === 'stopped-checker';
  if (env.CLIPPER_BROWSER_FAILSAFE_PROBE && !probe) throw Error('Invalid failsafe probe');
  if (!probe) {
    phase = 'current-checkout Go build (Go 1.26+, cached modules and fresh web/dist required)';
    const binary = join(temporary, 'igonotes');
    await run('go', ['build', '-trimpath', '-o', binary, './cmd/api'], { ...env, GOPROXY: 'off', GOSUMDB: 'off' }, 120000);
    // go build runs at the repository root, independent of npm's calling directory.
    env.CLIPPER_SMOKE_GO_BINARY = binary;
    for (const mode of ['normal', 'images', 'delivery', 'restart']) {
      phase = mode;
      assert.ok(remaining(suiteMs) > 10000, 'Suite budget exhausted before mode plus cleanup');
      const deadline = Math.floor(Math.min(modeMs, remaining(suiteMs) - 10000));
      const modeEnv = { ...env, CLIPPER_SMOKE_TIMEOUT_MS: String(deadline),
        CLIPPER_SMOKE_IMAGES: mode === 'images' ? '1' : '0', CLIPPER_SMOKE_DELIVERY: mode === 'delivery' ? '1' : '0', CLIPPER_SMOKE_RESTART: mode === 'restart' ? '1' : '0' };
      // Machine evidence is emitted only after the supervisor has cleaned up.
      const stdout = await run(process.execPath, ['scripts/smoke.js'], modeEnv, deadline + 10000);
      assert.ok(stdout.includes('SMOKE_CLEANUP {"profileRemoved":true,"serverClosed":true,"processesStopped":true}'), `${mode}: missing cleanup evidence`);
      const marker = stdout.split('\n').find(l => l.startsWith('BROWSER_EVIDENCE '));
      assert.ok(marker, `${mode}: missing evidence`);
      const report = modeReport(mode, JSON.parse(marker.slice(17)));
      summary.modes.push(report);
      assert.ok(report.passed && report.browser, `${mode}: missing required case evidence`);
      console.log(`${mode}: ${report.cases.length} cases PASS (Chromium ${report.browser})`);
    }
  } else { summary.failsafeProbe = true; console.log('FAILSAFE_PROBE_READY'); }
  phase = 'five supervisor fault cases';
  assert.equal(remaining(checkerMs), checkerMs, 'Not enough suite budget for the bounded five-case fault matrix');
  const faults = (await run(process.execPath, ['scripts/check-smoke-failures.js', '1'], env, checkerMs)).trim().split('\n').map(line => JSON.parse(line));
  if (probe) throw Error('Failsafe probe unexpectedly completed without external timeout');
  assert.equal(faults.length, 5);
  summary.faults = faults.map(({ fault, watchdog, result }) => ({ fault, watchdog, result }));
  assert.ok(faults.every(f => f.result === 'PASS'));
  summary.result = 'PASS';
} catch (error) {
  summary.result = 'FAIL'; summary.failedPhase = phase;
  if (['normal', 'images', 'delivery', 'restart'].includes(phase) && !summary.modes.some(m => m.mode === phase)) summary.modes.push(modeReport(phase));
  // Error messages above are controlled; worker stderr/DOM never enters reports.
  console.error(`Browser suite failed in ${phase}. ${error.message}`);
  process.exitCode = 1;
} finally {
  if (ownership) {
    try {
      summary.cleanup = await ownership.cleanup();
      if (summary.cleanup.errors.length) { summary.result = 'FAIL'; summary.failedPhase ??= 'cleanup'; process.exitCode = 1; }
    } catch { summary.result = 'FAIL'; summary.failedPhase ??= 'cleanup'; process.exitCode = 1; }
    try { await ownership.dispose(); }
    catch { summary.result = 'FAIL'; summary.failedPhase ??= 'cleanup'; process.exitCode = 1; }
  }
  summary.elapsedMs = Math.round(performance.now() - start);
  const path = resolve(process.env.CLIPPER_BROWSER_REPORT ?? join(import.meta.dirname, 'results.json'));
  writeFileSync(path, JSON.stringify(summary, null, 2) + '\n');
  console.log('Browser JSON summary written (tests/browser/results.json by default).');
}
