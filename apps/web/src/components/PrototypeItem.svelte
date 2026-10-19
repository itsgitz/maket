<script lang="ts">
  // One Prototype in the list, with copy / open / re-upload / delete.
  import { api, type Prototype } from "../lib/api";
  import { formatBytes } from "../lib/format";
  import CopyButton from "./CopyButton.svelte";
  import ErrorNotice from "./ErrorNotice.svelte";
  import RelativeTime from "./RelativeTime.svelte";
  import UploadForm from "./UploadForm.svelte";

  let {
    prototype: p,
    onupdated,
    ondeleted,
  }: {
    prototype: Prototype;
    onupdated: (p: Prototype) => void;
    ondeleted: (slug: string) => void;
  } = $props();

  let mode = $state<"view" | "reupload" | "confirm-delete">("view");
  let deleting = $state(false);
  let error = $state<unknown>(null);
  let justUpdated = $state(false);

  async function remove() {
    deleting = true;
    error = null;
    try {
      await api.deletePrototype(p.slug);
      ondeleted(p.slug);
    } catch (e) {
      error = e;
      deleting = false;
    }
  }

  function reuploaded(updated: Prototype) {
    mode = "view";
    justUpdated = true;
    onupdated(updated);
  }
</script>

<article class="card" aria-labelledby="proto-{p.slug}">
  <div class="head">
    <h2 id="proto-{p.slug}">
      {p.name || p.slug}
      {#if p.spa}<span class="badge" title="SPA mode is on">SPA</span>{/if}
    </h2>
    <a class="url" href={p.url} target="_blank" rel="noopener noreferrer">{p.url.replace(/^https?:\/\//, "")}</a>
  </div>

  <dl class="meta">
    <div><dt>Slug</dt><dd><code>{p.slug}</code></dd></div>
    <div><dt>Version</dt><dd>{p.version}</dd></div>
    <div><dt>Size</dt><dd>{formatBytes(p.size_bytes)}</dd></div>
    <div><dt>Files</dt><dd>{p.file_count}</dd></div>
    <div><dt>Updated</dt><dd><RelativeTime iso={p.updated_at} /></dd></div>
  </dl>

  {#if justUpdated && mode === "view"}
    <p class="ok" role="status">Version {p.version} uploaded.</p>
  {/if}

  {#if mode === "view"}
    <div class="actions">
      <CopyButton text={p.url} label="Copy link" small />
      <a class="btn small" href={p.url} target="_blank" rel="noopener noreferrer">Open</a>
      <button type="button" class="btn small" onclick={() => ((mode = "reupload"), (justUpdated = false))}>
        Re-upload
      </button>
      <button type="button" class="btn small danger" onclick={() => ((mode = "confirm-delete"), (error = null))}>
        Delete
      </button>
    </div>
  {:else if mode === "confirm-delete"}
    <div class="confirm" role="group" aria-label="Confirm delete">
      <p>Delete <strong>{p.name || p.slug}</strong>? Its Preview URL stops working and the files are removed.</p>
      <div class="actions">
        <button type="button" class="btn small danger solid" disabled={deleting} onclick={remove}>
          {deleting ? "Deleting…" : "Yes, delete"}
        </button>
        <button type="button" class="btn small" disabled={deleting} onclick={() => (mode = "view")}>Cancel</button>
      </div>
    </div>
  {:else}
    <div class="reupload">
      <h3>Upload a new Version</h3>
      <UploadForm slug={p.slug} spa={p.spa} compact oncomplete={reuploaded} oncancel={() => (mode = "view")} />
    </div>
  {/if}

  {#if error}
    <ErrorNotice {error} />
  {/if}
</article>

<style>
  article {
    display: grid;
    gap: 0.75rem;
  }
  .head {
    display: grid;
    gap: 0.15rem;
  }
  h2 {
    margin: 0;
    font-size: 1.05rem;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem;
    word-break: break-word;
  }
  h3 {
    font-size: 0.95rem;
    margin: 0 0 0.5rem;
  }
  .url {
    word-break: break-all;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem 1.5rem;
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
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
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
  .reupload {
    border-top: 1px solid var(--border);
    padding-top: 0.75rem;
  }
  .ok {
    margin: 0;
    color: var(--ok);
    font-size: 0.9rem;
  }
</style>
