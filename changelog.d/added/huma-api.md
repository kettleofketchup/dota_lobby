- **OpenAPI spec.** The `/v1` API is described as OpenAPI 3.1, served without the API key at
  `/openapi.json` and `/openapi.yaml` (`/openapi-3.0.json` and `/openapi-3.0.yaml` for older
  client generators) and committed as `api/openapi.yaml`. Every operation has a stable
  `operationId`, and the webhook event envelope is included as the `LobbyEvent` schema under
  `webhooks.lobby-event`, so clients and webhook receivers can be generated from one document.
- `just go::openapi` regenerates `api/openapi.yaml`; a test fails when it is out of date.
