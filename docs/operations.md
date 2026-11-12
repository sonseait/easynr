# Operations and reference

Deployment, cache behavior, access control and recovery for EasyNR.
For a product overview and quick start, see the [README](../README.md).

Console dialogs keep their title and footer buttons visible while their content
scrolls. Package inspection expands to 1120px on larger screens and fits the
viewport on smaller screens.

## Resolution and cache

Every package follows the same policy; there is no local-only mode:

1. Local versions win over the same version from a remote.
2. Remote metadata and downloaded tarballs are reused from cache.
3. Missing remote content is fetched from the selected upstream.

The package metadata combines local and remote version entries. Local dist-tags
win over remote tags of the same name. Local tag mutations must target a local
version. Metadata requests populate the remote version list even when local
versions exist, so npm can resolve ranges across both sources. Remote metadata
is reused for five minutes, then revalidated using ETag/Last-Modified where
supported. A direct request for a missing version triggers a refresh immediately.

npm install requests use the upstream's abbreviated `install-v1` metadata when
available. Full metadata (for example, `npm view`) and abbreviated metadata have
separate caches and HTTP validators; local versions and tags take precedence in
both. An existing fresh full cache can also serve installs. If only abbreviated
metadata is cached while the upstream is unavailable, full requests can still
return cached versions, but documentation fields may be absent.

To bound work during concurrent installs, at most four npm package metadata
responses are decoded/rendered at once, and at most eight uncached tarballs are
downloaded/verified at once. Additional requests wait; disconnected queued requests
are cancelled. Cached tarball reads bypass the download queue. Archives stream
to disk, computing only SHA-256 and the checksums needed for verification.
These concurrency limits are not a hard memory cap: large documents, database
mapping and the Go heap still consume memory. Go's `GOMEMLIMIT` environment
variable can set a soft heap budget below a container's memory limit.

Large metadata cache records are compressed in bbolt to reduce resident mapped
pages. Existing plain JSON records remain readable and are compressed on their
next successful refresh/revalidation. Local package records and tarball archives
keep their existing formats. The metadata DB file does not shrink automatically;
freed pages are reused. Older binaries cannot read compressed cache records, so
keep a pre-upgrade backup if a downgrade is needed.

On connection failures, non-success remote responses (except 404), or invalid
remote metadata, an existing metadata cache may be served stale. Only downloaded
tarballs work offline; caching the version list does not download all tarballs.
A missing package returns 404; an unavailable/invalid upstream with no usable
cache returns 502. A remote 404 is authoritative, not a fallback to another
upstream. Local versions continue to be available if the remote fails.

Tarballs are fetched lazily, verified against integrity/shasum, and atomically
stored by SHA-256. Concurrent downloads of the same artifact are coalesced. URL
identity includes the source, package version and checksum information. A local
publish does not change the content of a remote tarball URL already in a lockfile.
Upstreams must provide a supported checksum (SHA-1, SHA-256, SHA-384 or SHA-512 SRI,
or a SHA-1 shasum). Tarballs are stored as archives without extraction.

Successful npm metadata responses, including version/tag selectors and dist-tags,
use `Cache-Control: private, max-age=300, must-revalidate` and a content ETag.
Clients can reuse metadata for five minutes, then revalidate with `If-None-Match`;
unchanged content returns 304 without a body. Packuments retain `Vary: Accept`.
Client freshness can add five minutes to the upstream metadata cache lifetime.
Use the client's online/revalidation option when checking a new publish or tag.
Tarball responses use `private, max-age=31536000, immutable` with a content ETag.
Shared proxies/CDNs must not cache these authenticated responses. Authentication
and lookup errors, writes, ping, whoami and admin API responses remain `no-store`.

Every network request still authenticates and checks package access before
returning content or 304. Revoking a token or clearing registry cache does not
erase content already stored in a client's private cache. Authorization is not
included in `Vary`, avoiding storage of bearer tokens in npm's cache index.

Deleting local versions can reveal remote versions with the same number again.
Deleting a local tag can reveal the remote tag. These actions do not unpublish
anything remotely. Old tarball references may remain usable until GC. Clearing
remote cache invalidates its tarball references immediately; a subsequent metadata
request rebuilds them. Offline GC reclaims the unreferenced files.

## Multiple upstreams

The built-in default is `https://registry.npmjs.org` (`npmjs`). Use the separate
**Upstreams** and **Routes** screens:

1. In **Upstreams**, add an ID, display name, URL and optional Bearer token.
2. In **Routes**, save a route such as `@company → company-registry`.
3. In **Routes**, use `*` to change the default for packages without a matching scope route.

Use **Test** next to a saved upstream to send an npm `/-/ping` request with its
stored token, if configured. The result shows the HTTP status and elapsed time,
or a connection/authentication error, with a ten-second timeout. The probe leaves
routes and package cache unchanged. Package-level permissions are checked when
downloading or publishing packages.

Each scope selects exactly one remote. Lookup always checks local/cache first.
Removing a scope route uses the default again; removing the `*` route restores
npmjs. An upstream in use by a route cannot be deleted. The built-in npmjs record
cannot be deleted. Upstream ID `local` is reserved.

