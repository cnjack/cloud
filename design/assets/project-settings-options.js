(() => {
  const buttons = [...document.querySelectorAll('[data-settings-section]')];
  const panels = [...document.querySelectorAll('[data-settings-panel]')];
  if (buttons.length === 0 || panels.length === 0) return;

  const heading = document.querySelector('[data-settings-heading]');
  const description = document.querySelector('[data-settings-description]');

  const activate = (id, updateHash = true) => {
    const button = buttons.find((item) => item.dataset.settingsSection === id) || buttons[0];
    if (!button) return;
    const nextId = button.dataset.settingsSection;

    buttons.forEach((item) => {
      if (item === button) item.setAttribute('aria-current', 'page');
      else item.removeAttribute('aria-current');
    });
    panels.forEach((panel) => panel.toggleAttribute('hidden', panel.dataset.settingsPanel !== nextId));
    if (heading) heading.textContent = button.dataset.title || button.textContent || '';
    if (description) description.textContent = button.dataset.description || '';
    if (updateHash) history.replaceState(null, '', `#${nextId}`);
    document.querySelector('.surface-scroll')?.scrollTo({ top: 0, behavior: 'smooth' });
  };

  buttons.forEach((button) => button.addEventListener('click', () => activate(button.dataset.settingsSection)));
  activate(location.hash.slice(1) || 'general', false);
})();
