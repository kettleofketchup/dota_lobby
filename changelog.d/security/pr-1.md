- API keys are compared in constant time.
- Credentials and the API key use a `Secret` type that prints and marshals to JSON as
  `***REDACTED***`, so they stay out of logs.
- The service logs `WARNING: API key authentication disabled - all endpoints are public` at
  startup when `server.api_key` is not set.
