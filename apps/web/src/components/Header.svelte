<script lang="ts">
  import type { User } from "../lib/api";
  import { router } from "../lib/router.svelte";
  import { signOut } from "../lib/session.svelte";

  let { user }: { user: User } = $props();

  const links = [
    { href: "/", label: "Prototypes" },
    { href: "/upload", label: "Upload" },
    { href: "/keys", label: "API keys" },
    { href: "/help", label: "Agent/CLI help" },
  ];

  let signingOut = $state(false);

  async function onSignOut() {
    signingOut = true;
    await signOut();
    signingOut = false;
  }
</script>

<header>
  <div class="bar">
    <a class="wordmark" href="/">maket</a>
    <nav aria-label="Main">
      <ul>
        {#each links as l (l.href)}
          <li>
            <a href={l.href} aria-current={router.path === l.href ? "page" : undefined}>{l.label}</a>
          </li>
        {/each}
      </ul>
    </nav>
    <div class="user">
      {#if user.avatar_url}
        <img src={user.avatar_url} alt="" width="28" height="28" />
      {/if}
      <span class="login" title={user.name || user.github_login}>{user.github_login}</span>
      <button type="button" class="btn small" disabled={signingOut} onclick={onSignOut}>Sign out</button>
    </div>
  </div>
</header>

<style>
  header {
    background: var(--surface);
    border-bottom: 1px solid var(--border);
  }
  .bar {
    max-width: 60rem;
    margin: 0 auto;
    padding: 0.6rem 1rem;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem 1.25rem;
  }
  .wordmark {
    font-weight: 800;
    font-size: 1.2rem;
    letter-spacing: -0.02em;
    color: var(--text);
    text-decoration: none;
  }
  nav {
    flex: 1 1 auto;
    order: 2;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
  }
  nav a {
    display: inline-block;
    padding: 0.3rem 0.6rem;
    border-radius: var(--radius);
    color: var(--muted);
    text-decoration: none;
    font-weight: 500;
  }
  nav a:hover {
    color: var(--text);
    background: var(--surface-2);
  }
  nav a[aria-current="page"] {
    color: var(--accent);
    background: var(--accent-soft);
  }
  .user {
    order: 3;
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-left: auto;
  }
  img {
    border-radius: 50%;
  }
  .login {
    font-weight: 500;
    max-width: 10rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  @media (max-width: 40rem) {
    /* Wordmark and user on the first row, nav below. */
    nav {
      order: 4;
      flex-basis: 100%;
    }
  }
</style>
