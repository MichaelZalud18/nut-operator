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
# no_new_privs does not suppress this, and the reason is worth stating precisely, because the
# general rule says it should. AllowPrivilegeEscalation: false sets no_new_privs, and the kernel's
# downgrade branch in cap_bprm_creds_from_file intersects the new permitted set with the caller's
# whenever execve would gain a capability under it. Measured on kind with one image, one pod, and
# one securityContext:
#
#     runtime execs the setcap binary   -> CapPrm 0000000000400000
#     sh -c '<same binary>'             -> CapPrm 0000000000000000
#
# The entrypoint is not a gain. containerd writes permitted: [CAP_SYS_BOOT] into the OCI spec, so
# the runtime's init already holds the capability when it execs this binary, and there is nothing
# to take away. Anything spawned *inside* the container is a gain -- the parent is a
# capability-dumb process whose permitted set went empty at its own exec -- and loses it.
#
# So this only works while the actuator is the container's entrypoint. Wrapping it in a shell, an
# init shim, or a debug wrapper would disarm actuation while leaving every probe and status field
# reporting healthy. cmd/node-actuator checks the permitted set at startup and refuses to arm
# rather than trusting that; TestDockerfileExecsTheActuatorAsTheEntrypoint guards the exec form.
#
# The extended attribute survives the whole distribution path, which was the other thing likely to
# break it: a multi-stage COPY into distroless, a push to a registry, and a pull unpacked by
# containerd on the node all preserve it. Checked with getcap against the binary as unpacked on the
# node, not as built.
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
# Same question the readiness probe asks: has the watch loop completed a pass recently (F-64).
#
# This was `--version`, which prints a string and returns before any configuration is parsed -- it
# passed on a process parked forever in block() and on one that had stopped looping. F-46 retired
# the same instruction on the server image for the same reason.
#
# Run bare with no state volume this reports unhealthy, which is correct rather than awkward: there
# is no watch loop recording anything, so there is nothing to be healthy about.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/node-actuator", "--ready"]
ENTRYPOINT ["/node-actuator"]
