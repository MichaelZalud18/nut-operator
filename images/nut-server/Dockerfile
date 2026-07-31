ARG DEBIAN_VERSION=13-slim
FROM debian:${DEBIAN_VERSION}

ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
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
      org.opencontainers.image.vendor="Michael Zalud"

RUN apt-get update \
    && apt-get install -y --no-install-recommends nut-server ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /run/nut \
    && chown -R 65532:65532 /run/nut

COPY images/nut-server/entrypoint.sh /usr/local/bin/nut-server-entrypoint

RUN chmod 0555 /usr/local/bin/nut-server-entrypoint

USER 65532:65532
EXPOSE 3493
ENTRYPOINT ["/usr/local/bin/nut-server-entrypoint"]
