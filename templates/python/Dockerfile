FROM python:3.13-slim-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends tar findutils coreutils git ca-certificates && rm -rf /var/lib/apt/lists/*
RUN groupadd --gid 1000 sandbox && useradd --uid 1000 --gid 1000 --home-dir /workspace --no-create-home sandbox && install -d -o 1000 -g 1000 /workspace
USER 1000:1000
WORKDIR /workspace
ENV HOME=/workspace
ENV VIRTUAL_ENV=/workspace/.venv
ENV PATH=/workspace/.venv/bin:$PATH
ENV PIP_CACHE_DIR=/workspace/.cache/pip
CMD ["/bin/sleep", "infinity"]
