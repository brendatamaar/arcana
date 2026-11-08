FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends tar findutils coreutils git ca-certificates && rm -rf /var/lib/apt/lists/*
RUN groupadd --gid 1000 sandbox && useradd --uid 1000 --gid 1000 --home-dir /workspace --no-create-home sandbox && install -d -o 1000 -g 1000 /workspace
USER 1000:1000
WORKDIR /workspace
ENV HOME=/workspace
CMD ["/bin/sleep", "infinity"]
