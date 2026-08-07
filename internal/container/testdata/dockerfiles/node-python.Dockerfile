# syntax=docker/dockerfile:1
FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive
ARG HOST_UID=1000

# Base system packages (cached layer)
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    sudo \
    && rm -rf /var/lib/apt/lists/*

# User-specified apt packages
RUN apt-get update && apt-get install -y --no-install-recommends \
    make \
    && rm -rf /var/lib/apt/lists/*

# Node.js 20
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y nodejs \
    && rm -rf /var/lib/apt/lists/*

# Python 3
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 python3-pip \
    && rm -rf /var/lib/apt/lists/*

# strike CLI is provided at launch (bind-mount or copy); not baked here.

# Non-root user matching host UID
RUN set -eux; \
    if ! getent passwd strike >/dev/null; then \
      useradd --create-home --shell /bin/bash --uid "${HOST_UID}" strike; \
    fi; \
    echo 'strike ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/strike; \
    chmod 0440 /etc/sudoers.d/strike
USER strike
WORKDIR /home/strike

ENV STRIKE_WORKSPACE=/workspace
WORKDIR /workspace
SHELL ["/bin/bash", "-c"]
CMD ["sleep", "infinity"]
