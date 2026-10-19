<script lang="ts">
  // Placeholder dashboard for milestone 1: proves the SPA is embedded and
  // can reach the API through the same origin.
  let health = $state<"checking" | "ok" | "down">("checking");

  $effect(() => {
    fetch("/api/healthz", { credentials: "same-origin" })
      .then((r) => (health = r.ok ? "ok" : "down"))
      .catch(() => (health = "down"));
  });
</script>

<main>
  <h1>maket</h1>
  <p>Upload and share static frontend prototypes.</p>
  <p class="status">API: <strong data-status={health}>{health}</strong></p>
</main>

<style>
  main {
    max-width: 40rem;
    margin: 4rem auto;
    padding: 0 1rem;
    font-family: system-ui, sans-serif;
  }
  .status strong[data-status="ok"] {
    color: #15803d;
  }
  .status strong[data-status="down"] {
    color: #b91c1c;
  }
</style>
