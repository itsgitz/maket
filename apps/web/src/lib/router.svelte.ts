// A tiny History API router.
//
// `router.path` is reactive. navigate() pushes a new entry; back/forward
// arrive via popstate. startRouter() also turns clicks on internal <a> links
// into navigate() calls, so pages just use ordinary hrefs.

export const router = $state({ path: location.pathname });

export function navigate(path: string, { replace = false } = {}) {
  if (path === location.pathname + location.search) return;
  if (replace) history.replaceState(null, "", path);
  else history.pushState(null, "", path);
  router.path = new URL(path, location.origin).pathname;
  window.scrollTo(0, 0);
}

function shouldIntercept(e: MouseEvent): HTMLAnchorElement | null {
  if (e.defaultPrevented || e.button !== 0) return null;
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return null;
  const a = (e.target as Element | null)?.closest?.("a");
  if (!a || !a.href) return null;
  if (a.target && a.target !== "_self") return null;
  if (a.hasAttribute("download") || a.getAttribute("rel")?.includes("external")) return null;
  const url = new URL(a.href);
  if (url.origin !== location.origin) return null;
  // Server endpoints (e.g. the GitHub sign-in redirect) need a real request.
  if (url.pathname.startsWith("/api/")) return null;
  // Same-page fragment links keep their default behaviour.
  if (url.hash && url.pathname === location.pathname) return null;
  return a;
}

/** Wire up popstate and link interception. Returns a cleanup function. */
export function startRouter(): () => void {
  const onPop = () => (router.path = location.pathname);
  const onClick = (e: MouseEvent) => {
    const a = shouldIntercept(e);
    if (!a) return;
    e.preventDefault();
    const url = new URL(a.href);
    navigate(url.pathname + url.search);
  };
  window.addEventListener("popstate", onPop);
  document.addEventListener("click", onClick);
  return () => {
    window.removeEventListener("popstate", onPop);
    document.removeEventListener("click", onClick);
  };
}
