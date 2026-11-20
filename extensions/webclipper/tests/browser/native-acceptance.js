// Opt-in headed Linux acceptance. Uses the PRODUCT dist, never adds host grants.
// Native dialogs are answered by XTest on an owned, private Xvfb display.
import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync, mkdirSync, readdirSync } from 'node:fs';
import { createServer } from 'node:http';
import { resolve, join } from 'node:path';
import { createHash } from 'node:crypto';
import { platform, release, arch } from 'node:os';
import { chromium } from 'playwright-core';
import { openOwnership } from './ownership.js';
import { deliveryFixture } from './delivery-fixture.js';

const executable = process.env.CHROMIUM_PATH;
const output = process.env.CLIPPER_NATIVE_OUTPUT;
assert.ok(executable && output?.startsWith('/tmp/opencode/'), 'Set CHROMIUM_PATH and CLIPPER_NATIVE_OUTPUT under /tmp/opencode');
assert.ok(process.env.CLIPPER_SMOKE_GO_BINARY, 'Build current ./cmd/api and set CLIPPER_SMOKE_GO_BINARY');
assert.equal(process.versions.node.split('.')[0], '24');
const dist = resolve(import.meta.dirname, '../../dist');
const hash = value => createHash('sha256').update(value).digest('hex');
const own = openOwnership(process.env, '/tmp/opencode');
const profile = own.temporary('/tmp/opencode', 'clipper-profile-');
mkdirSync(output, { recursive: true, mode: 0o700 });
const env = { ...process.env, ...own.env,
  GIT_AUTHOR_NAME: 'Clipper acceptance', GIT_AUTHOR_EMAIL: 'acceptance@example.invalid',
  GIT_COMMITTER_NAME: 'Clipper acceptance', GIT_COMMITTER_EMAIL: 'acceptance@example.invalid' };
const report = { date: new Date().toISOString(), executable, installation: 'browser CDP Extensions.loadUnpacked (--enable-unsafe-extension-debugging); not file chooser',
  sandbox: 'enabled (no --no-sandbox)', nativePermissionAutoGrant: false,
  os: { platform: platform(), release: release(), arch: arch(), distribution: readFileSync('/etc/os-release', 'utf8').match(/^PRETTY_NAME="([^"]+)"/m)?.[1] },
  goBinarySHA256: hash(readFileSync(process.env.CLIPPER_SMOKE_GO_BINARY)),
  manifestSHA256: hash(readFileSync(join(dist, 'manifest.json'))), cases: [], screenshots: [] };
