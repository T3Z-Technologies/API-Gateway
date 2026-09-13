# Studio Frontend API

The Cloudflare Pages frontend calls `/apis/studio`. All business logic and Firebase access run in this Go gateway. Existing `/apis/auth`, `/apis/admin`, `/apis/webhooks`, and Google Sheets integrations remain available. The Studio API uses profile roles, not the legacy admin custom claim.

## Setup

1. Supply the variables in the root `.env.example` through your container, shell, or service manager. Go does not load `.env` automatically. Preserve the existing gateway database and RSA key volumes.
2. Set `GOOGLE_APPLICATION_CREDENTIALS` to a server-only service-account JSON file for the existing Firebase project, plus `T3Z_FIREBASE_PROJECT_ID` and `T3Z_FIREBASE_API_KEY`. Use least-privilege Firebase Authentication/Firestore IAM access. Do not reuse any previously exposed service-account key without rotating it.
3. Set `T3Z_FRONTEND_ORIGINS` to exact trusted origins. In production use same-site domains, e.g. `https://studio.example.com` and API `https://api.example.com`, with secure, SameSite=Lax cookies. `pages.dev` plus an unrelated API domain requires SameSite=None, Secure, and browser support for third-party cookies; a custom same-site domain is recommended.
4. For HTTP localhost only, set `T3Z_COOKIE_SECURE=false`. Use the same hostname for frontend and API. Session lifetime defaults to 24 hours and can be 1-336 hours. Sessions are revocation-checked; the browser never receives Firebase ID/refresh tokens. Expiry requires signing in again.
5. Keep the Go port private behind Caddy. Configure `T3Z_TRUSTED_PROXY_CIDRS` for the actual proxy network and have the proxy replace client-supplied forwarding headers. Without it, rate limits use the direct TCP peer address. Do not trust all addresses.
6. Run `go test ./...`, `go build -o bin/api-gateway ./cmd/server`, then start that binary. If Firebase is not configured, legacy gateway routes remain available while Studio routes return 503.

Go uses the existing SQLite database for persistent rate limits; dashboard records remain in the existing Firestore database. No PostgreSQL migration is performed. The Go Firebase SDK is backend-only. Use a matching Linux build/container for the actual server architecture.

## Browser Contract

Set the frontend's `NEXT_PUBLIC_API_BASE_URL` to `https://api.example.com/apis/studio` before building. Requests use `credentials: include`; JSON mutations include `X-CSRF-Token: 1` and an exact allowed `Origin`. The header forces a preflight, and the server validates the origin independently of CORS. All Studio responses are `Cache-Control: no-store`; never cache these endpoints at Caddy or Cloudflare. Do not include a service secret or trusted tenant ID in browser code.

Errors use `{ "error": "message" }` with 400/401/403/404/409/413/415/429/5xx status codes. JSON request sizes and list sizes are bounded. 401 clears frontend authentication; 409 means refetch/review before retrying a versioned operation. Paused clients can access support/billing but cannot execute modules or activate agents.

