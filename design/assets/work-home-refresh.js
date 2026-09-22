/* All records and options are fixtures. This prototype never calls product APIs. */
(() => {
  const $ = (selector) => document.querySelector(selector);
  const $$ = (selector) => [...document.querySelectorAll(selector)];
  const tasks = [
    { id: 1, title: '修复 Composer 菜单层级与键盘焦点', detail: '正在检查组件与相关测试', status: 'running', label: '运行中', group: 'active', time: '刚刚', icon: 'terminal', model: 'GLM-5.2' },
    { id: 2, title: '为对话列表补充搜索与筛选', detail: '需要确认：是否保留归档对话？', status: 'waiting', label: '等待确认', group: 'active', time: '12 分钟前', icon: 'info', model: 'GLM-5.2' },
    { id: 3, title: '验证 CubeSandbox 工作区恢复', detail: '运行失败 · 查看执行记录以继续', status: 'failed', label: '失败', group: 'attention', time: '昨天', icon: 'warning', model: 'GLM-5.2' },
    { id: 4, title: '统一 /copy 的引用与目标选择', detail: 'main · 实现与测试已完成', status: 'done', label: '已完成', group: 'done', time: '昨天', icon: 'check', model: 'GPT-5.6' },
    { id: 5, title: '梳理仓库架构与核心模块', detail: 'main · 已生成架构说明', status: 'done', label: '已完成', group: 'done', time: '9 月 19 日', icon: 'check', model: 'GLM-5.2' },
  ];
  const params = new URLSearchParams(location.search);
  const previewStorage = { get(key, fallback = '') { try { return sessionStorage.getItem(`jcode-design:${key}`) ?? fallback; } catch { return fallback; } }, set(key, value) { try { sessionStorage.setItem(`jcode-design:${key}`, value); } catch {} } };
  let repository = 'jcode';
  let filter = 'all';
  let unavailable = false;
  let attachments = [];
  const svg = (name) => `<svg class="icon" aria-hidden="true"><use href="./assets/icons.svg#i-${name}"/></svg>`;
  const dot = (status) => `<span class="state-icon ${status}" aria-hidden="true"></span>`;
  function showTask(id) {
    location.href = `./cloud-conversation.html?task=${encodeURIComponent(id)}&repo=jcode`;
  }
  function render() {
    const scoped = repository === 'jcode' ? tasks : [];
    const query = $('#task-search').value.trim().toLocaleLowerCase();
    const shown = scoped.filter((task) => (filter === 'all' || filter === task.group) && `${task.title} ${task.detail}`.toLocaleLowerCase().includes(query));
    $('#task-count').textContent = scoped.length;
    $('#task-list').innerHTML = shown.map((task) => `<button class="task-row" data-task="${task.id}"><span class="task-symbol">${svg(task.icon)}</span><span class="task-copy"><strong>${task.title}</strong><small>${task.detail}</small></span><span class="task-status">${dot(task.status)}${task.label}</span><time>${task.time}</time>${svg('chevron-right')}</button>`).join('');
    $('#empty-state').hidden = scoped.length !== 0;
    $('#no-results').hidden = !scoped.length || !!shown.length;
    $('#task-description').textContent = scoped.length ? '接着上次的进度，继续向前。' : 'cnjack / bigmodel-usage';
    $$('[data-filter]').forEach((button) => {
      button.classList.toggle('active', button.dataset.filter === filter);
      button.setAttribute('aria-pressed', String(button.dataset.filter === filter));
      button.querySelector('span').textContent = scoped.filter((task) => button.dataset.filter === 'all' || button.dataset.filter === task.group).length;
    });
    $$('[data-task]').forEach((button) => button.addEventListener('click', () => showTask(button.dataset.task)));
  }
  function selectRepository(value) {
    repository = value;
    previewStorage.set('repo', value);
    const current = new URL(location.href);
    current.searchParams.set('repo', value);
    history.replaceState(null, '', current);
    document.querySelectorAll('.repo-tabs a').forEach((link) => { const next = new URL(link.href); next.searchParams.set('repo', value); link.href = next; });
    $('#repository').value = value;
    $('#branch').value = previewStorage.get(`branch:${value}`, 'main');
    $$('[data-repo]').forEach((button) => {
      button.classList.toggle('selected', button.dataset.repo === value);
      button.setAttribute('aria-pressed', String(button.dataset.repo === value));
    });
    filter = 'all';
    $('#task-search').value = '';
    render();
  }
  function renderRepositories() {
    const query = $('#repository-search').value.trim().toLocaleLowerCase();
    const options = [...$('#repository').options].filter((option) => option.textContent.toLocaleLowerCase().includes(query));
    $('#repository-results').replaceChildren(...options.map((option) => {
      const button = document.createElement('button');
      const selected = option.value === repository;
      button.type = 'button';
      button.className = 'repository-option';
      button.setAttribute('aria-current', String(selected));
      button.innerHTML = `${svg('git')}<span><strong></strong><small>GitHub · main</small></span>${selected ? svg('check') : svg('arrow-right')}`;
      button.querySelector('strong').textContent = option.textContent;
      button.addEventListener('click', () => {
        selectRepository(option.value);
        $('#repository-browser').hidePopover();
        closeRail();
        $('#repository').focus();
      });
      return button;
    }));
    $('#repository-no-results').hidden = options.length !== 0;
  }
  $('#repository-browser').addEventListener('beforetoggle', (event) => {
    if (event.newState === 'open') {
      $('#repository-search').value = '';
      renderRepositories();
    }
  });
  $('#repository-search').addEventListener('input', renderRepositories);
  function updateComposer() {
    previewStorage.set('home-draft', $('#prompt').value);
    $('.start-button').disabled = unavailable || !$('#prompt').value.trim();
    $('.model-blocker').hidden = !unavailable;
    $('#model').disabled = unavailable;
    $('#effort').disabled = unavailable;
    $('#readiness').setAttribute('aria-pressed', String(unavailable));
    $('#readiness').textContent = unavailable ? '恢复可用模型预览' : '预览缺少模型状态';
  }
  function closeRail() {
    $('#rail').classList.remove('open');
    $('.mobile-toggle').setAttribute('aria-expanded', 'false');
  }
  function startNew() {
    closeRail();
    $('#prompt').focus();
    $('#prompt').scrollIntoView({ block: 'center', behavior: 'smooth' });
  }
  $('#history').innerHTML = tasks.slice(0, 4).map((task) => `<button class="history-item" data-history="${task.id}">${dot(task.status)}<span><strong>${task.title}</strong><small>jcode · ${task.label}</small></span></button>`).join('');
  $$('[data-history]').forEach((button) => button.addEventListener('click', () => showTask(button.dataset.history)));
  $$('[data-repo]').forEach((button) => button.addEventListener('click', () => { selectRepository(button.dataset.repo); closeRail(); }));
  $('#repository').addEventListener('change', (event) => selectRepository(event.target.value));
  $$('[data-filter]').forEach((button) => button.addEventListener('click', () => { filter = button.dataset.filter; render(); }));
  $('#task-search').addEventListener('input', render);
  $('#clear-filters').addEventListener('click', () => { filter = 'all'; $('#task-search').value = ''; render(); });
  $$('[data-new]').forEach((button) => button.addEventListener('click', startNew));
  function focusSearch() { closeRail(); $('#task-search').focus(); }
  $('[data-search-focus]').addEventListener('click', focusSearch);
  $$('[data-prompt]').forEach((button) => button.addEventListener('click', () => { $('#prompt').value = button.dataset.prompt; updateComposer(); startNew(); }));
  $('#prompt').addEventListener('input', updateComposer);
  $('#readiness').addEventListener('click', () => { unavailable = !unavailable; updateComposer(); });
  $('#model').addEventListener('change', () => { const gpt = $('#model').value === 'gpt'; $('#provider-icon').src = `./assets/provider-${gpt ? 'openai' : 'zhipu'}.svg`; $('#provider-icon').alt = gpt ? 'OpenAI' : '智谱'; });
  $('#composer').addEventListener('submit', (event) => {
    event.preventDefault();
    if (unavailable || !$('#prompt').value.trim()) return;
    previewStorage.set('start-draft', $('#prompt').value);
    previewStorage.set('start-context', JSON.stringify({ branch: $('#branch').value, model: $('#model').selectedOptions[0].textContent, effort: $('#effort').value, permission: $('#permission').value, attachments: attachments.map(file => file.name) }));
    location.href = `./cloud-conversation.html?new=1&repo=${encodeURIComponent(repository)}`;
  });
  function renderAttachments() {
    $('#attachments').replaceChildren(...attachments.map((file, index) => {
      const button = document.createElement('button');
      button.type = 'button'; button.textContent = `${file.name} ×`; button.setAttribute('aria-label', `移除附件 ${file.name}`);
      button.addEventListener('click', () => { attachments.splice(index, 1); renderAttachments(); });
      return button;
    }));
  }
  $('#attach').addEventListener('click', () => $('#files').click());
  $('#files').addEventListener('change', (event) => { attachments.push(...event.target.files); renderAttachments(); event.target.value = ''; });
  $('[data-settings]').addEventListener('click', () => { location.href = './cloud-settings.html'; });
  $('[data-model-settings]').addEventListener('click', () => { location.href = './cloud-settings-models.html'; });
  $('.mobile-toggle').addEventListener('click', () => { const open = $('#rail').classList.toggle('open'); $('.mobile-toggle').setAttribute('aria-expanded', String(open)); });
  document.addEventListener('click', (event) => { if (!event.target.closest('#rail, .mobile-toggle')) closeRail(); });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') { closeRail(); return; }
    if ($('#repository-browser').matches(':popover-open')) return;
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') { event.preventDefault(); focusSearch(); }
    if (event.altKey && event.code === 'KeyN') { event.preventDefault(); startNew(); }
    if ((event.metaKey || event.ctrlKey) && event.key === 'Enter' && event.target === $('#prompt')) { event.preventDefault(); $('#composer').requestSubmit(); }
  });
  ['model', 'effort', 'permission'].forEach(id => {
    const saved = previewStorage.get(`composer:${id}`);
    if (saved && [...document.getElementById(id).options].some(option => option.value === saved)) document.getElementById(id).value = saved;
    document.getElementById(id).addEventListener('change', event => previewStorage.set(`composer:${id}`, event.target.value));
  });
  $('#branch').addEventListener('change', event => previewStorage.set(`branch:${repository}`, event.target.value));
  $('#model').dispatchEvent(new Event('change'));
  $('#prompt').value = previewStorage.get('home-draft');
  selectRepository(params.get('repo') === 'bigmodel-usage' ? 'bigmodel-usage' : params.get('repo') === 'jcode' ? 'jcode' : previewStorage.get('repo', 'jcode'));
  if (params.get('focus') === 'search') focusSearch();
  updateComposer();
})();
