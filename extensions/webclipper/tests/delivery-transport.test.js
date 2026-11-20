import { it, expect, vi } from 'vitest';
import { createTransport } from '../src/connection/transport.js';
const id = '550e8400-e29b-41d4-a716-446655440000';
const receipt = { import_id: id, base: 'work', path: 'a.md', revision: `sha256:${'a'.repeat(64)}`, warnings: ['external'] };
const json = (value, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } });
const api = fetch => createTransport({ endpoint: 'http://localhost:9123', token: 'private' }, { fetch });
it('sends only typed multipart and status routes with fixed connection/auth and protected fetch policy', async () => {
  const fetch = vi.fn(async (_url, options) => json(options.method === 'POST' ? receipt : { import_id: id, state: 'succeeded', result: receipt }, options.method === 'POST' ? 201 : 200));
  const transport = api(fetch), body = new FormData();
  body.append('manifest', new Blob(['{}'], { type: 'application/json' }), 'manifest.json');
  expect(await transport.postImport(body)).toEqual({ state: 'succeeded', result: receipt });
  expect(await transport.importStatus(id)).toEqual({ state: 'succeeded', result: receipt });
  expect(fetch.mock.calls[0]).toMatchObject(['http://localhost:9123/api/clipper/v1/imports', { method: 'POST', body, credentials: 'omit', redirect: 'error', headers: { Authorization: 'Bearer private' } }]);
  expect(fetch.mock.calls[1][0]).toBe(`http://localhost:9123/api/clipper/v1/imports?id=${id}`);
});
it.each([
  [202, 'clipper_import_pending', 'pending'], [409, 'clipper_request_changed', 'uncertain'],
  [409, 'clipper_recovery_required', 'recovery_required'], [409, 'git_conflict_pending', 'pending'],
  [409, 'note_conflict', 'rejected'], [409, 'clipper_destination_changed', 'rejected'],
  [404, 'clipper_destination_not_found', 'rejected'], [400, 'clipper_invalid_request', 'rejected'],
  [413, 'clipper_too_large', 'rejected'], [415, 'clipper_unsupported_image', 'rejected'],
])('classifies POST %s %s safely', async (status, code, state) => {
  expect(await api(async () => json({ code, message: 'private' }, status)).postImport(new FormData())).toEqual({ state, code });
});
it('distinguishes unknown GET from rejected journal and rejects malformed or unknown states', async () => {
  expect(await api(async () => json({ code: 'clipper_invalid_request' }, 400)).importStatus(id)).toEqual({ state: 'unknown' });
  expect(await api(async () => json({ import_id: id, state: 'rejected', error: { code: 'clipper_import_abandoned', message: 'private' } })).importStatus(id))
    .toEqual({ state: 'rejected', code: 'clipper_import_abandoned' });
  for (const value of [{ import_id: 'wrong', state: 'pending' }, { import_id: id, state: 'mystery' }, { import_id: id, state: 'pending', result: receipt },
    { import_id: id, state: 'pending', error: null }, { import_id: id, state: 'succeeded', result: receipt, error: null },
    { import_id: id, state: 'succeeded', result: { ...receipt, import_id: '550e8400-e29b-41d4-a716-446655440001' } }, { ...receipt, warnings: [{}] }]) {
    await expect(api(async () => json(value)).importStatus(id)).rejects.toMatchObject({ code: 'invalid_response' });
  }
});
it('accepts internal_error ONLY in a matching GET 200 rejected journal snapshot, without reflected diagnostics', async () => {
  const snapshot = { import_id: id, state: 'rejected', error: { code: 'internal_error', message: 'private reflected server diagnostic', field: 'private' } };
  expect(await api(async () => json(snapshot)).importStatus(id)).toEqual({ state: 'rejected', code: 'internal_error' });
  await expect(api(async () => json({ code: 'internal_error', message: 'private' }, 500)).postImport(new FormData()))
    .rejects.toMatchObject({ code: 'unavailable' });
  await expect(api(async () => json(snapshot, 500)).importStatus(id)).rejects.toMatchObject({ code: 'unavailable' });
  for (const value of [{ ...snapshot, import_id: 'wrong' }, { ...snapshot, state: 'pending' }, { ...snapshot, result: null },
    { ...snapshot, error: { code: 'unrecognized_error', message: 'private' } }]) {
    await expect(api(async () => json(value)).importStatus(id)).rejects.toMatchObject({ code: 'invalid_response' });
  }
});
it('keeps arbitrary 5xx/unknown codes ambiguous and bounds upload response body deadline', async () => {
  await expect(api(async () => json({ code: 'note_conflict', message: 'private' }, 500)).postImport(new FormData())).rejects.toMatchObject({ code: 'unavailable' });
  await expect(api(async () => json({ code: 'private' }, 409)).postImport(new FormData())).rejects.toMatchObject({ code: 'invalid_response' });
  vi.useFakeTimers();
  try {
    const result = expect(api(async () => ({ status: 201, ok: true, headers: new Headers({ 'Content-Type': 'application/json' }), json: () => new Promise(() => {}) })).postImport(new FormData())).rejects.toMatchObject({ code: 'timeout' });
    await vi.advanceTimersByTimeAsync(10000); await result;
  } finally { vi.useRealTimers(); }
});
it('cannot turn a discovery read or status check into a POST through options', async () => {
  const fetch = vi.fn(async () => json({ import_id: id, state: 'pending' }));
  await api(fetch).importStatus(id, { body: new FormData(), delivery: false });
  expect(fetch.mock.calls[0][1].method).toBe('GET');
  expect(fetch.mock.calls[0][1].body).toBeUndefined();
});
it.each([[404, 'clipper_destination_not_found'], [409, 'clipper_destination_changed']])('maps folders %s into explicit reconfirmation without reflecting server text', async (status, code) => {
  const error = await api(async () => json({ code, message: 'private' }, status)).folders('work').catch(error => error);
  expect(error.code).toBe(code); expect(error.message).not.toContain('private');
});
