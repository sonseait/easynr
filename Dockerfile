FROM --platform=$BUILDPLATFORM golang:1.26.5-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/easynr ./cmd/easynr \
    && mkdir -p /out/data && chmod 0700 /out/data && chown 65532:65532 /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/easynr /easynr
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
WORKDIR /
VOLUME ["/data"]
EXPOSE 4873
ENTRYPOINT ["/easynr"]
CMD ["serve"]
