import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { existsSync, mkdtempSync, readdirSync, readFileSync, readlinkSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { createConnection } from 'node:net';
import { join, resolve } from 'node:path';

// Independent black-box watchdog regressions. Inspect ONLY descendants of the
// child we launched and its private temp tree; never search browsers by name.
const root = resolve(import.meta.dirname, '../..');
const repetitions = Number(process.argv[2] ?? 3);
assert.ok(Number.isInteger(repetitions) && repetitions >= 1 && repetitions <= 3);
assert.equal(process.platform, 'linux');
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const children = pid => {
  try { return readFileSync(`/proc/${pid}/task/${pid}/children`, 'utf8').trim().split(/\s+/).filter(Boolean).map(Number); }
  catch (e) { if (e.code === 'ENOENT') return []; throw e; }
};
const alive = pid => { try { process.kill(-pid, 0); return true; } catch (e) { if (e.code === 'ESRCH') return false; throw e; } };
const kill = pid => { try { process.kill(-pid, 'SIGKILL'); } catch (e) { if (e.code !== 'ESRCH') throw e; } };
async function until(predicate, timeout) {
  const deadline = performance.now() + timeout;
  do { const value = predicate(); if (value) return value; await sleep(20); } while (performance.now() < deadline);
  throw Error('outer regression phase deadline');
}
function profiles(parent) {
  return readdirSync(parent, { withFileTypes: true }).filter(e => e.isDirectory()).flatMap(e => {
    const path = join(parent, e.name);
    return e.name.startsWith('clipper-profile-') ? [path] : e.name.startsWith('clipper-browser-') ? profiles(path) : [];
  });
}
function listenerPorts(pid) {
  const inodes = new Set(readdirSync(`/proc/${pid}/fd`).flatMap(fd => {
    try { return readlinkSync(`/proc/${pid}/fd/${fd}`).match(/^socket:\[(\d+)\]$/)?.[1] ?? []; } catch { return []; }
  }));
  return readFileSync('/proc/net/tcp', 'utf8').trim().split('\n').slice(1).flatMap(line => {
    const fields = line.trim().split(/\s+/);
    return fields[3] === '0A' && fields[1].startsWith('0100007F:') && inodes.has(fields[9]) ? [parseInt(fields[1].split(':')[1], 16)] : [];
  });
}
async function closeBrowser(profile) {
  if (!existsSync(join(profile, 'DevToolsActivePort'))) return;
  const [port, path] = readFileSync(join(profile, 'DevToolsActivePort'), 'utf8').trim().split('\n');
  await new Promise(resolve => {
    const socket = new WebSocket(`ws://127.0.0.1:${port}${path}`);
    const done = () => { clearTimeout(timer); try { socket.close(); } catch { /* Independent emergency group kill follows. */ } resolve(); };
    const timer = setTimeout(done, 700);
    socket.addEventListener('open', () => { try { socket.send(JSON.stringify({ id: 1, method: 'Browser.close' })); } catch { done(); } });
    socket.addEventListener('error', done); socket.addEventListener('close', done);
  });
}
let failed = false;
const cases = [];
const portClosed = port => new Promise(resolve => {
  const socket = createConnection({ host: '127.0.0.1', port });
  const done = closed => { socket.destroy(); resolve(closed); };
  socket.once('connect', () => done(false)); socket.once('error', () => done(true)); socket.setTimeout(300, () => done(false));
});
for (let repetition = 1; repetition <= repetitions; repetition++) for (const boundary of ['checker', 'suite']) {
  const parent = mkdtempSync(join(tmpdir(), 'clipper-outer-regression-'));
  let child, supervisor, checker, profile, groups = [], ports = [], stdout = '', stderr = '';
  try {
    child = spawn(process.execPath, boundary === 'checker' ? ['scripts/check-smoke-failures.js', '1'] : ['tests/browser/run.js'], {
      cwd: root, detached: true, stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env,
        CLIPPER_SMOKE_TMP: parent, CLIPPER_BROWSER_TMP: parent,
        CLIPPER_BROWSER_REPORT: join(parent, 'report.json'),
        // These change external test budgets only, never the normal smoke clocks.
        CLIPPER_SMOKE_CHECK_TIMEOUT_MS: '2500', CLIPPER_BROWSER_CHECKER_TIMEOUT_MS: '5000',
        CLIPPER_BROWSER_FAILSAFE_PROBE: boundary === 'suite' ? 'stopped-checker' : '',
      },
    });
    child.stdout.on('data', b => { stdout += b; }); child.stderr.on('data', b => { stderr += b; });
    const exited = new Promise((resolve, reject) => { child.once('error', reject); child.once('exit', (code, signal) => resolve({ code, signal })); });
    if (boundary === 'checker') { checker = child.pid; }
    else {
      // Before the probe switch exists, this also reaches the real suite's
      // checker after all functional modes; no fabricated ownership records.
      await until(() => stdout.includes('restart:') || stdout.includes('FAILSAFE_PROBE_READY'), 60000);
      checker = await until(() => children(child.pid)[0], 5000);
    }
    supervisor = await until(() => children(checker)[0], 5000);
    profile = await until(() => profiles(parent).find(p => existsSync(join(p, 'DevToolsActivePort'))), 5000);
    process.kill(supervisor, 'SIGSTOP');
    groups = children(supervisor).filter(pid => {
      try { return Number(readFileSync(`/proc/${pid}/stat`, 'utf8').split(') ')[1].split(' ')[2]) === pid; } catch { return false; }
    });
    assert.ok(groups.length > 0, 'real detached Chromium group exists before external watchdog');
    ports = listenerPorts(supervisor); assert.equal(ports.length, 1, 'real fixture listener exists');
    if (boundary === 'suite') process.kill(checker, 'SIGSTOP');
    await until(() => child.exitCode !== null || child.signalCode !== null, boundary === 'checker' ? 22000 : 95000);
    const exit = await exited;
    const leaked = groups.filter(alive).length;
    const profileDeleted = !existsSync(profile);
    const portsClosed = (await Promise.all(ports.map(portClosed))).every(Boolean);
    const evidence = { repetition, boundary, exitCode: exit.code, ownedGroupsAlive: leaked, profileDeleted, portsClosed, resourcesForwarded: stdout.includes('SMOKE_RESOURCES '), result: leaked === 0 && profileDeleted && portsClosed && exit.code !== 0 ? 'PASS' : 'FAIL' };
    cases.push(evidence); console.log(JSON.stringify(evidence));
    assert.notEqual(exit.code, 0, 'the original external timeout must remain failure');
    assert.match(stderr, /ETIMEDOUT|timeout|failsafe|deadline/i, 'original failure is reported');
    assert.equal(leaked, 0, 'external fallback must remove detached groups without stdout ownership');
    assert.equal(profileDeleted, true, 'external fallback removes only its generated profile');
    assert.equal(portsClosed, true);
    if (boundary === 'suite') {
      const report = JSON.parse(readFileSync(join(parent, 'report.json'), 'utf8'));
      assert.equal(report.result, 'FAIL'); assert.equal(report.failedPhase, 'five supervisor fault cases');
      assert.equal(stdout.includes('SMOKE_RESOURCES '), false, 'root fallback is proved without streamed resource records');
    }
  } catch (error) {
    failed = true;
    const evidence = cases.find(c => c.repetition === repetition && c.boundary === boundary);
    if (evidence) evidence.result = 'FAIL'; else cases.push({ repetition, boundary, result: 'FAIL' });
    console.error(`${boundary}: ${error.message}`);
  }
  finally {
    // The regression's own emergency cleanup runs even on RED so demonstration
    // of a leak cannot leave stopped supervisors or browsers on the host.
    for (const pid of [supervisor, checker, child?.pid].filter(Boolean)) {
      try { process.kill(pid, 'SIGKILL'); } catch (e) { if (e.code !== 'ESRCH') throw e; }
    }
    if (profile) await closeBrowser(profile);
    for (const pid of [...groups, child?.pid].filter(Boolean)) kill(pid);
    if (child) await until(() => child.exitCode !== null || child.signalCode !== null, 2000).catch(() => {});
    rmSync(parent, { recursive: true, force: true });
  }
}
writeFileSync(resolve(import.meta.dirname, 'outer-results.json'), JSON.stringify({ schema: 1, platform: process.platform, result: failed ? 'FAIL' : 'PASS', cases, manualAcceptance: 'NOT RUN (task 9.2)' }, null, 2) + '\n');
process.exitCode = failed ? 1 : 0;
