FROM registry.access.redhat.com/hi/go:latest-fips-builder@sha256:f42ef98620724069ec6ee4eb92286f68222352898c971f30f69867db41362052 AS build
WORKDIR /src/ecr-creds-sync
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /tmp/ecr-creds-sync ./cmd/ecr-creds-sync

FROM registry.access.redhat.com/hi/go:latest-fips@sha256:b6eaebcd16fd334e7a7ecc04a09b1906ea0d016b4b12a23377538fe99dddc2bc
COPY --from=build /tmp/ecr-creds-sync /usr/local/bin/ecr-creds-sync
USER 65532
ENTRYPOINT ["/usr/local/bin/ecr-creds-sync"]
