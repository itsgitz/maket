<script lang="ts">
  import CopyButton from "./CopyButton.svelte";

  let { code, label }: { code: string; label?: string } = $props();
</script>

<div class="block">
  <pre aria-label={label}><code>{code}</code></pre>
  <div class="copy"><CopyButton text={code} small /></div>
</div>

<style>
  .block {
    position: relative;
    margin: 0.5rem 0 1rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface-2);
  }
  pre {
    margin: 0;
    padding: 0.75rem 0.85rem;
    overflow-x: auto;
    white-space: pre;
  }
  .copy {
    display: flex;
    justify-content: flex-end;
    padding: 0 0.5rem 0.5rem;
  }
</style>
