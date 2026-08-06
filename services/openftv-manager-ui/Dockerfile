# OpenFTV's management interface: the UI for the Manager's policies,
# attributes, entities and deployments.
#
# Built from the same pinned OpenFTV commit as the Manager and the PDP —
# it talks to the Manager's API, so it has to match it.
FROM node:22.17.1-alpine AS builder

ARG OPENFTV_REF=5da6cf1e9c8a55385fbac61aa9eeb9fc889f2b8f

WORKDIR /src
ADD https://gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/-/archive/${OPENFTV_REF}/open-ftv-${OPENFTV_REF}.tar.gz /tmp/open-ftv.tar.gz
RUN tar -xzf /tmp/open-ftv.tar.gz -C /src --strip-components=1

WORKDIR /src/apps/management-interface
RUN npm ci && npm run build

FROM nginxinc/nginx-unprivileged:1.29-alpine
COPY --from=builder /src/apps/management-interface/dist /usr/share/nginx/html
# The upstream template renders /runtime-env.js from VITE_* at container
# start, so one image serves any deployment.
COPY --from=builder /src/apps/management-interface/util/default.conf.template /etc/nginx/templates/default.conf.template
EXPOSE 8080
