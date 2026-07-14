(() => {
  const root = document.documentElement;
  const stored = localStorage.getItem('jcode-design-theme');
  const preferredDark = window.matchMedia?.('(prefers-color-scheme: dark)').matches;
  root.dataset.theme = stored || (preferredDark ? 'dark' : 'light');

  const syncThemeButtons = () => {
    document.querySelectorAll('[data-theme-toggle]').forEach((button) => {
      const dark = root.dataset.theme === 'dark';
      button.setAttribute('aria-label', dark ? 'Use light theme' : 'Use dark theme');
      button.setAttribute('title', dark ? 'Use light theme' : 'Use dark theme');
      button.dataset.themeState = dark ? 'dark' : 'light';
    });
  };

  document.addEventListener('click', async (event) => {
    const target = event.target instanceof Element ? event.target : null;
    if (!target) return;

    const themeButton = target.closest('[data-theme-toggle]');
    if (themeButton) {
      root.dataset.theme = root.dataset.theme === 'dark' ? 'light' : 'dark';
      localStorage.setItem('jcode-design-theme', root.dataset.theme);
      syncThemeButtons();
      return;
    }

    const copyButton = target.closest('[data-copy]');
    if (copyButton instanceof HTMLButtonElement) {
      const value = copyButton.dataset.copy || '';
      try {
        await navigator.clipboard.writeText(value);
        const original = copyButton.dataset.label || copyButton.textContent || 'Copy';
        copyButton.dataset.label = original;
        copyButton.textContent = 'Copied';
        window.setTimeout(() => { copyButton.textContent = original; }, 1400);
      } catch {
        showToast('Clipboard access is unavailable. Select the command to copy it.');
      }
      return;
    }

    const prototypeAction = target.closest('[data-prototype-action]');
    if (prototypeAction) {
      event.preventDefault();
      showToast(prototypeAction.getAttribute('data-prototype-action') || 'Prototype action');
    }
  });

  const search = document.querySelector('[data-project-search]');
  if (search instanceof HTMLInputElement) {
    const rows = [...document.querySelectorAll('[data-project-row]')];
    const count = document.querySelector('[data-project-count]');
    const empty = document.querySelector('[data-search-empty]');
    const filter = () => {
      const query = search.value.trim().toLowerCase();
      let visible = 0;
      rows.forEach((row) => {
        const match = !query || (row.textContent || '').toLowerCase().includes(query);
        row.toggleAttribute('hidden', !match);
        if (match) visible += 1;
      });
      if (count) count.textContent = String(visible);
      if (empty) empty.toggleAttribute('hidden', visible > 0);
    };
    search.addEventListener('input', filter);
  }

  const modelSearch = document.querySelector('[data-model-search]');
  if (modelSearch instanceof HTMLInputElement) {
    const cards = [...document.querySelectorAll('[data-provider-card]')];
    const count = document.querySelector('[data-model-count]');
    const empty = document.querySelector('[data-model-search-empty]');
    const filterModels = () => {
      const query = modelSearch.value.trim().toLowerCase();
      let visible = 0;

      cards.forEach((card) => {
        const providerText = `${card.getAttribute('data-provider-search') || ''} ${card.querySelector('.provider-card-head')?.textContent || ''}`.toLowerCase();
        const providerMatch = !query || providerText.includes(query);
        const rows = [...card.querySelectorAll('[data-catalog-model]')];
        let visibleInCard = 0;

        rows.forEach((row) => {
          const match = providerMatch || (row.textContent || '').toLowerCase().includes(query);
          row.toggleAttribute('hidden', !match);
          if (match) visibleInCard += 1;
        });

        card.toggleAttribute('hidden', visibleInCard === 0);
        visible += visibleInCard;
      });

      if (count) count.textContent = String(visible);
      if (empty) empty.toggleAttribute('hidden', visible > 0);
    };

    modelSearch.addEventListener('input', filterModels);
  }

  document.querySelectorAll('[data-form-prototype]').forEach((form) => {
    form.addEventListener('submit', (event) => {
      event.preventDefault();
      const required = form.querySelector('[required]');
      if (required instanceof HTMLInputElement && !required.value.trim()) {
        required.setAttribute('aria-invalid', 'true');
        const error = form.querySelector('[data-field-error]');
        if (error) error.removeAttribute('hidden');
        required.focus();
        return;
      }
      showToast(form.getAttribute('data-form-prototype') || 'Prototype only — nothing was saved.');
    });
  });

  function showToast(message) {
    let toast = document.querySelector('.prototype-toast');
    if (!toast) {
      toast = document.createElement('div');
      toast.className = 'prototype-toast';
      toast.setAttribute('role', 'status');
      document.body.append(toast);
    }
    toast.textContent = message;
    toast.dataset.visible = 'true';
    window.clearTimeout(showToast.timeout);
    showToast.timeout = window.setTimeout(() => { delete toast.dataset.visible; }, 2600);
  }

  syncThemeButtons();
})();
