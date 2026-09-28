# Overview

## Architecture

```
Merchant Application
        |
        | API Request (X-API-Key)
        v
Payment Gateway API
        |
        +---> authenticate merchant
        +---> validate request
        +---> create transaction
        +---> persist to database
        +---> generate payment_url
        |
        v
Customer Payment Page
        |
        | Customer pays
        v
Payment Status Changes
        |
        +---> Dashboard sees same transaction
        |
        v
Webhook sent to merchant
```

## Three Integration Surfaces

### 1. Merchant API

The REST API merchants call to create payments, check status, cancel, and refund.

- Base URL: `http://localhost:8080` (development)
- Authentication: `X-API-Key` header
- Format: JSON

### 2. Customer Payment Page

The browser-facing payment page customers see after being redirected.

- URL: provided by backend as `payment_url`
- No authentication required
- Displays payment instructions

### 3. Webhook

Server-to-server notifications sent to merchant endpoint when payment events occur.

- Method: `POST`
- URL: merchant-configured endpoint
- Authentication: HMAC-SHA256 signature header

## Key Concepts

- **Transaction ID**: UUID assigned by gateway on payment creation
- **Merchant Order ID**: Your unique order identifier (1-100 chars)
- **Payment URL**: Backend-generated URL for customer payment
- **Idempotency Key**: Client-generated key for safe retries
- **Webhook Secret**: Signing secret for webhook verification