let browser, browserProcess, context, session, extensionId, backend, server, fixture, fixtureURL, stopped = false;
const sleep = ms => new Promise(r => setTimeout(r, ms));
function owned(command, args, options = {}) {
  assert.equal(stopped, false);
  const { role = 'backend', ...opts } = options;
  const p = spawn(command, args, { detached: true, stdio: ['ignore', 'pipe', 'pipe'], ...opts, env: { ...env, ...opts.env } });
  own.recordProcess(p, role, own.scope, role === 'browser' ? profile : undefined);
  return p;
}
function input(...keys) { execFileSync('python3', [join(import.meta.dirname, 'native-input.py'), ...keys], { env, timeout: 5000 }); }
function shot(name) {
  const filename = join(output, name + '.png');
  execFileSync('import', ['-window', 'root', filename], { env, timeout: 5000 });
  report.screenshots.push({ name, sha256: hash(readFileSync(filename)) });
}
function pass(name, details = {}) { report.cases.push({ name, result: 'PASS', ...details }); console.log('PASS', name); }
async function until(check, timeout = 10000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) { const result = await check(); if (result) return result; await sleep(50); }
  throw Error('Acceptance observation deadline');
}
async function launch() {
  const p = owned(executable, [`--user-data-dir=${profile}`, '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1',
    '--enable-unsafe-extension-debugging', '--no-first-run', '--no-default-browser-check',
    '--disable-background-networking', '--disable-component-update', '--disable-sync', '--no-proxy-server',
    '--host-resolver-rules=MAP images.example.test 127.0.0.1, MAP cdn.example.test 127.0.0.1, MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1', 'about:blank'], { role: 'browser' });
  let log = '';
  browserProcess = p;
  p.stderr.on('data', bytes => { log = (log + bytes).slice(-16000); });
  const endpoint = await until(() => { if (p.exitCode !== null) throw Error('Browser launch exited: ' + log); const m = log.match(/DevTools listening on (ws:\/\/\S+)/); return m?.[1]; });
  browser = await chromium.connectOverCDP(endpoint);
  context = browser.contexts()[0]; context.setDefaultTimeout(7000);
  session = await browser.newBrowserCDPSession();
  ({ id: extensionId } = await session.send('Extensions.loadUnpacked', { path: dist }));
  report.browser = browser.version();
  return p;
}
async function closeBrowser() {
  await session.send('Browser.close').catch(() => {});
  await until(() => browserProcess.exitCode !== null || browserProcess.signalCode !== null);
}
async function openEditor(id) {
  const page = await context.newPage();
  await page.goto(`chrome-extension://${extensionId}/editor.html${id ? '?draft=' + id : ''}`);
  if (id) await page.getByLabel('Markdown', { exact: true }).waitFor();
  return page;
}
async function connection(page, address, token = fixture.secret) {
  await page.getByRole('button', { name: 'Connection settings', exact: true }).click();
  await page.getByLabel('Endpoint', { exact: true }).fill(address);
  await page.getByLabel('Connection token', { exact: true }).fill(token);
  await page.getByRole('button', { name: 'Save connection', exact: true }).click();
  await page.getByText('Connection saved locally. Not tested.', { exact: true }).waitFor();
}
async function confirm(page) {
  await page.getByRole('button', { name: 'Load destinations', exact: true }).click();
  await page.getByLabel('Destination base', { exact: true }).selectOption('inactive');
  await page.getByLabel('Destination folder', { exact: true }).selectOption('research/articles');
  await page.getByRole('button', { name: 'Confirm destination', exact: true }).click();
  await page.getByText('Confirmed destination: inactive / research/articles', { exact: true }).waitFor();
}
async function api(path, body, method = 'POST') {
  const r = await fetch(fixture.endpoint + path, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
  assert.ok(r.ok, `${method} ${path}: ${r.status}`); return r.json();
}
const git = (cwd, ...args) => execFileSync('git', ['-C', cwd, ...args], { env, timeout: 10000, stdio: ['ignore', 'pipe', 'pipe'] }).toString().trim();
async function bundle(page, id) {
  return page.evaluate(async id => {
    const db = await new Promise((resolve, reject) => { const r = indexedDB.open('igonotes-webclipper', 1); r.onsuccess = () => resolve(r.result); r.onerror = () => reject(r.error); });
    const b = await new Promise((resolve, reject) => {
      const tx = db.transaction(['drafts', 'assets', 'attempts'], 'readonly'), b = {};
      for (const n of ['drafts', 'assets', 'attempts']) { const r = n === 'drafts' ? tx.objectStore(n).get(id) : tx.objectStore(n).index('draftId').getAll(id); r.onsuccess = () => { b[n] = r.result; }; }
      tx.oncomplete = () => resolve(b); tx.onabort = () => reject(tx.error);
    }); db.close();
    b.assets = await Promise.all(b.assets.map(async a => ({ ...a, blob: a.blob ? [...new Uint8Array(await a.blob.arrayBuffer())] : null })));
    return b;
  }, id);
}
async function capture(source, mode = 'Article') {
  await source.bringToFront();
  // Coordinates are pinned to the recorded 1050x880 browser at (10,10).
  input('click:947:73'); await sleep(350); input('click:780:260'); await sleep(400);
  const target = (await session.send('Target.getTargets')).targetInfos.find(t => t.url === `chrome-extension://${extensionId}/popup.html`);
  assert.ok(target, 'Actual toolbar action popup must exist');
  const opened = context.waitForEvent('page');
  input(mode === 'Article' ? 'click:614:215' : 'click:680:215');
  const page = await opened; await page.getByLabel('Markdown', { exact: true }).waitFor(); return page;
}
async function run() {
  const xv = owned('Xvfb', ['-displayfd', '3', '-screen', '0', '1280x900x24', '-nolisten', 'tcp'], { role: 'display', stdio: ['ignore', 'ignore', 'pipe', 'pipe'] });
  env.DISPLAY = ':' + await new Promise(r => xv.stdio[3].once('data', b => r(b.toString().trim())));
  fixture = await deliveryFixture(profile, (cmd, args, opts) => owned(cmd, args, { ...opts, env: { ...env, ...opts.env } }), p => { backend = p; });
  // This identity belongs only to the fixture HOME, never the user's Git config.
  writeFileSync(join(fixture.home, '.gitconfig'), '[user]\n name = Clipper acceptance\n email = acceptance@example.invalid\n');
  let imageRequests = 0, heldUpload = false, uploadHeld = false;
  server = createServer((req, res) => {
    if (req.url === '/pixel.gif') { imageRequests++; res.writeHead(200, { 'Content-Type': 'image/gif' }); res.end(Buffer.from('R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAkQBADs=', 'base64')); return; }
    if (req.url === '/redirect') { res.writeHead(302, { Location: `http://images.example.test:${server.address().port}/pixel.gif` }); res.end(); return; }
    if (heldUpload && req.method === 'POST' && req.url === '/api/clipper/v1/imports') { heldUpload = false; uploadHeld = true; req.pause(); return; }
    fixture.handle(req, res);
  });
  await new Promise(r => server.listen(0, '127.0.0.1', r)); own.recordListener(server.address().port);
  fixtureURL = `http://127.0.0.1:${server.address().port}/`;
  await launch(); pass('product-unpacked-CDP-install');
  const app = await context.newPage(); await app.goto(fixture.endpoint);
  await app.getByRole('button', { name: 'Открыть настройки', exact: true }).click();
  await app.getByRole('tab', { name: 'Веб-клиппер', exact: true }).click();
  await app.getByLabel('Название подключения', { exact: true }).fill('Native acceptance');
  await app.getByRole('button', { name: 'Создать подключение', exact: true }).click();
  const secret = app.locator('[role="status"] code'); await secret.waitFor(); fixture.secret = await secret.innerText();
  await app.getByRole('button', { name: 'Скрыть секрет', exact: true }).click();
  pass('create-connection-through-product-settings'); await app.close();
  let page = await openEditor();
  await connection(page, fixtureURL.slice(0, -1));
  await page.getByRole('button', { name: 'Test connection', exact: true }).click();
  await page.getByText('Connected: protocol v1 is supported.', { exact: true }).waitFor(); shot('loopback-127-custom-port-no-prompt');
  await page.getByLabel('Endpoint', { exact: true }).fill(fixtureURL.slice(0, -1).replace('127.0.0.1', 'localhost'));
  await page.getByRole('button', { name: 'Test connection', exact: true }).click();
  await page.getByText('Connected: protocol v1 is supported.', { exact: true }).waitFor();
  pass('loopback-custom-port', { nativePrompt: 'ABSENT; required manifest hosts; no runtime deny/allow claim', hosts: ['127.0.0.1', 'localhost'] });
  await page.getByRole('button', { name: 'Close connection settings', exact: true }).click();
  const source = await context.newPage(); await source.goto(fixtureURL);
  const original = await source.locator('body').innerHTML();
  page = await capture(source); const id = new URL(page.url()).searchParams.get('draft');
  assert.equal(await source.locator('body').innerHTML(), original); pass('actual-toolbar-article');
  await source.evaluate(() => { const r = document.createRange(); r.selectNodeContents(document.querySelector('h1')); const s = getSelection(); s.removeAllRanges(); s.addRange(r); });
  const selection = await capture(source, 'Selection');
  assert.match(await selection.getByLabel('Markdown', { exact: true }).inputValue(), /Foreground delivery/);
  assert.ok((await selection.getByLabel('Markdown', { exact: true }).inputValue()).length < 200); pass('actual-toolbar-selection'); await selection.close();
  await page.bringToFront();
  const cdn = `http://cdn.example.test:${server.address().port}`;
  await page.getByLabel('Markdown', { exact: true }).fill(`Native acceptance ![cdn](${cdn}/pixel.gif)`);
  assert.equal(await page.evaluate(() => chrome.permissions.contains({ origins: ['http://cdn.example.test/*'] })), false);
  await page.getByRole('button', { name: 'Prepare images', exact: true }).click(); await sleep(650); shot('cdn-native-deny-dialog'); input('Escape');
  await page.getByText(/permission: Image host access was not granted/).waitFor();
  assert.equal(imageRequests, 0); assert.ok((await bundle(page, id)).drafts); pass('native-CDN-deny-retains-draft');
  await page.getByRole('button', { name: 'Retry images / grant access', exact: true }).click(); await sleep(650); shot('cdn-native-allow-dialog'); input('Tab', 'Return');
  await page.getByText(/1 \/ 1 stored locally/).waitFor();
  assert.equal(await page.evaluate(() => chrome.permissions.contains({ origins: ['http://cdn.example.test/*'] })), true);
  assert.equal(imageRequests, 1); pass('native-CDN-allow-stores-Blob');
  const saved = await bundle(page, id);
  await source.close(); await closeBrowser(); await launch(); page = await openEditor(id);
  assert.deepEqual(await bundle(page, id), saved); assert.equal(imageRequests, 1);
  await context.setOffline(true); await page.reload(); await page.getByText(/1 \/ 1 stored locally/).waitFor();
  assert.deepEqual(await bundle(page, id), saved); await context.setOffline(false); pass('browser-restart-offline-cache-no-refetch');
  await page.getByLabel('Filename', { exact: true }).fill('native.md'); await confirm(page);
  // Hold the REQUEST before forwarding any bytes to Go; this is interrupted upload,
  // distinct from the existing task-9.1 held-success-response test.
  heldUpload = true; await page.getByRole('button', { name: 'Send', exact: true }).click(); await until(() => uploadHeld);
  const posting = await bundle(page, id); assert.equal(posting.attempts[0].phase, 'posting');
  assert.equal(readdirSync(join(fixture.home, 'bases/inactive/research/articles')).length, 0);
  await closeBrowser(); server.closeAllConnections();
  process.kill(-backend.pid, 'SIGKILL'); await until(() => backend.exitCode !== null || backend.signalCode !== null); await fixture.restart();
  await launch(); page = await openEditor(id);
  assert.deepEqual(await bundle(page, id), posting); assert.equal(imageRequests, 1);
  await page.getByRole('button', { name: 'Check / retry exact attempt', exact: true }).click();
  await page.getByText(/The destination changed. Load and explicitly confirm it again/).waitFor();
  pass('interrupted-upload-browser-Go-restart', { initialPublication: false, frozenAttemptPreserved: true, staleDestinationRejected: true });
  // Definitive stale rejection unlocks the saved draft; only explicit reconfirm+Send
  // creates a new attempt on the new server instance.
  await confirm(page);
  await page.getByText(/1 \/ 1 stored locally/).waitFor();
  await page.getByRole('button', { name: 'Send', exact: true }).click(); await page.getByRole('heading', { name: 'Import confirmed', exact: true }).waitFor();
  assert.equal(readdirSync(join(fixture.home, 'bases/inactive/research/articles')).length, 1);
  pass('explicit-reconfirm-and-import-after-restart');
  // Separate native permission for a second origin; redirect must not obtain it.
  const textSource = await context.newPage(); await textSource.goto(fixtureURL);
  page = await capture(textSource); const warningId = new URL(page.url()).searchParams.get('draft');
  const images = `http://images.example.test:${server.address().port}`;
  await page.getByLabel('Markdown', { exact: true }).fill(`![redirect](${cdn}/redirect)\n![missing](${images}/missing.gif)`);
  await page.getByLabel('Filename', { exact: true }).fill('fallback.md');
  await page.getByRole('button', { name: 'Prepare images', exact: true }).click(); await sleep(650); shot('second-origin-native-deny-dialog'); input('Escape');
  await page.getByText('Image preparation finished. Review each image below.', { exact: true }).waitFor();
  assert.equal(await page.evaluate(() => chrome.permissions.contains({ origins: ['http://images.example.test/*'] })), false);
  await page.getByRole('button', { name: 'Retry images / grant access', exact: true }).click(); await sleep(650); shot('second-origin-native-allow-dialog'); input('Tab', 'Return');
  await page.getByText('Image preparation finished. Review each image below.', { exact: true }).waitFor();
  await page.getByText(/http \(404\)/).waitFor();
  await page.getByText(/redirect/).first().waitFor();
  await confirm(page); assert.equal(await page.getByRole('button', { name: 'Send', exact: true }).isDisabled(), true);
  await page.getByLabel(`Keep external reference: ${cdn}/redirect`, { exact: true }).check();
  await page.getByLabel(`Keep external reference: ${images}/missing.gif`, { exact: true }).check();
  assert.equal(await page.getByRole('button', { name: 'Send', exact: true }).isDisabled(), true);
  await page.getByRole('button', { name: 'Confirm external references', exact: true }).click();
  await page.getByText('This material is not fully offline / uses external images.', { exact: true }).waitFor();
  await page.getByRole('button', { name: 'Send', exact: true }).click(); await page.getByRole('heading', { name: 'Import confirmed', exact: true }).waitFor();
  const stats = await (await fetch(fixtureURL + 'stats')).json();
  assert.equal(stats.receipts.at(-1).warnings.length, 2);
  assert.ok(stats.receipts.at(-1).warnings.every(w => typeof w === 'string'));
  assert.equal((await bundle(page, warningId)).drafts, undefined);
  pass('native-second-origin-deny-allow-and-explicit-warning-fallback');

  // Actual local bare-remote divergence, managed by the production Git worker.
  const root = join(fixture.home, 'bases/inactive'), remote = join(fixture.home, 'remote.git'), other = join(fixture.home, 'other');
  git(fixture.home, 'init', '--bare', '--initial-branch=main', remote);
  git(root, 'init', '--initial-branch=main'); writeFileSync(join(root, 'conflict.md'), 'baseline\n');
  git(root, 'add', '.'); git(root, 'commit', '-m', 'fixture baseline'); git(root, 'remote', 'add', 'origin', remote); git(root, 'push', 'origin', 'main');
  await api('/api/git/config?base=inactive', { git_url: remote, git_branch: 'main', auto_sync: false, auto_sync_interval_minutes: 0,
    git_commit_message_template: 'Acceptance {{base}} {{count}}', confirmations: {} }, 'PUT');
  const state = async () => { const r = await fetch(fixture.endpoint + '/api/git/status?base=inactive'); const b = await r.json(); assert.ok(r.ok, `Git status ${r.status}: ${JSON.stringify(b)}`); const s = b.statuses[0]; assert.ok(!['error', 'paused', 'needs_reconnect'].includes(s.state), `Git ${s.state}: ${s.error?.code ?? 'no code'} at ${s.stage}`); return s.state; };
  await until(async () => (await state()) === 'ready', 20000);
  git(fixture.home, 'clone', remote, other); writeFileSync(join(other, 'conflict.md'), 'remote edit\n');
  git(other, 'add', '.'); git(other, 'commit', '-m', 'remote divergence'); git(other, 'push', 'origin', 'main');
  writeFileSync(join(root, 'conflict.md'), 'local edit\n'); await api('/api/git/sync?base=inactive');
  await until(async () => (await state()) === 'conflict', 20000);
  page = await capture(textSource); await page.getByLabel('Markdown', { exact: true }).fill('Git gate acceptance');
  await page.getByLabel('Filename', { exact: true }).fill('after-conflict.md');
  await page.getByRole('button', { name: 'Prepare images', exact: true }).click();
  await page.getByText('Image preparation finished. Review each image below.', { exact: true }).waitFor(); await confirm(page);
  const beforeConflict = git(root, 'status', '--porcelain=v1');
  const conflictBytes = readFileSync(join(root, 'conflict.md'));
  await page.getByRole('button', { name: 'Send', exact: true }).click();
  await page.getByText(/conflict/i).last().waitFor(); await sleep(400);
  assert.equal(git(root, 'status', '--porcelain=v1'), beforeConflict);
  assert.deepEqual(readFileSync(join(root, 'conflict.md')), conflictBytes);
  assert.equal(readdirSync(join(root, 'research/articles')).includes('after-conflict.md'), false);
  assert.ok((await bundle(page, new URL(page.url()).searchParams.get('draft'))).drafts);
  const gated = await (await fetch(fixtureURL + 'stats')).json();
  assert.ok(gated.outcomes.some(o => o.status === 409 && o.code === 'git_conflict_pending'));
  shot('git-conflict-retained-draft'); pass('real-Git-conflict-gates-product-Send-no-file-changes');
  // Select the target base explicitly, then resolve through the product UI.
  await api('/api/bases/switch', { name: 'inactive' });
  const resolver = await context.newPage(); await resolver.goto(fixture.endpoint);
  await resolver.getByLabel('Оставить версию на этом устройстве', { exact: true }).check();
  await resolver.getByRole('button', { name: 'Применить решение', exact: true }).click();
  await resolver.getByRole('button', { name: 'Завершить слияние', exact: true }).click();
  await sleep(1200); await until(async () => (await state()) === 'ready', 20000);
  assert.equal(readFileSync(join(root, 'conflict.md'), 'utf8'), 'local edit\n');
  await resolver.close(); await page.bringToFront();
  await page.getByRole('button', { name: 'Check / retry exact attempt', exact: true }).click();
  await page.getByRole('heading', { name: 'Import confirmed', exact: true }).waitFor();
  assert.equal(readFileSync(join(root, 'research/articles/after-conflict.md'), 'utf8').includes('Git gate acceptance'), true);
  pass('Git-resolve-through-product-UI-explicit-exact-attempt-retry');
  await api('/api/git/sync?base=inactive'); await sleep(1200); await until(async () => (await state()) === 'ready', 20000);
  git(other, 'fetch', 'origin');
  assert.match(git(other, 'show', 'origin/main:research/articles/after-conflict.md'), /Git gate acceptance/);
  pass('ordinary-Git-sync-includes-import');
  report.result = 'PASS';
}
let timeout;
try {
  await Promise.race([run(), new Promise((_, reject) => { timeout = setTimeout(() => reject(Error('Native acceptance overall deadline')), 150000); })]);
} catch (error) { report.result = 'FAIL'; report.failure = error.message; process.exitCode = 1; console.error(error.message); }
finally {
  clearTimeout(timeout); stopped = true; server?.closeAllConnections(); server?.close();
  await Promise.race([browser?.close().catch(() => {}), sleep(1000)]);
  report.cleanup = await own.cleanup(); await own.dispose();
  if (report.cleanup.errors.length) { report.result = 'FAIL'; process.exitCode = 1; }
  writeFileSync(join(output, 'report.json'), JSON.stringify(report, null, 2) + '\n');
}
