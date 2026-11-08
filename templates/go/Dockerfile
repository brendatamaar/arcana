FROM golang:1.25-bookworm AS toolchain
FROM debian:bookworm-slim
COPY --from=toolchain /usr/local/go /usr/local/go
RUN apt-get update && apt-get install -y --no-install-recommends tar findutils coreutils git ca-certificates && rm -rf /var/lib/apt/lists/*
RUN groupadd --gid 1000 sandbox && useradd --uid 1000 --gid 1000 --home-dir /workspace --no-create-home sandbox && install -d -o 1000 -g 1000 /workspace
USER 1000:1000
WORKDIR /workspace
ENV HOME=/workspace
ENV GOPATH=/workspace/.go
ENV GOCACHE=/workspace/.cache/go
ENV PATH=/workspace/.go/bin:/usr/local/go/bin:$PATH
CMD ["/bin/sleep", "infinity"]
