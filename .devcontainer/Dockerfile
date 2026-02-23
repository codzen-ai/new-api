# Development container for new-api
# This image provides Go, Bun, and necessary tools for full-stack development

FROM oven/bun:latest

# Install essential development tools
RUN apt-get update && apt-get install -y \
    # Go installation dependencies
    wget \
    # Build tools
    ca-certificates \
    # Editor and utilities
    && rm -rf /var/lib/apt/lists/*

# Install Go latest stable
ENV GO_VERSION=1.25.6
RUN wget -q https://go.dev/dl/go${GO_VERSION}.linux-arm64.tar.gz && \
    tar -C /usr/local -xzf go${GO_VERSION}.linux-arm64.tar.gz && \
    rm go${GO_VERSION}.linux-arm64.tar.gz

# Go environment variables
ENV GOROOT=/usr/local/go
ENV GOPATH=/go
ENV PATH=$PATH:$GOROOT/bin:$GOPATH/bin

# Install Go development tools
RUN go install github.com/air-verse/air@latest && \
    go install github.com/go-delve/delve/cmd/dlv@latest && \
    go install golang.org/x/tools/gopls@latest

# Create workspace directory
WORKDIR /workspace

# Expose common development ports
# 3000 - Main application
# 8005 - pprof
# 5173 - Vite dev server
EXPOSE 3000 8005 5173

# Default command (can be overridden by devcontainer.json)
CMD ["sleep", "infinity"]
