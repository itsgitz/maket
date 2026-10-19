<script lang="ts">
  import type { Usage } from "../lib/api";
  import { formatBytes } from "../lib/format";

  let { usage }: { usage: Usage } = $props();

  const pct = $derived(
    usage.quota_bytes > 0 ? Math.min(100, (usage.used_bytes / usage.quota_bytes) * 100) : 0,
  );
  const level = $derived(pct >= 90 ? "high" : pct >= 70 ? "mid" : "low");
</script>

<div class="quota">
  <div class="label">
    <span id="quota-label">Quota</span>
    <span class="muted">
      {formatBytes(usage.used_bytes)} of {formatBytes(usage.quota_bytes)} used ({pct < 1 && pct > 0
        ? "<1"
        : Math.round(pct)}%)
    </span>
  </div>
  <div
    class="track"
    role="meter"
    aria-labelledby="quota-label"
    aria-valuemin={0}
    aria-valuemax={usage.quota_bytes}
    aria-valuenow={usage.used_bytes}
    aria-valuetext="{formatBytes(usage.used_bytes)} of {formatBytes(usage.quota_bytes)}"
  >
    <div class="fill {level}" style:width="{pct}%"></div>
  </div>
</div>

<style>
  .label {
    display: flex;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 0.25rem 1rem;
    font-size: 0.9rem;
    margin-bottom: 0.35rem;
  }
  #quota-label {
    font-weight: 600;
  }
  .track {
    height: 0.5rem;
    border-radius: 999px;
    background: var(--surface-2);
    overflow: hidden;
  }
  .fill {
    height: 100%;
    background: var(--accent);
    transition: width 0.3s;
  }
  .fill.mid {
    background: var(--warn);
  }
  .fill.high {
    background: var(--danger);
  }
</style>
