import { mount } from "svelte";
import "./app.css";
import App from "./App.svelte";

async function start() {
  // DEV ONLY: `?mock` swaps the API for an in-memory fake (see lib/mock.ts).
  // The choice is kept in sessionStorage so it survives reloads on other
  // routes; `?mock=off` clears it. import.meta.env.DEV is statically false in
  // production builds, so this branch and the mock module are dropped.
  if (import.meta.env.DEV) {
    const param = new URLSearchParams(location.search).get("mock");
    if (param === "off") sessionStorage.removeItem("maket-mock");
    else if (param !== null) sessionStorage.setItem("maket-mock", param);
    const mode = sessionStorage.getItem("maket-mock");
    if (mode !== null) {
      const [{ createMockTransport }, { setTransport }] = await Promise.all([
        import("./lib/mock"),
        import("./lib/api"),
      ]);
      setTransport(createMockTransport(mode));
      console.info(`maket: using the in-memory mock API (mode "${mode || "signed-in"}")`);
    }
  }
  mount(App, { target: document.getElementById("app")! });
}

start();
