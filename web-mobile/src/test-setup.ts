function memoryStorage(): Storage {
  const values = new Map<string, string>()
  return {
    get length() { return values.size },
    clear: () => values.clear(),
    getItem: (key) => values.get(key) ?? null,
    key: (index) => [...values.keys()][index] ?? null,
    removeItem: (key) => { values.delete(key) },
    setItem: (key, value) => { values.set(key, String(value)) },
  }
}

// Node 22's experimental Storage global shadows jsdom storage with undefined.
// Install deterministic browser-like storage before each spec module runs.
for (const key of ['localStorage', 'sessionStorage'] as const) {
  if (window[key] === undefined) {
    Object.defineProperty(window, key, { configurable: true, value: memoryStorage() })
  }
}
