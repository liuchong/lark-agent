# Credential-safe SDK logging

Third-party SDK log levels are a security boundary. A normal "connected" line
may include a full signed or credential-bearing URL even when request debug
logging is disabled.

Inject an application-owned logger into every SDK client. Drop debug/info
output, redact credential query parameters and JSON fields from warning/error
output, and keep non-secret operational context. Verify the production adapter
with a local fake endpoint carrying unique canaries; testing only the sanitizer
does not prove that the SDK client actually uses it.
