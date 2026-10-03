# OneClub backend image: one binary, several roles (api | worker | migrate).
# Technical Doc §5.4. Base image distroless, non-root, no shell.
#
#   docker build -f deploy/docker/Dockerfile --build-arg VERSION=$(git rev-parse --short HEAD) -t oneclub:dev .
#
# VPS releases (Staging/Production) are built with OBFUSCATE=true, which
# compiles with garble (Technical Doc §9.4 — obfuscation only for VPS deploys).

ARG GO_VERSION=1.27

FROM golang:${GO_VERSION} AS build
ARG VERSION=dev
ARG OBFUSCATE=false
ARG GARBLE_SEED=random
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY db ./db
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
RUN set -eu; \
    LDFLAGS="-s -w -X oneclub/internal/app.Version=${VERSION}"; \
    if [ "$OBFUSCATE" = "true" ]; then \
      go install mvdan.cc/garble@latest; \
      garble -literals -tiny -seed="${GARBLE_SEED}" build -ldflags="$LDFLAGS" -o /out/oneclub ./cmd/oneclub; \
    else \
      go build -ldflags="$LDFLAGS" -o /out/oneclub ./cmd/oneclub; \
    fi; \
    go build -ldflags="-s -w" -o /out/bridge-agent ./cmd/bridge-agent

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/oneclub /oneclub
USER nonroot
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 CMD ["/oneclub", "healthcheck"]
ENTRYPOINT ["/oneclub"]
CMD ["api"]
