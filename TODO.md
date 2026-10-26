# Project TODOs

Welcome to your newly scaffolded `exemplar` project! Here are the next steps to get your project fully production-ready based on the features you selected.
## API & Authentication
- [ ] **Authentication**: The boilerplate provides middleware placeholders in `internal/auth/auth.go`. `BearerTokenMiddleware` rejects every request until you pass it a `TokenValidator` implementing your auth scheme (e.g., JWT validation, OAuth 2.0 + PKCE, or OIDC).
- [ ] **OpenAPI Spec**: Update `openapi.yaml` to match any new endpoints you add, or consider generating it automatically from code comments using tools like `swag`.
## Database
- [ ] **Configuration**: Update the database credentials in `exemplar-config.toml` or set the appropriate environment variables.
- [ ] **Schema Migrations**: The project provides a `internal/db/schema.sql` for initial setup. Consider adding a migration tool like `golang-migrate/migrate` or `pressly/goose` for managing schema evolution.
## Docker
- [ ] **Registry**: Update the Docker tag registry prefix in `Taskfile.yml` or your CI pipelines before pushing the image.

## General
- [ ] **Dependencies**: Run `go mod tidy` if you add any new imports. (This was run automatically during scaffolding).
- [ ] **Tests**: Run `task test` to ensure all generated components pass their initial unit tests.
