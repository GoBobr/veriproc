import "@testing-library/jest-dom";

// Node 26 does not provide a global localStorage in the test environment.
// Provide a minimal in-memory mock so AuthProvider and other components
// that read/write localStorage work in tests.
const store: Record<string, string> = {};
const localStorageMock = {
  getItem: (key: string) => store[key] ?? null,
  setItem: (key: string, value: string) => { store[key] = String(value); },
  removeItem: (key: string) => { delete store[key]; },
  clear: () => { for (const k of Object.keys(store)) delete store[k]; },
  key: (index: number) => Object.keys(store)[index] ?? null,
  get length() { return Object.keys(store).length; },
};
Object.defineProperty(globalThis, "localStorage", {
  value: localStorageMock,
  writable: true,
  configurable: true,
});

// window.open is not available in the test environment; mock it as a no-op.
if (typeof window !== "undefined" && !window.open) {
  Object.defineProperty(window, "open", {
    value: () => null,
    writable: true,
    configurable: true,
  });
}
