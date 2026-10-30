FROM scratch
ARG TARGETARCH
COPY dist/openbao-plugin-gitea_linux_${TARGETARCH} /openbao-plugin-gitea
ENTRYPOINT ["/openbao-plugin-gitea"]
