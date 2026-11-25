FROM registry.access.redhat.com/hi/go:latest-fips-builder@sha256:0374b76ef9a21619360dd9920540e912c64a33980a472962017f532c8f19b56d AS build
WORKDIR /src/ecr-creds-sync
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /tmp/ecr-creds-sync ./cmd/ecr-creds-sync

FROM registry.access.redhat.com/hi/go:latest-fips@sha256:900dcc80cc2d493c9de3d5a97c347fd78cf26ca1b92a9e556d253409584869ae
COPY --from=build /tmp/ecr-creds-sync /usr/local/bin/ecr-creds-sync
USER 65532
ENTRYPOINT ["/usr/local/bin/ecr-creds-sync"]
