import { describe, it, expect } from 'vitest';
import { modeReport } from './report.js';

describe('browser evidence contract', () => {
  it('fails incomplete evidence even when the worker says PASS', () => {
    const report = modeReport('delivery', { result: 'PASS', browser: '154.0.0.0' });
    expect(report.passed).toBe(false);
    expect(report.cases.every(c => c.status === 'FAIL')).toBe(true);
  });
  it('never copies arbitrary worker data, source text, tokens or private paths', () => {
    const report = modeReport('delivery', { result: 'PASS', browser: '154.0.0.0', token: 'PRIVATE_TOKEN', home: '/home/private', markdown: 'PRIVATE_SOURCE', nativeHeaders: [{ authorization: 'PRIVATE_TOKEN' }] });
    expect(JSON.stringify(report)).not.toMatch(/PRIVATE_|\/home/);
  });
  it('rejects unknown modes instead of reporting an empty passing suite', () => {
    expect(() => modeReport('typo', {})).toThrow();
  });
});
