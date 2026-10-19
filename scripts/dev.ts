// Runs the Vite dev server and the Go server together.
// Bun loads .env from the repo root, and both children inherit it.
// Uses `air` for Go hot reload when it is installed, else plain `go run`.
import { spawn, which, type Subprocess } from "bun";

const go = which("air")
  ? ["air", "-c", ".air.toml"]
  : ["go", "run", "./cmd/server"];
if (go[0] === "go") {
  console.log("dev: `air` not found, running Go without hot reload (go install github.com/air-verse/air@latest)");
}

const procs: Subprocess[] = [
  spawn(go, { cwd: "apps/api", stdout: "inherit", stderr: "inherit", env: process.env }),
  spawn(["bun", "run", "dev"], { cwd: "apps/web", stdout: "inherit", stderr: "inherit", env: process.env }),
];

const stop = () => procs.forEach((p) => p.kill());
process.on("SIGINT", stop);
process.on("SIGTERM", stop);

// When either side exits, take the other down too.
const code = await Promise.race(procs.map((p) => p.exited));
stop();
process.exit(code);
