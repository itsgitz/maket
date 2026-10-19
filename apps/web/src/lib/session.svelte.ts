// Who is signed in. Loaded once from GET /api/me; any later 401 (or 403
// not_allowed) from any endpoint switches the dashboard to that view.
import { api, ApiError, errorMessage, setAuthFailureHandler, type User } from "./api";

type Session =
  | { status: "loading" }
  | { status: "signed-in"; user: User }
  | { status: "signed-out" }
  | { status: "not-allowed"; message: string }
  | { status: "error"; message: string };

export const session = $state<{ current: Session }>({ current: { status: "loading" } });

setAuthFailureHandler((e) => {
  session.current = e.status === 401 ? { status: "signed-out" } : { status: "not-allowed", message: e.message };
});

export async function loadSession() {
  session.current = { status: "loading" };
  try {
    session.current = { status: "signed-in", user: await api.me() };
  } catch (e) {
    // 401 and 403 not_allowed already went through the auth failure handler.
    if (!(e instanceof ApiError && (e.status === 401 || e.code === "not_allowed")))
      session.current = { status: "error", message: errorMessage(e) };
  }
}

export async function signOut() {
  try {
    await api.logout();
  } catch {
    // Whatever the server said, the dashboard should no longer act signed in.
  }
  session.current = { status: "signed-out" };
}
