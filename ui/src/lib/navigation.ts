// Page models are data. Validate navigation again at the point where a value
// becomes a URL. Native POSTs stay on the control host; the few links that
// intentionally leave it must opt in.
export function navigationTarget(
  raw: string,
  external = false,
): string | undefined {
  if (
    !raw ||
    raw !== raw.trim() ||
    /[\u0000-\u001f\u007f\\]/u.test(raw) ||
    raw.startsWith("//")
  )
    return;
  let target: URL;
  try {
    target = new URL(raw, window.location.href);
  } catch {
    return;
  }
  if (target.username || target.password) return;
  if (target.protocol !== "https:" && target.protocol !== "http:") return;
  if (target.origin === window.location.origin) {
    if (raw.startsWith("#")) return target.hash;
    // A pathname starting with two slashes is not safe as a relative URL.
    if (target.pathname.startsWith("//")) return;
    return target.pathname + target.search + target.hash;
  }
  if (!external) return;
  if (target.protocol === "https:") return target.href;
  // Development uses an HTTP control origin with tenant hosts under localhost.
  const loopback = (h: string) =>
    h === "localhost" ||
    h.endsWith(".localhost") ||
    h === "127.0.0.1" ||
    h === "[::1]";
  if (
    window.location.protocol === "http:" &&
    loopback(window.location.hostname) &&
    loopback(target.hostname)
  )
    return target.href;
}

export function postTarget(raw: string) {
  const path = navigationTarget(raw);
  // An absent action posts to the current page. Use a harmless fixed route
  // that rejects POSTs if a model ever contains an invalid action.
  return path && path.startsWith("/") ? path : "/healthz";
}