| Method | Path (relative to /apis/studio) | Contract |
| --- | --- | --- |
| POST | `/auth/login` | `{email,password}` -> HttpOnly cookie and `{user:{uid,email},profile}`; provisioned users only |
| GET | `/auth/session` | Current authenticated user/profile |
| POST | `/auth/logout` | Clears the current browser's cookie |
| GET | `/users` | Admin; `{items,nextCursor}`, 200 records/page, optional `?cursor=` |
| POST | `/users` | Admin; `{email,password,displayName}` -> `{ok,uid}`; role always client; minimum 8-character initial password |
| GET/PATCH | `/users/{uid}` | Owner/admin; patch is field-allowlisted; only admins change billing/modules/gatewayClientId; role and email are immutable here |
| DELETE | `/users/{uid}` | Admin; deletes client login/profile, prevents access; historical subcollections are retained for deliberate retention cleanup |
| GET/POST | `/users/{uid}/agents` | Owner/admin read, admin create |
| PATCH/DELETE | `/users/{uid}/agents/{id}` | Admin edit/delete; client may change only status |
| GET | `/users/{uid}/call-logs` | Latest 100, owner/admin |
| GET/POST | `/users/{uid}/requests` | Latest 100; client creates `{title,description}` |
| GET | `/requests` | Admin; latest 500 across clients |
| PATCH | `/users/{uid}/requests/{id}` | Admin `{response?,status?,expectedRevision}`; conflict-safe |
| POST | `/users/{uid}/requests/read` | Owner `{items:[{id,revision}]}`; stale receipts do not clear newer updates |
| GET | `/users/{uid}/tickets` | Latest 100, owner/admin |
| GET | `/tickets` | Admin; latest 500 across clients |
| POST | `/tickets` | Commands below |
| GET | `/users/{uid}/tickets/{id}/messages` | Up to 100 messages in chronological order |
| POST | `/users/{uid}/tickets/read` | Owner/admin `{items:[{id,version}]}`; each role acknowledges its own unread flag |
| GET/POST | `/announcements` | Signed-in read latest 100, admin `{title,body}` create |
| DELETE | `/announcements/{id}` | Admin only |
| POST | `/modules/data` | `{endpoint,payload?,ownerUid?,preview?}`; only admin may preview unsaved endpoints or specify another owner |
| POST | `/billing/create-order` | Client; `{packId}` or `{kind,quantity}`; price/owner determined server-side |
| POST | `/billing/razorpay-webhook` | Server webhook; HMAC and idempotent crediting, no browser cookie |
| POST | `/billing/deduct` | Server webhook; HMAC and stable eventId required |
| POST | `/demo` | Public `{name,email,company,workflow,consent,website?}`; exact origin/CSRF header and persistent limits |

Ticket creation: `{action:"create",subject,message,category}`. Clients are limited to five new tickets per ten minutes and 250 lifetime tickets. Message: `{action:"message",ticketId,expectedVersion,message,status?,ownerUid?}`. Clients may send 30 messages/ten minutes; conversations cap at 100 messages. Admin status-only update: `{action:"status",ticketId,ownerUid,expectedVersion,status}`. Every successful update advances `version`; stale commands return 409. A client reply reopens a resolved ticket.

## Module Upstreams

Use `/apis/webhooks/{workflow_id}` in a dashboard module to call an existing gateway workflow. The Studio handler derives the profile UID, optionally maps `gatewayClientId`, issues a workflow-scoped backend JWT, and calls the existing gateway internally. Both client and workflow must be active in SQLite. Set Gateway client ID in the admin dashboard's settings when existing gateway client IDs differ from Firebase UIDs. The mapping does not provision a Windmill client/workflow; use the existing gateway provisioning tools for that.

For other HTTPS module APIs, set exact `T3Z_MODULE_ORIGINS` and a server-only `T3Z_MODULE_API_TOKEN`. These are trusted upstreams only. The gateway sends `Authorization: Bearer <server token>`, `X-T3Z-User-Id`, and `X-T3Z-Client-Id`; the upstream must authenticate that token and derive tenancy from these trusted headers, never a caller-supplied payload UID. No browser cookie/Firebase token is forwarded. Redirects are rejected, JSON responses cap at 1 MiB, and requests time out. Old direct Firebase-token n8n endpoints must adopt this server contract or be replaced by gateway workflow paths before cutover.

## Payments and Demo Delivery

Configure `RAZORPAY_KEY_ID` and `RAZORPAY_KEY_SECRET` on the gateway. The frontend uses the public key ID only to enable payment controls. Credit pack prices remain 150 minutes/INR 750, 350/INR 1575, 1000/INR 4000; voice packs 100/300/600 at INR 5/minute and action packs 500/1000/5000 at INR 2/action. Subscription checkout reads `monthlyPrice` from the profile, not the browser.

Create a Razorpay webhook for **order.paid** at `/apis/studio/billing/razorpay-webhook` and set `RAZORPAY_WEBHOOK_SECRET`. HMAC is SHA-256 of the exact raw body in `X-Razorpay-Signature`. Only orders recorded by this gateway are credited; verify amount/currency; repeat events do not credit twice. Voice/actions increment addOn after a verified payment. A subscription payment records `lastSubscriptionPaymentAt`/order ID; recurring autopay provisioning, monthly usage reset, and external agent resume/provisioning remain owned by your billing/workflow runtime, not a browser callback. Disable the old n8n top-up-crediting handler for new orders to prevent double crediting, while explicitly handling any outstanding legacy orders during cutover.

