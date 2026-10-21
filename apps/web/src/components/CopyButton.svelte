<script lang="ts">
  import { tick } from "svelte";

  // Copies `text` with the Clipboard API when the page is a secure context, and
  // with a temporary textarea + execCommand otherwise (that also works over
  // plain http). If both fail, the text is selected in a read-only field and the
  // user is asked to copy it by hand. That message stays until a copy succeeds.
  let {
    text,
    label = "Copy",
    small = false,
  }: { text: string; label?: string; small?: boolean } = $props();

  let status = $state<"idle" | "copied" | "failed">("idle");
  let timer: ReturnType<typeof setTimeout> | undefined;
  let field = $state<HTMLInputElement>();

  function copyWithExecCommand(value: string): boolean {
    const area = document.createElement("textarea");
    area.value = value;
    area.readOnly = true;
    area.style.position = "fixed";
    area.style.opacity = "0";
    document.body.appendChild(area);
    area.select();
    try {
      return document.execCommand("copy");
    } catch {
      return false;
    } finally {
      area.remove();
    }
  }

  async function copy() {
    clearTimeout(timer);
    let ok = false;
    if (window.isSecureContext && navigator.clipboard) {
      try {
        await navigator.clipboard.writeText(text);
        ok = true;
      } catch {
        // Refused or unavailable: try the fallback below.
      }
    }
    if (!ok) ok = copyWithExecCommand(text);

    if (ok) {
      status = "copied";
      timer = setTimeout(() => (status = "idle"), 2000);
    } else {
      status = "failed";
      await tick();
      field?.select();
    }
  }

  $effect(() => () => clearTimeout(timer));
</script>

<span class="wrap">
  <button type="button" class="btn" class:small onclick={copy}>
    {status === "copied" ? "Copied" : label}
  </button>
  {#if status === "failed"}
    <input class="fallback" type="text" readonly value={text} bind:this={field} aria-label="Text to copy" />
  {/if}
  <span class="status" role="status">
    {#if status === "failed"}
      Couldn't copy automatically — the text is selected, press Ctrl+C (⌘C on Mac).
    {/if}
  </span>
</span>

<style>
  .wrap {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
    max-width: 100%;
  }
  .fallback {
    flex: 1 1 14rem;
    min-width: 0;
    max-width: 100%;
    padding: 0.3rem 0.5rem;
    font-family: var(--mono);
    font-size: 0.85rem;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }
  .status:empty {
    display: none;
  }
  .status {
    font-size: 0.85rem;
    color: var(--warn);
  }
</style>
