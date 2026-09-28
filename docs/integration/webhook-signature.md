# Webhook Signature Verification

## Overview

Every webhook request includes an HMAC-SHA256 signature. You must verify this signature before processing the event.

## Signature Algorithm

```
signature = "sha256=" + hex(HMAC-SHA256(secret, timestamp + "." + rawBody))
```

- `secret`: webhook secret (whsec_...) received on configuration
- `timestamp`: value from `X-PayGate-Timestamp` header
- `rawBody`: raw request body bytes (do NOT reserialize JSON)

## Verification Steps

1. Read the raw request body (before JSON parsing)
2. Extract `X-PayGate-Timestamp` header
3. Extract `X-PayGate-Signature` header
4. Compute expected signature using the formula above
5. Compare using constant-time comparison
6. Reject if signature is invalid (return non-2xx)

## Go Example

```go
import (
    "crypto/hmac"
    "crypto/sha256"
    "crypto/subtle"
    "encoding/hex"
    "net/http"
)

func verifyWebhook(secret string, timestamp string, body []byte, signature string) bool {
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write([]byte(timestamp))
    mac.Write([]byte("."))
    mac.Write(body)
    expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
    return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}

func webhookHandler(w http.ResponseWriter, r *http.Request) {
    body, _ := io.ReadAll(r.Body)
    timestamp := r.Header.Get("X-PayGate-Timestamp")
    signature := r.Header.Get("X-PayGate-Signature")

    if !verifyWebhook(secret, timestamp, body, signature) {
        http.Error(w, "invalid signature", http.StatusUnauthorized)
        return
    }

    // Process event...
    w.WriteHeader(http.StatusOK)
}
```

## Node.js/TypeScript Example

```typescript
import { createHmac, timingSafeEqual } from 'crypto';

function verifyWebhook(secret: string, timestamp: string, body: Buffer, signature: string): boolean {
  const expected = createHmac('sha256', secret)
    .update(timestamp + '.' + body)
    .digest('hex');
  const expectedSig = `sha256=${expected}`;
  return timingSafeEqual(Buffer.from(expectedSig), Buffer.from(signature));
}
```

## Important

- Always verify signature BEFORE processing the event
- Use raw request body for verification (not re-serialized JSON)
- Use constant-time comparison to prevent timing attacks
- Never log the webhook secret
- Return non-2xx for invalid signatures
