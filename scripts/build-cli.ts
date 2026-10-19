// Cross-compiles the `maket` CLI into bin/ for linux, darwin and windows on
// amd64 and arm64.
import { $ } from "bun";

const described = (await $`git describe --tags --always --dirty`.nothrow().quiet().text()).trim();
const version = process.env.MAKET_VERSION || described || "dev";
const targets = ["linux", "darwin", "windows"].flatMap((os) => ["amd64", "arm64"].map((arch) => ({ os, arch })));

for (const { os, arch } of targets) {
  const out = `../../bin/maket-${os}-${arch}${os === "windows" ? ".exe" : ""}`;
  await $`go build -trimpath -ldflags=${`-s -w -X main.version=${version}`} -o ${out} ./cmd/maket`
    .cwd("apps/api")
    .env({ ...process.env, CGO_ENABLED: "0", GOOS: os, GOARCH: arch });
  console.log(`built ${out.slice(6)}`);
}
