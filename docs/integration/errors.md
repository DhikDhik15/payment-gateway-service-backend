# Error Handling

## Error Response Structure

All errors follow a consistent envelope:

```json
{
  "success": false,
  "error": {
    "code": "ERROR_CODE",
    "message": "Human readable message",
    "details": {}
  },
  "meta": {
    "request_id": "req_xxx"
  }
}
```

## Error Codes

### Authentication Errors

| HTTP Status | Error Code | Meaning |
|-------------|------------|---------|
| 401 | `INVALID_API_KEY` | Missing or invalid API key |
| 401 | `MERCHANT_INACTIVE` | Merchant account is not active |
| 401 | `LEGACY_CREDENTIALS_NOT_ENABLED` | Legacy credentials disabled |

### Authorization Errors

| HTTP Status | Error Code | Meaning |
|-------------|------------|---------|
| 403 | `FORBIDDEN` | Cross-merchant access denied |

### Validation Errors

| HTTP Status | Error Code | Meaning |
|-------------|------------|---------|
| 400 | `VALIDATION_ERROR` | Request validation failed |
| 400 | `INVALID_REQUEST` | Malformed request |
| 400 | `INVALID_CURRENCY` | Unsupported currency |
| 400 | `INVALID_PAYMENT_METHOD` | Unsupported payment method |

### Not Found Errors

| HTTP Status | Error Code | Meaning |
|-------------|------------|---------|
| 404 | `TRANSACTION_NOT_FOUND` | Transaction not found or belongs to another merchant |
| 404 | `REFUND_NOT_FOUND` | Refund not found or belongs to another merchant |
| 404 | `MERCHANT_NOT_FOUND` | Merchant not found |

### Conflict Errors

| HTTP Status | Error Code | Meaning |
|-------------|------------|---------|
| 409 | `DUPLICATE_ORDER` | merchant_order_id already exists |
| 409 | `IDEMPOTENCY_KEY_REUSED` | Key used with different payload |
| 409 | `IDEMPOTENCY_REQUEST_IN_PROGRESS` | Concurrent request with same key |
| 409 | `INVALID_TRANSACTION_STATE` | Invalid state transition |
| 409 | `API_KEY_ALREADY_REVOKED` | API key already revoked |

### Refund Errors

| HTTP Status | Error Code | Meaning |
|-------------|------------|---------|
| 400 | `REFUND_TRANSACTION_NOT_PAID` | Transaction not in PAID state |
| 400 | `REFUND_AMOUNT_EXCEEDED` | Exceeds refundable balance |
| 400 | `REFUND_CURRENCY_MISMATCH` | Currency mismatch |

### Payment Provider Errors

| HTTP Status | Error Code | Meaning |
|-------------|------------|---------|
| 502 | `PAYMENT_PROVIDER_ERROR` | Provider rejected the request |
| 504 | `PAYMENT_PROVIDER_TIMEOUT` | Provider did not respond in time |

### Rate Limiting

| HTTP Status | Error Code | Meaning |
|-------------|------------|---------|
| 429 | `RATE_LIMIT_EXCEEDED` | Too many requests |

### Internal Errors

| HTTP Status | Error Code | Meaning |
|-------------|------------|---------|
| 500 | `INTERNAL_ERROR` | Unexpected server error |

## Error Handling Best Practices

- Always check `success` field before processing `data`
- Handle 401 by checking API key validity
- Handle 404 by verifying transaction ownership
- Handle 409 idempotency errors by retrying with same key
- Handle 429 by implementing exponential backoff
- Never expose internal error details to customers
