FROM --platform=$BUILDPLATFORM golang:1.26 AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download
COPY cmd/node-actuator/ cmd/node-actuator/
COPY internal/nodeagent/ internal/nodeagent/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -ldflags "-X main.version=${VERSION}" -o node-actuator ./cmd/node-actuator

# CAP_SYS_BOOT is granted to the binary, not to the process (F-61).
#
# The DaemonSet asks for the capability through the container securityContext, and Kubernetes does
# put it in the bounding set -- but the pod also runs as UID 65532, and the permitted set does not
# survive that transition. Measured in a real pod, the actuator held:
#
#     CapPrm: 0000000000000000
#     CapEff: 0000000000000000
#     CapBnd: 0000000000400000     <- SYS_BOOT allowed, but not held
#
# so reboot(2) would have failed with EPERM. Actuation has only ever run stubbed, which is why
# nothing noticed. Kubernetes has no field for ambient capabilities, so the container securityContext
# alone cannot fix this.
#
# A file capability can, and it costs nothing else: the container keeps RunAsNonRoot,
# AllowPrivilegeEscalation: false, and drop ALL. The alternative -- running the actuator as root
# with CAP_SYS_BOOT only -- also works and was rejected for giving up non-root on the one container
# that can halt the machine.
#
# It is =p and not =ep, and that is the whole difference between a working image and a fleet-wide
# crash loop. With the effective bit set, execve fails with EPERM wherever CAP_SYS_BOOT is outside
# the bounding set:
#
#     $ docker run --rm node-actuator --version
#     exec /node-actuator: operation not permitted
#
# That is every configuration except actuation, including the DryRun/Stub default every deployment
# starts in -- so =ep would have crash-looped the actuator container on every node in order to make
# one rarely-used path work. Permitted-only masks instead of failing: the process starts with empty
# sets where the capability is not granted, and cmd/node-actuator raises it into the effective set
# immediately before reboot(2).
#
# Verified on kind rather than reasoned about, because three things here are easy to get wrong:
# that no_new_privs (set by AllowPrivilegeEscalation: false) would suppress the file capability, that
# a multi-stage COPY into distroless would drop the extended attribute, and the exec behavior above.
# The first two do not happen; the third does.
FROM alpine:3.22 AS capabilities

COPY --from=builder /workspace/node-actuator /out/node-actuator
RUN apk add --no-cache libcap \
    && setcap cap_sys_boot=p /out/node-actuator \
    && getcap /out/node-actuator | grep -q cap_sys_boot

FROM gcr.io/distroless/static:nonroot

ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
ARG SOURCE=https://github.com/MichaelZalud18/nut-operator
ARG DOCUMENTATION=https://github.com/MichaelZalud18/nut-operator/blob/main/README.md
ARG LICENSES=Apache-2.0

LABEL org.opencontainers.image.title="NUT Operator Node Actuator" \
      org.opencontainers.image.description="Fail-closed node actuator stub for nut-operator node power agents" \
      org.opencontainers.image.url="https://github.com/MichaelZalud18/nut-operator" \
      org.opencontainers.image.source="${SOURCE}" \
      org.opencontainers.image.documentation="${DOCUMENTATION}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.licenses="${LICENSES}" \
      org.opencontainers.image.vendor="Michael Zalud"

WORKDIR /
COPY --from=capabilities /out/node-actuator .
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/node-actuator", "--version"]
ENTRYPOINT ["/node-actuator"]
