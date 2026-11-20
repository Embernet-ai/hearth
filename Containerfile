# Two stages, and the runtime stage has nothing in it but the binary.
#
# The predecessor's image is 809 MB and takes 1h32m to build. This one is a
# static binary on a distroless base, which is a few megabytes and under a
# minute. That difference is not vanity: it is how fast the verification loop
# runs, and the verification loop is what determines whether a rewrite finishes.

# The rebuilt interface (ui/: Astro and Svelte), built to static files that the Go binary
# embeds. Pinned to the same Node the dashboard's console builds with. Nothing here needs
# a credential: the design tokens are vendored in ui/src/styles and the rest comes from npm.
FROM node:22.23.4-bookworm AS ui
WORKDIR /ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY ui/ ./
RUN npm run build

FROM golang:1.26.11-alpine AS build

# git is needed so the build stamps the real VCS revision into the binary.
# /healthz reports it, which is how you tell what is actually deployed.
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Modules come from the Go module proxy and are checked against go.sum, which pins every
# one of them by hash. `go mod verify` fails the build if a downloaded module does not
# match. They are fetched before the source is copied, so a source-only change reuses
# this layer.
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
# The built interface goes where internal/web embeds it from. A checkout holds only a
# placeholder there, so the Go tests and a plain `go build` need no Node.
COPY --from=ui /ui/dist ./internal/web/next/dist

# CGO off gives a genuinely static binary, which is what lets the runtime stage
# be distroless/static rather than a distro.
# No -tags dev. A development build is a different artifact by construction, and
# CI asserts the deployed binary reports profile=release.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/hearth ./cmd/hearth \
    && /out/hearth --help 2>&1 | head -1 || true

# The operator's side, in the same image on purpose. A backup taken by a
# different build than the one that wrote the rows is a backup whose archive
# format and schema version are somebody's assumption rather than a fact, and
# the CronJob that runs it should not need a second artifact kept in step.
RUN go build -trimpath -ldflags="-s -w" -o /out/hearthctl ./cmd/hearthctl

# Prove the artifacts are what we think they are, in the builder, where a
# failure is cheap. Discovering this at `kubectl get pods` three days later is
# not.
RUN test -x /out/hearth && test -x /out/hearthctl \
    && echo "binaries ok:" && ls -lh /out/hearth /out/hearthctl

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/hearth /hearth
COPY --from=build /out/hearthctl /usr/local/bin/hearthctl

# nonroot is uid 65532. Declared explicitly so it is obvious, and so a
# securityContext that disagrees fails loudly rather than silently running as root.
USER 65532:65532

EXPOSE 8080
ENTRYPOINT ["/hearth"]
