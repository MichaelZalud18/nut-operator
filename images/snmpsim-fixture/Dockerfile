ARG PYTHON_VERSION=3.12-alpine
FROM python:${PYTHON_VERSION}

ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
ARG SOURCE=https://github.com/MichaelZalud18/nut-operator
ARG DOCUMENTATION=https://github.com/MichaelZalud18/nut-operator/blob/main/README.md
ARG LICENSES=Apache-2.0

LABEL org.opencontainers.image.title="NUT Operator SNMP Simulator Fixture" \
      org.opencontainers.image.description="snmpsim-command-responder serving a static RFC1628 UPS-MIB fixture, for snmp-ups driver conformance testing" \
      org.opencontainers.image.url="https://github.com/MichaelZalud18/nut-operator" \
      org.opencontainers.image.source="${SOURCE}" \
      org.opencontainers.image.documentation="${DOCUMENTATION}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.licenses="${LICENSES}" \
      org.opencontainers.image.vendor="Michael Zalud"

# Test-only fixture image: never shipped as a NodePowerAgent/NUTServer operand, only used to
# stand up a simulated SNMP UPS in the e2e suite. snmpsim (the original PyPI package) is
# unmaintained; snmpsim-lextudio is the maintained fork with the same CLI.
RUN pip install --no-cache-dir --root-user-action=ignore snmpsim-lextudio==1.1.1

COPY images/snmpsim-fixture/data /data

# snmpsim refuses to run as root without an explicit --process-user/--process-group to drop
# to; running the container itself as non-root sidesteps that check entirely, matching this
# project's other operand images.
USER 65532:65532

EXPOSE 161/udp
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD pgrep -f snmpsim-command-responder >/dev/null || exit 1

ENTRYPOINT ["snmpsim-command-responder", "--data-dir=/data"]
CMD ["--agent-udpv4-endpoint=0.0.0.0:161"]
