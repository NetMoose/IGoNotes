import { spawn } from 'node:child_process';
import { cpSync, mkdtempSync, readFileSync, copyFileSync } from 'node:fs';
import { rm } from 'node:fs/promises';
import { createServer } from 'node:http';
import { join, resolve } from 'node:path';

// Linux-only supervisor: Chromium and the worker each have a private process
// group created by us. Never search for or signal the user's browser processes.
if (process.platform !== 'linux') throw new Error('This headed smoke supervisor requires Linux/Xvfb');
const timeoutMs = Number(process.env.CLIPPER_SMOKE_TIMEOUT_MS ?? 30000);
if (!Number.isSafeInteger(timeoutMs) || timeoutMs <= 0) throw new Error('Invalid CLIPPER_SMOKE_TIMEOUT_MS');
const cleanupStepMs = 2000;
const dist = resolve(import.meta.dirname, '../dist');
const imageMode = process.env.CLIPPER_SMOKE_IMAGES === '1';
const imageRequests = [];
let profile;
let browser;
let worker;
let endpoint;
let fixtureURL;
let result;
let failure;
let browserLog = '';
const stopped = new AbortController();
// All resources are local. Live-page baseline requests are allowed; conversion
// must not activate lazy image URLs, copied CSS/frames or any other resource.
const fixture = readFileSync(new URL('../tests/fixtures/article.html', import.meta.url), 'utf8')
  .replace(/<base[^>]*>/, '')
  .replace(/<img[^>]*>/, '<img src="/baseline.png" data-src="/lazy-never-request.png" alt="Current"><img src="/placeholder.gif" data-src="/conversion-never-request.png" alt="Lazy">')
  .replace('</article>', `${readFileSync(new URL('../tests/fixtures/conversion.html', import.meta.url), 'utf8')}${readFileSync(new URL('../tests/fixtures/conversion-adjacent.html', import.meta.url), 'utf8')}<iframe src="/baseline-frame">discard frame</iframe><style>.smoke-resource { background-image: url(/baseline-css.png) }</style><p class="smoke-resource">CSS baseline</p><form>PRIVATE_FORM<input value="PRIVATE_VALUE"></form></article>`);
const server = createServer((request, response) => {
  if (imageMode) {
    if (request.url === '/log') {
      response.writeHead(200, { 'Content-Type': 'application/json' });
      response.end(JSON.stringify(imageRequests)); return;
    }
    imageRequests.push({ path: request.url, host: request.headers.host, authorization: !!request.headers.authorization, cookie: !!request.headers.cookie });
    if (request.url === '/redirect') { response.writeHead(302, { Location: `http://cdn.example.test:${server.address().port}/must-not-follow` }); response.end(); return; }
    if (request.url === '/missing') { response.writeHead(404); response.end(); return; }
    response.writeHead(200, { 'Content-Type': 'image/gif', 'Set-Cookie': 'imagehost=private; Path=/' });
    response.end(Buffer.from('R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==', 'base64')); return;
  }
  if (request.url !== '/') {
    if (/\.(png|gif)$/.test(request.url)) {
      response.writeHead(200, { 'Content-Type': 'image/gif' });
      response.end(Buffer.from('R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==', 'base64'));
    } else {
      response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
      response.end('<p>Local resource baseline</p>');
    }
    return;
  }
  response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
  response.end(fixture);
});

const bounded = async (operation, milliseconds, label) => {
  let timer;
  try {
    return await Promise.race([operation, new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error(label)), milliseconds);
    })]);
  } finally {
    clearTimeout(timer);
  }
};

function owned(command, args, options = {}) {
  stopped.signal.throwIfAborted();
  const child = spawn(command, args, { detached: true, stdio: ['ignore', 'ignore', 'pipe'], ...options });
  child.finished = new Promise((resolve, reject) => {
    child.once('error', reject);
    child.once('exit', (code, signal) => resolve({ code, signal }));
  });
  // Launch failures must not become unhandled rejections while awaiting CDP.
  child.finished.catch(() => {});
  return child;
}

function signalGroup(child, signal) {
  if (!child?.pid) return;
  try { process.kill(-child.pid, signal); } catch (error) {
    if (error.code !== 'ESRCH') throw error;
  }
}

async function terminate(child) {
  if (!child) return;
  // SIGKILL, not just SIGTERM, bounds a child that traps/ignores termination.
  signalGroup(child, 'SIGKILL');
  await bounded(child.finished, cleanupStepMs, `Owned process ${child.pid} did not exit after SIGKILL`);
}

async function closeOwnedBrowser() {
  // Ask the browser process directly, independently of Playwright's worker or
  // browser.close(). This lets Chromium reap its children before the kill fallback.
  try {
    if (endpoint && browser?.exitCode === null && browser?.signalCode === null) {
      await new Promise((resolve) => {
        const socket = new WebSocket(endpoint);
        const done = () => {
          clearTimeout(timer);
          try { socket.close(); } catch { /* The owned-group fallback still runs. */ }
          resolve();
        };
        const timer = setTimeout(done, 500);
        socket.addEventListener('open', () => {
          try { socket.send(JSON.stringify({ id: 1, method: 'Browser.close' })); } catch { done(); }
        });
        socket.addEventListener('close', done);
        socket.addEventListener('error', done);
      });
    }
  } finally {
    // Always signal our group, even if its leader exited, to cover descendants.
    await terminate(browser);
  }
}

