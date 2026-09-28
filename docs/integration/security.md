# Security

## API Credential Protection

- Store API keys in environment variables or secure vaults
- Never commit API keys to version control
- Never expose API keys in browser frontend or mobile apps
- Use HTTPS in production
- Rotate credentials if compromised

## Webhook Secret Protection

- Store webhook secrets securely
- Never log webhook secrets
- Never include webhook secrets in URLs or frontend code
- Verify webhook signatures before processing events

## HTTPS

Production environments must use HTTPS for all API calls and webhook endpoints.

## Merchant Isolation

The API key determines merchant identity. You cannot access another merchant's transactions by changing the `transaction_id` — the API returns `404 TRANSACTION_NOT_FOUND` for transactions belonging to other merchants.

## SSRF Protection

Webhook URL configuration is protected by SSRF validation. The webhook destination must be a valid public HTTPS URL in production. Localhost and private network addresses are blocked.

## Idempotency

- Use unique idempotency keys per logical operation
- Reuse the same key when retrying after timeout
- Do not use a global key for all operations

## Webhook Security

- Always verify HMAC-SHA256 signature before processing
- Use constant-time comparison
- Return non-2xx for invalid signatures
- Do not rely solely on event type or transaction ID without verification
