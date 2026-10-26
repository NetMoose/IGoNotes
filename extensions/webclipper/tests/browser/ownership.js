import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { appendFileSync, chmodSync, lstatSync, mkdtempSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve, isAbsolute, basename, sep } from 'node:path';
import { createConnection } from 'node:net';

const scopePattern = /^root(?:\/[a-f0-9]{32})*$/;
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const bounded = async (operation, ms) => {
  let timer;
  try { return await Promise.race([operation, new Promise((_, reject) => { timer = setTimeout(() => reject(Error('cleanup deadline')), ms); })]); }
  finally { clearTimeout(timer); }
};
function identity(pid) {
  try {
    const value = readFileSync(`/proc/${pid}/stat`, 'utf8');
    const fields = value.slice(value.lastIndexOf(') ') + 2).split(' ');
    return { group: Number(fields[2]), start: fields[19] };
  } catch (e) { if (e.code === 'ENOENT' || e.code === 'ESRCH') return null; throw e; }
}
function groupExists(pid) {
  try { process.kill(-pid, 0); return true; } catch (e) { if (e.code === 'ESRCH') return false; throw e; }
}
function belongsToRun(record, nonce) {
  if (!groupExists(record.pid)) return false;
  const leader = identity(record.pid);
  if (leader?.group === record.pid && leader.start === record.start) return true;
  // A leader can exit before its group. A recycled numeric PGID alone is NOT
  // authority: require a surviving member with this private run's environment.
  return readdirSync('/proc').filter(name => /^\d+$/.test(name)).some(name => {
    try {
      if (identity(Number(name))?.group !== record.pid) return false;
      const environment = readFileSync(`/proc/${name}/environ`, 'utf8').split('\0');
      const scope = environment.find(value => value.startsWith('CLIPPER_RESOURCE_SCOPE='))?.slice('CLIPPER_RESOURCE_SCOPE='.length);
      return environment.includes(`CLIPPER_RESOURCE_NONCE=${nonce}`) && (scope === record.scope || scope?.startsWith(record.scope + '/'));
    }
    catch (e) { if (['ENOENT', 'ESRCH', 'EACCES', 'EPERM'].includes(e.code)) return false; throw e; }
  });
}
async function closeBrowser(profile) {
  let endpoint;
  try {
    const [port, path] = readFileSync(join(profile, 'DevToolsActivePort'), 'utf8').trim().split('\n');
    if (!/^\d+$/.test(port) || !/^\/devtools\/browser\/[a-f0-9-]+$/.test(path)) return;
    endpoint = `ws://127.0.0.1:${port}${path}`;
  } catch (e) { if (e.code === 'ENOENT') return; throw e; }
  await new Promise(resolve => {
    const socket = new WebSocket(endpoint);
    const done = () => { clearTimeout(timer); try { socket.close(); } catch { /* Group fallback is independent. */ } resolve(); };
    const timer = setTimeout(done, 700);
    socket.addEventListener('open', () => { try { socket.send(JSON.stringify({ id: 1, method: 'Browser.close' })); } catch { done(); } });
    socket.addEventListener('error', done); socket.addEventListener('close', done);
  });
}
const listenerClosed = port => new Promise(resolve => {
  const socket = createConnection({ host: '127.0.0.1', port });
  const done = closed => { socket.destroy(); resolve(closed); };
  socket.setTimeout(300, () => done(false));
  socket.once('connect', () => done(false)); socket.once('error', () => done(true));
});

