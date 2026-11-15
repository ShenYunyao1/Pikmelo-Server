ARG NODE_IMAGE=node:22-bookworm-slim
FROM ${NODE_IMAGE}
WORKDIR /runner
COPY server.mjs execute.mjs ./
COPY NOTICE LICENSE-LX-Music.txt /usr/share/doc/pikmelo-online-worker/
ENV NODE_ENV=production PORT=3000
USER node
ARG VCS_REF=unknown
ARG IMAGE_VERSION=local
LABEL org.opencontainers.image.title="Pikmelo Server online worker" \
      org.opencontainers.image.source="https://github.com/ShenYunyao1/Pikmelo-Server" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.version="${IMAGE_VERSION}"
EXPOSE 3000
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD ["node", "-e", "fetch('http://127.0.0.1:3000/health').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))"]
CMD ["node", "server.mjs"]
