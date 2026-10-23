// @vitest-environment jsdom
import { afterEach, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/svelte';
import ConnectionSettings from '../src/ui/ConnectionSettings.svelte';
import { TransportError } from '../src/connection/transport.js';

const token = 'private-test-token';
const click = name => fireEvent.click(screen.getByRole('button', { name, exact: true }));
const edit = (label, value) => fireEvent.input(screen.getByLabelText(label), { target: { value } });
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };
function setup(overrides = {}) {
  const store = { load: vi.fn(async () => ({ endpoint: 'http://localhost:8080', token })), save: vi.fn(async () => {}), clear: vi.fn(async () => {}), ...overrides };
  const info = vi.fn(async () => ({ protocol_version: 1 }));
  const transportFactory = vi.fn(() => ({ info }));
  const view = render(ConnectionSettings, { store, transportFactory });
  return { ...view, store, info, transportFactory };
}
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it('loads trusted settings without network; password/manual save/test/clear are distinct actions', async () => {
  const { store, info, transportFactory } = setup();
  await waitFor(() => expect(screen.getByLabelText('Connection token').value).toBe(token));
  expect(screen.getByLabelText('Connection token').type).toBe('password');
  expect(transportFactory).not.toHaveBeenCalled();
  await edit('Endpoint', 'http://127.0.0.1:9090');
  await click('Save connection');
  await screen.findByText('Connection saved locally. Not tested.');
  expect(store.save).toHaveBeenCalledWith({ endpoint: 'http://127.0.0.1:9090', token });
  expect(info).not.toHaveBeenCalled();
  await click('Test connection');
  await screen.findByText('Connected: protocol v1 is supported.');
  expect(transportFactory).toHaveBeenCalledWith({ endpoint: 'http://127.0.0.1:9090', token });
  await click('Clear connection');
  await screen.findByText('Local connection removed. Server token is not revoked.');
  expect(screen.getByLabelText('Connection token').value).toBe('');
  expect(store.clear).toHaveBeenCalledOnce();
  expect(info).toHaveBeenCalledOnce();
  expect(document.body.textContent).not.toContain(token);
});

it('keeps edited inputs and marks a late check stale rather than trusting a new endpoint', async () => {
  const gate = deferred();
  const { info } = setup();
  await waitFor(() => expect(screen.getByLabelText('Connection token').value).toBe(token));
  info.mockReturnValueOnce(gate.promise);
  await click('Test connection');
  await edit('Endpoint', 'http://localhost:9999');
  gate.resolve({});
  await screen.findByText('Inputs changed. Test the current connection again.');
  expect(screen.queryByText('Connected: protocol v1 is supported.')).toBeNull();
  expect(screen.getByLabelText('Endpoint').value).toBe('http://localhost:9999');
});

it('sanitizes arbitrary errors, retains unsaved form, and ignores late unmounted work', async () => {
  const { store, unmount, info } = setup();
  await waitFor(() => expect(screen.getByLabelText('Connection token').value).toBe(token));
  store.save.mockRejectedValueOnce(new Error(token));
  await click('Save connection');
  await screen.findByRole('alert');
  expect(document.body.textContent).not.toContain(token);
  expect(screen.getByLabelText('Connection token').value).toBe(token);
  const gate = deferred(); info.mockReturnValueOnce(gate.promise);
  await click('Test connection');
  const signal = info.mock.calls[0][0].signal;
  unmount();
  expect(signal.aborted).toBe(true);
  gate.reject(new Error(token));
  await Promise.resolve();
  expect(document.body.textContent).not.toContain(token);
});

it.each(['setup_required', 'unauthorized', 'unavailable', 'protocol_mismatch'])('shows a distinct friendly %s status', async code => {
  const { info } = setup();
  await waitFor(() => expect(screen.getByLabelText('Connection token').value).toBe(token));
  info.mockRejectedValueOnce(new TransportError(code));
  await click('Test connection');
  const alert = await screen.findByRole('alert');
  expect(alert.textContent).toBe(new TransportError(code).message);
  expect(alert.textContent).not.toContain(token);
});

it('ignores a late credential load after settings unmount', async () => {
  const gate = deferred();
  const { unmount, transportFactory } = setup({ load: () => gate.promise });
  unmount();
  gate.resolve({ endpoint: 'http://localhost:8080', token });
  await Promise.resolve();
  expect(screen.queryByLabelText('Connection token')).toBeNull();
  expect(document.body.textContent).not.toContain(token);
  expect(transportFactory).not.toHaveBeenCalled();
});
