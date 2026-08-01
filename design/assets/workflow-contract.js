document.addEventListener('DOMContentLoaded', () => {
  const disclosure = document.querySelector('[data-contract-disclosure]');
  const body = document.querySelector('[data-contract-body]');
  disclosure?.addEventListener('click', () => {
    const expanded = disclosure.getAttribute('aria-expanded') === 'true';
    disclosure.setAttribute('aria-expanded', String(!expanded));
    if (body) body.classList.toggle('wc-hidden', expanded);
    disclosure.textContent = expanded ? 'Show scope' : 'Hide scope';
  });

  document.querySelectorAll('[data-review-tab]').forEach((button) => {
    button.addEventListener('click', () => {
      document.querySelectorAll('[data-review-tab]').forEach((item) => item.setAttribute('aria-selected', String(item === button)));
      const target = button.getAttribute('data-review-tab');
      document.querySelectorAll('[data-review-panel]').forEach((panel) => {
        panel.classList.toggle('wc-hidden', panel.getAttribute('data-review-panel') !== target);
      });
    });
  });
});