async function run() {
  // Allocate synchronously so a deadline cannot race an unfinished mkdtemp and
  // create an unrecorded directory after cleanup has already inspected profile.
  profile = mkdtempSync(join(process.env.CLIPPER_SMOKE_TMP ?? '/tmp/opencode', 'clipper-profile-'));
  let extensionPath = dist;
  if (imageMode) {
    // Test-only copy: exercise the unconnected 8.1 ESM in a real extension
    // context, without adding an entry point or acquisition UI to the package.
    extensionPath = join(profile, 'extension-under-test');
    cpSync(dist, extensionPath, { recursive: true });
    copyFileSync(resolve(import.meta.dirname, '../src/images/acquire.js'), join(extensionPath, 'acquire-under-test.js'));
  }
  stopped.signal.throwIfAborted();
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  fixtureURL = `http://${imageMode ? 'images.example.test' : '127.0.0.1'}:${server.address().port}/`;
  browser = owned(process.env.CHROMIUM_PATH ?? '/usr/bin/chromium', [
    `--user-data-dir=${profile}`, '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1',
    `--disable-extensions-except=${extensionPath}`, `--load-extension=${extensionPath}`,
    '--disable-background-networking', '--disable-component-update', '--disable-sync',
    '--no-first-run', '--no-default-browser-check',
    imageMode ? '--host-resolver-rules=MAP images.example.test 127.0.0.1, MAP cdn.example.test 127.0.0.1, MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1' : '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1',
    ...(imageMode ? ['--no-proxy-server'] : []),
    ...(process.env.CLIPPER_SMOKE_NO_SANDBOX === '1' ? ['--no-sandbox'] : []),
    'about:blank',
  ]);
  const ready = new Promise((resolve, reject) => {
    browser.stderr.on('data', (chunk) => {
      browserLog = (browserLog + chunk).slice(-16000);
      const match = browserLog.match(/DevTools listening on (ws:\/\/[^\s]+)/);
      if (match) { endpoint = match[1]; resolve(); }
    });
    browser.finished.then(() => reject(new Error(`Chromium exited before smoke finished:\n${browserLog}`)), reject);
  });
  // The browser readiness listener stays active even when launch is deliberately
  // stalled, so emergency cleanup still has an independent CDP close endpoint.
  ready.catch(() => {});
  console.log(`SMOKE_RESOURCES ${JSON.stringify({ profile, browserPid: browser.pid, fixtureURL })}`);
  if (process.env.CLIPPER_SMOKE_FAULT === 'launch-hang') {
    console.log('SMOKE_FAULT_REACHED launch-hang');
    await new Promise(() => {});
  }
  await ready;
  worker = owned(process.execPath, [resolve(import.meta.dirname, imageMode ? 'smoke-images-worker.js' : 'smoke-worker.js'), endpoint, fixtureURL], {
    stdio: ['ignore', 'inherit', 'inherit', 'ipc'],
  });
  console.log(`SMOKE_RESOURCES ${JSON.stringify({ profile, browserPid: browser.pid, workerPid: worker.pid, fixtureURL })}`);
  worker.on('message', (message) => { result = message; });
  const exit = await worker.finished;
  if (exit.code !== 0 || !result) throw new Error(`Smoke worker failed (${exit.code ?? exit.signal})`);
}

try {
  // The clock lives outside the Playwright process: it covers launch, all CDP /
  // evaluate calls and graceful close, even if that process's event loop stalls.
  await bounded(run(), timeoutMs, `Smoke deadline exceeded (${timeoutMs}ms)`);
} catch (error) {
  failure = error;
} finally {
  stopped.abort();
  // Independent cleanup steps: a rejected/hung close cannot skip another step.
  const cleanup = await Promise.allSettled([
    closeOwnedBrowser(),
    terminate(worker),
    bounded(new Promise((resolve, reject) => {
      server.closeAllConnections();
      if (!server.listening) return resolve();
      server.close((error) => error ? reject(error) : resolve());
    }), cleanupStepMs, 'Fixture server close timed out'),
  ]);
  // Remove only our mkdtemp directory, after processes have stopped writing to it.
  const removed = await Promise.allSettled([
    bounded(profile ? rm(profile, { recursive: true, force: true }) : Promise.resolve(), cleanupStepMs, 'Profile removal timed out'),
  ]);
  for (const step of [...cleanup, ...removed]) {
    if (step.status === 'rejected') {
      console.error(`Smoke cleanup failed: ${step.reason}`);
      failure ??= step.reason;
    }
  }
  if ([...cleanup, ...removed].every((step) => step.status === 'fulfilled')) {
    console.log('SMOKE_CLEANUP {"profileRemoved":true,"serverClosed":true,"processesStopped":true}');
  }
}
if (failure) console.error(failure.message);
else console.log(JSON.stringify(result, null, 2));
// No lingering IPC/CDP handles can keep the supervisor alive after cleanup.
process.exit(failure ? 1 : 0);
