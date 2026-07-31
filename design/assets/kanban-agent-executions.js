(() => {
  const buttons = [...document.querySelectorAll("[data-ka-state-button]")];
  const panels = [...document.querySelectorAll("[data-ka-state-panel]")];
  const policy = document.querySelector("[data-ka-policy]");
  const policyStatus = policy?.querySelector(".ka-policy-status");
  const policyTitle = policy?.querySelector("#policyTitle");
  const policyHint = policy?.querySelector(".ka-policy-lead small");

  const setState = (state) => {
    buttons.forEach((button) => {
      button.setAttribute("aria-pressed", String(button.dataset.kaStateButton === state));
    });
    panels.forEach((panel) => {
      panel.hidden = panel.dataset.kaStatePanel !== state;
    });

    const blocked = state === "blocked";
    if (policy) policy.dataset.kaPolicy = blocked ? "blocked" : "ready";
    if (policyTitle) {
      policyTitle.textContent = blocked
        ? "拖入 Agent queue 会被记录，但当前无法执行"
        : "拖入 Agent queue 即请求 Cloud Agent 执行";
    }
    if (policyHint) {
      policyHint.textContent = blocked
        ? "缺少可用模型；同一次请求会在修复后继续，不需要重新拖卡。"
        : "每次有效进入只受理一次；列内编辑和重扫不会重复运行。";
    }
    if (policyStatus) {
      policyStatus.dataset.tone = blocked ? "warning" : "success";
      policyStatus.innerHTML = blocked
        ? '<svg class="icon icon-sm"><use href="./assets/icons.svg#i-warning"></use></svg>模型未配置'
        : '<svg class="icon icon-sm"><use href="./assets/icons.svg#i-check"></use></svg>可以执行';
    }
  };

  buttons.forEach((button) => {
    button.addEventListener("click", () => setState(button.dataset.kaStateButton));
  });
})();
