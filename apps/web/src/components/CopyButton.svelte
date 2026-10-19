<script lang="ts">
  // Copies `text` with the Clipboard API. Where that is unavailable (e.g. a
  // non-secure context) or refused, it tells the user to copy by hand.
  let {
    text,
    label = "Copy",
    small = false,
  }: { text: string; label?: string; small?: boolean } = $props();

  let status = $state<"idle" | "copied" | "failed">("idle");
  let timer: ReturnType<typeof setTimeout> | undefined;

  async function copy() {
    clearTimeout(timer);
    try {
      if (!navigator.clipboard) throw new Error("no clipboard");
      await navigator.clipboard.writeText(text);
      status = "copied";
    } catch {
      status = "failed";
    }
    timer = setTimeout(() => (status = "idle"), status === "copied" ? 2000 : 6000);
  }

  $effect(() => () => clearTimeout(timer));
</script>

<span class="wrap">
  <button type="button" class="btn" class:small onclick={copy}>
    {status === "copied" ? "Copied" : label}
  </button>
  <span class="status" role="status">
    {#if status === "failed"}
      Couldn't copy automatically. Select the text and press Ctrl+C (⌘C on Mac).
    {/if}
  </span>
</span>

<style>
  .wrap {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  .status:empty {
    display: none;
  }
  .status {
    font-size: 0.85rem;
    color: var(--warn);
  }
</style>
