<script lang="ts">
  import { api, type ApiKey } from "../lib/api";
  import ErrorNotice from "./ErrorNotice.svelte";
  import RelativeTime from "./RelativeTime.svelte";

  let { apiKey: k, onrevoked }: { apiKey: ApiKey; onrevoked: () => void } = $props();

  let confirming = $state(false);
  let revoking = $state(false);
  let error = $state<unknown>(null);

  async function revoke() {
    revoking = true;
    error = null;
    try {
      await api.revokeKey(k.id);
      confirming = false;
      onrevoked();
    } catch (e) {
      error = e;
    } finally {
      revoking = false;
    }
  }
</script>

<article class="card" class:revoked={k.revoked_at !== null}>
  <div class="row">
    <div class="ident">
      <h3>
        {k.name}
        {#if k.revoked_at}<span class="badge danger">Revoked</span>{/if}
      </h3>
      <code>mk_{k.prefix}…</code>
    </div>
    {#if !k.revoked_at && !confirming}
      <button type="button" class="btn small danger" onclick={() => (confirming = true)}>Revoke</button>
    {/if}
  </div>

  <dl class="meta">
    <div><dt>Created</dt><dd><RelativeTime iso={k.created_at} /></dd></div>
    <div>
      <dt>Last used</dt>
      <dd>{#if k.last_used_at}<RelativeTime iso={k.last_used_at} />{:else}Never{/if}</dd>
    </div>
    {#if k.revoked_at}
      <div><dt>Revoked</dt><dd><RelativeTime iso={k.revoked_at} /></dd></div>
    {/if}
  </dl>

  {#if confirming}
    <div class="confirm" role="group" aria-label="Confirm revoke">
      <p>Revoke <strong>{k.name}</strong>? Agents and scripts using it will stop working immediately.</p>
      <div class="actions">
        <button type="button" class="btn small danger solid" disabled={revoking} onclick={revoke}>
          {revoking ? "Revoking…" : "Yes, revoke"}
        </button>
        <button type="button" class="btn small" disabled={revoking} onclick={() => (confirming = false)}>Cancel</button>
      </div>
    </div>
  {/if}

  {#if error}
    <ErrorNotice {error} />
  {/if}
</article>

<style>
  article {
    display: grid;
    gap: 0.5rem;
  }
  .revoked {
    opacity: 0.75;
  }
  .row {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 0.75rem;
  }
  .ident {
    display: grid;
    gap: 0.1rem;
    min-width: 0;
  }
  h3 {
    margin: 0;
    font-size: 1rem;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem;
    word-break: break-word;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem 1.5rem;
    margin: 0;
    font-size: 0.875rem;
  }
  .meta div {
    display: flex;
    gap: 0.35rem;
  }
  dt {
    color: var(--muted);
  }
  dd {
    margin: 0;
  }
  .confirm {
    padding: 0.75rem;
    border-radius: var(--radius);
    background: var(--danger-soft);
    display: grid;
    gap: 0.6rem;
  }
  .confirm p {
    margin: 0;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }
</style>
