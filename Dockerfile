FROM registry.access.redhat.com/hi/go:latest-fips-builder@sha256:9c9ea263e72d671940017c68dd0a1d9032235d1cc3b4e3f66c40f88a2afada5d AS build
WORKDIR /src/ecr-creds-sync
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /tmp/ecr-creds-sync ./cmd/ecr-creds-sync

FROM registry.access.redhat.com/hi/go:latest-fips@sha256:cbeb139cbce889663602b3d450bdd87b2bb9c4b730a6441027c03ac67239fba9
COPY --from=build /tmp/ecr-creds-sync /usr/local/bin/ecr-creds-sync
USER 65532
ENTRYPOINT ["/usr/local/bin/ecr-creds-sync"]
