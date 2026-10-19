<script lang="ts">
  import { api, type Prototype, type Usage } from "../lib/api";
  import ErrorNotice from "../components/ErrorNotice.svelte";
  import PrototypeItem from "../components/PrototypeItem.svelte";
  import QuotaBar from "../components/QuotaBar.svelte";

  let prototypes = $state<Prototype[] | null>(null);
  let usage = $state<Usage | null>(null);
  let error = $state<unknown>(null);

  async function load() {
    error = null;
    try {
      const res = await api.listPrototypes();
      prototypes = res.prototypes;
      usage = res.usage;
    } catch (e) {
      error = e;
    }
  }

  // Update the list right away, then reload for the server's Quota figures.
  function updated(np: Prototype) {
    prototypes = prototypes?.map((p) => (p.slug === np.slug ? np : p)) ?? null;
    load();
  }

  function deleted(slug: string) {
    prototypes = prototypes?.filter((p) => p.slug !== slug) ?? null;
    load();
  }

  load();
</script>

<div class="title">
  <h1>Prototypes</h1>
  <a class="btn primary" href="/upload">Upload</a>
</div>

{#if usage}
  <div class="card quota"><QuotaBar {usage} /></div>
{/if}

{#if error}
  <ErrorNotice {error} />
  <p><button type="button" class="btn" onclick={load}>Try again</button></p>
{:else if prototypes === null}
  <p class="muted" role="status">Loading Prototypes…</p>
{:else if prototypes.length === 0}
  <div class="card empty">
    <h2>No Prototypes yet</h2>
    <p class="muted">Upload a <code>.html</code> file or a <code>.zip</code> of your build to get a Preview URL.</p>
    <a class="btn primary" href="/upload">Upload your first Prototype</a>
  </div>
{:else}
  <ul class="list">
    {#each prototypes as p (p.slug)}
      <li>
        <PrototypeItem prototype={p} onupdated={updated} ondeleted={deleted} />
      </li>
    {/each}
  </ul>
{/if}

<style>
  .title {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 1rem;
    margin-bottom: 1rem;
  }
  .title h1 {
    margin: 0;
  }
  .quota {
    margin-bottom: 1rem;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.75rem;
  }
  .empty {
    text-align: center;
    padding: 2rem 1rem;
  }
</style>
