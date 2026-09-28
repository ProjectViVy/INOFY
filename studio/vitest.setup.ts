import "@testing-library/jest-dom/vitest";

// jsdom lacks ResizeObserver/DOMMatrixReadOnly used by React Flow.
class RO {
  observe() {}
  unobserve() {}
  disconnect() {}
}
(globalThis as Record<string, unknown>).ResizeObserver ??= RO;

if (typeof DOMMatrixReadOnly === "undefined") {
  (globalThis as Record<string, unknown>).DOMMatrixReadOnly = class {
    constructor() {}
  };
}
