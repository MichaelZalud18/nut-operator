ARG ALPINE_VERSION=3.22
ARG NUT_VERSION=2.8.5

FROM --platform=$BUILDPLATFORM golang:1.26 AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download
COPY cmd/power-signal-writer/ cmd/power-signal-writer/
COPY cmd/power-notify-writer/ cmd/power-notify-writer/
COPY internal/nodeagent/ internal/nodeagent/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -ldflags "-X main.version=${VERSION}" -o power-signal-writer ./cmd/power-signal-writer \
    && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -ldflags "-X main.version=${VERSION}" -o power-notify-writer ./cmd/power-notify-writer

# NUT is built from source for the reasons in images/nut-server/Dockerfile (F-39).
# The agent needs the same treatment and for the same reason: the operator renders
# CERTPATH, CERTVERIFY and FORCESSL into upsmon.conf, and which SSL backend upsmon
# is linked against decides whether a client that is told to verify its server
# actually does. A client that silently skips verification is the same class of
# defect as a server that silently skips encryption.
#
# It matters for client certificates too. When upsmon can present an identity, the
# OpenSSL path takes a PEM file through CERTFILE while the NSS path needs a
# certificate database; building on OpenSSL now is what keeps that work to a
# rendering change later rather than a rebuild of the image.
FROM alpine:${ALPINE_VERSION} AS nut-builder

ARG NUT_VERSION
ARG NUT_SHA256=18bf32e59eb764b13da3c4fa70384926d7fa584cb31d2fe7f137a570633eeec1

RUN apk add --no-cache build-base curl libtool openssl-dev libltdl

WORKDIR /build
RUN curl -fsSL -o nut.tar.gz "https://networkupstools.org/source/2.8/nut-${NUT_VERSION}.tar.gz" \
    && echo "${NUT_SHA256}  nut.tar.gz" | sha256sum -c - \
    && tar -xzf nut.tar.gz \
    && rm nut.tar.gz

WORKDIR /build/nut-${NUT_VERSION}

# The agent is a pure client: it monitors upsd over the network and never drives a
# device, so no driver is built into it at all.
RUN ./configure \
      --prefix=/usr \
      --sysconfdir=/etc/nut \
      --datadir=/usr/share/nut \
      --libexecdir=/usr/lib/nut \
      --with-drvpath=/usr/lib/nut \
      --with-statepath=/run/nut \
      --with-altpidpath=/run/nut \
      --with-user=nut \
      --with-group=nut \
      --with-openssl \
      --without-nss \
      --without-snmp \
      --without-neon \
      --without-modbus \
      --without-usb \
      --without-serial \
      --without-ipmi \
      --without-freeipmi \
      --without-powerman \
      --without-avahi \
      --without-cgi \
      --without-wrap \
      --without-dev \
      --disable-static \
    && make -j"$(nproc)" \
    && make DESTDIR=/out install \
    && rm -rf /out/usr/share/man /out/usr/share/doc

FROM alpine:${ALPINE_VERSION}

ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
ARG NUT_VERSION
ARG SOURCE=https://github.com/MichaelZalud18/nut-operator
ARG DOCUMENTATION=https://github.com/MichaelZalud18/nut-operator/blob/main/README.md
ARG LICENSES=Apache-2.0

LABEL org.opencontainers.image.title="NUT Operator upsmon Agent" \
      org.opencontainers.image.description="Unprivileged Network UPS Tools upsmon client image for nut-operator node agents" \
      org.opencontainers.image.url="https://github.com/MichaelZalud18/nut-operator" \
      org.opencontainers.image.source="${SOURCE}" \
      org.opencontainers.image.documentation="${DOCUMENTATION}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.licenses="${LICENSES}" \
      org.opencontainers.image.vendor="Michael Zalud" \
      io.nut-operator.nut.version="${NUT_VERSION}" \
      io.nut-operator.nut.ssl-backend="openssl"

RUN apk add --no-cache ca-certificates openssl libltdl \
    && addgroup -g 65532 -S nut \
    && adduser -u 65532 -S -G nut -H -s /sbin/nologin nut \
    && mkdir -p /run/power-agent \
    && chown -R 65532:65532 /run/power-agent

COPY --from=nut-builder /out/ /
COPY --from=builder /workspace/power-signal-writer /usr/local/bin/power-signal-writer
# Receives upsmon NOTIFYCMD dispatches (F-68). Without a NOTIFYCMD upsmon falls back to `wall`,
# which this image does not ship, so every notification failed rather than being merely unused.
COPY --from=builder /workspace/power-notify-writer /usr/local/bin/power-notify-writer

# The client-side half of the check in images/nut-server/Dockerfile: an agent told
# to verify its server can only do so if it was linked against the backend whose
# directives the operator renders.
RUN upsmon -V 2>&1 | grep -q "${NUT_VERSION}" \
    && ldd /usr/sbin/upsmon | grep -q libcrypto \
    && ! ldd /usr/sbin/upsmon | grep -q libnss3

USER 65532:65532
# Same question the readiness probe asks: every UPS this agent is configured to monitor is being
# served to it (F-65). Kubernetes ignores Docker HEALTHCHECK, so under this operator the probe is
# what runs -- but the image is also runnable directly, and there it should answer the same question
# the same way rather than drift into a second definition of healthy.
#
# The previous instruction ran `upsmon -V`, which proves only that the binary executes: it would
# have passed on a container whose upsmon was dead, whose credentials were wrong, or whose config
# named no UPS at all. A check that cannot fail is worse than none, because it reads as coverage
# (F-64, and F-46 before it).
#
# Zero MONITOR targets fails rather than passing vacuously, which is the specific bug the probe's
# old `while` pipeline had.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD test -r /etc/nut/upsmon.conf \
    && targets=$(awk '/^MONITOR[[:space:]]/ {print $2}' /etc/nut/upsmon.conf) \
    && test -n "$targets" \
    && for target in $targets; do upsc "$target" >/dev/null || exit 1; done
ENTRYPOINT ["upsmon"]
