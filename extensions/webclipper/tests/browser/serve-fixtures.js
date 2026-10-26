import { createServer } from 'node:http';
import { readFileSync } from 'node:fs';

// Optional manual 9.2 fixture server. It never starts Go/Chrome or creates a token.
const port = Number(process.env.CLIPPER_FIXTURE_PORT ?? 19491);
if (!Number.isSafeInteger(port) || port < 1024 || port > 65535) throw Error('CLIPPER_FIXTURE_PORT must be 1024..65535');
const pixel = Buffer.from('R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAkQBADs=', 'base64');
const server = createServer((request, response) => {
  if (request.url === '/pixel.gif') { response.writeHead(200, { 'Content-Type': 'image/gif' }); response.end(pixel); }
  else if (request.url === '/redirect') { response.writeHead(302, { Location: `http://cdn.example.test:${port}/pixel.gif` }); response.end(); }
  else if (request.url === '/') {
    response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    response.end(readFileSync(new URL('./fixtures/manual.html', import.meta.url), 'utf8')
      .replaceAll('{{PORT}}', String(port)).replace('{{ARTICLE}}', 'This offline fixture has enough main-document article prose for extraction. '.repeat(40)));
  } else { response.writeHead(404); response.end(); }
});
server.listen(port, '127.0.0.1', () => console.log(`Local manual fixture: http://127.0.0.1:${port}/ (Ctrl-C to stop)`));
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => { server.closeAllConnections(); server.close(); });
