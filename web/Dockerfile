# syntax=docker/dockerfile:1

FROM node:24-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM node:24-alpine AS build
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
RUN npm run build

# distroless: no shell, no package manager, non-root by default — same bar
# as the Go controller's image.
FROM gcr.io/distroless/nodejs24-debian12:nonroot AS runtime
WORKDIR /app
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static ./.next/static
COPY --from=build /app/public ./public
ENV NODE_ENV=production
ENV PORT=8080
EXPOSE 8080
# Base image already sets ENTRYPOINT ["/nodejs/bin/node"] and a non-root user.
CMD ["server.js"]
