// Register synchronously so MV3 can wake this entry point. Coordination comes later.
chrome.runtime.onInstalled.addListener(() => {});
