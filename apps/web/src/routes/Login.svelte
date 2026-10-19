<script lang="ts">
  // Shown at any route while GET /api/me answers 401. The sign-in link is a
  // real navigation to the server, which redirects to GitHub.
</script>

<div class="center">
  <div class="card">
    <h1 class="wordmark">maket</h1>
    <p>Upload static frontend Prototypes and share them at a Preview URL.</p>
    <p class="muted">maket is private: only GitHub accounts on the Allowlist can sign in.</p>
    <a class="btn primary" href="/api/auth/github/login">Sign in with GitHub</a>
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
    max-width: 26rem;
    width: 100%;
    padding: 2rem 1.5rem;
    text-align: center;
  }
  .wordmark {
    font-size: 2rem;
    font-weight: 800;
    letter-spacing: -0.03em;
  }
  .btn {
    margin-top: 0.75rem;
    width: 100%;
  }
</style>
