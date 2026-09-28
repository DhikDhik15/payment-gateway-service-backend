# Rate Limits

## Limits

| Scope | Limit | Window |
|-------|-------|--------|
| API (per authenticated merchant) | 120 requests | 1 minute |
| API (per client IP, pre-auth) | 120 requests | 1 minute |

## Behavior When Exceeded

When the rate limit is exceeded, the API returns:

```http
HTTP/1.1 429 Too Many Requests
Retry-After: 60
Content-Type: application/json

{
  "success": false,
  "error": {
    "code": "RATE_LIMIT_EXCEEDED",
    "message": "Too many requests, please retry later"
  },
  "meta": {
    "request_id": "req_xxx"
  }
}
```

## Retry Strategy

The `Retry-After` header indicates how many seconds to wait before retrying.

```bash
# Check Retry-After header
curl -i -X POST http://localhost:8080/api/v1/payments \
  -H "X-API-Key: pk_xxx:sk_xxx" \
  -H "Content-Type: application/json" \
  -d '{...}'

# Wait and retry
sleep 60
```

## Best Practices

- Implement exponential backoff for 429 responses
- Respect the `Retry-After` header
- Cache results where appropriate to reduce API calls
- Use webhooks instead of polling to reduce API traffic
