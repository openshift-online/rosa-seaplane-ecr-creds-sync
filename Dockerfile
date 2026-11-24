FROM registry.access.redhat.com/hi/go:latest-fips-builder@sha256:dc428f0d29b8f64bfbdaf5af58110a361deeb1ac23f074261931f012982b64c3 AS build
WORKDIR /src/ecr-creds-sync
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /tmp/ecr-creds-sync ./cmd/ecr-creds-sync

FROM registry.access.redhat.com/hi/go:latest-fips@sha256:043e6232a9a689a9c36b14ee7b8ec50e8bb5cd88a7e202d82497c5c0e71adedd
COPY --from=build /tmp/ecr-creds-sync /usr/local/bin/ecr-creds-sync
USER 65532
ENTRYPOINT ["/usr/local/bin/ecr-creds-sync"]
