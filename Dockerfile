FROM registry.access.redhat.com/hi/go:latest-fips-builder@sha256:9c8fb40ab55e721ddc35370e3c5d37969f5a65290973263e7874eecb62482834 AS build
WORKDIR /src/ecr-creds-sync
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /tmp/ecr-creds-sync ./cmd/ecr-creds-sync

FROM registry.access.redhat.com/hi/go:latest-fips@sha256:04e15ddcf64b1d72d618d7ab3f02a8ff90bd1607ba500b7e31fcaa007bbcec07
COPY --from=build /tmp/ecr-creds-sync /usr/local/bin/ecr-creds-sync
USER 65532
ENTRYPOINT ["/usr/local/bin/ecr-creds-sync"]
