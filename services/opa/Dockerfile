FROM openpolicyagent/opa:1.16.2

# Demo Rego policies baked in; the default entrypoint args already load
# /policies. A volume mount at /policies shadows these files — drop the
# mount to use the baked-in versions.
COPY policies/ /policies/
