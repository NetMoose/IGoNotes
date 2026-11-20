import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { createConnection } from 'node:net';
import { join, resolve } from 'node:path';
import { openOwnership } from './ownership.js';

const repetitions = Number(process.argv[2] ?? 3);
assert.ok(Number.isInteger(repetitions) && repetitions >= 1 && repetitions <= 3);
assert.equal(process.platform, 'linux');
const root = resolve(import.meta.dirname, '../..'), cases = [];
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const same = (a, b) => a.dev === b.dev && a.ino === b.ino && b.isDirectory() && !b.isSymbolicLink();
const alive = pid => { try { process.kill(-pid, 0); return true; } catch (e) { if (e.code === 'ESRCH') return false; throw e; } };
const kill = pid => { try { process.kill(-pid, 'SIGKILL'); } catch (e) { if (e.code !== 'ESRCH') throw e; } };
async function until(predicate, ms) {
  const end = performance.now() + ms;
  do { const value = predicate(); if (value) return value; await sleep(10); } while (performance.now() < end);
  throw Error('profile replacement phase deadline');
}
const portClosed = port => new Promise(resolve => {
  const socket = createConnection({ host: '127.0.0.1', port });
  const done = closed => { socket.destroy(); resolve(closed); };
  socket.once('connect', () => done(false)); socket.once('error', () => done(true)); socket.setTimeout(300, () => done(false));
});
function removeTestDirectory(path, expected) {
  if (!path || !existsSync(path)) return;
  assert.ok(expected && same(expected, lstatSync(path)), 'TEST cleanup refuses a directory it did not create/observe');
  rmSync(path, { recursive: true, force: true });
}
let failed = false;
for (let repetition = 1; repetition <= repetitions; repetition++) for (const mode of ['standalone', 'inherited-global-scope']) {
  const parent = mkdtempSync(join(tmpdir(), 'clipper-profile-regression-')), parentIdentity = lstatSync(parent);
  let registry, controller, child, state, original, replacement, moved, stdout = '', stderr = '';
  const evidence = { repetition, mode, result: 'FAIL' };
  try {
    let temporary = parent;
    if (mode === 'inherited-global-scope') {
      registry = openOwnership({}, parent);
      temporary = registry.temporary(parent, 'clipper-browser-');
      controller = spawn(process.execPath, ['-e', 'setInterval(()=>{},1000)'], { detached: true, stdio: 'ignore', env: { ...process.env, ...registry.env } });
      registry.recordProcess(controller, 'command');
    }
    const env = { ...process.env, ...(registry?.env ?? {}), CLIPPER_SMOKE_TMP: temporary,
      CLIPPER_SMOKE_FAULT: 'hang-evaluate', CLIPPER_SMOKE_IMAGES: '0', CLIPPER_SMOKE_DELIVERY: '0', CLIPPER_SMOKE_RESTART: '0',
      CLIPPER_SMOKE_TIMEOUT_MS: '10000', CLIPPER_SMOKE_FAULT_TIMEOUT_MS: '1000' };
    if (!registry) for (const key of ['CLIPPER_RESOURCE_DIRECTORY', 'CLIPPER_RESOURCE_NONCE', 'CLIPPER_RESOURCE_SCOPE']) delete env[key];
    child = spawn(process.execPath, ['scripts/smoke.js'], { cwd: root, env, detached: true, stdio: ['ignore', 'pipe', 'pipe'] });
    child.stdout.on('data', bytes => { stdout += bytes; }); child.stderr.on('data', bytes => { stderr += bytes; });
    const exited = new Promise((resolve, reject) => { child.once('error', reject); child.once('exit', code => resolve(code)); });
    await until(() => stdout.split('\n').some(line => line.startsWith('SMOKE_FAULT_ARMED ') && JSON.parse(line.slice(18)).sessionReady === true), 5000);
    state = JSON.parse(stdout.split('\n').filter(line => line.startsWith('SMOKE_RESOURCES ')).at(-1).slice(16));
    assert.ok(state.browserPid > 0 && state.workerPid > 0);
    assert.ok(existsSync(join(state.profile, 'DevToolsActivePort')), 'fresh actual Chromium has native resources');
    process.kill(child.pid, 'SIGSTOP');
    original = lstatSync(state.profile); moved = state.profile + '-test-moved';
    renameSync(state.profile, moved); mkdirSync(state.profile);
    replacement = lstatSync(state.profile);
    const sentinel = join(state.profile, 'foreign-sentinel.txt'), content = 'test-created foreign replacement: preserve exact bytes';
    writeFileSync(sentinel, content, { flag: 'wx' });
    process.kill(child.pid, 'SIGCONT');
    await until(() => child.exitCode !== null || child.signalCode !== null, 15000);
    evidence.exitCode = await exited;
    evidence.sentinelSurvived = existsSync(sentinel) && readFileSync(sentinel, 'utf8') === content && same(replacement, lstatSync(state.profile));
    evidence.ownedGroupsGone = [state.browserPid, state.workerPid].every(pid => !alive(pid));
    evidence.serverClosed = await portClosed(Number(new URL(state.fixtureURL).port));
    evidence.parentControllerSurvived = !controller || (controller.exitCode === null && controller.signalCode === null && alive(controller.pid));
    const cleanupLine = stdout.split('\n').find(line => line.startsWith('SMOKE_OWNERSHIP_CLEANUP '));
    const cleanup = cleanupLine ? JSON.parse(cleanupLine.slice('SMOKE_OWNERSHIP_CLEANUP '.length)) : null;
    evidence.identityCleanupError = cleanup?.errors.includes('directory-removal') === true && cleanup.profilesRemoved === false;
    evidence.noFalseCleanupPASS = !stdout.includes('SMOKE_CLEANUP {');
    if (registry) {
      const ancestor = await registry.cleanup();
      evidence.ancestorPreserved = existsSync(temporary) && existsSync(sentinel) && ancestor.errors.includes('directory-removal');
    } else evidence.ancestorPreserved = existsSync(parent) && same(parentIdentity, lstatSync(parent)) && existsSync(sentinel);
    assert.equal(evidence.sentinelSurvived, true, 'ACTUAL smoke local cleanup must preserve the replacement sentinel');
    assert.equal(evidence.exitCode, 1);
    assert.match(stderr, /ownership cleanup failed/i);
    for (const key of ['ownedGroupsGone', 'serverClosed', 'parentControllerSurvived', 'identityCleanupError', 'noFalseCleanupPASS', 'ancestorPreserved']) assert.equal(evidence[key], true, key);
    evidence.result = 'PASS';
  } catch (error) { failed = true; console.error(`${mode}: ${error.message}`); }
  finally {
    // Only this test created the replacement and renamed the original. Remove
    // them AFTER assertions, with their captured inode/device identities.
    for (const pid of [child?.pid, state?.browserPid, state?.workerPid, controller?.pid].filter(Boolean)) kill(pid);
    if (child) await until(() => child.exitCode !== null || child.signalCode !== null, 2000).catch(() => {});
    removeTestDirectory(state?.profile, replacement);
    removeTestDirectory(moved, original);
    if (registry) { await registry.cleanup(); await registry.dispose(); }
    removeTestDirectory(parent, parentIdentity);
    cases.push(evidence); console.log(JSON.stringify(evidence));
  }
}
writeFileSync(resolve(import.meta.dirname, 'profile-results.json'), JSON.stringify({ schema: 1, result: failed ? 'FAIL' : 'PASS', cases, manualAcceptance: 'NOT RUN (task 9.2)' }, null, 2) + '\n');
process.exitCode = failed ? 1 : 0;
