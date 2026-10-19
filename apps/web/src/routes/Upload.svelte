<script lang="ts">
  import type { Prototype } from "../lib/api";
  import CopyButton from "../components/CopyButton.svelte";
  import UploadForm from "../components/UploadForm.svelte";

  let result = $state<Prototype | null>(null);
</script>

<h1>Upload</h1>
<p class="muted">
  Upload a single <code>.html</code> file, or a <code>.zip</code> of a folder or build output with
  <code>index.html</code> at its root. You'll get a Preview URL to share.
</p>

{#if result}
  <section class="card done" aria-labelledby="done-title">
    <h2 id="done-title">“{result.name || result.slug}” is up</h2>
    <p class="url"><a href={result.url} target="_blank" rel="noopener noreferrer">{result.url}</a></p>
    <div class="actions">
      <CopyButton text={result.url} label="Copy link" />
      <a class="btn" href={result.url} target="_blank" rel="noopener noreferrer">Open Preview</a>
      <a class="btn" href="/">Go to Prototypes</a>
      <button type="button" class="btn" onclick={() => (result = null)}>Upload another</button>
    </div>
  </section>
{:else}
  <div class="card">
    <UploadForm oncomplete={(p) => (result = p)} />
  </div>
{/if}

<style>
  h1 {
    margin-bottom: 0.25rem;
  }
  .card {
    margin-top: 1rem;
  }
  .done {
    border-color: color-mix(in srgb, var(--ok) 50%, var(--border));
    background: var(--ok-soft);
  }
  .url {
    font-size: 1.1rem;
    font-weight: 600;
    word-break: break-all;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }
</style>
