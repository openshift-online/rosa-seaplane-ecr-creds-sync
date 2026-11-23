FROM registry.access.redhat.com/hi/go:latest-fips-builder@sha256:ea524c060979cf725f7bca8e3cdd0ef2d99d07b780d734092bd0bde68900aa0f AS build
WORKDIR /src/ecr-creds-sync
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /tmp/ecr-creds-sync ./cmd/ecr-creds-sync

FROM registry.access.redhat.com/hi/go:latest-fips@sha256:09bb6f946265bd6689f424c68827fa3719b09bb161db15e20aa28c3773f94389
COPY --from=build /tmp/ecr-creds-sync /usr/local/bin/ecr-creds-sync
USER 65532
ENTRYPOINT ["/usr/local/bin/ecr-creds-sync"]