export function openOwnership(env = process.env, parent = tmpdir()) {
  const inherited = Boolean(env.CLIPPER_RESOURCE_DIRECTORY);
  const directory = inherited ? resolve(env.CLIPPER_RESOURCE_DIRECTORY) : mkdtempSync(join(resolve(parent), 'clipper-ownership-'));
  const nonce = inherited ? env.CLIPPER_RESOURCE_NONCE : randomBytes(32).toString('hex');
  const scope = inherited ? env.CLIPPER_RESOURCE_SCOPE : 'root';
  if (!/^[a-f0-9]{64}$/.test(nonce) || !scopePattern.test(scope)) throw Error('Invalid ownership run');
  if (!inherited) {
    chmodSync(directory, 0o700);
    writeFileSync(join(directory, 'run.json'), JSON.stringify({ nonce }), { mode: 0o600, flag: 'wx' });
    writeFileSync(join(directory, 'resources.jsonl'), '', { mode: 0o600, flag: 'wx' });
  }
  for (const [path, mode] of [[directory, 0o700], [join(directory, 'run.json'), 0o600], [join(directory, 'resources.jsonl'), 0o600]]) {
    const stat = lstatSync(path);
    if (stat.isSymbolicLink() || stat.uid !== process.getuid() || (stat.mode & 0o777) !== mode) throw Error('Untrusted ownership storage');
  }
  if (JSON.parse(readFileSync(join(directory, 'run.json'), 'utf8')).nonce !== nonce) throw Error('Foreign ownership run');
  const registryIdentity = lstatSync(directory);
  const append = record => appendFileSync(join(directory, 'resources.jsonl'), JSON.stringify({ nonce, ...record }) + '\n');
  const owner = {
    owner: !inherited, directory, scope,
    env: { CLIPPER_RESOURCE_DIRECTORY: directory, CLIPPER_RESOURCE_NONCE: nonce, CLIPPER_RESOURCE_SCOPE: scope },
    newScope: () => `${scope}/${randomBytes(16).toString('hex')}`,
    recordProcess(child, role, childScope = scope, profile) {
      if (!child.pid) return;
      const stat = identity(child.pid);
      if (stat && stat.group !== child.pid) throw Error('Owned process must have a private group');
      append({ kind: 'process', scope: childScope, pid: child.pid, start: stat?.start ?? null, role, ...(profile ? { profile } : {}) });
    },
    temporary(parent, prefix) {
      if (!['clipper-profile-', 'clipper-browser-'].includes(prefix)) throw Error('Invalid owned directory prefix');
      const path = mkdtempSync(join(resolve(parent), prefix));
      const stat = lstatSync(path);
      append({ kind: 'directory', role: prefix === 'clipper-profile-' ? 'profile' : 'suite', scope, path, dev: stat.dev, ino: stat.ino });
      return path;
    },
    recordListener(port) { append({ kind: 'listener', scope, port, pid: process.pid }); },
    async cleanup(selectedScope = scope) {
      if (!scopePattern.test(selectedScope) || !(selectedScope === scope || selectedScope.startsWith(scope + '/'))) throw Error('Foreign cleanup scope');
      const errors = [];
      const read = () => readFileSync(join(directory, 'resources.jsonl'), 'utf8').split('\n').filter(Boolean).flatMap(line => {
        try {
          const r = JSON.parse(line);
          if (r.nonce !== nonce || !scopePattern.test(r.scope) || !['directory', 'process', 'listener'].includes(r.kind)
            || (r.kind === 'process' && (!Number.isSafeInteger(r.pid) || r.pid <= 1 || !['command', 'browser', 'worker', 'backend', 'display'].includes(r.role) || !(r.start === null || /^\d+$/.test(r.start))))
            || (r.kind === 'listener' && (!Number.isSafeInteger(r.port) || r.port < 1 || r.port > 65535 || !Number.isSafeInteger(r.pid) || r.pid <= 1))
            || (r.kind === 'directory' && (typeof r.path !== 'string' || !isAbsolute(r.path) || !['profile', 'suite'].includes(r.role)
              || !basename(r.path).startsWith(r.role === 'profile' ? 'clipper-profile-' : 'clipper-browser-') || !Number.isSafeInteger(r.dev) || !Number.isSafeInteger(r.ino)))) throw Error();
          return r.scope === selectedScope || r.scope.startsWith(selectedScope + '/') ? [r] : [];
        } catch { errors.push('invalid-record'); return []; }
      });
      const owned = r => { try { return belongsToRun(r, nonce); } catch { errors.push('group-identity'); return false; } };
      const signal = r => {
        try { if (r.pid !== process.pid && owned(r)) process.kill(-r.pid, 'SIGKILL'); }
        catch (e) { if (e.code !== 'ESRCH') errors.push('group-signal'); }
      };
      // Stop allocators first, including a SIGSTOP'ed checker/supervisor. Re-read
      // the synchronous journal after that, independently of their stdout pipes.
      for (const r of read().filter(r => r.kind === 'process' && r.role === 'command')) signal(r);
      const records = read(), processes = records.filter(r => r.kind === 'process' && r.pid !== process.pid);
      const directoryMatches = r => {
        try { const stat = lstatSync(r.path); return stat.isDirectory() && !stat.isSymbolicLink() && stat.dev === r.dev && stat.ino === r.ino; }
        catch (e) { if (e.code === 'ENOENT') return false; throw e; }
      };
      const settled = await Promise.allSettled(processes.filter(r => r.role === 'browser' && owned(r)).map(async r => {
        const profile = records.find(d => d.kind === 'directory' && d.role === 'profile' && d.path === r.profile);
        if (!profile || !directoryMatches(profile)) throw Error('Browser profile identity changed');
        await closeBrowser(r.profile);
      }));
      if (settled.some(r => r.status === 'rejected')) errors.push('browser-close');
      for (const r of processes) signal(r);
      const deadline = performance.now() + 2000;
      const live = () => processes.some(owned);
      while (live() && performance.now() < deadline) await sleep(20);
      const processGroupsGone = !live();
      // Each removal is identity-pinned and independently bounded. Never delete
      // an arbitrary path from stdout or a different inode substituted later.
      const remove = async r => {
        if (!processGroupsGone) throw Error('Owned process groups must stop before directory removal');
        let stat;
        try { stat = lstatSync(r.path); } catch (e) { if (e.code === 'ENOENT') return; throw e; }
        if (!stat.isDirectory() || stat.isSymbolicLink() || stat.dev !== r.dev || stat.ino !== r.ino) throw Error('Directory identity changed');
        await bounded(rm(r.path, { recursive: true, force: true }), 2000);
      };
      const dirs = records.filter(r => r.kind === 'directory');
      // Profiles precede their suite parent, avoiding concurrent recursive rm.
      const profileDirs = dirs.filter(r => r.role === 'profile');
      const profiles = await Promise.allSettled(profileDirs.map(remove));
      const protectedPaths = profileDirs.filter((_, i) => profiles[i].status === 'rejected').map(r => r.path);
      const parents = await Promise.allSettled(dirs.filter(r => r.role === 'suite').map(async r => {
        if (protectedPaths.some(path => path.startsWith(r.path + sep))) throw Error('Parent contains an unverified descendant');
        await remove(r);
      }));
      const profilesRemoved = [...profiles, ...parents].every(r => r.status === 'fulfilled');
      const listeners = await Promise.all(records.filter(r => r.kind === 'listener').map(r => listenerClosed(r.port)));
      if (!processGroupsGone) errors.push('group-survived');
      if (!profilesRemoved) errors.push('directory-removal');
      if (!listeners.every(Boolean)) errors.push('listener-survived');
      return { processGroupsGone, profilesRemoved, listenersClosed: listeners.every(Boolean), errors };
    },
    async dispose() {
      if (inherited) return;
      let current;
      try { current = lstatSync(directory); } catch (e) { if (e.code === 'ENOENT') return; throw e; }
      if (!current.isDirectory() || current.isSymbolicLink() || current.dev !== registryIdentity.dev || current.ino !== registryIdentity.ino) throw Error('Registry identity changed');
      // A registry can itself be a caller-supplied temp parent. Never bypass
      // profile identity/error handling by recursively disposing its ancestor.
      for (const line of readFileSync(join(directory, 'resources.jsonl'), 'utf8').split('\n').filter(Boolean)) {
        const record = JSON.parse(line);
        if (record.nonce !== nonce) throw Error('Foreign ownership record');
        if (record.kind === 'directory' && typeof record.path === 'string' && record.path.startsWith(directory + sep)) {
          try { lstatSync(record.path); } catch (e) { if (e.code === 'ENOENT') continue; throw e; }
          throw Error('Registry contains an unresolved owned directory');
        }
      }
      await bounded(rm(directory, { recursive: true, force: true }), 2000);
    },
  };
  return owner;
}

