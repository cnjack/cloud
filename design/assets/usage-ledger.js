document.addEventListener("click", (event) => {
  const button = event.target.closest("[data-usage-range]");
  if (!button) return;
  const group = button.closest("[data-usage-range-group]");
  group?.querySelectorAll("[data-usage-range]").forEach((candidate) => {
    candidate.setAttribute("aria-pressed", String(candidate === button));
  });
  const label = document.querySelector("[data-usage-current-label]");
  if (label) label.textContent = button.dataset.usageRangeLabel || button.textContent;
});
