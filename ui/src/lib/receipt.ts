// A history entry can freeze a receipt even when the server disallows caching.
export function protectReceipt(back: string, clearModel?: () => void) {
  const clear = () => {
    clearModel?.();
    const el = document.querySelector<HTMLInputElement | HTMLTextAreaElement>(
      "[data-secret-receipt]",
    );
    if (el) {
      el.value = el.defaultValue = "";
      el.removeAttribute("value");
      el.textContent = "";
    }
  };
  const restore = (event: PageTransitionEvent) => {
    if (event.persisted) {
      clear();
      window.location.replace(back);
    }
  };
  window.addEventListener("pagehide", clear);
  window.addEventListener("pageshow", restore);
  return () => {
    window.removeEventListener("pagehide", clear);
    window.removeEventListener("pageshow", restore);
  };
}