// An async parent can register PID/PG identity synchronously BEFORE any wait.
// Persistent ownership also survives this parent's own SIGSTOP/SIGKILL/timeout.
export async function runOwned(command, args, { ownership, env = process.env, cwd, timeout }) {
  const scope = ownership.newScope();
  const child = spawn(command, args, { cwd, env: { ...env, ...ownership.env, CLIPPER_RESOURCE_SCOPE: scope }, detached: true, stdio: ['ignore', 'pipe', 'pipe'] });
  try { ownership.recordProcess(child, 'command', scope); }
  catch (error) {
    if (child.pid) { try { process.kill(-child.pid, 'SIGKILL'); } catch (e) { if (e.code !== 'ESRCH') throw e; } }
    throw error;
  }
  let stdout = '', stderr = '', timer;
  child.stdout.on('data', b => { stdout = (stdout + b).slice(-4 * 1024 * 1024); });
  child.stderr.on('data', b => { stderr = (stderr + b).slice(-4 * 1024 * 1024); });
  const result = await new Promise(resolve => {
    timer = setTimeout(() => resolve({ status: null, error: Object.assign(Error('Owned command external timeout (ETIMEDOUT)'), { code: 'ETIMEDOUT' }) }), timeout);
    child.once('error', error => resolve({ status: null, error }));
    child.once('exit', (status, signal) => resolve({ status, signal }));
  });
  clearTimeout(timer);
  // Direct-child termination is independent of registry IO/cleanup failures.
  // Node still owns this live child, so its PID cannot have been recycled.
  if (child.pid && child.exitCode === null && child.signalCode === null) {
    try { process.kill(-child.pid, 'SIGKILL'); } catch (e) { if (e.code !== 'ESRCH') throw e; }
  }
  let cleanup;
  try { cleanup = await ownership.cleanup(scope); }
  catch { cleanup = { processGroupsGone: false, profilesRemoved: false, listenersClosed: false, errors: ['registry-cleanup'] }; }
  // Kill/reap closes inherited stdout/stderr too; don't let pipe descendants
  // turn an exit event into an unbounded wait for ChildProcess.close.
  child.stdout.destroy(); child.stderr.destroy();
  return { ...result, stdout, stderr, cleanup };
}
