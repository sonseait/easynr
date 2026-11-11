# Verification

Observed on 2026-11-08 on macOS arm64, with Go 1.26.5, Node.js 24.18.1,
npm 11.16.0, Playwright 1.64.1 and Docker Engine 29.9.2.

| Check | Observed result |
| --- | --- |
| `make verify` | PASS: formatting, vet, all Go tests with race detector, CGO-disabled binary build and module verification |
| `make test-e2e` | PASS: real npm CLI, scoped and unscoped packages, isolated local upstreams and npm caches |
| `npm run test:ui` | PASS: two live-browser tests, desktop and mobile Chromium |
| `make test-docker` | PASS: image build and non-root runtime, named-volume persistence, restart and offline maintenance |
| OpenAPI schema validation | PASS: `swagger-cli validate docs/openapi.yaml` |
| Live public npmjs smoke | PASS: real `npm install is-number@7.0.0` through a temporary EasyNR server; verified version and populated tarball cache |
| `npm run screenshots` | PASS: four console screenshots from explicit demo fixtures; checked for browser errors and horizontal overflow |
| Publish workflow lint | PASS: actionlint; pinned action commits resolved against their upstream version tags |
| Multi-platform image build | PASS: Buildx OCI export contains linux/amd64 and linux/arm64 images; no registry push performed |

## Behaviors exercised

- First-start admin password is emitted once, before listening; restart preserves
  credentials. Password reset invalidates sessions and preserves npm tokens.
- npm Bearer permissions cover exact packages, scope wildcards and global grants.
  Read cannot publish; revoked credentials cannot fetch metadata. Admin APIs reject
  missing sessions, wrong Origin and missing CSRF. Secret hashes are not exposed.
- npm publish, install, ci, ping, whoami, view and dist-tag work with ordinary and
  scoped names. npm 11's client publish preflight requires `--force` to intentionally
  publish a version already present remotely; a duplicate local version still fails.
- Remote and local versions merge with local precedence, including local dist-tags.
  Old lockfile tarball URLs retain their original content after a conflicting local
  publish. Local deletion reveals the corresponding upstream version/tag.
- Metadata TTL, ETag/304 revalidation, stale fallback, missing-version refresh,
  coalesced concurrent downloads and cache separation after route changes work.
  A clear during revalidation cannot resurrect metadata with missing artifact records.
- Known direct local version/tag requests avoid remote access. Cached tarballs work
  with the remote offline. Real npm ci uses a fresh client cache to verify this.
- Corrupt tarballs fail checksum verification and do not become cached blobs.
  Signed tarball URLs work; upstream credentials are not sent to a separate blob
  origin or cross-origin redirect. Upstream IDs cannot be repurposed for another URL.
- Clearing remote cache preserves local blobs. GC preserves referenced shared blobs
  and deletes orphans. A real offline gc-dry-run leaves metadata bytes unchanged.
  Maintenance fails while a server holds the data-directory lock.
- Live console checks cover incorrect/correct login, reload session persistence,
  package source display, cancelled deletion, one-time token display, literal XSS
  text, token revocation, upstream credentials, separate scope routes and logout.
  Home cache statistics cover downloaded content, cancelling/confirming clear,
  zero totals after clear and preservation of local packages. Backend tests verify
  shared tarball bytes are deduplicated across packages and upstreams.
- Upstream connection probes use the saved URL prefix and token, leave persistent
  state unchanged, and report HTTP success, authentication rejection, missing ping,
  server failure, timeout and connection failure. API tests cover session, Origin
  and CSRF enforcement; browser tests exercise Test on desktop and mobile.
- Docker smoke uses a disposable named volume and removes test-owned resources.
  The image runs as 65532:65532 and retains both session and npm token after restart.
- Install performance checks on 2026-11-09 cover full/install-v1 cache separation,
  independent conditional validators, offline fallback, compressed and legacy JSON
  caches, corrupt compressed-record rejection, cache clearing/GC, concurrent URL
  rendering, metadata response bounds, queued cancellation and warm tarball reads.
  An isolated real-npm container load test measured CPU and kernel peak memory;
  methodology and observed results are in [performance.md](performance.md).

## Limits of these results

Docker execution was verified for linux/arm64. The Dockerfile supports build target
arguments, and image builds were verified for both architectures; linux/amd64
execution was not tested. The GitHub Actions workflow has been linted locally but
has not run on GitHub or pushed an image to GHCR from this workspace. A public HTTPS reverse-proxy
installation, other npm versions, Yarn/pnpm, production-scale load testing and failure under physical
disk exhaustion were not exercised. No deployment or package publication to an
external registry was performed; the public npmjs smoke only downloaded a package.
