import '@testing-library/dom';

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
