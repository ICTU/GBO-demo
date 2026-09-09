// The PDP's bundle pull. Since OpenFTV v0.1.0 the Manager's internal bundle
// endpoint runs through the same authorizer as its UI API, and these Cedar
// policies are permit-only (Cedar denies by default), so without this the
// PDP gets 403 on every retrieval and never receives a policy bundle.
//
// The PDP presents no identity — it is a service on the internal port, not a
// user — so this cannot be scoped by principal. It is scoped instead to the
// single path it needs and to reading: the bundle named in
// services/openftv-manager/bundles/, nothing else, GET only.
//
// Keep the resource in step with GBO_BUNDLE_MANAGER in the Makefile: both
// name the gbo-pdp bundle.
permit (
    principal,
    action == name::"can_read",
    resource == service::"/v1/bundle/gbo-pdp"
);
