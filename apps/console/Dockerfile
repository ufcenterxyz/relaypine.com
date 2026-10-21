# syntax=docker/dockerfile:1
FROM node:22-alpine AS dependencies
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1
COPY package.json yarn.lock ./
RUN --mount=type=cache,target=/usr/local/share/.cache/yarn \
    yarn install --frozen-lockfile --non-interactive

FROM dependencies AS build
COPY . .
ARG GATEWAY_URL=http://relaypine-gateway:4811
ENV NODE_ENV=production GATEWAY_URL=$GATEWAY_URL
RUN yarn build

FROM node:22-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 HOSTNAME=0.0.0.0 PORT=3000
RUN addgroup -S -g 10001 relaypine && adduser -S -D -H -u 10001 -G relaypine relaypine
# postbuild stages public/ and .next/static inside standalone/.
COPY --from=build --chown=relaypine:relaypine /app/.next/standalone ./
USER relaypine
EXPOSE 3000
CMD ["node", "server.js"]
