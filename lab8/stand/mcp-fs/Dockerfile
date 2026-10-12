# Filesystem MCP-сервер (Node, stdio) за тем же supergateway.
FROM node:22-alpine
ARG SUPERGATEWAY_VERSION
ARG FS_MCP_VERSION
RUN npm install -g supergateway@${SUPERGATEWAY_VERSION} \
    @modelcontextprotocol/server-filesystem@${FS_MCP_VERSION}
USER node
ENTRYPOINT ["supergateway"]
