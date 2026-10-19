// DEV ONLY: an in-memory fake of the maket API, so the dashboard can be
// exercised without GitHub OAuth. Enabled by opening the dev server with
// `?mock` (see main.ts); it is never imported in production builds.
//
//   ?mock          signed in as a fake User
//   ?mock=401      signed out (Login view)
//   ?mock=403      signed in with an account not on the Allowlist
//   ?mock=off      turn the mock off again
//
// Uploads succeed after simulated progress. Put one of these words in the
// file name to get an error instead: bad, unsafe, noindex, many, big, quota.

import { ApiError, type ApiKey, type Prototype, type RawResponse, type Transport } from "./api";

const QUOTA = 1024 * 1024 * 1024;
const PREVIEW_DOMAIN = "cloudshare.my.id";

const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();
const delay = (ms: number) => new Promise((r) => setTimeout(r, ms));
const ok = (status: number, body: unknown = null): RawResponse => ({ status, body });
const fail = (status: number, code: string, message: string, details?: object): RawResponse => ({
  status,
  body: { error: { code, message, ...(details ? { details } : {}) } },
});

function randomSlug(): string {
  const chars = "abcdefghijklmnopqrstuvwxyz0123456789";
  return Array.from({ length: 8 }, () => chars[Math.floor(Math.random() * chars.length)]).join("");
}

function proto(slug: string, name: string, size: number, files: number, minutesAgo: number, spa = false, version = 1): Prototype {
  return {
    slug,
    name,
    url: `https://${slug}.${PREVIEW_DOMAIN}`,
    spa,
    version,
    size_bytes: size,
    file_count: files,
    created_at: ago(minutesAgo + 60 * 24 * 3),
    updated_at: ago(minutesAgo),
  };
}

