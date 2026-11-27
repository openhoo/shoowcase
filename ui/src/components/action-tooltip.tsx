import { useEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";

const tooltipID = "action-tooltip";

// Position the single tooltip in the external stylesheet so the control host
// needs neither inline styles nor separate listeners for every table row.
export function ActionTooltipProvider({ children }: { children: ReactNode }) {
  const [hint, setHint] = useState("");
  const bubble = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let active: HTMLElement | null = null,
      timer: ReturnType<typeof setTimeout> | undefined;
    let focus: HTMLElement | null = null,
      hover: HTMLElement | null = null,
      dismissed: HTMLElement | null = null;
    const target = (node: EventTarget | null) =>
      node instanceof Element
        ? node.closest<HTMLElement>("[data-action-hint]")
        : null;
    const cancel = () => {
      clearTimeout(timer);
    };
    const close = () => {
      cancel();
      if (active) {
        active.removeAttribute("data-action-hint-open");
        const ids = (active.getAttribute("aria-describedby") || "")
          .split(/\s+/)
          .filter((id) => id && id !== tooltipID);
        if (ids.length) active.setAttribute("aria-describedby", ids.join(" "));
        else active.removeAttribute("aria-describedby");
      }
      active = null;
      setHint("");
    };
    const show = (next: HTMLElement | null, delay: number) => {
      cancel();
      if (!next || next === dismissed) {
        close();
        return;
      }
      if (next === active) return;
      const open = () => {
        close();
        if (!next.isConnected) return;
        active = next;
        next.setAttribute("data-action-hint-open", "");
        next.setAttribute(
          "aria-describedby",
          [next.getAttribute("aria-describedby"), tooltipID]
            .filter(Boolean)
            .join(" "),
        );
        setHint(
          next.getAttribute("data-action-hint") ||
            next.getAttribute("aria-label") ||
            "",
        );
      };
      if (delay) timer = setTimeout(open, delay);
      else open();
    };
    const pointer = (e: PointerEvent) => {
      if (e.pointerType === "touch") return;
      if (e.target instanceof Node && bubble.current?.contains(e.target)) {
        cancel();
        return;
      }
      const next = target(e.target);
      if (next !== hover) dismissed = null;
      hover = next;
      if (next) show(next, 180);
      else {
        cancel();
        timer = setTimeout(() => show(focus, 0), 230);
      }
    };
    const leave = (e: PointerEvent) => {
      if (!e.relatedTarget) {
        hover = null;
        show(focus, 0);
      }
    };
    const focusIn = (e: FocusEvent) => {
      focus = target(e.target);
      dismissed = null;
      show(focus, 0);
    };
    const focusOut = (e: FocusEvent) => {
      focus = target(e.relatedTarget);
      if (!focus) show(hover, 0);
    };
    const dismiss = () => {
      dismissed = active || hover || focus;
      close();
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") dismiss();
    };
    document.addEventListener("pointerover", pointer);
    document.addEventListener("pointerout", leave);
    document.addEventListener("focusin", focusIn);
    document.addEventListener("focusout", focusOut);
    document.addEventListener("keydown", key);
    document.addEventListener("click", dismiss);
    return () => {
      document.removeEventListener("pointerover", pointer);
      document.removeEventListener("pointerout", leave);
      document.removeEventListener("focusin", focusIn);
      document.removeEventListener("focusout", focusOut);
      document.removeEventListener("keydown", key);
      document.removeEventListener("click", dismiss);
      close();
    };
  }, []);
  return (
    <>
      {children}
      {hint &&
        createPortal(
          <div
            ref={bubble}
            id={tooltipID}
            role="tooltip"
            data-action-tooltip
            className="z-50 w-max max-w-[min(20rem,calc(100vw-1rem))] rounded-md bg-foreground px-3 py-2 text-sm font-medium break-words text-background shadow-md"
          >
            {hint}
          </div>,
          document.body,
        )}
    </>
  );
}