Usage deductions use `/apis/studio/billing/deduct`, `BILLING_WEBHOOK_SECRET`, and `X-Webhook-Signature` (hex HMAC-SHA256 of exact raw body). Send `{eventId,uid,durationSeconds}` or `{eventId,uid,minutes}`, plus optional cost/callerNumber/summary/outcome/recordingUrl. Use a stable call/job ID for `eventId`; replay returns success without another debit. Negative/inconsistent/oversized usage is rejected. Balance deduction, call-log record, deduplication marker, and active-agent status updates are atomic. Updating the agent document does not itself terminate an external telephony session; that runtime must enforce depletion too.

Demo enquiries go to optional HTTPS `DEMO_WEBHOOK_URL` with optional `DEMO_WEBHOOK_SECRET` bearer auth, otherwise Firestore `demoRequests`. Success means accepted delivery or committed persistence. Configure your team notification/retention process; there is no demo inbox UI. Frontend `NEXT_PUBLIC_DEMO_BOOKING_URL` can replace the form with a scheduler.

## Firestore Rules and Rollout

1. Deploy this backend's indexes to the existing Firebase project: `npx firebase-tools deploy --only firestore:indexes --project YOUR_PROJECT`.
2. Verify gateway credentials, cookie/CORS settings, profile permissions, module mappings, and payment webhooks in staging. Configure frontend build variables and deploy `out` to Pages.
3. At coordinated cutover, deploy this backend's deny-all rules: `npx firebase-tools deploy --only firestore:rules --project YOUR_PROJECT`. Existing direct-browser Firestore access will stop immediately; reload old frontend sessions.
4. Do not delete or open the rules. Firestore endpoints remain reachable even without its browser SDK. Admin SDK bypasses rules using IAM, so the Go authorization tests are essential. Keep service-account files outside the served/static trees and out of Git.
5. The frontend's previous Firebase files were removed from that repository on 2026-09-13. Their rollback copies are in `docs/legacy-frontend-firebase/`: `firestore.rules.legacy`, `firestore.indexes.json.legacy`, `firebase.json.legacy`, and `.firebaserc.legacy`. The original contents are retained as historical configuration, including references to the old browser/Next.js flow. The `.legacy` names are not discovered as default Firebase CLI configuration and are not referenced by the active root `firebase.json`. Restoring a pre-migration frontend requires a deliberate reviewed rules rollback, not blindly deploying these files. Local file removal does not change live Firebase rules, accounts, or data.

## Verification

`go test ./...` covers existing gateway tests and Studio authorization, CSRF/CORS, fields, version conflicts, rates, HMACs, idempotency, module origin restrictions, and demo failure behavior. Tests with fake payment/upstream transports never create live payments or leads.

For real storage/auth integration, start local Firebase Auth (9099) and Firestore (8989) emulators from the backend repository root using its `firebase.json` and project `demo-t3z-pages`; this also keeps generated emulator logs out of the frontend. Set `FIREBASE_AUTH_EMULATOR_HOST=127.0.0.1:9099` and `FIRESTORE_EMULATOR_HOST=127.0.0.1:8989`, then run `go test -run TestFirebaseEmulatorIntegration -count=1 -v ./internal/studio`. It creates 400 test clients and an admin, validates real session cookies/transactions/pagination, and confirms direct Firestore reads fail. The test refuses nonlocal emulator addresses. Never set emulator variables in production.

To add the real browser flow, first build the frontend with `NEXT_PUBLIC_API_BASE_URL=http://127.0.0.1:8789/apis/studio` and preview Pages on `http://127.0.0.1:3000`. Set `STUDIO_BROWSER_TEST_URL=http://127.0.0.1:3000` and `STUDIO_BROWSER_TEST_SCRIPT` to the frontend's absolute `tests/api/gateway-browser.mjs` path, then run that same Go test. The test starts the real gateway handlers on 8789 and runs Playwright. Screenshots go to the OS temporary `t3z-pages-integration` directory. Remove browser-test variables before ordinary unit runs.

These are correctness tests with 400 account records, not a 400-concurrent-user load test. Auth revocation checks, Firestore reads, upstream jobs, connection pools, and polling rates all consume backend capacity. Persist the SQLite rate-limit volume; multiple replicas need shared limits at the edge or in a shared store. Add monitoring and capacity testing before making a production concurrency commitment.