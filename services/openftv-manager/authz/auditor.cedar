permit (
    principal is user,
    action == name::"can_read",
    resource is service
)
when {
    principal has roles &&
    (principal.roles == "auditor" || principal.roles.contains("auditor"))
};
