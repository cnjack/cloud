import '@testing-library/dom';
// Initialise i18next so components rendered in tests resolve real English copy
// (react-i18next uses the shared default instance; without this, t() returns
// raw keys). Uses the package's own bundles so the suite runs standalone.
import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import { deviceUiInterpolation, deviceUiResources } from '../i18n';

if (!i18n.isInitialized) {
  void i18n.use(initReactI18next).init({
    resources: deviceUiResources,
    lng: 'en',
    fallbackLng: 'en',
    interpolation: { ...deviceUiInterpolation },
  });
}

// jsdom does not implement element scrolling; some rendered trees call
// scrollIntoView/scrollTo through hooks.
if (!HTMLElement.prototype.scrollTo) {
  HTMLElement.prototype.scrollTo = function scrollTo(options?: ScrollToOptions | number) {
    if (typeof options === 'object' && typeof options.top === 'number') {
      this.scrollTop = options.top;
    }
  };
}
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = function scrollIntoView() {};
}

// jsdom lacks the observers some anchored panels need; no-op stubs are enough —
// tests assert on options/selection, never on popup geometry.
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
