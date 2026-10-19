<script lang="ts">
  import { signOut } from "../lib/session.svelte";

  let { message }: { message: string } = $props();
</script>

<div class="center">
  <div class="card">
    <h1>Not allowed</h1>
    <p>{message || "This GitHub account isn't allowed to use maket."}</p>
    <p class="muted">Ask the person who runs this maket to add you to the Allowlist, or sign in with another account.</p>
    <button type="button" class="btn" onclick={signOut}>Sign out</button>
  </div>
</div>

<style>
  .center {
    min-height: 100vh;
    display: grid;
    place-items: center;
    padding: 1rem;
  }
  .card {
    max-width: 28rem;
    width: 100%;
    padding: 2rem 1.5rem;
    text-align: center;
  }
</style>
