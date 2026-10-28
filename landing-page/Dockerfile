FROM --platform=$BUILDPLATFORM node:26-alpine@sha256:40cd011b55415bc894e45b02fdd5a5dfde056921b7d23a64c74f80497af48797 AS build
WORKDIR /app
COPY package.json package-lock.json* .npmrc ./
RUN npm install
COPY . .
RUN npm run build

FROM nginx:1.31-alpine@sha256:892bb2152464f0c5d8972593a5f5afaf8054552580b699d95f8ff4af08c2d33d
COPY --from=build /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY 15-configure-eudi-offers.sh /docker-entrypoint.d/15-configure-eudi-offers.sh
RUN chmod 755 /docker-entrypoint.d/15-configure-eudi-offers.sh
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
    EUDI_UL_BASE="" \
    JAEGER_PUBLIC_URL="" \
    GRAFANA_PUBLIC_URL="" \
    FSC_CONTROLLER_BRON_URL="" \
    FSC_CONTROLLER_DVTP_URL="" \
    FSC_CONTROLLER_EUDI_URL="" \
    VERSION_STRING=""

EXPOSE 80
