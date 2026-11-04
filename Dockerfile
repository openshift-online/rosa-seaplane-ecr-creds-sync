FROM registry.access.redhat.com/hi/go:latest-fips-builder@sha256:8b2ff041651f118890d61393ed362b9e6a9f36aaaa643b546dc5d7b992f6a354 AS build
WORKDIR /src/ecr-creds-sync
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /tmp/ecr-creds-sync ./cmd/ecr-creds-sync

FROM registry.access.redhat.com/hi/go:latest-fips@sha256:d47aa37c51118bc92bb537bbadb707ed948d3eb83893d3cbe18ec45408befd4d
COPY --from=build /tmp/ecr-creds-sync /usr/local/bin/ecr-creds-sync
USER 65532
ENTRYPOINT ["/usr/local/bin/ecr-creds-sync"]
