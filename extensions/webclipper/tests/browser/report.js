// Project an allowlist, never worker logs, DOM, manifests, credentials or paths.
const required = {
  normal: ['domUnchanged', 'selectionUnchanged', 'markdownConversion', 'trustedStorageOnly', 'adjacentCodeRenderedSafely'],
  images: ['nativeImageAcquisition', 'offlineCachedBytes', 'revokedPermission', 'cookieAndBearerOmitted'],
  delivery: ['nativeHeadersVerified', 'authorizedOPTIONS', 'hostilePreflightRejected', 'lostResponseGETResolved', 'backendPreviewImageBytes', 'noDuplicateFiles', 'activeBaseUnchangedByImport'],
  restart: ['offlineCapturedDraft', 'partialPreparationDurable', 'noAutoPOSTorGET', 'historicalExactPOSTReplay', 'originalIDManifestBlobsPreserved', 'warningsPathsHashesPreserved', 'workerTargetTerminatedAndCaptureWoke', 'oldDestinationRequiresExplicitReconfirm'],
};
export function modeReport(mode, evidence = {}) {
  if (!required[mode]) throw Error('Unknown browser mode');
  const cases = required[mode].map(name => ({ name, status: evidence[name] === true ? 'PASS' : 'FAIL' }));
  const version = evidence.browser ?? evidence.browserVersions?.[0];
  return { mode, browser: /^\d+\.\d+\.\d+\.\d+$/.test(version) ? version : null,
    passed: cases.every(c => c.status === 'PASS'), cases,
    nativeHeaders: (evidence.nativeHeaders ?? []).map(h => ({
      method: ['GET', 'POST', 'OPTIONS'].includes(h.method) ? h.method : null,
      origin: /^(chrome-extension:\/\/[a-p]{32}|http:\/\/hostile\.example\.test:\d+)$/.test(h.origin) ? h.origin : null,
      site: ['none', 'same-origin', 'same-site', 'cross-site'].includes(h.site) ? h.site : null,
      mode: ['cors', 'no-cors', 'navigate', 'same-origin'].includes(h.mode) ? h.mode : null,
      dest: ['', 'empty'].includes(h.dest) ? h.dest : null,
      bearer: h.bearer === true, status: Number.isInteger(h.status) ? h.status : null,
    })), nativePreflightObserved: evidence.nativePreflightObserved === true, nativePermissionDialogTested: false };
}
