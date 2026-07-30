# This multi-stage image turns the two source projects in this repository into
# one Cloud Run service. The first stage builds the React application, the
# second compiles the Go API, and the final stage contains only those production
# artifacts. Cloud Build uses this file when the GitHub repository is connected
# to Cloud Run with "Dockerfile" selected as the build type.

# ---------------------------------------------------------------------------
# React production bundle
# ---------------------------------------------------------------------------

# Pin the current Node 22 patch release so local and Cloud Build installations
# use the same JavaScript runtime. Debian Bookworm matches the Go build stage and
# the deliberately small "slim" variant is sufficient for Vite.
FROM node:22.23.1-bookworm-slim AS frontend-build

WORKDIR /workspace/frontend

# pnpm is installed at the exact version declared by frontend/package.json.
# Copying dependency metadata before application source lets Docker reuse the
# install layer when only React or CSS files change.
RUN npm install --global pnpm@11.9.0
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile

# Only build inputs are copied. Local node_modules, frontend/dist, and any
# machine-specific files are therefore excluded even when they exist beside the
# repository checkout.
COPY frontend/index.html frontend/tsconfig.json frontend/vite.config.ts ./
COPY frontend/src ./src
RUN pnpm build

# ---------------------------------------------------------------------------
# Go production binary
# ---------------------------------------------------------------------------

# This version matches the current Go 1.26 module and local toolchain. Module
# files are copied first so downloaded dependencies remain cached across normal
# backend source changes.
FROM golang:1.26.5-bookworm AS backend-build

WORKDIR /workspace/backend

COPY backend/go.mod backend/go.sum ./
RUN go mod download

# Test files are harmless build inputs because `go build` ignores *_test.go.
# The timetzdata tag embeds the IANA timezone database needed for the
# America/New_York weekly boundary even though the final image has no full OS.
COPY backend/*.go ./
RUN CGO_ENABLED=0 go build \
    -buildvcs=false \
    -tags timetzdata \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/personal-ai \
    .

# ---------------------------------------------------------------------------
# Minimal non-root Cloud Run image
# ---------------------------------------------------------------------------

# The Node and Go sections above are temporary workspaces used only during the
# build. This line starts the much smaller image that Cloud Run actually runs.
# "distroless" means it contains the trusted certificates needed to contact
# Supabase and OpenRouter, but not development tools such as Node, Go, or a
# command shell. "nonroot" means the application runs without administrator
# privileges. "AS runtime" is only a readable name for this final build stage.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

WORKDIR /app

COPY --from=backend-build --chown=65532:65532 /out/personal-ai /app/personal-ai
COPY --from=frontend-build --chown=65532:65532 /workspace/frontend/dist /app/frontend/dist

# main.go reads this directory at startup and serves index.html plus the hashed
# Vite assets from the same origin as /api.
ENV FRONTEND_DIST_DIR=/app/frontend/dist

# Cloud Run supplies PORT=8080 automatically. EXPOSE documents that contract for
# local container tools without overriding Cloud Run's environment value.
EXPOSE 8080

USER 65532:65532
ENTRYPOINT ["/app/personal-ai"]
