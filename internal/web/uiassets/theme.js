// Run before the stylesheet so a saved choice is in effect on the first paint.
(() => {
  const key = "shoowcase-appearance";
  const root = document.documentElement;
  const picker = "select[data-appearance]";

  function apply(value) {
    const choice = value === "light" || value === "dark" ? value : "system";
    if (choice === "system") delete root.dataset.theme;
    else root.dataset.theme = choice;
    for (const field of document.querySelectorAll(picker)) field.value = choice;
  }

  function restore() {
    try {
      apply(window.localStorage.getItem(key));
    } catch {
      // CSS follows the system when the browser does not allow storage.
    }
  }

  restore();
  window.addEventListener("storage", (event) => {
    if (event.key === key || event.key === null) restore();
  });
  window.addEventListener("pageshow", (event) => {
    if (event.persisted) restore();
  });
  document.addEventListener("change", (event) => {
    const field = event.target;
    if (!(field instanceof HTMLSelectElement) || !field.matches(picker)) return;
    apply(field.value);
    try {
      if (root.dataset.theme)
        window.localStorage.setItem(key, root.dataset.theme);
      else window.localStorage.removeItem(key);
    } catch {
      // The current page can still use the new choice.
    }
  });
  root.dataset.appearanceReady = "";
})();
