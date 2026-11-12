FROM registry.access.redhat.com/hi/go:latest-fips-builder@sha256:2944ca2126a611f87c3ff7a28d567bdd051cafb69a6a1f35e73c6f1f8bf72b9f AS build
WORKDIR /src/ecr-creds-sync
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /tmp/ecr-creds-sync ./cmd/ecr-creds-sync

FROM registry.access.redhat.com/hi/go:latest-fips@sha256:5d4d54bffb06c691f14316f524b8ea40202c38314b624ee9d37a7668468eaf60
COPY --from=build /tmp/ecr-creds-sync /usr/local/bin/ecr-creds-sync
USER 65532
ENTRYPOINT ["/usr/local/bin/ecr-creds-sync"]
