FROM --platform=$BUILDPLATFORM node:26-alpine@sha256:e88a35be04478413b7c71c455cd9865de9b9360e1f43456be5951032d7ac1a66 AS build
WORKDIR /app
COPY package.json package-lock.json* ./
RUN npm install
COPY . .
RUN npm run build

FROM nginx:1.31-alpine@sha256:4a73073bd557c65b759505da037898b61f1be6cbcc3c2c3aeac22d2a470c1752
COPY --from=build /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY runtime-config.js.template /etc/nginx/templates/runtime-config.js.template
ENV NGINX_ENVSUBST_OUTPUT_DIR=/usr/share/nginx/html

# envsubst vervangt alleen variabelen die in de omgeving staan; een
# ongedeclareerde ${VAR} blijft letterlijk staan en wordt dan een kapotte
# href. Leeg declareren zodat de template altijd volledig wordt ingevuld
# en src/config.ts op zijn eigen defaults terugvalt.
ENV DEVELOPER_PORTAL_URL="" \
    TOESTEMMINGSPORTAAL_URL="" \
    DIENSTVERLENER_URL="" \
    EUDI_PUBLIC_URL="" \
    EUDI_CLIENT_ID="" \
    EUDI_UL_BASE="" \
    JAEGER_PUBLIC_URL="" \
    GRAFANA_PUBLIC_URL="" \
    FSC_CONTROLLER_BRON_URL="" \
    FSC_CONTROLLER_DVTP_URL="" \
    FSC_CONTROLLER_EUDI_URL="" \
    VERSION_STRING=""

EXPOSE 80
