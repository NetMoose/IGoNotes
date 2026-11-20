import { mkdirSync, writeFileSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { createServer, request as httpRequest } from 'node:http';

// Task-8.5 foreground smoke, using a real isolated Go instance. The supervisor
// owns its process group, temporary HOME and deadline, including on failures.
export async function deliveryFixture(profile, owned, register) {
  const home = join(profile, 'go-home'), config = join(home, 'config'), bases = join(home, 'bases');
  for (const path of [config, join(bases, 'active'), join(bases, 'inactive', 'research', 'articles')]) mkdirSync(path, { recursive: true });
  writeFileSync(join(config, 'config.json'), JSON.stringify({ base_dir: bases, current_base: 'active', setup_completed: true,
    bases: ['active', 'inactive'].map(name => ({ name, path: join(bases, name), auto_sync: false })) }));
  const reservation = createServer();
  await new Promise(resolve => reservation.listen(0, '127.0.0.1', resolve));
  const port = reservation.address().port;
  await new Promise(resolve => reservation.close(resolve));
  const endpoint = `http://127.0.0.1:${port}`;
  async function start() {
    const backend = owned(process.env.CLIPPER_SMOKE_GO_BINARY, ['--config', config, '--port', String(port), '--no-browser'], {
      env: { ...process.env, HOME: home, XDG_CONFIG_HOME: join(home, 'xdg') },
    });
    register(backend);
    let log = '';
    backend.stderr.on('data', chunk => { log = (log + chunk).slice(-12000); });
    for (;;) {
      if (backend.exitCode !== null) throw Error(`Go smoke backend stopped: ${log}`);
      try { if ((await fetch(`${endpoint}/api/info`)).ok) break; } catch { /* Await HTTP readiness. Supervisor bounds the loop. */ }
      await new Promise(resolve => setTimeout(resolve, 50));
    }
  }
  await start();
  const created = await fetch(`${endpoint}/api/clipper/connections`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ label: 'Task 8.5 smoke' }) });
  if (created.status !== 201) throw Error(`Connection creation failed: ${created.status}`);
  const { secret } = await created.json();
  let dropNext = false, holdNext = false, releaseImages = false, unknownNext = false;
  const stats = { posts: 0, gets: 0, dropped: 0, held: 0, imageRequests: 0, receipts: [], outcomes: [], headers: [] };
  // Go gif.Encode output includes the LZW end code. Many 34-byte web pixels
  // omit it and are displayable, but correctly fail the backend integrity check.
  const gif = 'R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAkQBADs=';
  return { endpoint, home, secret, restart: start,
    handle(request, response) {
      if (request.url === '/arm-unknown') { unknownNext = true; response.end('armed'); return; }
      if (request.url === '/arm-hold') { holdNext = true; response.end('armed'); return; }
      if (request.url === '/release-images') { releaseImages = true; response.end('released'); return; }
      if (request.url === '/missing.gif') { response.writeHead(404); response.end(); return; }
      if (request.url === '/slow-image') {
        stats.imageRequests++; response.writeHead(200, { 'Content-Type': 'image/gif' });
        const bytes = Buffer.from(gif, 'base64');
        if (releaseImages) response.end(bytes); else response.write(bytes.subarray(0, 8));
        return;
      }
      if (request.url === '/arm-drop') { dropNext = true; response.end('armed'); return; }
      if (request.url === '/stats') { response.setHeader('Content-Type', 'application/json'); response.end(JSON.stringify(stats)); return; }
      if (!request.url.startsWith('/api/')) {
        response.setHeader('Content-Type', 'text/html; charset=utf-8');
        response.end(readFileSync(new URL('./fixtures/delivery.html', import.meta.url), 'utf8')
          .replace('{{ARTICLE}}', 'A locally captured article for a real foreground import. '.repeat(40))
          .replace('{{GIF}}', gif)
          .replace('{{SLOW}}', process.env.CLIPPER_SMOKE_RESTART === '1' ? `<img alt="slow" data-src="http://images.example.test:${request.headers.host.split(':').at(-1)}/slow-image">` : '')); return;
      }
      const isPost = request.method === 'POST' && request.url === '/api/clipper/v1/imports';
      const drop = isPost && dropNext;
      const hold = isPost && holdNext;
      if (hold) holdNext = false;
      if (isPost) { stats.posts++; if (drop) dropNext = false; }
      if (request.method === 'GET' && request.url.startsWith('/api/clipper/v1/imports?')) stats.gets++;
      if (unknownNext && request.method === 'GET' && request.url.startsWith('/api/clipper/v1/imports?')) {
        unknownNext = false;
        // Deliberate transient stale status, not a statement about Go's journal.
        response.writeHead(400, { 'Content-Type': 'application/json' });
        response.end(JSON.stringify({ code: 'clipper_invalid_request', message: 'Invalid clipper request' })); return;
      }
      const observed = { method: request.method, origin: request.headers.origin ?? null,
        site: request.headers['sec-fetch-site'] ?? null, mode: request.headers['sec-fetch-mode'] ?? null,
        dest: request.headers['sec-fetch-dest'] ?? null, bearer: /^Bearer /i.test(request.headers.authorization ?? '') };
      const upstream = httpRequest(new URL(request.url, endpoint), { method: request.method, headers: { ...request.headers, host: `127.0.0.1:${port}` } }, incoming => {
        const chunks = [];
        incoming.on('data', chunk => chunks.push(chunk));
        incoming.on('end', () => {
          if (request.url.startsWith('/api/clipper/v1/')) stats.headers.push({ ...observed, status: incoming.statusCode });
          const bytes = Buffer.concat(chunks);
          if (isPost) stats.outcomes.push({ status: incoming.statusCode, code: JSON.parse(bytes).code });
          if (isPost && [200, 201].includes(incoming.statusCode)) stats.receipts.push(JSON.parse(bytes));
          if (hold) { stats.held++; return; }
          if (drop) {
            stats.dropped++;
            // Deliver headers then truncate the receipt. An empty keep-alive
            // reset can make Chromium itself transparently retransmit the POST.
            response.writeHead(incoming.statusCode, { ...incoming.headers, 'content-length': bytes.length });
            response.flushHeaders(); response.write(bytes.subarray(0, 8));
            setTimeout(() => response.destroy(), 20); return;
          }
          response.writeHead(incoming.statusCode, incoming.headers); response.end(bytes);
        });
      });
      upstream.on('error', () => response.destroy());
      request.pipe(upstream);
    },
  };
}
