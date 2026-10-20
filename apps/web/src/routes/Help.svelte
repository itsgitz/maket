<script lang="ts">
  import CodeBlock from "../components/CodeBlock.svelte";

  const origin = window.location.origin;
</script>

<h1>Agent/CLI help</h1>
<p class="muted">
  Agents and scripts manage your Prototypes with an API Key, either through the <code>maket</code> CLI or plain
  HTTP. Create a key on the <a href="/keys">API keys</a> page first.
</p>

<section>
  <h2>1. Install the CLI</h2>
  <p>
    Download the <code>maket</code> binary from the
    <a href="https://github.com/itsgitz/maket/releases/latest" target="_blank" rel="noopener">releases page</a>, make
    it executable and put it on your <code>PATH</code>. It is a single static binary with no dependencies.
  </p>
  <p>
    On Linux and macOS, this one-liner installs the right binary for your machine, checks its checksum and puts it
    on your <code>PATH</code>:
  </p>
  <CodeBlock code="curl -fsSL https://raw.githubusercontent.com/itsgitz/maket/main/install.sh | sh" />
  <p>Or do it by hand:</p>
  <CodeBlock code={"chmod +x maket\nsudo mv maket /usr/local/bin/"} />
</section>

<section>
  <h2>2. Sign in with an API Key</h2>
  <p>
    <code>maket login</code> asks for your API Key, checks it and saves it to
    <code>~/.config/maket/config.json</code>. Agents can set <code>MAKET_API_KEY</code> (and
    <code>MAKET_API_URL</code>) instead.
  </p>
  <CodeBlock code="maket login" />
  <CodeBlock code={`export MAKET_API_KEY=mk_YOUR_KEY\nexport MAKET_API_URL=${origin}`} />
</section>

<section>
  <h2>3. Upload</h2>
  <p>
    Upload a folder (zipped for you) or a single HTML file. The Preview URL is the only thing printed on stdout,
    so agents can capture it.
  </p>
  <CodeBlock code="maket push ./dist" />
  <CodeBlock code="maket push index.html --name demo" />
  <p>Turn on SPA mode for apps with client-side routing:</p>
  <CodeBlock code="maket push ./dist --spa" />
  <p>Upload a new Version of an existing Prototype, keeping its Slug and Preview URL:</p>
  <CodeBlock code={"maket push ./dist --slug <slug>"} />
</section>

<section>
  <h2>4. List and delete</h2>
  <CodeBlock code="maket ls" />
  <CodeBlock code={"maket rm <slug>"} />
  <p>Add <code>--json</code> to <code>push</code> and <code>ls</code> for machine-readable output.</p>
  <CodeBlock code="maket ls --json" />
</section>

<section>
  <h2>Without the CLI: curl</h2>
  <p>Any HTTP client works. Send the API Key in an <code>Authorization: Bearer</code> header and the file as multipart form data:</p>
  <CodeBlock
    code={`curl -H "Authorization: Bearer mk_YOUR_KEY" \\\n  -F file=@site.zip \\\n  -F name=demo \\\n  ${origin}/api/prototypes`}
  />
  <p>
    Add <code>-F spa=true</code> for SPA mode. To Upload a new Version, send the same request with
    <code>-X PUT</code> to <code>{origin}/api/prototypes/&lt;slug&gt;</code>.
  </p>
</section>

<style>
  section {
    margin-top: 1.5rem;
  }
  p {
    margin: 0.4rem 0;
  }
</style>
