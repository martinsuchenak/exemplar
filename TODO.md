# Project TODOs

Welcome to your newly scaffolded `exemplar` project! Here are the next steps to get your project fully production-ready based on the features you selected.
## API & Authentication
- [x] **Authentication**: per-account API keys (`X-API-Key`) resolved by the account service.
- [ ] **SSO**: `BearerTokenMiddleware` is kept for a later SSO validator (JWT, OAuth 2.0 + PKCE, or OIDC).
- [ ] **OpenAPI Spec**: Update `openapi.yaml` to match any new endpoints you add, or consider generating it automatically from code comments using tools like `swag`.
## Database
- [ ] **Configuration**: Update the database credentials in `exemplar-config.toml` or set the appropriate environment variables.
- [x] **Schema Migrations**: versioned migrations run through `xdal/pkg/migrate` (`internal/db/migrations/`).
## Docker
- [ ] **Registry**: Update the Docker tag registry prefix in `Taskfile.yml` or your CI pipelines before pushing the image.

## General
- [ ] **Dependencies**: Run `go mod tidy` if you add any new imports. (This was run automatically during scaffolding).
- [ ] **Tests**: Run `task test` to ensure all generated components pass their initial unit tests.
