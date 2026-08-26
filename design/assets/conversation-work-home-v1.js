(() => {
  const $ = (selector, scope = document) => scope.querySelector(selector);
  const $$ = (selector, scope = document) => [...scope.querySelectorAll(selector)];
  const labels = { repository: '按 Repository', recent: '最近使用', context: '当前上下文' };

  function filter(query = '') {
    const view = $('[data-view]:not([hidden])');
    const needle = query.trim().toLowerCase();
    let visible = 0;
    $$('[data-conversation]', view).forEach((item) => {
      const match = !needle || `${item.textContent} ${item.dataset.context}`.toLowerCase().includes(needle);
      item.hidden = !match;
      if (match) visible += 1;
    });
    $$('.repo-group', view).forEach((group) => {
      const hasMatch = $$('[data-conversation]', group).some((item) => !item.hidden);
      group.hidden = !hasMatch;
      if (needle && hasMatch) {
        group.classList.add('is-open');
        $('[data-group]', group)?.setAttribute('aria-expanded', 'true');
      }
    });
    $('[data-empty]').hidden = visible !== 0;
  }

  function setVariant(name) {
    $$('[data-variant]').forEach((button) => button.classList.toggle('is-active', button.dataset.variant === name));
    $$('[data-view]').forEach((view) => { view.hidden = view.dataset.view !== name; });
    $('[data-list-label]').textContent = labels[name];
    $('[data-search]').value = '';
    filter();
  }

  $$('[data-variant]').forEach((button) => button.addEventListener('click', () => setVariant(button.dataset.variant)));
  $('[data-all]')?.addEventListener('click', () => setVariant('recent'));
  $('[data-search]')?.addEventListener('input', (event) => filter(event.currentTarget.value));
  $$('[data-group]').forEach((button) => button.addEventListener('click', () => {
    const group = button.closest('.repo-group');
    const open = !group.classList.contains('is-open');
    group.classList.toggle('is-open', open);
    button.setAttribute('aria-expanded', String(open));
  }));

  function showHome() {
    $('[data-home]').hidden = false;
    $('[data-preview]').hidden = true;
    $('[data-surface-label]').textContent = 'Work Home';
    $$('[data-conversation]').forEach((item) => item.classList.remove('is-active'));
    $('[data-new]').classList.add('is-active');
    document.body.classList.remove('mobile-rail-open');
  }
  $('[data-new]').addEventListener('click', showHome);
  $('[data-back]').addEventListener('click', showHome);
  $$('[data-conversation]').forEach((item) => item.addEventListener('click', () => {
    $$('[data-conversation]').forEach((candidate) => candidate.classList.remove('is-active'));
    item.classList.add('is-active');
    $('[data-new]').classList.remove('is-active');
    $('[data-preview-title]').textContent = item.dataset.title;
    $('[data-preview-context]').textContent = item.dataset.context;
    $('[data-surface-label]').textContent = item.dataset.context;
    $('[data-home]').hidden = true;
    $('[data-preview]').hidden = false;
    document.body.classList.remove('mobile-rail-open');
  }));

  $$('[data-rail-toggle]').forEach((button) => button.addEventListener('click', () => {
    const compact = window.matchMedia('(max-width: 58rem)').matches;
    document.body.classList.toggle(compact ? 'mobile-rail-open' : 'rail-collapsed');
  }));

  function toggleMenu(trigger, menu) {
    const open = menu.hidden;
    $$('[data-account-menu],[data-mode-menu]').forEach((candidate) => { candidate.hidden = true; });
    menu.hidden = !open;
    trigger.setAttribute('aria-expanded', String(open));
  }
  $('[data-account]').addEventListener('click', (event) => { event.stopPropagation(); toggleMenu(event.currentTarget, $('[data-account-menu]')); });
  $('[data-mode]').addEventListener('click', (event) => { event.stopPropagation(); toggleMenu(event.currentTarget, $('[data-mode-menu]')); });
  $$('[data-account-menu],[data-mode-menu]').forEach((menu) => menu.addEventListener('click', (event) => event.stopPropagation()));
  document.addEventListener('click', () => $$('[data-account-menu],[data-mode-menu]').forEach((menu) => { menu.hidden = true; }));

  const drawer = $('[data-drawer]');
  const backdrop = $('[data-backdrop]');
  function showNotes(open) {
    drawer.hidden = !open;
    backdrop.hidden = !open;
    $('[data-notes-toggle]').setAttribute('aria-expanded', String(open));
  }
  $('[data-notes-toggle]').addEventListener('click', () => showNotes(drawer.hidden));
  $('[data-close]').addEventListener('click', () => showNotes(false));
  backdrop.addEventListener('click', () => showNotes(false));

  const input = $('[data-input]');
  input.addEventListener('input', () => { $('[data-send]').disabled = !input.value.trim(); });
  $('[data-composer]').addEventListener('submit', (event) => event.preventDefault());
  $('[data-theme]').addEventListener('click', () => {
    const dark = document.documentElement.dataset.theme !== 'dark';
    document.documentElement.dataset.theme = dark ? 'dark' : 'light';
    $('[data-theme] use').setAttribute('href', `./assets/icons.svg#${dark ? 'i-sun' : 'i-moon'}`);
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') { showNotes(false); document.body.classList.remove('mobile-rail-open'); }
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') { event.preventDefault(); $('[data-search]').focus(); }
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'n') { event.preventDefault(); showHome(); input.focus(); }
  });
  setVariant('repository');
})();
