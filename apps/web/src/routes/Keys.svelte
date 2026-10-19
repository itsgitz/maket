<script lang="ts">
  import { api, type ApiKey, type NewApiKey } from "../lib/api";
  import ApiKeyItem from "../components/ApiKeyItem.svelte";
  import CopyButton from "../components/CopyButton.svelte";
  import ErrorNotice from "../components/ErrorNotice.svelte";

  let keys = $state<ApiKey[] | null>(null);
  let loadError = $state<unknown>(null);

  let name = $state("");
  let creating = $state(false);
  let createError = $state<unknown>(null);
  let created = $state<NewApiKey | null>(null);

  async function load() {
    loadError = null;
    try {
      keys = (await api.listKeys()).keys;
    } catch (e) {
      loadError = e;
    }
  }

  async function create(e: SubmitEvent) {
    e.preventDefault();
    if (creating) return;
    creating = true;
    createError = null;
    try {
      created = await api.createKey(name.trim());
      name = "";
      load();
    } catch (err) {
      createError = err;
    } finally {
      creating = false;
    }
  }

  load();
</script>

<h1>API keys</h1>
<p class="muted">
  Agents and scripts use an API Key with the <code>maket</code> CLI or <code>curl</code> to Upload, list and
  delete your Prototypes. API Keys can't manage other API Keys. See <a href="/help">Agent/CLI help</a>.
</p>

<section class="card" aria-labelledby="create-title">
  <h2 id="create-title">Create an API Key</h2>
  <form onsubmit={create}>
    <label for="key-name">Name</label>
    <div class="inline">
      <input
        id="key-name"
        class="input"
        type="text"
        required
        maxlength="100"
        placeholder="e.g. my agent"
        bind:value={name}
        disabled={creating}
      />
      <button type="submit" class="btn primary" disabled={creating || !name.trim()}>
        {creating ? "Creating…" : "Create"}
      </button>
    </div>
  </form>
  {#if createError}
    <ErrorNotice error={createError} />
  {/if}

  {#if created}
    <div class="secret" role="status">
      <p><strong>Copy your new API Key “{created.name}” now.</strong> It won't be shown again.</p>
      <code class="key">{created.key}</code>
      <div class="actions">
        <CopyButton text={created.key} label="Copy API Key" />
        <button type="button" class="btn" onclick={() => (created = null)}>I've saved it</button>
      </div>
    </div>
  {/if}
</section>

<h2 class="list-title">Your API Keys</h2>
{#if loadError}
  <ErrorNotice error={loadError} />
  <p><button type="button" class="btn" onclick={load}>Try again</button></p>
{:else if keys === null}
  <p class="muted" role="status">Loading API Keys…</p>
{:else if keys.length === 0}
  <p class="muted">No API Keys yet.</p>
{:else}
  <ul class="list">
    {#each keys as k (k.id)}
      <li><ApiKeyItem apiKey={k} onrevoked={load} /></li>
    {/each}
  </ul>
{/if}

<style>
  section {
    margin: 1rem 0 1.5rem;
    display: grid;
    gap: 0.6rem;
  }
  h2 {
    margin: 0;
  }
  form {
    display: grid;
    gap: 0.3rem;
  }
  .inline {
    display: flex;
    gap: 0.5rem;
  }
  .inline .input {
    flex: 1 1 auto;
    min-width: 0;
  }
  .secret {
    border: 2px solid var(--warn);
    background: var(--warn-soft);
    border-radius: var(--radius);
    padding: 0.85rem;
    display: grid;
    gap: 0.6rem;
  }
  .secret p {
    margin: 0;
  }
  .key {
    display: block;
    padding: 0.5rem 0.65rem;
    border-radius: var(--radius);
    background: var(--surface);
    color: var(--text);
    word-break: break-all;
    user-select: all;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }
  .list-title {
    margin-bottom: 0.75rem;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 0.6rem;
  }
</style>
