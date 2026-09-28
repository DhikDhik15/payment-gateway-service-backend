# Development Environment

## Local Setup

### Backend

```bash
cd pay-gate-backend
go run ./cmd/server
```

Default: `http://localhost:8080`

### Frontend

```bash
cd pay-gate-dashboard
npm run dev
```

Default: `http://localhost:5173`

### Mock Merchant Webhook Receiver

```bash
cd mock-merchant-receiver
MOCK_MERCHANT_WEBHOOK_SECRET=whsec_test_secret go run ./cmd/receiver
```

Default: `http://127.0.0.1:9090`

## Configuration

| Variable | Development | Production |
|----------|-------------|------------|
| `VITE_API_BASE_URL` | `http://localhost:8080` | `https://api.example.com` |
| `FRONTEND_PUBLIC_URL` | `http://localhost:5173` | `https://app.example.com` |
| `PAYMENT_PROVIDER` | `mock` | `midtrans` |
| `PAYMENT_SIMULATOR_ENABLED` | `true` | `false` |
| `WEBHOOK_REQUIRE_HTTPS` | `false` | `true` |

## Mock Payment Flow

In development, the mock payment provider simulates payment processing:

1. Create payment → receive `payment_url`
2. Open `payment_url` in browser
3. Click "Simulate successful payment" or "Simulate failed payment"
4. Transaction status updates
5. Webhook sent to configured endpoint

## Testing Webhooks Locally

Use the Mock Merchant Webhook Receiver:

```bash
# Terminal 1: Start receiver
cd mock-merchant-receiver
MOCK_MERCHANT_WEBHOOK_SECRET=whsec_test_secret go run ./cmd/receiver

# Terminal 2: Configure webhook (note: SSRF blocks localhost in gateway)
# For local testing, use a tunnel or configure webhook URL via database

# Terminal 3: Check received events
curl http://127.0.0.1:9090/events
```

## Important Notes

- Mock Merchant Webhook Receiver is **development/test only**
- Payment Gateway operates normally without the receiver
- Simulator is disabled in production
- Use environment variables for all configuration
