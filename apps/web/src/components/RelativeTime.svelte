<script lang="ts">
  import { exactTime, relativeTime } from "../lib/format";

  let { iso }: { iso: string } = $props();
</script>

<time datetime={iso} title={exactTime(iso)}>{relativeTime(iso)}</time>
