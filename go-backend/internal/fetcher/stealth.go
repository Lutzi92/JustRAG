package fetcher

// stealthScript is injected via page.EvalOnNewDocument BEFORE navigation in
// Tier 2 to mask common headless browser detection signals. It patches:
//   - navigator.webdriver (Chrome sets this to true in headless mode)
//   - window.chrome.runtime (missing in headless indicates automation)
//   - navigator.plugins (empty in headless, non-empty in real Chrome)
//   - navigator.languages (empty or wrong in headless)
//   - navigator.permissions.query (behaves differently for notifications)
const stealthScript = `
Object.defineProperty(navigator, 'webdriver', {
  get: () => false,
});

window.chrome = window.chrome || {};
if (!window.chrome.runtime) {
  window.chrome.runtime = {};
}

Object.defineProperty(navigator, 'plugins', {
  get: () => {
    const makePlugin = (name, description, filename) => {
      const p = { name, description, filename, length: 1 };
      p[0] = { type: 'application/x-' + name.toLowerCase().replace(/\s+/g, '-'), suffixes: '', description };
      return p;
    };
    const plugins = [
      makePlugin('Chrome PDF Plugin', 'Portable Document Format', 'internal-pdf-viewer'),
      makePlugin('Chrome PDF Viewer', 'Portable Document Format', 'mhjfbmdgcfjbbpaeojofohoefgiehjai'),
      makePlugin('Native Client', 'Native Client Executable', 'internal-nacl-plugin'),
    ];
    const arr = Object.create(PluginArray.prototype, {});
    for (let i = 0; i < plugins.length; i++) {
      arr[i] = plugins[i];
    }
    Object.defineProperty(arr, 'length', { get: () => plugins.length });
    arr.item = (index) => plugins[index] || null;
    arr.namedItem = (name) => plugins.find(p => p.name === name) || null;
    arr.refresh = () => {};
    return arr;
  },
});

Object.defineProperty(navigator, 'languages', {
  get: () => ['en-US', 'en', 'de-DE', 'de'],
});

if (window.navigator && window.navigator.permissions && typeof window.navigator.permissions.query === 'function') {
  const origQuery = window.navigator.permissions.query;
  window.navigator.permissions.query = (params) => (
    params.name === 'notifications'
      ? Promise.resolve({ state: Notification.permission })
      : origQuery.call(window.navigator.permissions, params)
  );
}
`
