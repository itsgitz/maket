<script lang="ts">
  // Shows an error from the API client. For disallowed_files / unsafe_paths
  // the server lists the offending paths in details.paths (+ details.more).
  import { ApiError, errorMessage } from "../lib/api";

  let { error }: { error: unknown } = $props();

  const paths = $derived(
    error instanceof ApiError && Array.isArray(error.details?.paths) ? error.details.paths : [],
  );
  const more = $derived(
    error instanceof ApiError && typeof error.details?.more === "number" ? error.details.more : 0,
  );
</script>

<div class="error" role="alert">
  <p>{errorMessage(error)}</p>
  {#if paths.length}
    <ul>
      {#each paths as p (p)}
        <li><code>{p}</code></li>
      {/each}
    </ul>
    {#if more > 0}
      <p class="more">and {more} more</p>
    {/if}
  {/if}
</div>

<style>
  .error {
    border: 1px solid color-mix(in srgb, var(--danger) 40%, transparent);
    background: var(--danger-soft);
    color: var(--danger);
    border-radius: var(--radius);
    padding: 0.6rem 0.85rem;
  }
  p {
    margin: 0;
  }
  ul {
    margin: 0.4rem 0 0;
    padding-left: 1.2rem;
    max-height: 12rem;
    overflow: auto;
  }
  code {
    color: var(--text);
    word-break: break-all;
  }
  .more {
    margin-top: 0.25rem;
    font-size: 0.9rem;
  }
</style>
