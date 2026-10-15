import "@testing-library/jest-dom/vitest";

// jsdom lacks a few browser APIs that Radix and the theme provider use.
if (!window.matchMedia) {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
      addListener: () => undefined,
      removeListener: () => undefined,
      dispatchEvent: () => false,
    }) as MediaQueryList;
}

if (!("ResizeObserver" in window)) {
  class ResizeObserverStub {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  (window as unknown as { ResizeObserver: typeof ResizeObserverStub }).ResizeObserver = ResizeObserverStub;
}

if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => undefined;
if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false;

window.scrollTo = () => undefined;
