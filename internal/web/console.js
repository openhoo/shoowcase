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
const receipt = document.querySelector("[data-secret-receipt]");
if (receipt) {
  window.addEventListener(
    "pagehide",
    () => {
      receipt.value = "";
      receipt.removeAttribute("value");
      receipt.textContent = "";
    },
    { once: true },
  );
  window.addEventListener("pageshow", (event) => {
    if (event.persisted) location.replace(location.pathname === "/quickstart" ? "/quickstart" : "/keys");
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

// Selection belongs to the rows currently shown in this directory. A filter,
// page change or second table must never silently extend a destructive action.
for (const directory of document.querySelectorAll("[data-selection]")) {
  const rows = [...directory.querySelectorAll("input[data-select-row]:not(:disabled)")];
  const master = directory.querySelector("[data-select-all]");
  const label = directory.querySelector("[data-select-all-label]");
  const status = directory.querySelector("[data-selection-count]");
  const submit = directory.querySelectorAll("[data-selection-submit], [data-selection-export]");
  const download = directory.querySelector("[data-selection-export]");
  if (master && rows.length) {
    master.hidden = false;
    if (label) label.hidden = false;
    master.addEventListener("change", () => {
      for (const row of rows) row.checked = master.checked;
      update();
    });
  }
  function update() {
    const chosen = rows.filter(row => row.checked);
    const n = chosen.length;
    if (master) {
      master.checked = n > 0 && n === rows.length;
      master.indeterminate = n > 0 && n < rows.length;
    }
    if (status) {
      const mixed = new Set(chosen.map(row => row.dataset.selectionKind)).size > 1;
      status.textContent = n + " selected" + (mixed ? ". Select only active or only suspended members to continue." : "");
    }
    for (const button of submit) {
      const kind = button.dataset.requiresKind;
      button.disabled = !n || Boolean(kind && chosen.some(row => row.dataset.selectionKind !== kind));
    }
  }
  for (const row of rows) row.addEventListener("change", update);
  window.addEventListener("pageshow", update);
  update();

  // Immutable journals can export the visible records without a write endpoint.
  if (download && rows.length) {
    download.hidden = false;
    for (const el of directory.querySelectorAll("[data-export-toolbar], [data-export-cell]")) el.hidden = false;
    download.addEventListener("click", () => {
      const chosen = rows.filter(row => row.checked);
      if (!chosen.length) return;
      const table = chosen[0].closest("table");
      const columns = [...table.tHead.rows[0].cells].map((cell, i) =>
        cell.classList.contains("selection-cell") || cell.textContent.trim() === "Actions" || i === chosen[0].closest("td").cellIndex ? -1 : i
      ).filter(i => i >= 0);
      const field = cell => {
        const copy = cell.cloneNode(true);
        for (const el of copy.querySelectorAll(".mobile-label, [aria-hidden='true'], form, button")) el.remove();
        for (const el of copy.querySelectorAll("div, span, code, time, a")) el.append(" ");
        let v = copy.textContent.replace(/\s+/g, " ").trim();
        if (/^[=+@-]/.test(v)) v = "'" + v;
        return '"' + v.replaceAll('"', '""') + '"';
      };
      const lines = [table.tHead.rows[0], ...chosen.map(row => row.closest("tr"))]
        .map(tr => columns.map(i => field(tr.cells[i])).join(","));
      const url = URL.createObjectURL(new Blob(["\uFEFF", lines.join("\r\n") + "\r\n"], {type:"text/csv;charset=utf-8"}));
      const a = document.createElement("a");
      a.href = url;
      a.download = download.dataset.selectionExport + ".csv";
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    });
  }
}