export function createMockTransport(mode: string): Transport {
  let signedIn = mode !== "401";
  const notAllowed = mode === "403";

  let prototypes: Prototype[] = [
    proto("ab12cd34", "mock-landing-page", 2_456_789, 23, 4, false, 3),
    proto("q9w8e7r6", "checkout flow (mock)", 812_345, 9, 60 * 5, true, 1),
    proto("zz00xx11", "mock pricing table", 48_120, 1, 60 * 24 * 12, false, 2),
  ];
  let keys: ApiKey[] = [
    { id: 3, name: "mock agent", prefix: "Ab3dE9xY", created_at: ago(90), last_used_at: ago(3), revoked_at: null },
    { id: 2, name: "mock laptop CLI", prefix: "Qr7tUv2W", created_at: ago(60 * 24 * 9), last_used_at: null, revoked_at: null },
    { id: 1, name: "old mock script", prefix: "Zx81Lm0P", created_at: ago(60 * 24 * 40), last_used_at: ago(60 * 24 * 35), revoked_at: ago(60 * 24 * 30) },
  ];
  let nextKeyId = 4;

  const used = () => prototypes.reduce((n, p) => n + p.size_bytes, 0);
  const sortProtos = () => prototypes.sort((a, b) => b.updated_at.localeCompare(a.updated_at));

  function guard(): RawResponse | null {
    if (!signedIn) return fail(401, "unauthorized", "Sign in to continue.");
    if (notAllowed) return fail(403, "not_allowed", "The GitHub account mock-outsider is not allowed to use maket.");
    return null;
  }

  async function request(method: string, path: string, json?: unknown): Promise<RawResponse> {
    await delay(250);
    const denied = guard();
    if (denied && !(method === "POST" && path === "/auth/logout")) return denied;

    if (method === "GET" && path === "/me")
      return ok(200, {
        id: 1,
        github_login: "mock-user",
        name: "Mock User",
        avatar_url: "https://avatars.githubusercontent.com/u/9919?v=4",
        auth_method: "session",
      });
    if (method === "POST" && path === "/auth/logout") {
      signedIn = false;
      return ok(204);
    }
    if (method === "GET" && path === "/prototypes")
      return ok(200, { prototypes: sortProtos(), usage: { used_bytes: used(), quota_bytes: QUOTA } });

    const slugMatch = path.match(/^\/prototypes\/([^/]+)$/);
    if (slugMatch) {
      const slug = decodeURIComponent(slugMatch[1]);
      const p = prototypes.find((x) => x.slug === slug);
      if (!p) return fail(404, "not_found", "No Prototype with that Slug.");
      if (method === "GET") return ok(200, p);
      if (method === "DELETE") {
        prototypes = prototypes.filter((x) => x !== p);
        return ok(204);
      }
    }

    if (method === "GET" && path === "/keys") return ok(200, { keys });
    if (method === "POST" && path === "/keys") {
      const name = String((json as { name?: unknown })?.name ?? "").trim();
      if (name.length < 1 || name.length > 100)
        return fail(400, "invalid_name", "Name must be between 1 and 100 characters.");
      const prefix = Math.random().toString(36).slice(2, 10).padEnd(8, "x");
      const key: ApiKey = { id: nextKeyId++, name, prefix, created_at: new Date().toISOString(), last_used_at: null, revoked_at: null };
      keys = [key, ...keys];
      return ok(201, { ...key, key: `mk_${prefix}MockSecretNotReal0123456789abcdef` });
    }
    const keyMatch = path.match(/^\/keys\/(\d+)$/);
    if (method === "DELETE" && keyMatch) {
      const k = keys.find((x) => x.id === Number(keyMatch[1]));
      if (!k) return fail(404, "not_found", "No API Key with that id.");
      k.revoked_at ??= new Date().toISOString();
      return ok(204);
    }
    return fail(404, "not_found", "Not found.");
  }

  function uploadError(fileName: string): RawResponse | null {
    const n = fileName.toLowerCase();
    if (n.includes("bad"))
      return fail(400, "disallowed_files", "The upload contains file types maket does not serve.", {
        paths: ["server.php", "scripts/deploy.sh", "bin/tool.exe", "a.py", "b.rb", "c.cgi", "d.jsp", "e.asp", "f.pl", "g.bat", "h.cmd"],
        more: 7,
      });
    if (n.includes("unsafe"))
      return fail(400, "unsafe_paths", "The zip contains paths that point outside the Prototype.", {
        paths: ["../etc/passwd", "/absolute/index.html"],
      });
    if (n.includes("noindex")) return fail(400, "missing_index", "The upload has no index.html at its root.");
    if (n.includes("many")) return fail(400, "too_many_files", "The zip contains more than 2000 files.");
    if (n.includes("big")) return fail(413, "too_large", "The upload is larger than 50 MB.");
    if (n.includes("quota")) return fail(413, "quota_exceeded", "This Upload would exceed your 1 GB Quota.");
    return null;
  }

  return {
    request,

    upload(method, path, form, onProgress, signal) {
      return new Promise((resolve, reject) => {
        const file = form.get("file") as File;
        const total = Math.max(file.size, 1);
        let loaded = 0;
        const timer = setInterval(() => {
          loaded = Math.min(total, loaded + total / 12);
          onProgress(loaded, total);
          if (loaded >= total) {
            clearInterval(timer);
            setTimeout(() => resolve(finish()), 300);
          }
        }, 120);
        signal?.addEventListener("abort", () => {
          clearInterval(timer);
          reject(new ApiError(0, "aborted", "Upload cancelled."));
        });

        function finish(): RawResponse {
          const denied = guard();
          if (denied) return denied;
          if (!/\.(html?|zip)$/i.test(file.name))
            return fail(400, "invalid_file", "Upload a .html, .htm or .zip file.");
          const err = uploadError(file.name);
          if (err) return err;

          const name = form.get("name") as string | null;
          const spa = form.get("spa");
          const size = file.size * (file.name.endsWith(".zip") ? 3 : 1);
          const files = file.name.endsWith(".zip") ? 5 + Math.floor(Math.random() * 30) : 1;

          if (method === "PUT") {
            const slug = decodeURIComponent(path.split("/").pop()!);
            const p = prototypes.find((x) => x.slug === slug);
            if (!p) return fail(404, "not_found", "No Prototype with that Slug.");
            Object.assign(p, {
              name: name ?? p.name,
              spa: spa === null ? p.spa : spa === "true",
              version: p.version + 1,
              size_bytes: size,
              file_count: files,
              updated_at: new Date().toISOString(),
            });
            return ok(200, p);
          }
          const slug = randomSlug();
          const p = proto(slug, name ?? file.name.replace(/\.(html?|zip)$/i, ""), size, files, 0, spa === "true");
          p.created_at = p.updated_at;
          prototypes.unshift(p);
          return ok(201, p);
        }
      });
    },
  };
}
