// Typed client for the maket JSON API.
//
// Every call goes through a Transport. In production that is the browser
// (fetch + XMLHttpRequest for uploads); in development `?mock` swaps in an
// in-memory fake (see main.ts and mock.ts).

export type User = {
  id: number;
  github_login: string;
  name: string;
  avatar_url: string;
  auth_method: "session" | "api_key";
};

export type Prototype = {
  slug: string;
  name: string;
  url: string;
  spa: boolean;
  version: number;
  size_bytes: number;
  file_count: number;
  created_at: string;
  updated_at: string;
};

export type Usage = { used_bytes: number; quota_bytes: number };

export type ApiKey = {
  id: number;
  name: string;
  prefix: string;
  created_at: string;
  last_used_at: string | null;
  revoked_at: string | null;
};

/** The create response is the only one that carries the full key. */
export type NewApiKey = ApiKey & { key: string };

export type ErrorDetails = { paths?: string[]; more?: number; [key: string]: unknown };

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly details?: ErrorDetails,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

export type RawResponse = { status: number; body: unknown };
export type Progress = (loaded: number, total: number) => void;

export interface Transport {
  request(method: string, path: string, json?: unknown): Promise<RawResponse>;
  upload(
    method: "POST" | "PUT",
    path: string,
    form: FormData,
    onProgress: Progress,
    signal?: AbortSignal,
  ): Promise<RawResponse>;
}

const networkError = () =>
  new ApiError(0, "network", "Couldn't reach maket. Check your connection and try again.");

function parseBody(text: string): unknown {
  if (!text) return null;
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

const browserTransport: Transport = {
  async request(method, path, json) {
    let res: Response;
    try {
      res = await fetch("/api" + path, {
        method,
        credentials: "same-origin",
        headers: json === undefined ? undefined : { "Content-Type": "application/json" },
        body: json === undefined ? undefined : JSON.stringify(json),
      });
    } catch {
      throw networkError();
    }
    return { status: res.status, body: parseBody(await res.text()) };
  },

  upload(method, path, form, onProgress, signal) {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open(method, "/api" + path);
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable) onProgress(e.loaded, e.total);
      };
      xhr.onload = () => resolve({ status: xhr.status, body: parseBody(xhr.responseText) });
      xhr.onerror = () => reject(networkError());
      xhr.onabort = () => reject(new ApiError(0, "aborted", "Upload cancelled."));
      signal?.addEventListener("abort", () => xhr.abort(), { once: true });
      xhr.send(form);
    });
  },
};

let transport: Transport = browserTransport;
let onAuthFailure: (e: ApiError) => void = () => {};

export function setTransport(t: Transport) {
  transport = t;
}

/**
 * Called whenever the server answers 401 (e.g. the session expired) or
 * 403 not_allowed (the account was removed from the Allowlist).
 */
export function setAuthFailureHandler(fn: (e: ApiError) => void) {
  onAuthFailure = fn;
}

function unwrap<T>({ status, body }: RawResponse): T {
  if (status >= 200 && status < 300) return body as T;
  const err = (body as { error?: { code?: string; message?: string; details?: ErrorDetails } } | null)?.error;
  const e = new ApiError(status, err?.code ?? `http_${status}`, err?.message ?? fallbackMessage(status), err?.details);
  if (status === 401 || (status === 403 && e.code === "not_allowed")) onAuthFailure(e);
  throw e;
}

function fallbackMessage(status: number): string {
  if (status === 413) return "The file is too large.";
  if (status >= 500) return "Something went wrong on the server. Please try again.";
  return `Request failed (HTTP ${status}).`;
}

async function call<T>(method: string, path: string, json?: unknown): Promise<T> {
  return unwrap<T>(await transport.request(method, path, json));
}

export type UploadFields = { file: File; name?: string; spa?: boolean };

function uploadForm({ file, name, spa }: UploadFields): FormData {
  const form = new FormData();
  form.append("file", file);
  if (name !== undefined && name !== "") form.append("name", name);
  if (spa !== undefined) form.append("spa", spa ? "true" : "false");
  return form;
}

const slugPath = (slug: string) => `/prototypes/${encodeURIComponent(slug)}`;

export const api = {
  me: () => call<User>("GET", "/me"),
  logout: () => call<null>("POST", "/auth/logout"),

  listPrototypes: () => call<{ prototypes: Prototype[]; usage: Usage }>("GET", "/prototypes"),
  getPrototype: (slug: string) => call<Prototype>("GET", slugPath(slug)),
  deletePrototype: (slug: string) => call<null>("DELETE", slugPath(slug)),

  /** Upload a new Prototype (no slug) or a new Version of an existing one. */
  async upload(fields: UploadFields & { slug?: string }, onProgress: Progress, signal?: AbortSignal) {
    const method = fields.slug ? "PUT" : "POST";
    const path = fields.slug ? slugPath(fields.slug) : "/prototypes";
    return unwrap<Prototype>(await transport.upload(method, path, uploadForm(fields), onProgress, signal));
  },

  listKeys: () => call<{ keys: ApiKey[] }>("GET", "/keys"),
  createKey: (name: string) => call<NewApiKey>("POST", "/keys", { name }),
  revokeKey: (id: number) => call<null>("DELETE", `/keys/${id}`),
};

/** A user-facing message for anything thrown by the client. */
export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  return "Something unexpected went wrong.";
}
