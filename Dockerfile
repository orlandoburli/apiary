# Minimal runtime image. GoReleaser builds the static (CGO-free) binary and
# places it in the build context; this Dockerfile just wraps it.
# distroless/static = no shell, no package manager, tiny attack surface.
FROM gcr.io/distroless/static-debian12:nonroot

COPY apiary /usr/bin/apiary

# Apiary persists state in a SQLite db — mount a volume here at runtime, e.g.
#   docker run -v apiary-data:/data ghcr.io/orlandoburli/apiary ...
WORKDIR /data

ENTRYPOINT ["/usr/bin/apiary"]
