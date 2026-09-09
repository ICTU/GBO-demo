# Operator-managed source documents

Documents in this directory serve sources that publish no `/.well-known/gbo`
endpoint of their own. A source configuration selects one with

```yaml
metadata_endpoint:
  transport: file
  path: centric/gbo.json
data_access:
  transport: fsc
  provider_peer_id: "99999999900000000300"
```

The path is relative to this directory and may not escape it.

What this transport removes is the fetch, not the review. The document is still
validated against `schemas/gbo-source-metadata-v1.schema.json`, its `source_oin`
must still match the provisioned certificate set, and promotion remains a
separate step. What changes is who signs off on the content: with no source
endpoint to publish it, the operator maintaining this file has taken over the
description of someone else's product. Record who agreed to it.

A file carries no freshness of its own. `expires_at` must stay at least one hour
in the future or the source turns `stale` and then `blocked`; a document parked
here therefore needs someone to refresh it. Treat that as the reason to move the
source onto its own metadata endpoint rather than as a routine chore.
