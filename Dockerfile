# Run with
# docker build -t gastown:latest -f Dockerfile .
FROM docker/sandbox-templates:claude-code

ARG GO_VERSION=1.26.11
# sha256 of the official go${GO_VERSION}.linux-<arch>.tar.gz (https://go.dev/dl/?mode=json).
# Update these together with GO_VERSION.
ARG GO_SHA256_AMD64=9849f4e04ee35303714e34fbc3ac8ab44ea3e448a95aa182f91e3b9b79c710ec
ARG GO_SHA256_ARM64=6c2f29b458b5add0835f18b776f69c8881a6d88428f1484a6da55755e620757e
ARG DOLT_VERSION=2.6.0

USER root

# The base image (Ubuntu 26.04) ships git 2.53.0, which is vulnerable to
# CVE-2026-105437 and has no fixed build in the Ubuntu archive. Pull git from
# the git-core PPA (signing key F911AB184317630C59970973E363C90F8F1B6217,
# committed in docker/git-core-ppa.asc). The PPA keeps only its newest build,
# so assert a version floor below instead of pinning an exact version.
ARG GIT_MIN_VERSION=1:2.56.1
COPY docker/git-core-ppa.asc /etc/apt/keyrings/git-core-ppa.asc
RUN . /etc/os-release && \
    printf '%s\n' \
      'Types: deb' \
      'URIs: https://ppa.launchpadcontent.net/git-core/ppa/ubuntu' \
      "Suites: ${VERSION_CODENAME}" \
      'Components: main' \
      'Signed-By: /etc/apt/keyrings/git-core-ppa.asc' \
      > /etc/apt/sources.list.d/git-core-ppa.sources

# Install system dependencies
RUN apt-get update && apt-get install -y \
    build-essential \
    git \
    libicu-dev \
    sqlite3 \
    tmux \
    curl \
    ripgrep \
    zsh \
    gh \
    netcat-openbsd \
    tini \
    vim \
    && GIT_PKG_VERSION="$(dpkg-query -W -f='${Version}' git)" \
    && echo "git package version: ${GIT_PKG_VERSION}" \
    && dpkg --compare-versions "${GIT_PKG_VERSION}" ge "${GIT_MIN_VERSION}" \
    && git --version \
    && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*

# Install Go from official tarball (apt golang-go is too old). The base image
# ships its own Go in /usr/local/go; remove it so the two trees do not mix.
RUN ARCH=$(dpkg --print-architecture) && \
    case "${ARCH}" in \
      amd64) GO_SHA256="${GO_SHA256_AMD64}" ;; \
      arm64) GO_SHA256="${GO_SHA256_ARM64}" ;; \
      *) echo "unsupported architecture for Go tarball: ${ARCH}" >&2; exit 1 ;; \
    esac && \
    curl -fsSL -o /tmp/go.tar.gz "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" && \
    echo "${GO_SHA256}  /tmp/go.tar.gz" | sha256sum -c - && \
    rm -rf /usr/local/go && \
    tar -C /usr/local -xzf /tmp/go.tar.gz && \
    rm -f /tmp/go.tar.gz
ENV PATH="/app/gastown:/usr/local/go/bin:/home/agent/go/bin:${PATH}"

# Install beads (bd) and dolt
RUN curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash
RUN curl -fsSL https://github.com/dolthub/dolt/releases/download/v${DOLT_VERSION}/install.sh | bash

# Set up directories
RUN mkdir -p /app /gt /gt/.dolt-data && chown -R agent:agent /app /gt

# Environment setup for bash and zsh
RUN echo 'export PATH="/app/gastown:$PATH"' >> /etc/profile.d/gastown.sh && \
    echo 'export PATH="/app/gastown:$PATH"' >> /etc/zsh/zshenv
RUN echo 'export COLORTERM="truecolor"' >> /etc/profile.d/colorterm.sh && \
    echo 'export COLORTERM="truecolor"' >> /etc/zsh/zshenv
RUN echo 'export TERM="xterm-256color"' >> /etc/profile.d/term.sh && \
    echo 'export TERM="xterm-256color"' >> /etc/zsh/zshenv

USER agent

COPY --chown=agent:agent . /app/gastown

RUN cd /app/gastown && make build

COPY --chown=agent:agent docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh

WORKDIR /gt

ENTRYPOINT ["tini", "--", "/app/docker-entrypoint.sh"]
CMD ["sleep", "infinity"]
