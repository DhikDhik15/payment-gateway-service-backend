# Authentication

## API Key

All requests must include the `X-API-Key` header:

```
X-API-Key: pk_xxx:sk_xxx
```

### Credential Format

```
pk_<hex>:sk_<hex>
```

- `pk_` prefix: public key ID (160-bit)
- `sk_` prefix: secret credential (256-bit)
- Separator: `:`

### Example

```bash
curl -X POST http://localhost:8080/api/v1/payments \
  -H "X-API-Key: pk_abc123:sk_xyz789" \
  -H "Content-Type: application/json" \
  -d '{"merchant_order_id":"ORDER-001","amount":100000,"currency":"IDR","payment_method":"QRIS"}'
```

## Authentication Behavior

- Missing header: `401 INVALID_API_KEY`
- Invalid credential: `401 INVALID_API_KEY`
- Revoked key: `401 INVALID_API_KEY`
- Expired key: `401 INVALID_API_KEY`
- Inactive merchant: `401 MERCHANT_INACTIVE`

## Merchant Identity

The API key determines the merchant identity. Clients must never send a `merchant_id` — it is always derived from the authenticated API key.

## API Key Lifecycle

### Create

```bash
POST /api/v1/merchants/{merchantId}/api-keys
X-API-Key: pk_xxx:sk_xxx

{"name":"Production Key"}
```

Response includes `secret` — store it securely, it will not be returned again.

### Revoke

```bash
DELETE /api/v1/merchants/{merchantId}/api-keys/{keyId}
```

Revocation is permanent.

### Rotate

```bash
POST /api/v1/merchants/{merchantId}/api-keys/{keyId}/rotate
```

Atomically revokes the old key and creates a replacement.

## Security Best Practices

- Never commit API keys to version control
- Never expose API keys in browser frontend
- Use environment variables for storage
- Rotate credentials if compromised
