FROM openpolicyagent/opa:1.16.2@sha256:a915d8b59ddb09a9badecd8e061d43cf3111283494c4cf1d38a675bdb4e81a13

# Demo Rego policies baked in; the default entrypoint args already load
# /policies. A volume mount at /policies shadows these files — drop the
# mount to use the baked-in versions.
COPY policies/ /policies/
