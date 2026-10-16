# Container image for the wenyuan API server and its background worker.
#
# The image carries both binaries; which one runs is chosen by the command, so
# a deployment runs the same image with two roles. The build is static
# (CGO_ENABLED=0) so the runtime layer needs no libc and no toolchain.
#
# The PostgreSQL driver is compiled under the pgx build tag. A build without
# the tag produces a server that cannot select the postgres backend, which is
# why the tag is set here rather than left to the caller.

FROM golang:1.25-alpine AS builder

WORKDIR /src

# Resolve modules before copying the source so a source-only change reuses the
# dependency layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG PGX_TAG=pgx
RUN CGO_ENABLED=0 go build -tags "${PGX_TAG}" -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -tags "${PGX_TAG}" -o /out/worker ./cmd/worker && \
    CGO_ENABLED=0 go build -tags "${PGX_TAG}" -o /out/bankctl ./cmd/bankctl

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/server /usr/local/bin/server
COPY --from=builder /out/worker /usr/local/bin/worker
COPY --from=builder /out/bankctl /usr/local/bin/bankctl

# The conventional configuration file path is etc/wenyuan.yaml relative to the
# working directory, which is / here, so the shipped template lands where a
# ConfigMap or a bind mount is expected to replace it. The template is checked
# in as .yaml.example; the real file is a deployment's own and stays local.
COPY --from=builder /src/etc/example.wenyuan.yaml /etc/wenyuan.yaml

# The distroless nonroot user is uid 65532.
USER 65532:65532

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/server"]