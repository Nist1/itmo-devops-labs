# GitHub MCP-сервер (официальный, Go) умеет только stdio.
# supergateway запускает его дочерним процессом и отдаёт наружу как Streamable HTTP,
# чтобы n8n (MCP Client Tool) ходил к нему по сети compose.
ARG GITHUB_MCP_VERSION
FROM ghcr.io/github/github-mcp-server:${GITHUB_MCP_VERSION} AS gh

FROM node:22-alpine
ARG SUPERGATEWAY_VERSION
RUN npm install -g supergateway@${SUPERGATEWAY_VERSION}
# Путь бинаря в исходном образе проверь: docker inspect --format '{{.Config.Entrypoint}}' ghcr.io/github/github-mcp-server:<tag>
COPY --from=gh /server/github-mcp-server /usr/local/bin/github-mcp-server
USER node
ENTRYPOINT ["supergateway"]
