import { existsSync, readFileSync, readdirSync, mkdtempSync, rmSync } from 'node:fs';
import { resolve, dirname, join, relative } from 'node:path';
import { tmpdir } from 'node:os';
import { execFileSync } from 'node:child_process';
import { Script } from 'node:vm';
import { afterAll, expect, test } from 'vitest';

const root = resolve(import.meta.dirname, '..');
const buildTimeoutMs = Number(process.env.CLIPPER_BUILD_TIMEOUT_MS ?? 10000);
if (!Number.isSafeInteger(buildTimeoutMs) || buildTimeoutMs <= 0) throw new Error('Invalid CLIPPER_BUILD_TIMEOUT_MS');
const temporary = mkdtempSync(join(tmpdir(), 'igonotes-clipper-build-'));
afterAll(() => rmSync(temporary, { recursive: true, force: true }));

test('builds a deterministic, local-only MV3 package with executable standalone injection', () => {
  expect(existsSync(join(root, 'manifest.json')), 'MV3 manifest is provided').toBe(true);
  const build = (out) => execFileSync(process.execPath, ['scripts/build.js', out], {
    cwd: root,
    // Vitest's timeout cannot interrupt a synchronous child-process call.
    timeout: buildTimeoutMs,
    killSignal: 'SIGKILL',
  });
  const out = join(temporary, 'first');
  build(out);
  const text = (name) => readFileSync(join(out, name), 'utf8');
  const manifest = JSON.parse(text('manifest.json'));
  expect(manifest.manifest_version).toBe(3);
  expect(manifest.minimum_chrome_version).toBe('120');
  expect(manifest.permissions.toSorted()).toEqual(['activeTab', 'scripting', 'storage']);
  expect(manifest.host_permissions.toSorted()).toEqual(['http://127.0.0.1/*', 'http://localhost/*']);
  expect(manifest.optional_host_permissions.toSorted()).toEqual(['http://*/*', 'https://*/*']);
  for (const forbidden of ['content_scripts', 'externally_connectable', 'web_accessible_resources']) {
    expect(manifest).not.toHaveProperty(forbidden);
  }
  expect(manifest.background.type).toBe('module');
  expect(manifest.options_ui).toEqual({ page: 'editor.html', open_in_tab: true });
  expect(manifest.content_security_policy.extension_pages).toBe("script-src 'self'; object-src 'none'; base-uri 'none'");
  for (const entry of [manifest.background.service_worker, manifest.action.default_popup, manifest.options_ui.page, 'content-script.js']) {
    expect(existsSync(join(out, entry)), entry).toBe(true);
  }
  for (const page of ['popup.html', 'editor.html']) {
    const html = text(page);
    expect(html).not.toMatch(/<script[^>]*>\s*[^<\s]/);
    const resources = [...html.matchAll(/(?:src|href)="([^"]+)"/g)].map((match) => match[1]);
    expect(resources.length).toBeGreaterThan(0);
    for (const resource of resources) {
      expect(resource).toMatch(/^\.\//);
      expect(existsSync(resolve(out, resource)), resource).toBe(true);
    }
  }
  const files = (dir) => readdirSync(dir, { withFileTypes: true }).flatMap((entry) =>
    entry.isDirectory() ? files(join(dir, entry.name)) : [join(dir, entry.name)]);
  for (const path of files(out).filter((path) => path.endsWith('.js'))) {
    const js = readFileSync(path, 'utf8');
    expect(js).not.toMatch(/\beval\s*\(|\bnew Function\s*\(/);
    for (const match of js.matchAll(/(?:from\s*|import\s*\(?)['"]([^'"]+)['"]/g)) {
      expect(match[1]).toMatch(/^\.\.?\//);
      expect(existsSync(resolve(dirname(path), match[1])), match[1]).toBe(true);
    }
  }
  // A classic script parser rejects ESM imports/exports; this is the executeScript file contract.
  const content = new Script(text('content-script.js'));
  const document = { body: { innerHTML: '<p>unchanged</p>' } };
  const isolated = { document };
  content.runInNewContext(isolated);
  expect(isolated.IGoNotesContentScript).toEqual({ status: 'scaffold', captureAvailable: false });
  expect(document.body.innerHTML).toBe('<p>unchanged</p>');
  const notices = text('THIRD_PARTY_NOTICES.txt');
  for (const name of ['@mozilla/readability', 'turndown', 'turndown-plugin-gfm', 'svelte']) {
    expect(notices).toContain(name);
  }
  expect(text('LICENSES/mozilla-readability-LICENSE.md')).toContain('Apache License');
  expect(text('LICENSES/Apache-2.0.txt')).toContain('END OF TERMS AND CONDITIONS');
  expect(text('LICENSES/turndown-LICENSE')).toContain('Permission is hereby granted');
  expect(text('LICENSES/turndown-plugin-gfm-LICENSE')).toContain('Permission is hereby granted');
  expect(text('LICENSES/svelte-LICENSE.md')).toContain('Permission is hereby granted');
  const second = join(temporary, 'second');
  build(second);
  for (const path of files(out)) {
    expect(readFileSync(join(second, relative(out, path)))).toEqual(readFileSync(path));
  }
});
