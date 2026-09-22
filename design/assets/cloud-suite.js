/* Local design fixtures only. No product API or credential access. */
(() => {
  const $ = (s, root = document) => root.querySelector(s);
  const $$ = (s, root = document) => [...root.querySelectorAll(s)];
  const page = document.body.dataset.page;
  const params = new URLSearchParams(location.search);
  const storage = {
    get(key, fallback = '') { try { return sessionStorage.getItem(`jcode-design:${key}`) ?? fallback; } catch { return fallback; } },
    set(key, value) { try { sessionStorage.setItem(`jcode-design:${key}`, value); } catch { /* Preview still works without storage. */ } },
  };
  const repo = params.get('repo') === 'bigmodel-usage' ? 'bigmodel-usage' : params.get('repo') === 'jcode' ? 'jcode' : storage.get('repo', 'jcode');
  storage.set('repo', repo);
  const device = ['mac', 'linux', 'nas'].includes(params.get('device')) ? params.get('device') : 'mac';
  const deviceName = { mac: 'Jack 的 MacBook', linux: '开发工作站', nas: '家庭服务器' }[device];
  const isAccountUsage = page === 'usage' && params.get('scope') === 'account';
  const svg = (name) => `<svg class="icon" aria-hidden="true"><use href="./assets/icons.svg#i-${name}"/></svg>`;
  const url = (file, values = {}) => { const u = new URL(file, location.href); Object.entries(values).forEach(([key, value]) => u.searchParams.set(key, value)); return u.href; };
  let toastTimer;
  function toast(message) { clearTimeout(toastTimer); $('.s-toast').textContent = message; $('.s-toast').hidden = false; toastTimer = setTimeout(() => { $('.s-toast').hidden = true; }, 6500); }
  function dialog(title, markup) { $('#suite-dialog-title').textContent = title; $('#suite-dialog-body').innerHTML = markup; $('#suite-dialog').showModal(); return $('#suite-dialog-body'); }
  const previewNote = '<p class="s-muted">仅更新本地设计预览，不会提交到真实服务。</p>';
  function goRepo(next) { storage.set('repo', next); const currentIsRepo = document.body.dataset.scope === 'repository' && !isAccountUsage; location.href = url(currentIsRepo ? location.pathname : 'work-home-refresh.html', { repo: next }); }
  $$('[data-repo-name]').forEach((el) => { el.textContent = `cnjack / ${repo}`; });
  $$('[data-repo-link]').forEach((link) => { link.href = url(link.getAttribute('href'), { repo }); });
  $$('[data-device-link]').forEach((link) => { link.href = url(link.getAttribute('href'), { device }); });
  $$('[data-repo]').forEach((button) => {
    button.classList.toggle('selected', button.dataset.repo === repo && !['devices', 'device', 'device-connect', 'remote-conversation'].includes(page));
    button.setAttribute('aria-pressed', String(button.classList.contains('selected')));
    button.addEventListener('click', () => goRepo(button.dataset.repo));
  });
  function closeRail() { $('#rail').classList.remove('open'); $('.mobile-toggle').setAttribute('aria-expanded', 'false'); }
  $('.mobile-toggle').addEventListener('click', () => { $('.mobile-toggle').setAttribute('aria-expanded', String($('#rail').classList.toggle('open'))); });
  document.addEventListener('click', (event) => { if (!event.target.closest('#rail, .mobile-toggle, #repository-browser')) closeRail(); });
  document.addEventListener('keydown', (event) => { if (event.key === 'Escape') closeRail(); });
  $('[data-new]').addEventListener('click', () => { location.href = url('work-home-refresh.html', { repo }); });
  $('[data-search-focus]').addEventListener('click', () => { location.href = url('work-home-refresh.html', { repo, focus: 'search' }); });
  $('[data-settings]').addEventListener('click', () => { location.href = url('cloud-settings.html'); });
  $('[data-settings]').classList.toggle('active', ['profile', 'connections', 'models', 'preferences', 'admin'].includes(page) || isAccountUsage);
  $('.remote-nav a').classList.toggle('active', ['devices', 'device', 'device-connect', 'remote-conversation'].includes(page));
  const titles = ['修复 Composer 菜单层级与键盘焦点', '为对话列表补充搜索与筛选', '验证 CubeSandbox 工作区恢复', '统一 /copy 的引用与目标选择', '梳理仓库架构与核心模块'];
  const statuses = ['运行中', '等待确认', '失败', '已完成', '已完成'];
  $('#history').innerHTML = titles.slice(0, 4).map((title, i) => `<a class="history-item" href="${url('cloud-conversation.html', { task: i + 1, repo: 'jcode' })}" ${page === 'conversation' && Number(params.get('task') || 1) === i + 1 ? 'aria-current="page"' : ''}><span class="state-icon ${['running', 'waiting', 'failed', 'done'][i]}"></span><span><strong>${title}</strong><small>jcode · ${statuses[i]}</small></span></a>`).join('');
  function renderRepositories() {
    const query = $('#repository-search').value.toLocaleLowerCase().trim();
    const repos = ['jcode', 'bigmodel-usage'].filter((name) => `cnjack / ${name}`.includes(query));
    $('#repository-results').replaceChildren(...repos.map((name) => { const button = document.createElement('button'); button.className = 'repository-option'; button.setAttribute('aria-current', String(repo === name)); button.innerHTML = `${svg('git')}<span><strong>cnjack / ${name}</strong><small>GitHub · main</small></span>${svg(repo === name ? 'check' : 'arrow-right')}`; button.addEventListener('click', () => goRepo(name)); return button; }));
    $('#repository-no-results').hidden = !!repos.length;
  }
  $('#repository-browser').addEventListener('beforetoggle', (event) => { if (event.newState === 'open') { $('#repository-search').value = ''; renderRepositories(); } });
  $('#repository-search').addEventListener('input', renderRepositories);
  $$('[data-repo-populated]').forEach((el) => { el.hidden = repo !== 'jcode' && !isAccountUsage; });
  $$('[data-repo-empty]').forEach((el) => { el.hidden = repo === 'jcode' || isAccountUsage; });
  if (isAccountUsage) {
    document.body.dataset.scope = 'account';
    $('.s-repo-context')?.remove();
    $('.s-shared-composer')?.remove();
    $('.repo-tabs').innerHTML = '<a href="./cloud-settings.html">个人资料</a><a href="./cloud-settings-connections.html">Git 账号</a><a href="./cloud-settings-models.html">模型</a><a class="active" aria-current="page" href="./cloud-usage.html?scope=account">用量</a><a href="./cloud-settings-preferences.html">偏好</a>';
    $('.s-heading h1').textContent = '账号用量';
    $('.s-heading p').textContent = '所有仓库的个人运行汇总 · 最近 7 天 · 示例数据';
  }
  if ($('#composer')) {
    const prompt = $('#prompt');
    let modelUnavailable = false;
    let files = [];
    prompt.value = storage.get('home-draft');
    $('#repository').value = repo;
    $('#branch').value = storage.get(`branch:${repo}`, 'main');
    $('#branch').addEventListener('change', event => storage.set(`branch:${repo}`, event.target.value));
    ['model', 'effort', 'permission'].forEach(id => {
      const saved = storage.get(`composer:${id}`);
      if (saved && [...document.getElementById(id).options].some(option => option.value === saved)) document.getElementById(id).value = saved;
      document.getElementById(id).addEventListener('change', event => storage.set(`composer:${id}`, event.target.value));
    });
    $('#repository').addEventListener('change', (event) => goRepo(event.target.value));
    function updateComposer() {
      storage.set('home-draft', prompt.value);
      $('.start-button').disabled = modelUnavailable || !prompt.value.trim();
      $('.model-blocker').hidden = !modelUnavailable;
      $('#model').disabled = modelUnavailable;
      $('#effort').disabled = modelUnavailable;
      $('#readiness').textContent = modelUnavailable ? '恢复可用模型预览' : '预览缺少模型状态';
      $('#readiness').setAttribute('aria-pressed', String(modelUnavailable));
    }
    prompt.addEventListener('input', updateComposer);
    $('#readiness').addEventListener('click', () => { modelUnavailable = !modelUnavailable; updateComposer(); });
    $('[data-model-settings]').addEventListener('click', () => { location.href = url('cloud-settings-models.html'); });
    $$('[data-prompt]').forEach(button => button.addEventListener('click', () => { prompt.value = button.dataset.prompt; updateComposer(); prompt.focus(); }));
    $('#model').addEventListener('change', () => { const openai = $('#model').value === 'gpt'; $('#provider-icon').src = `./assets/provider-${openai ? 'openai' : 'zhipu'}.svg`; $('#provider-icon').alt = openai ? 'OpenAI' : '智谱'; });
    $('#attach').addEventListener('click', () => $('#files').click());
    function renderFiles() {
      $('#attachments').replaceChildren(...files.map((file, index) => { const button = document.createElement('button'); button.type = 'button'; button.textContent = `${file.name} ×`; button.setAttribute('aria-label', `移除附件 ${file.name}`); button.addEventListener('click', () => { files.splice(index, 1); renderFiles(); }); return button; }));
    }
    $('#files').addEventListener('change', (event) => { files.push(...event.target.files); renderFiles(); event.target.value = ''; });
    $('#composer').addEventListener('submit', (event) => { event.preventDefault(); if(modelUnavailable || !prompt.value.trim()) return; storage.set('start-draft', prompt.value); storage.set('start-context', JSON.stringify({ branch: $('#branch').value, model: $('#model').selectedOptions[0].textContent, effort: $('#effort').value, permission: $('#permission').value, attachments: files.map(file => file.name) })); location.href = url('cloud-conversation.html', { repo, new:'1' }); });
    prompt.addEventListener('keydown', (event) => { if((event.metaKey || event.ctrlKey) && event.key === 'Enter') { event.preventDefault(); $('#composer').requestSubmit(); } });
    $('#model').dispatchEvent(new Event('change'));
    updateComposer();
  }
  $$('[data-feedback]').forEach((button) => button.addEventListener('click', () => toast(button.dataset.feedback)));
  $$('[data-preview-toggle]').forEach((input) => input.addEventListener('change', () => toast(`本地预览已${input.checked ? '启用' : '暂停'}；真实自动化状态未改变。`)));
  $$('[data-preview-form]').forEach((form) => {
    const key = `${form.dataset.saveKey}:${document.body.dataset.scope === 'repository' ? repo : 'account'}`;
    let saved;
    try { saved = JSON.parse(storage.get(key, 'null')); } catch { saved = null; }
    if (saved) [...form.elements].forEach((el) => { if (el.name && saved[el.name] !== undefined) el.value = saved[el.name]; });
    form.addEventListener('submit', (event) => { event.preventDefault(); storage.set(key, JSON.stringify(Object.fromEntries(new FormData(form)))); $('.s-form-result', form).textContent = form.dataset.previewForm; });
  });
  $$('[data-list-search]').forEach((input) => input.addEventListener('input', () => { const items = $$('[data-search-list] > a'); let shown = 0; items.forEach((item) => { item.hidden = !item.textContent.toLocaleLowerCase().includes(input.value.trim().toLocaleLowerCase()); if (!item.hidden) shown++; }); $('.s-search-empty').hidden = !!shown; }));
  // Repository workflow views.
  $$('[data-card]').forEach((card) => card.addEventListener('click', () => {
    const body = dialog(`JC-${card.dataset.card} · ${card.dataset.title}`, '<p>来自 jtype / jcode 看板的示例卡片。</p><label class="s-field"><span>所在列</span><select id="card-column"><option value="backlog">待办</option><option value="queue">Agent 队列</option><option value="progress">进行中</option><option value="done">已完成</option></select></label><div class="s-actions"><button class="s-btn s-primary" id="move-card">移动卡片预览</button><a class="s-btn" id="card-conversation">查看关联对话</a></div>' + previewNote);
    $('#card-column', body).value = card.closest('[data-column]').dataset.column;
    $('#card-conversation', body).href = url('cloud-conversation.html', { task: card.dataset.card === '96' ? '4' : card.dataset.card === '103' ? '2' : '1', repo });
    $('#move-card', body).addEventListener('click', () => { $(`[data-column="${$('#card-column', body).value}"] .s-board-items`).append(card); $$('.s-board-column').forEach((col) => { $('.column-count', col).textContent = $$('[data-card]', col).length; }); $('#suite-dialog').close(); toast('卡片仅在本页预览中移动；没有更新 jtype 或启动任务。'); });
  }));
  $$('[data-board-settings]').forEach((button) => button.addEventListener('click', () => {
    const body = dialog('连接 jtype 看板', '<p>选择当前仓库关联的看板和触发列。</p><label class="s-field"><span>工作区 / 看板</span><select><option>cnjack / jcode</option></select></label><label class="s-field"><span>Agent 队列</span><select><option>Agent 队列</option></select></label><button class="s-btn s-primary" id="save-board">保存连接预览</button><a class="s-text-link" href="./cloud-settings-connections.html">管理账号授权</a>' + previewNote);
    $('#save-board', body).addEventListener('click', () => { $('#suite-dialog').close(); toast('连接配置已预览；没有调用 jtype 或修改仓库。'); });
  }));
  $$('[data-new-review]').forEach((button) => button.addEventListener('click', () => {
    const body = dialog('发起代码审查', '<form id="new-review-form"><label class="s-field"><span>Pull request 链接</span><input type="url" required name="pr" placeholder="https://github.com/cnjack/jcode/pull/218"></label><label class="s-field"><span>模型</span><select><option>GLM-5.2</option></select></label><button class="s-btn s-primary" type="submit">预览审查请求</button><p id="review-request-result" role="status"></p></form>' + previewNote);
    $('#new-review-form', body).addEventListener('submit', (event) => { event.preventDefault(); $('#review-request-result', body).textContent = `已准备审查 ${$('input', body).value}。设计稿没有提交请求。`; });
  }));
  if (page === 'review-detail' && params.get('review') && params.get('review') !== '218') {
    const id = params.get('review');
    $('.s-heading h1').textContent = id === '216' ? '优化模型选择器键盘导航' : '统一会话复制操作';
    $('.s-heading p').textContent = `Pull request #${id} · 已完成审查`;
    $('.s-split>section').innerHTML = '<div class="s-completion">'+svg('check')+'<div><strong>本次审查未发现需要处理的问题</strong><p>示例结果仅覆盖所记录的提交，不代表未来提交也已审查。</p></div></div>';
    $('.s-aside dd').textContent = '已完成';
  }
  if (page === 'automation-edit') {
    const id = params.get('id');
    if (id === 'review') { $('[name="name"]').value = 'Pull request 自动审查'; $('[name="trigger"]').value = 'Pull request 就绪'; $('[name="prompt"]').value = '审查当前 Pull request 的改动，指出真实缺陷，并给出文件和行号。'; }
    else if (!id) { $('[name="name"]').value = ''; $('[name="prompt"]').value = ''; }
    const trigger = $('[name="trigger"]');
    const updateTrigger = () => { $('[name="schedule"]').closest('.s-two-fields').hidden = trigger.value !== '定时执行'; };
    trigger.addEventListener('change', updateTrigger); updateTrigger();
  }
  $('#usage-range')?.addEventListener('change', (event) => {
    const month = event.target.selectedIndex === 1;
    const metrics = $$('.s-metrics article');
    $('strong', metrics[0]).innerHTML = `${month ? '12.48' : '3.84'}<span>M</span>`;
    $('small', metrics[0]).textContent = month ? '输入 10.16M · 输出 2.32M' : '输入 3.12M · 输出 0.72M';
    $('strong', metrics[1]).textContent = month ? '93' : '28';
    $('small', metrics[1]).textContent = month ? '已完成 81 · 失败 12' : '已完成 24 · 失败 4';
    $('strong', metrics[2]).innerHTML = `${month ? '16.3' : '4.6'}<span>h</span>`;
    const bars = month ? [47,62,56,87,69,77,53] : [38,63,42,75,57,89,68];
    const dates = month ? ['8/24','8/29','9/3','9/8','9/13','9/18','9/22'] : ['9/16','9/17','9/18','9/19','9/20','9/21','9/22'];
    $$('.s-chart>div').forEach((bar, i) => { $('span',bar).style.setProperty('--bar',`${bars[i]}%`); $('small',bar).textContent = dates[i]; });
    $('.s-chart').setAttribute('aria-label', `示例：${event.target.value} Token 用量柱状图`);
    $('.s-chart-section .s-section-title>span').textContent = month ? 'Token / 时段' : 'Token / 天';
    const rows = $$('[data-repo-populated]>.s-record');
    $('strong:last-child',rows[0]).textContent = month ? '9.81M' : '2.96M';
    $('small',rows[0]).textContent = month ? '68 次运行' : '21 次运行';
    $('strong:last-child',rows[1]).textContent = month ? '2.67M' : '0.88M';
    $('small',rows[1]).textContent = month ? '25 次运行' : '7 次运行';
    $('.s-heading p').textContent = `${isAccountUsage ? '所有仓库的个人运行汇总' : '当前仓库'} · ${event.target.value} · 示例数据`;
  });
  // Account setup stays within the same shell.
  $$('[data-reauthorize]').forEach((button) => button.addEventListener('click', () => {
    const body = dialog('重新授权账号', '<p>正式产品会打开受影响提供商的授权流程。返回后重新检查凭据与模型状态。</p><button id="preview-reauthorize" class="s-btn s-primary">演示授权恢复</button>' + previewNote);
    $('#preview-reauthorize', body).addEventListener('click', () => { const container = button.closest('.s-provider-card, .s-connection'); const badge = $('.s-status', container); badge.className = 's-status success'; badge.innerHTML = '<i></i>可用 · 预览'; $('p', container).textContent = '授权已恢复 · 仅设计预览'; button.textContent = '重新授权'; if(page === 'connections') $('.s-notice.warning').hidden = true; if(page === 'models' && !$('[name="defaultModel"] option[value="GPT-5.6"]')) { const option = document.createElement('option'); option.textContent = 'GPT-5.6'; $('[name="defaultModel"]').append(option); } $('#suite-dialog').close(); toast('已展示恢复后的状态；没有进行真实 OAuth 授权。'); });
  }));
  $$('[data-connect-provider]').forEach((button) => button.addEventListener('click', () => { dialog(`连接 ${button.dataset.connectProvider}`, '<p>正式产品将进入提供商登录并申请仓库访问权限。凭据只通过授权流程处理。</p>'+previewNote); }));
  function providerEditor(name) {
    const body = dialog(name ? `配置 ${name}` : '添加模型提供商', '<form id="provider-preview-form"><label class="s-field"><span>提供商</span><select><option>智谱</option><option>OpenAI</option><option>Qwen</option></select></label><label class="s-field"><span>API Key</span><input type="password" placeholder="原型不接收真实凭据" disabled><small>正式产品中通过账号设置保存，凭据不会回显。</small></label><button class="s-btn s-primary" type="submit">查看配置结果示例</button><p role="status" id="provider-result"></p></form>'+previewNote);
    if (name) $('select', body).value = name;
    $('#provider-preview-form', body).addEventListener('submit', (event) => { event.preventDefault(); $('#provider-result', body).textContent = '配置状态示例：凭据已配置，模型可供当前账号使用。没有保存真实凭据。'; });
  }
  $('[data-add-provider]')?.addEventListener('click', () => providerEditor());
  $$('[data-config-provider]').forEach((button) => button.addEventListener('click', () => providerEditor(button.dataset.configProvider)));
  // Device and pairing gates.
  $$('[data-device-name]').forEach((el) => { el.textContent = deviceName; });
  $$('[data-device-platform]').forEach((el) => { el.textContent = device === 'mac' ? 'macOS' : 'Linux'; });
  function deviceBlocker(state) {
    return state === 'unpaired' ? '<div class="s-notice warning">'+svg('lock')+'<span>当前浏览器尚未配对，无法解密或显示工作区与会话内容。</span><a class="s-btn s-small" href="'+url('cloud-device-connect.html',{step:'pair',device})+'">配对此浏览器</a></div>' : '<div class="s-notice warning">'+svg('warning')+'<span>设备离线，暂时无法发送消息。请在设备上启动 jcode 并检查连接。</span><a class="s-btn s-small" href="'+url('cloud-device-connect.html',{device})+'">查看连接步骤</a></div>';
  }
  if (page === 'device') {
    const stateInput = $('[data-device-state]');
    stateInput.value = params.get('state') === 'unpaired' ? 'unpaired' : device === 'nas' ? 'offline' : 'online';
    $$('[data-device-link]').forEach(link => { link.dataset.defaultState = new URL(link.href).searchParams.get('state') || ''; });
    function updateDevice() {
      const state = stateInput.value;
      $('[data-device-status]').textContent = { online:'在线 · 已配对',offline:'离线',unpaired:'浏览器未配对' }[state];
      $('[data-device-blocker]').hidden = state === 'online';
      $('[data-device-blocker]').innerHTML = state === 'online' ? '' : deviceBlocker(state);
      $('[data-device-content]').hidden = state === 'unpaired';
      $('[data-device-send]').disabled = state !== 'online' || !$('#remote-start textarea').value.trim();
      $('#remote-start textarea').disabled = state !== 'online';
      $$('[data-device-link]').forEach((link) => { const u = new URL(link.href); if (state === 'offline') u.searchParams.set('state','offline'); else if(link.dataset.defaultState) u.searchParams.set('state',link.dataset.defaultState); else u.searchParams.delete('state'); link.href = u.href; });
    }
    stateInput.addEventListener('change',updateDevice); $('#remote-start textarea').addEventListener('input',updateDevice); updateDevice();
    $('#remote-start').addEventListener('submit',(event)=>{event.preventDefault();if(stateInput.value !== 'online') return;storage.set('remote-draft',$('#remote-start textarea').value);storage.set('remote-workplace',$('#remote-start select').value);location.href=url('cloud-remote-conversation.html',{device,new:'1'});});
  }
  if (page === 'device-connect') {
    function step(number) { $$('[data-connect-step]').forEach(el=>{el.hidden=el.dataset.connectStep!==String(number);}); $$('[data-step-label]').forEach(el=>el.classList.toggle('active',el.dataset.stepLabel===String(number))); }
    if(params.get('step')==='pair') step(3);
    $('[data-copy-command]').addEventListener('click',async()=>{try{await navigator.clipboard.writeText('jcode login --cloud https://cloud.j-code.net');toast('登录命令已复制。');}catch{toast('无法自动复制，请选中命令手动复制。');}});
    $('#device-code-form').addEventListener('submit',event=>{event.preventDefault();const code=$('[name="deviceCode"]').value.replace(/-/g,'').toUpperCase();$('[data-code-display]').textContent=`${code.slice(0,4)}-${code.slice(4)}`;step(2);});
    $$('[data-connect-next]').forEach(button=>button.addEventListener('click',()=>step(button.dataset.connectNext)));
    $('[data-pair-expired]').addEventListener('click',()=>{$('[data-pair-result]').innerHTML='<div class="s-notice warning"><span>示例配对请求已过期，请重新发起配对。</span><button class="s-text-link" id="pair-retry">重新发起预览</button></div>';$('[data-pair-complete]').disabled=true;$('#pair-retry').addEventListener('click',()=>{$('[data-pair-result]').replaceChildren();$('[data-pair-complete]').disabled=false;});});
    $('[data-pair-complete]').addEventListener('click',()=>{$('[data-pair-result]').innerHTML='<div class="s-completion">'+svg('check')+'<div><strong>配对完成 · 示例</strong><p>这是已配对界面的演示，没有生成或交换实际密钥。</p><a class="s-btn" href="'+url('cloud-device.html',{device})+'">进入设备工作区 '+svg('arrow-right')+'</a></div></div>';});
  }
  // Conversation detail, tool history, approvals, and local-only follow-ups.
  if (page === 'conversation' || page === 'remote-conversation') {
    const remote = page === 'remote-conversation';
    const task = Math.min(5,Math.max(1,Number(params.get('task'))||1));
    const stateSelect = $('#conversation-state');
    let state = params.get('state') || (remote ? (device==='nas'?'offline':'waiting') : ['running','waiting','failed','completed','completed'][task-1]);
    if (![...stateSelect.options].some(option=>option.value===state)) state='running';
    stateSelect.value=state;
    let stopped = false;
    if(!remote){$('[data-conversation-title]').textContent=titles[task-1];$('[data-conversation-context]').textContent=`cnjack / ${repo} · main`;}
    else {$('[data-conversation-context]').textContent=`${deviceName} · ~/work/jcode`;$('[data-inspector-panel="details"] dt').textContent='设备';$('[data-inspector-panel="details"] dd').textContent=deviceName;}
    if (remote) {
      $('.s-message.assistant').innerHTML = '<header><img src="./assets/app-icon.svg" alt="jcode"><strong>jcode</strong><time>14:32</time></header><p>我会在这台设备的当前工作区检查登录回调与重定向测试。</p><details class="s-tool" open><summary>'+svg('search')+'检查登录流程<span>2 个文件</span></summary><div><code>LoginCallback.tsx</code><code>login-redirect.test.ts</code></div></details><p>重定向发生在登录状态更新之前。已准备修复，接下来需要运行测试验证。</p>';
      $('.s-approval>code').textContent = 'pnpm test login-redirect';
      $('.s-inspector [data-inspector-panel="changes"]').innerHTML = '<div class="s-section-title"><h3>本地改动</h3><span>示例</span></div><button class="s-file" data-file="LoginCallback.tsx">'+svg('git')+'<span>LoginCallback.tsx</span><small>+8 −3</small></button><button class="s-file" data-file="login-redirect.test.ts">'+svg('git')+'<span>login-redirect.test.ts</span><small>+12 −0</small></button><p class="s-muted">改动保留在远程设备的当前工作区。</p>';
    }
    if (remote && params.get('session') === 'workspace') {
      $('[data-conversation-title]').textContent = '检查 workspace 依赖';
      $('[data-conversation-context]').textContent = `${deviceName} · ~/work/cloud`;
      $('[data-user-prompt]').textContent = '检查这个工作区的依赖与构建配置。';
      $('.s-message.assistant').innerHTML = '<header><img src="./assets/app-icon.svg" alt="jcode"><strong>jcode</strong><time>示例记录</time></header><p>已核对 workspace 包依赖和构建顺序，并整理了检查结果。此次检查没有修改文件。</p>';
      $('.s-completion p').textContent = '依赖检查已完成，未产生文件改动。';
      $('.s-inspector').hidden = true;
    }
    if (params.get('new')==='1') {
      $('[data-user-prompt]').textContent=storage.get(remote?'remote-draft':'start-draft','请开始处理这个任务。');
      $('[data-conversation-title]').textContent=remote?'新的远程对话':'新的任务';
      $$('.s-message.assistant').forEach(el=>el.hidden=true);
      $('.s-inspector').hidden=true;
      stateSelect.value='waiting';
      $('.s-approval').innerHTML='<strong>任务提交预览</strong><p>已展示你的输入。原型未提交请求，也没有生成模型回复。</p>';
      const summary = document.createElement('p');
      if (remote) {
        summary.textContent = `设备：${deviceName} · 工作区：${storage.get('remote-workplace','~/work/jcode')}`;
        $('[data-conversation-context]').textContent = `${deviceName} · ${storage.get('remote-workplace','~/work/jcode')}`;
      } else {
        let context = {}; try { context = JSON.parse(storage.get('start-context', '{}')); } catch {}
        summary.textContent = `分支：${context.branch || 'main'} · 模型：${context.model || 'GLM-5.2'} · ${context.effort || '标准思考'} · ${context.permission || '操作前审批'} · 附件：${context.attachments?.join('、') || '无'}`;
        $('[data-conversation-context]').textContent = `cnjack / ${repo} · ${context.branch || 'main'}`;
        $('#followup footer>span').textContent = `${context.model || 'GLM-5.2'} · ${context.permission || '操作前审批'}`;
      }
      $('.s-approval').append(summary);
    } else if (!remote && task !== 1) {
      $('[data-user-prompt]').textContent=['', '请为对话列表补充搜索与筛选，并确认归档对话的展示规则。','验证 CubeSandbox 的工作区保存与恢复流程。','统一 /copy 操作的引用格式和目标选择。','梳理当前仓库架构、核心模块与入口。'][task-1];
      const assistant=$('.s-message.assistant');
      assistant.innerHTML='<header><img src="./assets/app-icon.svg" alt="jcode"><strong>jcode</strong><time>示例记录</time></header><p>'+(['','已检查对话列表，接下来需要确认搜索是否包含归档内容。','开始验证工作区恢复时，模型请求中断，尚未得到完整结果。','已完成复制操作调整，并补充了相关测试。','已整理代码入口、核心模块与依赖关系。'][task-1])+'</p>';
      $('.s-inspector').hidden=true;
      if(task===2){$('.s-approval>p').textContent='搜索结果是否需要包含已归档的对话？';$('.s-approval>code').textContent='选择搜索范围：包含归档 / 仅活跃对话';$('[data-approval="approve"]').textContent='包含归档';$('[data-approval="deny"]').textContent='仅活跃对话';}
      if(task>=4)$('.s-completion p').textContent=task===4?'统一了复制操作与引用格式，相关测试已完成。':'架构说明已整理完成，可继续询问具体模块。';
    }
    const readableTitle = $('[data-conversation-title]').textContent;
    const readableContext = $('[data-conversation-context]').textContent;
    function updateState(){state=stateSelect.value;$$('[data-state-panel]').forEach(el=>{el.hidden=stopped||el.dataset.statePanel!==state;});$('[data-conversation-badge]').textContent={running:'运行中',waiting:'等待确认',failed:'运行失败',completed:'已完成',offline:'设备离线',unpaired:'浏览器未配对'}[state];$('[data-stop]').hidden=stopped||!['running','waiting'].includes(state);if(stopped)$('[data-conversation-badge]').textContent='已停止 · 预览';const blocked=['offline','unpaired'].includes(state);$('#followup textarea').disabled=blocked;$('#followup button').disabled=blocked||!$('#followup textarea').value.trim();if(remote){$('[data-conversation-title]').textContent=state==='unpaired'?'会话内容已加密':readableTitle;$('[data-conversation-context]').textContent=state==='unpaired'?deviceName:readableContext;$('[data-remote-blocker]').hidden=!blocked;$('[data-remote-blocker]').innerHTML=blocked?deviceBlocker(state):'';$('[data-remote-content]').hidden=state==='unpaired';}}
    stateSelect.addEventListener('change',()=>{stopped=false;updateState();});$('#followup textarea').addEventListener('input',updateState);updateState();
    $('[data-stop]').addEventListener('click',()=>{stopped=true;toast('已预览停止请求；没有停止真实任务。');$('[data-conversation-badge]').textContent='已停止 · 预览';$$('[data-state-panel]').forEach(el=>el.hidden=true);$('[data-stop]').hidden=true;});
    $('[data-retry]').addEventListener('click',()=>{stopped=false;stateSelect.value='running';updateState();toast('已展示重试中的界面，未发起实际运行。');});
    $$('[data-approval]').forEach(button=>button.addEventListener('click',()=>{$('.s-approval').innerHTML='<strong>已记录选择 · 本页预览</strong><p></p>';$('p',$('.s-approval')).textContent=`你的选择：${button.textContent}。未发送审批或执行命令。`;toast('仅演示审批结果。');}));
    $('#followup').addEventListener('submit',event=>{event.preventDefault();if(['offline','unpaired'].includes(state)||!$('#followup textarea').value.trim())return;const message=document.createElement('article');message.className='s-message user';message.innerHTML='<header><span class="s-mini-avatar">JA</span><strong>你</strong><time>本页预览 · 未发送</time></header><p></p>';$('p',message).textContent=$('#followup textarea').value;$('#transcript').append(message);$('#followup textarea').value='';updateState();message.scrollIntoView({block:'nearest',behavior:'smooth'});});
    $('#followup textarea').addEventListener('keydown',event=>{if((event.ctrlKey||event.metaKey)&&event.key==='Enter'){event.preventDefault();$('#followup').requestSubmit();}});
    $$('[data-inspector]').forEach(button=>button.addEventListener('click',()=>{$$('[data-inspector]').forEach(tab=>{tab.classList.toggle('active',tab===button);tab.setAttribute('aria-selected',String(tab===button));});$$('[data-inspector-panel]').forEach(panel=>panel.hidden=panel.dataset.inspectorPanel!==button.dataset.inspector);}));
    $$('[data-file]').forEach(button=>button.addEventListener('click',()=>{
      const snippets = {
        'ModelPicker.tsx': ['− setOpen(false)', '+ setOpen(false)', '+ triggerRef.current?.focus()'],
        'AccountRepositoryComposer.tsx': ['− overflow: hidden;', '+ overflow: visible;'],
        'ModelPicker.test.tsx': ['+ await user.keyboard("{Escape}")', '+ expect(trigger).toHaveFocus()'],
        'LoginCallback.tsx': ['− navigate(returnTo)', '+ await refreshSession()', '+ navigate(returnTo)'],
        'login-redirect.test.ts': ['+ await waitForSession()', '+ expect(location.pathname).toBe(returnTo)'],
      };
      const body=dialog(button.dataset.file,'<p>示例文件差异 · 不代表仓库当前改动</p><pre class="s-diff"></pre>');
      const pre=$('pre',body);
      (snippets[button.dataset.file]||[]).forEach(line=>{const span=document.createElement('span');span.className=line.startsWith('−')?'remove':'add';span.textContent=line;pre.append(span);});
    }));
  }
})();
