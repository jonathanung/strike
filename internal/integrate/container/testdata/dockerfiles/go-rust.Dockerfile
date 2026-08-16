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

# Go toolchain (distro packages; pin via base image or packages for exact versions)
RUN apt-get update && apt-get install -y --no-install-recommends \
    golang-go gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*
# go.mod suggested version: 1.22

# Rust toolchain (rustup)
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y \
    && echo 'source $HOME/.cargo/env' >> $HOME/.bashrc
ENV PATH="/home/strike/.cargo/bin:/root/.cargo/bin:${PATH}"

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
