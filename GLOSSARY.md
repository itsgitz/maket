# maket

A self-hosted tool where people and AI agents upload static frontend prototypes and share them at a preview URL.

## Language

### Prototypes

**Prototype**:
A static frontend (HTML, CSS, JS and assets) owned by one User and identified by its Slug.
_Avoid_: Site, project, app, deployment

**Slug**:
The short random name that identifies a Prototype and forms the first label of its Preview URL.
_Avoid_: ID, subdomain, handle

**Version**:
One uploaded set of files for a Prototype. Only the current Version is kept.
_Avoid_: Build, deploy, revision, release

**Upload**:
The act of creating a new Version, either for a new Prototype or for an existing one. The CLI's `push` and the dashboard's re-upload are both Uploads.
_Avoid_: Deploy, publish, push (except as the CLI command name)

**Preview**:
The current Version of a Prototype as served to visitors on the preview domain.
_Avoid_: Hosting, live site

**Preview URL**:
The public address of a Preview: the Slug as a subdomain of the preview domain.
_Avoid_: Share link, deploy URL

**SPA mode**:
A per-Prototype setting under which unknown page paths are answered with the root page, so client-side routing works.
_Avoid_: Fallback mode, history mode

### People and access

**User**:
A person who signed in with GitHub and owns Prototypes and API Keys.
_Avoid_: Account, member, owner (as a noun on its own)

**Allowlist**:
The set of GitHub usernames permitted to use maket at all.
_Avoid_: Whitelist, invite list

**API Key**:
A secret a User creates for agents and scripts to manage that User's Prototypes. It cannot manage API Keys.
_Avoid_: Token, access token, PAT

**Quota**:
The maximum total size of a User's current Versions.
_Avoid_: Limit (on its own), storage cap
