<script lang="ts">
  // Picks a file (drop or file picker), checks it, and Uploads it with
  // progress. Without `slug` it creates a new Prototype (POST); with `slug`
  // it Uploads a new Version of that Prototype (PUT).
  import { api, ApiError, type Prototype } from "../lib/api";
  import { formatBytes } from "../lib/format";
  import ErrorNotice from "./ErrorNotice.svelte";

  let {
    slug,
    spa: initialSpa = false,
    compact = false,
    oncomplete,
    oncancel,
  }: {
    slug?: string;
    spa?: boolean;
    compact?: boolean;
    oncomplete: (p: Prototype) => void;
    oncancel?: () => void;
  } = $props();

  const ACCEPT = ".html,.htm,.zip";
  const WARN_BYTES = 20 * 1024 * 1024;
  const uid = $props.id();

  let input: HTMLInputElement | undefined = $state();
  let file = $state<File | null>(null);
  let name = $state("");
  // The toggle starts from the Prototype's current SPA mode, then is the user's.
  // svelte-ignore state_referenced_locally
  let spa = $state(initialSpa);
  let dragging = $state(false);
  let error = $state<unknown>(null);
  let progress = $state<number | null>(null); // 0..1 while uploading
  let controller: AbortController | null = null;

  const uploading = $derived(progress !== null);
  const tooBigWarning = $derived(file !== null && file.size > WARN_BYTES);

  function pick(f: File | undefined | null) {
    error = null;
    if (!f) return;
    if (!/\.(html?|zip)$/i.test(f.name)) {
      file = null;
      error = new ApiError(0, "invalid_file", `"${f.name}" can't be uploaded. Choose a .html, .htm or .zip file.`);
      return;
    }
    file = f;
  }

  function onDrop(e: DragEvent) {
    e.preventDefault();
    dragging = false;
    if (uploading) return;
    const files = e.dataTransfer?.files;
    if (files && files.length > 1) {
      error = new ApiError(0, "invalid_file", "Drop one file. To Upload a folder, zip it first.");
      return;
    }
    pick(files?.[0]);
  }

  function onDragOver(e: DragEvent) {
    e.preventDefault();
    if (!uploading) dragging = true;
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!file || uploading) return;
    error = null;
    progress = 0;
    controller = new AbortController();
    try {
      const p = await api.upload(
        { file, slug, name: name.trim() || undefined, spa },
        (loaded, total) => (progress = total ? loaded / total : 0),
        controller.signal,
      );
      file = null;
      name = "";
      if (input) input.value = "";
      oncomplete(p);
    } catch (err) {
      if (!(err instanceof ApiError && err.code === "aborted")) error = err;
    } finally {
      progress = null;
      controller = null;
    }
  }

  $effect(() => () => controller?.abort());
</script>

<form class:compact onsubmit={submit} aria-busy={uploading}>
  <div
    class="drop"
    class:dragging
    class:has-file={file !== null}
    role="group"
    aria-label="File to upload"
    ondragover={onDragOver}
    ondragenter={onDragOver}
    ondragleave={() => (dragging = false)}
    ondrop={onDrop}
  >
    <input
      bind:this={input}
      id="{uid}-file"
      type="file"
      accept={ACCEPT}
      hidden
      onchange={(e) => pick(e.currentTarget.files?.[0])}
    />
    {#if file}
      <p class="file">
        <strong>{file.name}</strong>
        <span class="muted">{formatBytes(file.size)}</span>
      </p>
    {:else if !compact}
      <p class="hint">Drop a <code>.html</code> file or a <code>.zip</code> of your build here</p>
    {/if}
    <button type="button" class="btn small" disabled={uploading} onclick={() => input?.click()}>
      {file ? "Choose another file" : "Choose file"}
    </button>
    {#if compact && !file}
      <span class="muted small-hint">or drop a .html / .zip here</span>
    {/if}
  </div>

  {#if tooBigWarning}
    <p class="warn" role="status">
      This file is over 20 MB. The server may reject it if it exceeds the Upload size limit.
    </p>
  {/if}

  <div class="fields">
    <div class="field">
      <label for="{uid}-name">Name <span class="muted">(optional)</span></label>
      <input
        id="{uid}-name"
        class="input"
        type="text"
        maxlength="100"
        bind:value={name}
        disabled={uploading}
        placeholder={slug ? "Leave empty to keep the current name" : "e.g. landing page"}
      />
    </div>
    <div class="field check">
      <input id="{uid}-spa" type="checkbox" bind:checked={spa} disabled={uploading} />
      <div>
        <label for="{uid}-spa">SPA mode</label>
        <p class="muted help">Answer unknown page paths with <code>index.html</code>, so client-side routing works.</p>
      </div>
    </div>
  </div>

  {#if progress !== null}
    <div class="progress">
      <progress max="1" value={progress} aria-label="Upload progress"></progress>
      <span class="muted" role="status">
        {progress < 1 ? `Uploading… ${Math.round(progress * 100)}%` : "Processing on the server…"}
      </span>
    </div>
  {/if}

  {#if error}
    <ErrorNotice {error} />
  {/if}

  <div class="actions">
    {#if uploading}
      <button type="button" class="btn" onclick={() => controller?.abort()}>Cancel upload</button>
    {:else}
      <button type="submit" class="btn primary" disabled={!file}>
        {slug ? "Upload new Version" : "Upload"}
      </button>
      {#if oncancel}
        <button type="button" class="btn" onclick={oncancel}>Close</button>
      {/if}
    {/if}
  </div>
</form>

<style>
  form {
    display: grid;
    gap: 1rem;
  }
  form.compact {
    gap: 0.75rem;
  }
  .drop {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 0.6rem;
    padding: 2rem 1rem;
    border: 2px dashed var(--border);
    border-radius: var(--radius);
    background: var(--surface);
    text-align: center;
    transition:
      border-color 0.15s,
      background 0.15s;
  }
  .compact .drop {
    flex-direction: row;
    flex-wrap: wrap;
    padding: 0.75rem;
  }
  .drop.dragging {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .drop.has-file {
    border-style: solid;
  }
  .hint,
  .file {
    margin: 0;
  }
  .file {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    justify-content: center;
    word-break: break-all;
  }
  .small-hint {
    font-size: 0.875rem;
  }
  .warn {
    margin: 0;
    padding: 0.5rem 0.75rem;
    border-radius: var(--radius);
    background: var(--warn-soft);
    color: var(--warn);
  }
  .fields {
    display: grid;
    gap: 0.9rem;
  }
  .field {
    display: grid;
    gap: 0.3rem;
  }
  .check {
    grid-template-columns: auto 1fr;
    align-items: start;
    gap: 0.6rem;
  }
  .check input {
    width: 1.1rem;
    height: 1.1rem;
    margin-top: 0.2rem;
  }
  .help {
    margin: 0;
    font-size: 0.875rem;
  }
  .progress {
    display: grid;
    gap: 0.3rem;
  }
  progress {
    width: 100%;
    height: 0.6rem;
    accent-color: var(--accent);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }
</style>
