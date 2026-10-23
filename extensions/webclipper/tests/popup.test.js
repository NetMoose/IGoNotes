// @vitest-environment jsdom
import { afterEach, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, cleanup } from '@testing-library/svelte';
import Popup from '../src/ui/Popup.svelte';
import { CaptureError } from '../src/capture/errors.js';

afterEach(cleanup);
it.each(['Article', 'Selection'])('extracts explicit %s only on click, shows loading and escaped unsaved result', async mode => {
  let resolve; const capture = vi.fn(() => new Promise(yes => { resolve = yes; }));
  render(Popup, { capture }); expect(capture).not.toHaveBeenCalled();
  await fireEvent.click(screen.getByRole('button', { name: mode, exact: true }));
  expect(capture).toHaveBeenCalledExactlyOnceWith(mode.toLowerCase());
  expect(screen.getByRole('button', { name: 'Article', exact: true }).disabled).toBe(true);
  expect(screen.getByRole('button', { name: 'Selection', exact: true }).disabled).toBe(true);
  expect(screen.getByRole('status').textContent).toContain('Extracting');
  resolve({ title: '<img src=x onerror=alert(1)>', text: 'Hello 🌍', html: '<script>unsafe</script>' });
  await screen.findByText('<img src=x onerror=alert(1)>');
  expect(screen.getByRole('status').textContent).toContain('8 text characters');
  expect(document.querySelector('main img, main script')).toBeNull();
  expect(screen.getByText(/Not saved.*7\.3/)).toBeTruthy();
});
it('shows a friendly article error and never silently captures selection', async () => {
  const capture = vi.fn().mockRejectedValue(new Error('private URL <html>'));
  render(Popup, { capture });
  await fireEvent.click(screen.getByRole('button', { name: 'Article', exact: true }));
  expect((await screen.findByRole('alert')).textContent).not.toMatch(/private|<html>/);
  expect(capture).toHaveBeenCalledExactlyOnceWith('article');
  expect(screen.getByRole('button', { name: 'Selection', exact: true }).disabled).toBe(false);
});
it('offers Selection explicitly after an article failure and clears old errors/results on retry', async () => {
  const capture = vi.fn().mockRejectedValueOnce(new CaptureError('article_empty'))
    .mockResolvedValueOnce({ title: 'Chosen content', text: 'Selected', html: '<p>Selected</p>' });
  render(Popup, { capture });
  await fireEvent.click(screen.getByRole('button', { name: 'Article', exact: true }));
  expect((await screen.findByRole('alert')).textContent).toContain('choose Selection');
  expect(capture).toHaveBeenCalledExactlyOnceWith('article');
  await fireEvent.click(screen.getByRole('button', { name: 'Selection', exact: true }));
  await screen.findByText('Chosen content');
  expect(screen.queryByRole('alert')).toBeNull();
  expect(capture.mock.calls).toEqual([['article'], ['selection']]);
});
