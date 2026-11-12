# EasyNR working rules

- Keep the existing Go backend and embedded plain HTML/CSS/JavaScript console.
- Run `make build` to embed asset changes; use `make verify` for Go verification.
- Run `make test-e2e` for real npm CLI tests against disposable local registries.
- Follow [container build instructions](docs/build.md); `make test-docker` requires a prebuilt image and a working Docker daemon.
- Run `npm ci`, `npx playwright install chromium`, and `make test-ui` for browser checks.
- Format Go with gofmt; use lowercase package names and snake_case Go filenames.
- Group Go imports by standard library and project/dependency imports.
- Handle errors explicitly, propagate request contexts, and close bodies/files.
- Preserve metadata bounds, archive checksum validation, storage locks, and upstream credential origin restrictions.
- Validate HTTP inputs and retain authentication and CSRF checks.
- Match JavaScript double quotes, semicolons, and camelCase names.
- Use native accessible controls and DOM text APIs for untrusted package metadata.
- Keep modal headings and footer actions outside the scrollable `.dialog-content`.
- Use self-explanatory names and cohesive functions; avoid duplication and unnecessary abstractions.
- Comment only non-obvious constraints or decisions; preserve legal/tooling directives.
- Keep documentation in English and update affected configuration and usage docs with changes.
