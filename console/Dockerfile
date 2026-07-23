# syntax=docker/dockerfile:1
#
# jcloud console — static Vite build served by nginx, which also reverse-proxies
# /api and /auth to the orchestrator Service (same-origin in production, exactly
# like the dev proxy in vite.config.ts). The upstream is injected at runtime via
# ORCHESTRATOR_URL (the official nginx image envsubsts /etc/nginx/templates).
#
# No VITE_CONSOLE_TOKEN is baked in: production users land on the sign-in screen
# (OAuth login or the "Advanced: console token" fallback).

FROM node:22-alpine AS build
ARG VITE_APP_VERSION
ENV VITE_APP_VERSION=${VITE_APP_VERSION}
WORKDIR /app
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY . .
RUN pnpm run build

FROM nginx:1.27-alpine
COPY --from=build /app/dist /usr/share/nginx/html
COPY nginx/default.conf.template /etc/nginx/templates/default.conf.template
# In-cluster default; kustomize can override per environment.
ENV ORCHESTRATOR_URL=http://orchestrator:8080
EXPOSE 80
