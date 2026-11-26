"use strict";
for (const button of document.querySelectorAll("button[data-copy-target]")) {
  const field = document.getElementById(button.dataset.copyTarget);
  if (!field || typeof field.select !== "function") continue;
  const status = document.createElement("span");
  status.setAttribute("role", "status");
  status.className = "copy-feedback";
  button.after(status);
  button.hidden = false;
  button.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(field.value);
      status.textContent = "Copied.";
    } catch {
      field.focus();
      field.select();
      status.textContent = "Selected. Use your device’s copy command.";
    }
  });
}

// A history cache may retain the live document. Do not restore a credential
// when the user navigates back after leaving the one-time receipt.
const receipt = document.querySelector("input[data-secret-receipt]");
if (receipt) {
  window.addEventListener(
    "pagehide",
    () => {
      receipt.value = "";
      receipt.removeAttribute("value");
    },
    { once: true },
  );
  window.addEventListener("pageshow", (event) => {
    if (event.persisted) location.replace("/keys");
  });
}

// Small screens keep search in view; without JS the full GET form is visible.
{
  const toggle = document.getElementById("directory-toggle");
  const options = document.getElementById("directory-options");
  if (toggle && options) {
    const compact = window.matchMedia("(max-width: 43rem)");
    let expanded = false;
    const update = () => {
      toggle.hidden = !compact.matches;
      options.hidden = compact.matches && !expanded;
      toggle.setAttribute("aria-expanded", String(!options.hidden));
      options.parentElement.classList.toggle("search-only", options.hidden);
    };
    toggle.addEventListener("click", () => {
      expanded = !expanded;
      update();
    });
    compact.addEventListener("change", update);
    update();
  }
}
