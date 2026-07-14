import '@testing-library/dom';
// Initialise i18n so components rendered in tests resolve real English copy
// (react-i18next uses the shared instance; without this, t() returns raw keys).
import '../i18n';

// jsdom does not implement element scrolling. jcode-ui's stream-follow hook
// intentionally uses scrollTo, so provide the browser API shape for component
// tests without changing the production code path.
if (!HTMLElement.prototype.scrollTo) {
  HTMLElement.prototype.scrollTo = function scrollTo(options?: ScrollToOptions | number) {
    if (typeof options === 'object' && typeof options.top === 'number') {
      this.scrollTop = options.top;
    }
  };
}

// jsdom lacks the observers @headlessui/react's anchored panels (floating-ui)
// need to track the trigger's size/position. No-op stubs are enough — tests
// assert on options/selection, never on popup geometry.
class ObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() {
    return [];
  }
}
globalThis.ResizeObserver ??= ObserverStub as unknown as typeof ResizeObserver;
globalThis.IntersectionObserver ??=
  ObserverStub as unknown as typeof IntersectionObserver;
