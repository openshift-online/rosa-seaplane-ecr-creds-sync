FROM registry.access.redhat.com/hi/go:latest-fips-builder@sha256:e424db07a04673ec9c732981d3d97e96044cce39215f5c528d4f549e45eb1c2c AS build
WORKDIR /src/ecr-creds-sync
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /tmp/ecr-creds-sync ./cmd/ecr-creds-sync

FROM registry.access.redhat.com/hi/go:latest-fips@sha256:275df00eba7680ddee0a3701af59af981a4c9a6fad41a84e0bc371c778ed21e8
COPY --from=build /tmp/ecr-creds-sync /usr/local/bin/ecr-creds-sync
USER 65532
ENTRYPOINT ["/usr/local/bin/ecr-creds-sync"]
