# Build the static binary first with CGO_ENABLED=0 make build.
FROM scratch
COPY bin/arcana /arcana
USER 1000:1000
ENTRYPOINT ["/arcana"]
