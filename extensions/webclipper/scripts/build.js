import { cp, mkdir, readFile, readdir, writeFile } from 'node:fs/promises';
import { resolve, join } from 'node:path';
import { build } from 'vite';
import { root } from '../vite.config.js';

const outDir = resolve(root, process.argv[2] ?? 'dist');
await build({ configFile: resolve(root, 'vite.config.js'), build: { outDir, emptyOutDir: true } });
await build({ configFile: resolve(root, 'vite.content.config.js'), build: { outDir } });
await cp(resolve(root, 'manifest.json'), join(outDir, 'manifest.json'));

// Preserve direct runtime dependencies' texts verbatim, including NOTICE files
// when supplied. Vite separately inventories all actually bundled transitives.
const packageJSON = JSON.parse(await readFile(resolve(root, 'package.json'), 'utf8'));
const notices = ['IGoNotes Web Clipper — third-party notices', '',
  'Includes direct runtime dependencies; not every listed package is bundled yet.',
  'Bundled code inventories are in LICENSES/bundled-ui.md and bundled-content.md.', ''];
await mkdir(join(outDir, 'LICENSES'), { recursive: true });
await cp(resolve(root, 'LICENSES/Apache-2.0.txt'), join(outDir, 'LICENSES/Apache-2.0.txt'));
notices.push('Apache-2.0 full license: LICENSES/Apache-2.0.txt', '');
for (const name of Object.keys(packageJSON.dependencies).sort()) {
  const directory = resolve(root, 'node_modules', name);
  const pkg = JSON.parse(await readFile(join(directory, 'package.json'), 'utf8'));
  const names = (await readdir(directory)).filter((name) => /^(licen[cs]e|notice)(\.|$)/i.test(name)).sort();
  if (!names.some((name) => /^licen[cs]e/i.test(name))) throw new Error(`Missing license: ${pkg.name}`);
  notices.push(`${pkg.name}@${pkg.version} (${pkg.license})`);
  for (const name of names) {
    const destination = `LICENSES/${pkg.name.replace(/^@/, '').replaceAll('/', '-')}-${name}`;
    await cp(join(directory, name), join(outDir, destination));
    notices.push(`  ${destination}`);
  }
}
await writeFile(join(outDir, 'THIRD_PARTY_NOTICES.txt'), `${notices.join('\n')}\n`);
