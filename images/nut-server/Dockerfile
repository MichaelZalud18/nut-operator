ARG ALPINE_VERSION=3.22
ARG ALPINE_DIGEST=sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
# Base images are pinned to the multi-arch index digest, not a per-platform one, so
# --platform builds still resolve correctly. The tag is kept beside it for readability:
# the digest is what Docker honours, the tag is what tells a reader which release it is.
ARG NUT_VERSION=2.8.5

# NUT is built from source rather than installed from the distribution package.
#
# Alpine's `nut` aport passes both --with-nss and --with-openssl, and NSS wins: the
# resulting upsd is linked against libnss3 and rejects CERTFILE as an invalid
# directive, because that directive is OpenSSL-only. The operator renders CERTFILE,
# so a packaged operand silently served plaintext while reporting TLS Required
# (F-39). Building here pins both the NUT version and the TLS backend, so a base
# image bump cannot change either one underneath the operator.
#
# OpenSSL is also the backend the rest of Kubernetes assumes. Certificates arrive as
# PEM in a TLS Secret, which CERTFILE consumes directly; the NSS path would need a
# three-file certificate database provisioned inside the container instead. Client
# certificates are not wired up yet, but they need this same backend when they are.
FROM alpine:${ALPINE_VERSION}@${ALPINE_DIGEST} AS nut-builder

ARG NUT_VERSION
ARG NUT_SHA256=18bf32e59eb764b13da3c4fa70384926d7fa584cb31d2fe7f137a570633eeec1
# Fingerprint of the NUT release signing key (Jim Klimov, the upstream maintainer).
#
# Named FINGERPRINT rather than KEY_FPR on purpose: BuildKit's SecretsUsedInArgOrEnv check
# matches ARG names containing KEY and warns. A public key fingerprint is the opposite of a
# secret -- publishing it is the entire point -- so the name avoids a warning that would be
# wrong rather than suppressing one that is right.
#
# The sha256 above answers "did the bytes arrive intact"; it cannot answer "did upstream
# publish these bytes", because it is a value this repository chose by looking at the same
# download. The detached signature answers the second question, and only if the key is
# pinned: fetching the key alongside the tarball would let whoever served a bad tarball
# serve a matching key. So the key is committed at images/nut-signing-key.asc, the
# fingerprint is asserted here, and the build has no keyserver dependency at all.
#
# Both checks stay. They fail on different things -- a truncated download versus a
# substituted release -- and neither subsumes the other.
ARG NUT_SIGNING_FINGERPRINT=B83459F776B90224988F36C0DE0184DA7043DCF7

RUN apk add --no-cache \
      build-base \
      curl \
      gnupg \
      libtool \
      openssl-dev \
      net-snmp-dev \
      neon-dev \
      libmodbus-dev \
      libltdl \
      libtool

COPY images/nut-signing-key.asc /tmp/nut-signing-key.asc

WORKDIR /build
# The signature check asserts on gpg's status output rather than its exit code. A bare
# `gpg --verify` succeeds for any key in the keyring, which would make the pin decorative.
# VALIDSIG's last field is the *primary* key fingerprint, and that is deliberately what is
# matched: releases are signed by a subkey, so pinning the signing subkey instead would
# prove less and break on every routine rotation.
RUN curl -fsSL -o nut.tar.gz "https://networkupstools.org/source/2.8/nut-${NUT_VERSION}.tar.gz" \
    && curl -fsSL -o nut.tar.gz.sig "https://networkupstools.org/source/2.8/nut-${NUT_VERSION}.tar.gz.sig" \
    && echo "${NUT_SHA256}  nut.tar.gz" | sha256sum -c - \
    && export GNUPGHOME="$(mktemp -d)" \
    && gpg --batch --quiet --import /tmp/nut-signing-key.asc \
    && gpg --batch --list-keys "${NUT_SIGNING_FINGERPRINT}" > /dev/null \
    && gpg --batch --status-fd 1 --verify nut.tar.gz.sig nut.tar.gz \
       | grep -qE "^\[GNUPG:\] VALIDSIG .* ${NUT_SIGNING_FINGERPRINT}$" \
    && rm -rf "${GNUPGHOME}" /tmp/nut-signing-key.asc \
    && tar -xzf nut.tar.gz \
    && rm nut.tar.gz nut.tar.gz.sig

WORKDIR /build/nut-${NUT_VERSION}

# Network-reachable UPS devices are the supported baseline, so the local-attachment
# drivers are not built at all. That is the API's position (UPSDevice rejects USB and
# serial drivers) expressed in the image: a driver that cannot be configured should
# not be present to be exploited.
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
      --with-snmp \
      --with-neon \
      --with-modbus \
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

FROM alpine:${ALPINE_VERSION}@${ALPINE_DIGEST}

ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
ARG NUT_VERSION
ARG SOURCE=https://github.com/MichaelZalud18/nut-operator
ARG DOCUMENTATION=https://github.com/MichaelZalud18/nut-operator/blob/main/README.md
ARG LICENSES=Apache-2.0

LABEL org.opencontainers.image.title="NUT Operator NUT Server" \
      org.opencontainers.image.description="Network UPS Tools server image for nut-operator network UPS operands" \
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

RUN apk add --no-cache \
      ca-certificates \
      openssl \
      net-snmp-libs \
      neon \
      libmodbus \
      libltdl \
    && addgroup -g 65532 -S nut \
    && adduser -u 65532 -S -G nut -H -s /sbin/nologin nut

COPY --from=nut-builder /out/ /

COPY images/nut-server/entrypoint.sh /usr/local/bin/nut-server-entrypoint

RUN chmod 0555 /usr/local/bin/nut-server-entrypoint \
    && mkdir -p /run/nut /etc/nut \
    && chown -R 65532:65532 /run/nut

# Fail the build rather than ship a server that cannot serve TLS. Linking is what
# decides whether CERTFILE is honored, not the rendered configuration, and F-39 was
# exactly this check missing. It runs against the assembled image rather than the
# staging directory so it verifies what actually ships.
RUN upsd -V 2>&1 | grep -q "${NUT_VERSION}" \
    && ldd /usr/sbin/upsd | grep -q libcrypto \
    && ! ldd /usr/sbin/upsd | grep -q libnss3

USER 65532:65532
EXPOSE 3493
# Same check as the readiness probe the operator renders: at least one driver reports RESPONSIVE
# from `upsdrvctl status` (F-46). Kubernetes ignores Docker HEALTHCHECK, so under this operator the
# probe is what runs -- but the image is also runnable directly, and there it should answer the same
# question the same way rather than drift into a second definition of healthy.
#
# The previous instruction ran `upsd -V`, which proves only that the binary executes: it would have
# passed on a container whose driver never connected and whose upsd was dead. A check that cannot
# fail is worse than none, because it reads as coverage.
#
# NOT_RESPONSIVE contains RESPONSIVE, so the match is field-exact for the same reason the probe's is.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD upsdrvctl status 2>/dev/null | awk '{for (i = 1; i <= NF; i++) if ($i == "RESPONSIVE") { found = 1; exit } } END { exit !found }'
ENTRYPOINT ["/usr/local/bin/nut-server-entrypoint"]
