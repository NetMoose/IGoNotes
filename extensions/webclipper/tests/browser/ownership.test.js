import { it, expect } from 'vitest';
import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync, existsSync, appendFileSync, renameSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { openOwnership, runOwned } from './ownership.js';

it('cleans only the timed-out scope and preserves the original timeout', async () => {
  const parent = mkdtempSync(join(tmpdir(), 'clipper-registry-test-'));
  const owner = openOwnership({}, parent);
  const sibling = spawn(process.execPath, ['-e', 'setInterval(()=>{},1000)'], { detached: true, stdio: 'ignore', env: { ...process.env, ...owner.env } });
  owner.recordProcess(sibling, 'command', `${owner.scope}/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`);
  try {
    const result = await runOwned(process.execPath, ['-e', 'setInterval(()=>{},1000)'], { ownership: owner, env: process.env, timeout: 100 });
    expect(result.error?.code).toBe('ETIMEDOUT');
    expect(result.cleanup.processGroupsGone).toBe(true);
    expect(() => process.kill(-sibling.pid, 0)).not.toThrow();
    const inherited = openOwnership({ ...owner.env }, parent);
    expect(inherited.owner).toBe(false);
  } finally { await owner.cleanup(); await owner.dispose(); rmSync(parent, { recursive: true, force: true }); }
});

it('does not signal a recycled PID/foreign group or delete a replaced profile through its parent', async () => {
  const parent = mkdtempSync(join(tmpdir(), 'clipper-registry-test-'));
  const owner = openOwnership({}, parent);
  const sentinel = spawn(process.execPath, ['-e', 'setInterval(()=>{},1000)'], { detached: true, stdio: 'ignore' });
  try {
    const suite = owner.temporary(parent, 'clipper-browser-');
    const profile = owner.temporary(suite, 'clipper-profile-');
    renameSync(profile, profile + '-moved'); mkdirSync(profile);
    appendFileSync(join(owner.directory, 'resources.jsonl'), JSON.stringify({ nonce: owner.env.CLIPPER_RESOURCE_NONCE, scope: owner.scope, kind: 'process', role: 'command', pid: sentinel.pid, start: '0' }) + '\n');
    const result = await owner.cleanup();
    expect(result.errors).toContain('directory-removal');
    expect(existsSync(profile)).toBe(true);
    expect(() => process.kill(-sentinel.pid, 0)).not.toThrow();
  } finally {
    process.kill(-sentinel.pid, 'SIGKILL');
    await owner.dispose(); rmSync(parent, { recursive: true, force: true });
  }
});

it('rejects foreign-run records and never deletes an unregistered directory', async () => {
  const parent = mkdtempSync(join(tmpdir(), 'clipper-registry-test-'));
  const owner = openOwnership({}, parent);
  const unrelated = mkdtempSync(join(parent, 'unrelated-'));
  try {
    appendFileSync(join(owner.directory, 'resources.jsonl'), JSON.stringify({ nonce: 'wrong', scope: owner.scope, kind: 'directory', path: unrelated }) + '\n');
    const result = await owner.cleanup();
    expect(result.errors).toContain('invalid-record');
    expect(existsSync(unrelated)).toBe(true);
    expect(() => openOwnership({ ...owner.env, CLIPPER_RESOURCE_NONCE: '0'.repeat(64) }, parent)).toThrow();
    await expect(owner.dispose()).rejects.toThrow('Foreign ownership record');
  } finally { rmSync(parent, { recursive: true, force: true }); }
});

it('does not use a shared run nonce to kill a recycled group in a sibling scope', async () => {
  const parent = mkdtempSync(join(tmpdir(), 'clipper-registry-test-'));
  const owner = openOwnership({}, parent), selected = owner.newScope(), sibling = owner.newScope();
  const sentinel = spawn(process.execPath, ['-e', 'setInterval(()=>{},1000)'], { detached: true, stdio: 'ignore', env: { ...process.env, ...owner.env, CLIPPER_RESOURCE_SCOPE: sibling } });
  try {
    appendFileSync(join(owner.directory, 'resources.jsonl'), JSON.stringify({ nonce: owner.env.CLIPPER_RESOURCE_NONCE, scope: selected, kind: 'process', role: 'command', pid: sentinel.pid, start: '0' }) + '\n');
    await owner.cleanup(selected);
    await new Promise(resolve => setTimeout(resolve, 50));
    expect(sentinel.signalCode).toBe(null);
    expect(sentinel.exitCode).toBe(null);
    expect(() => process.kill(-sentinel.pid, 0)).not.toThrow();
  } finally {
    try { process.kill(-sentinel.pid, 'SIGKILL'); } catch (e) { if (e.code !== 'ESRCH') throw e; }
    await owner.dispose(); rmSync(parent, { recursive: true, force: true });
  }
});

it('does not dispose a substituted registry directory', async () => {
  const parent = mkdtempSync(join(tmpdir(), 'clipper-registry-test-'));
  const owner = openOwnership({}, parent);
  try {
    renameSync(owner.directory, owner.directory + '-moved'); mkdirSync(owner.directory);
    await expect(owner.dispose()).rejects.toThrow();
    expect(existsSync(owner.directory)).toBe(true);
  } finally { rmSync(parent, { recursive: true, force: true }); }
});

it('does not dispose a registry containing an unresolved substituted profile', async () => {
  const parent = mkdtempSync(join(tmpdir(), 'clipper-registry-test-'));
  const owner = openOwnership({}, parent);
  try {
    const profile = owner.temporary(owner.directory, 'clipper-profile-');
    renameSync(profile, profile + '-moved'); mkdirSync(profile);
    expect((await owner.cleanup()).errors).toContain('directory-removal');
    await expect(owner.dispose()).rejects.toThrow();
    expect(existsSync(profile)).toBe(true);
  } finally { rmSync(parent, { recursive: true, force: true }); }
});
