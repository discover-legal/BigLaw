# SPDX-License-Identifier: Apache-2.0
# TypeDB conflict-graph sidecar. Build context: sidecar/typedb/

FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json* ./
RUN npm install --no-audit --no-fund
COPY tsconfig.json server.ts typedb.ts schema.tql ./
RUN npx tsc && cp schema.tql dist/

FROM node:22-alpine
WORKDIR /app
ENV NODE_ENV=production
COPY package.json package-lock.json* ./
# /run/biglaw is shared with the Go core via a named volume; 1777 (sticky)
# so either uid can create the socket regardless of which image initialises it.
RUN npm install --omit=dev --no-audit --no-fund \
    && mkdir -p /run/biglaw && chown -R node:node /app && chmod 1777 /run/biglaw
COPY --from=build /app/dist ./dist

USER node
ENV TYPEDB_SOCKET=/run/biglaw/typedb.sock
ENTRYPOINT ["node", "dist/server.js"]
