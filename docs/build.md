# Container image build

Requires Docker with the Compose plugin and a running Docker daemon. Run these
commands from the repository root:

```sh
cp build/.env.example build/.env
# Edit build/.env with your registry, project, and tag.
docker compose --env-file build/.env -f build/compose.yml config --quiet
docker compose --env-file build/.env -f build/compose.yml build
```

`build/.env` is ignored by Git and excluded from the Docker build context.
`REGISTRY` is the registry host, optionally with a port, without a URL scheme or
trailing slash. `PROJECT` is the registry namespace or project path. Both are
required. `TAG` defaults to `latest` when empty or unset. The image name is
`${REGISTRY}/${PROJECT}/easynr:${TAG}`.

Compose resolves `context: ..` relative to `build/compose.yml`, using the existing
root Dockerfile. It builds both `linux/amd64` and `linux/arm64`, matching the
publishing workflow. The Docker daemon must support storing multi-platform images
(for example, the containerd image store in Docker Desktop). Runtime deployment
is covered in [Operations](operations.md#configuration-and-deployment).

To use the `easynr:local` name in deployment examples and Docker smoke tests, set
these shell variables to match `build/.env`, then tag the image:

```sh
docker tag "${REGISTRY}/${PROJECT}/easynr:${TAG:-latest}" easynr:local
make test-docker
```

`make test-docker` tests an already-built image; it no longer builds one. It uses
`easynr:local` by default, or the image specified by `EASYNR_TEST_IMAGE`.

To publish the built image, authenticate using your existing Docker credential
store, then push:

```sh
# Set REGISTRY in your shell to the same host as in build/.env.
docker login "$REGISTRY"
docker compose --env-file build/.env -f build/compose.yml push
```

Keep registry credentials out of Compose files and tracked environment files.
