# The hub's image, published as ghcr.io/aldrinsalazar/mcp-mesh-hub.
# Build from the repo root: docker build -f deploy/Dockerfile -t mcp-mesh-hub .
#
# The web and Go stages run on the build machine's platform and cross-compile,
# so a multi-platform build needs no emulation.
FROM --platform=$BUILDPLATFORM node:22 AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
ARG VERSION=dev
ARG TARGETOS TARGETARCH TARGETVARIANT
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/mcp-mesh-hub ./cmd/mcp-mesh-hub
# A named volume mounted on /data copies this directory's owner, so the
# nonroot user can write hub.db.
RUN mkdir /data

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/mcp-mesh-hub /mcp-mesh-hub
COPY --from=build --chown=65532:65532 /data /data
# 7000 dashboard and API (keep private), 7001 nodes, 7002 MCP clients
EXPOSE 7000 7001 7002
ENV MCPMESH_LOG_FORMAT=json MCPMESH_DATA_DIR=/data
VOLUME /data
HEALTHCHECK --interval=30s --timeout=5s CMD ["/mcp-mesh-hub", "healthcheck"]
ENTRYPOINT ["/mcp-mesh-hub"]
