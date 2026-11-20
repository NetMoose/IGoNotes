// DNS maps these public-shaped names to 127.0.0.1 and rejects all other names
// in smoke.js. Observe, never route/intercept native fetch: even continue-only
// Playwright routing can bypass Chromium's preflight and invalidate this test.
export async function fenceNetwork(context, unexpected) {
  context.on('request', request => {
    const url = new URL(request.url());
    const allowed = ['chrome-extension:', 'chrome:', 'data:', 'blob:', 'about:'].includes(url.protocol)
      || (url.protocol === 'http:' && ['127.0.0.1', 'localhost', 'images.example.test', 'cdn.example.test', 'hostile.example.test'].includes(url.hostname));
    if (!allowed) unexpected.push('non-fixture network request attempted');
  });
}
