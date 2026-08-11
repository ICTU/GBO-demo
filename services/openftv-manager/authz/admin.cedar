permit (
    principal is user,
    action,
    resource is service
)
when {
    principal has roles &&
    (principal.roles == "admin" || principal.roles.contains("admin"))
};