Upstream URLs are immutable after creation (even if an ID is deleted and reused): create a new ID and update routes to
change a registry URL. This keeps old tarball URLs tied to their original source.
Metadata cache is isolated by upstream ID. Old cache is retained until explicitly
cleared/GC; it does not supply versions to a newly selected upstream.

An empty token on edit preserves the existing token; `-` clears it. Tokens are
not returned by the API or displayed after saving. They are stored in the private
metadata DB, so protect backups. A token is only sent to the configured upstream's
origin; it is not forwarded to a different tarball origin or redirect origin.
HTTPS redirects may not downgrade to HTTP. Admin-configured remotes can be on
private networks; only trusted administrators should configure them.

## Configuration and deployment

| Setting | Default | Purpose |
| --- | --- | --- |
| `PORT` | `4873` | Listen port on `0.0.0.0` |
| `PUBLIC_URL` | Derived from request Host and direct TLS | External HTTP(S) origin for npm tarball URLs, without a path |
| Data directory | `./data` | Relative to the process working directory |
| Metadata TTL | 5 minutes | Fixed for this version |

```sh
PORT=8080 PUBLIC_URL=https://npm.example.com ./bin/easynr
```

Use a TLS reverse proxy for public deployment; keep the backend private. Preserve
Host and Origin, permit publish bodies up to 180 MiB, and set `PUBLIC_URL` to the
HTTPS origin. Forwarded headers are not trusted. Publish tarballs and individual
remote tarballs are limited to 128 MiB; metadata documents are limited to 32 MiB.
Remote HTTP requests have a two-minute total timeout.

Build and tag `easynr:local` using [Container image build](build.md), then run:

```sh
docker run -d --name easynr \
  -p 127.0.0.1:4873:4873 \
  -v easynr_data:/data \
  -e PUBLIC_URL=https://npm.example.com \
  easynr:local
docker logs easynr
```

The image runs as UID/GID 65532. A fresh named volume inherits its ownership.
For bind mounts or a different runtime user, provision `/data` ownership for that
UID/GID and mode 0700 before starting. The image contains no shell or startup
chown. `GET /healthz` is public and returns `{"status":"ok"}`.

Only one instance may use a local data directory. No shared/network filesystem,
multi-replica mode, SSO, object storage, replication or automatic cache eviction
is included. npm login, unpublish, audit, registry-wide remote search and token
management through npm are not implemented. Yarn/pnpm compatibility is not tested.

## Console and API

The console follows the system light/dark theme and works on mobile. Its package
list shows only packages published locally, their published version count and
stored tarball bytes. Packages only fetched from upstreams appear in cache
statistics. Package inspection still merges local and remote versions. Byte
counts deduplicate shared blobs within each package/source category and include
only current metadata references; they are not total filesystem usage.

The home page's **Cache statistics** section shows the number of packages with
cached remote metadata or tarballs and total downloaded tarball bytes across all
upstreams, deduplicated by content hash. **Clear cache** resets these statistics
and removes remote cache references while preserving local packages. Run offline
GC to reclaim the unreferenced files on disk.

Admin API uses session cookies and CSRF, separate from npm Bearer tokens. Sessions
expire after 12 hours. Mutations require matching `Origin` and `X-CSRF-Token`;
login also requires Origin. Cookies are HttpOnly and SameSite=Strict; matching
HTTPS Origin (or direct TLS) sets Secure. Passwords use bcrypt; npm tokens and
session secrets are hashed. API responses use no-store; UI assets have CSP and
no external dependencies. Token names and metadata are rendered as text.

See [docs/openapi.yaml](openapi.yaml) for admin routes and npm endpoints.
The npm registry occupies the root path; console assets use `/-/ui/` so valid
package names such as `app.js` are not shadowed.

## Maintenance and backup

Stop the server before maintenance. Commands acquire the same exclusive bbolt
lock and fail with `data directory is in use` if the server is running.

```sh
# After stopping EasyNR, back up all of ./data first.
cp -a ./data ./data-backup
./bin/easynr gc-dry-run
./bin/easynr gc
./bin/easynr admin reset-password
```

GC preserves tarballs referenced by local packages and cached metadata, deletes
unreferenced artifact records/blobs and cleans abandoned temporary files. Dry-run
does not mutate registry contents. Clearing cache or deleting a package removes
references; disk space is reclaimed by GC. GC can invalidate old lockfile URLs
whose versions are no longer referenced. Reset prints a new password once and
invalidates admin sessions while preserving npm tokens/packages.

For Docker, stop the server container and run a one-off container on the same
volume, for example:

```sh
docker stop easynr
docker run --rm -v easynr_data:/data easynr:local gc-dry-run
docker run --rm -v easynr_data:/data easynr:local gc
docker start easynr
```

Back up and restore the **whole** stopped data directory, including `metadata.db`
and `registry/`, preserving permissions and ownership. The metadata file is mode
0600 and data directories are 0700. Backups contain upstream credentials. Restore
to an empty directory, then verify admin login, an existing npm token and an
installation before switching over. Never copy EasyCR's data directory into EasyNR.

