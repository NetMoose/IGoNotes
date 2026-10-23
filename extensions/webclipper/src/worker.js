import { createConnectionStore } from './connection/settings.js';

const connections = createConnectionStore();
// Register synchronously so MV3 can wake this entry point. Protect on each worker
// evaluation too: a suspended/restarted worker need not receive onStartup.
// A failed restriction never reads/writes credentials and never logs native text.
const protect = () => connections.protect().catch(() => {});
chrome.runtime.onInstalled.addListener(protect);
chrome.runtime.onStartup.addListener(protect);
void protect();
