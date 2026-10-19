<script lang="ts">
  import type { Component } from "svelte";
  import Header from "./components/Header.svelte";
  import { router, startRouter } from "./lib/router.svelte";
  import { loadSession, session } from "./lib/session.svelte";
  import Help from "./routes/Help.svelte";
  import Keys from "./routes/Keys.svelte";
  import Login from "./routes/Login.svelte";
  import NotAllowed from "./routes/NotAllowed.svelte";
  import NotFound from "./routes/NotFound.svelte";
  import Prototypes from "./routes/Prototypes.svelte";
  import Upload from "./routes/Upload.svelte";

  const routes: Record<string, { page: Component; title: string }> = {
    "/": { page: Prototypes, title: "Prototypes" },
    "/upload": { page: Upload, title: "Upload" },
    "/keys": { page: Keys, title: "API keys" },
    "/help": { page: Help, title: "Agent/CLI help" },
  };

  const route = $derived(routes[router.path] ?? { page: NotFound, title: "Not found" });
  let main: HTMLElement | undefined = $state();

  $effect(() => startRouter());
  loadSession();

  $effect(() => {
    document.title = session.current.status === "signed-in" ? `${route.title} · maket` : "maket";
  });

  // After client-side navigation, move focus to the new page so screen
  // readers announce it (skipped on first load).
  let firstPath = true;
  $effect(() => {
    void router.path;
    if (firstPath) {
      firstPath = false;
      return;
    }
    main?.focus();
  });
</script>

{#if session.current.status === "loading"}
  <p class="loading muted" role="status">Loading…</p>
{:else if session.current.status === "signed-out"}
  <Login />
{:else if session.current.status === "not-allowed"}
  <NotAllowed message={session.current.message} />
{:else if session.current.status === "error"}
  <div class="loading">
    <p role="alert">{session.current.message}</p>
    <button type="button" class="btn" onclick={loadSession}>Try again</button>
  </div>
{:else}
  <a class="skip" href="#main">Skip to content</a>
  <Header user={session.current.user} />
  <main id="main" tabindex="-1" bind:this={main}>
    <route.page />
  </main>
{/if}

<style>
  main {
    max-width: 60rem;
    margin: 0 auto;
    padding: 1.5rem 1rem 3rem;
  }
  main:focus {
    outline: none;
  }
  .loading {
    text-align: center;
    margin-top: 30vh;
  }
  .skip {
    position: absolute;
    left: 0.5rem;
    top: -3rem;
    padding: 0.4rem 0.75rem;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    z-index: 10;
  }
  .skip:focus {
    top: 0.5rem;
  }
</style>
