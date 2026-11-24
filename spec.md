---

# Specification: SPIFFE mTLS Echo Lab (`spiffe-echo-lab`)

## 1. Intent & Objective

Build a minimal, local multi-container prototype that demonstrates **zero-secret workload-to-workload mutual TLS (mTLS)** using the SPIFFE standard and SPIRE runtime. The application proves that two services can authenticate and authorize each other strictly by cryptographic **SPIFFE ID** (`spiffe://lab.local/...`) over a Unix Domain Socket (`Workload API`), with zero hardcoded keys, passwords, or static certificates, and automatic in-memory X.509-SVID rotation.

---

## 2. System Architecture & Topology

```text
+-----------------------------------------------------------------------+
| Docker Host (Bridge Network: spiffe-net)                              |
|                                                                       |
|  +-----------------------+          Node API (TCP :8081)              |
|  |     spire-server      |<----------------------------------------+  |
|  | (Trust Domain:        |                                         |  |
|  |  lab.local)           |                                         |  |
|  +-----------------------+                                         |  |
|                                                                    |  |
|  +---------------------------------------------------------------+ |  |
|  | Shared Host / Docker PID Namespace (for Unix Attestation)     | |  |
|  |                                                               | |  |
|  |  +-----------------------+                                    | |  |
|  |  |      spire-agent      |------------------------------------+ |  |
|  |  +-----------+-----------+                                      |  |
|  |              | Exposes Workload API                             |  |
|  |              | (/tmp/spire-agent/public/api.sock)               |  |
|  |     +--------+--------+                                         |  |
|  |     |                 |                                         |  |
|  |     v                 v                                         |  |
|  |  +---------------+  +---------------+                           |  |
|  |  | order-client  |  | ledger-server |                           |  |
|  |  | (Go workload) |  | (Go workload) |                           |  |
|  |  +-------+-------+  +-------+-------+                           |  |
|  |          |                  ^                                   |  |
|  |          +---- mTLS :8443 --+                                   |  |
|  |     (X.509-SVID verified on both sides)                         |  |
|  +---------------------------------------------------------------+ |
+-----------------------------------------------------------------------+

```

### Trust Domain & Identities

* **Trust Domain:** `lab.local`
* **Agent SPIFFE ID:** `spiffe://lab.local/spire/agent/join_token/<token>`
* **Server Workload SPIFFE ID:** `spiffe://lab.local/ns/finance/sa/ledger-server`
* **Authorized Client SPIFFE ID:** `spiffe://lab.local/ns/orders/sa/order-client`
* **Unauthorized Rogue Client SPIFFE ID:** `spiffe://lab.local/ns/external/sa/rogue-client`

---

## 3. Functional Requirements

### FR-1: Zero-Secret Identity Bootstrap

* Neither `ledger-server` nor `order-client` may read `.pem`, `.crt`, or `.key` files from disk, nor accept TLS credentials via environment variables.
* Both workloads must connect exclusively to `unix:///tmp/spire-agent/public/api.sock` via `workloadapi.NewX509Source`.
* `spire-agent` must attest calling workloads using the **Unix Workload Attestor** (`uid` selector) or **Docker Workload Attestor** (`docker:label` selector).

### FR-2: Mutual TLS & SPIFFE ID Authorization (`ledger-server`)

* `ledger-server` must expose an HTTPS endpoint `POST /api/v1/entries` on port `8443` configured with `tlsconfig.MTLSServerConfig(source, source, tlsconfig.AuthorizeID(...))`.
* **Authorization Rule:** Only callers presenting an X.509-SVID matching `spiffe://lab.local/ns/orders/sa/order-client` are permitted to complete the TLS handshake.
* Any workload in the same trust domain with a different SPIFFE ID (e.g., `rogue-client`) must be rejected at the TLS handshake layer (`tls: bad certificate` / authorization failure) before any HTTP payload is processed.

### FR-3: Authenticated Client & Server Verification (`order-client`)

* `order-client` must call `https://ledger-server:8443/api/v1/entries` every **5 seconds** using `tlsconfig.MTLSClientConfig(source, source, tlsconfig.AuthorizeID(...))`.
* `order-client` must explicitly verify that the server's X.509-SVID matches `spiffe://lab.local/ns/finance/sa/ledger-server` (preventing confused-deputy or MITM connections from any other service in `lab.local`).
* On each request, log:
* Local X.509-SVID Serial Number and Expiration timestamp (`NotAfter`)
* Verified Peer SPIFFE ID
* HTTP Response Status and latency



### FR-4: Short-Lived SVID & Automatic Rotation Proof

* Configure `spire-server` with an intentionally short default SVID TTL (`default_x509_svid_ttl = "120s"`) so rotation occurs within 1 minute of startup.
* Both services must rotate their active leaf certificates automatically in memory without dropping requests, restarting processes, or throwing TLS errors.

---

## 4. Technical Stack & Directory Layout

* **Language:** Go `1.23+`
* **SDK:** `[github.com/spiffe/go-spiffe/v2](https://github.com/spiffe/go-spiffe/v2)`
* **Runtime:** `ghcr.io/spiffe/spire-server:1.11.0`, `ghcr.io/spiffe/spire-agent:1.11.0`
* **Orchestration:** Docker Compose

```text
spiffe-echo-lab/
├── docker-compose.yml
├── Makefile
├── conf/
│   ├── server/
│   │   └── server.conf
│   └── agent/
│       └── agent.conf
├── scripts/
│   └── bootstrap.sh          # Generates join token & registers workload entries
├── go.mod
└── cmd/
    ├── ledger-server/
    │   └── main.go
    └── order-client/
        └── main.go

```

---

## 5. Core Implementation Contracts

### 5.1 `ledger-server` (`cmd/ledger-server/main.go`)

* **Environment Variables:**
* `SPIFFE_ENDPOINT_SOCKET` (default: `unix:///tmp/spire-agent/public/api.sock`)
* `ALLOWED_CLIENT_ID` (default: `spiffe://lab.local/ns/orders/sa/order-client`)
* `LISTEN_ADDR` (default: `:8443`)


* **Behavior:**
1. Initialize `ctx, cancel := context.WithCancel(context.Background())`.
2. Create `source, err := workloadapi.NewX509Source(ctx)` (blocks until SPIRE Agent attests the workload and delivers the first SVID).
3. Log initial SVID metadata (`svid.ID.String()`, `svid.Certificates[0].SerialNumber`, `svid.Certificates[0].NotAfter`).
4. Build `tlsConfig := tlsconfig.MTLSServerConfig(source, source, tlsconfig.AuthorizeID(spiffeid.RequireFromString(allowedClientID)))`.
5. Register HTTP handler `/api/v1/entries` returning JSON:
```json
{
  "status": "accepted",
  "server_spiffe_id": "spiffe://lab.local/ns/finance/sa/ledger-server",
  "client_spiffe_id": "<extracted from r.TLS.PeerCertificates[0]>",
  "svid_serial": "<hex serial>"
}

```


6. Serve HTTPS and handle `SIGINT`/`SIGTERM` gracefully.



### 5.2 `order-client` (`cmd/order-client/main.go`)

* **Environment Variables:**
* `SPIFFE_ENDPOINT_SOCKET` (default: `unix:///tmp/spire-agent/public/api.sock`)
* `EXPECTED_SERVER_ID` (default: `spiffe://lab.local/ns/finance/sa/ledger-server`)
* `TARGET_URL` (default: `https://ledger-server:8443/api/v1/entries`)


* **Behavior:**
1. Initialize `workloadapi.NewX509Source(ctx)`.
2. Build `tlsConfig := tlsconfig.MTLSClientConfig(source, source, tlsconfig.AuthorizeID(spiffeid.RequireFromString(expectedServerID)))`.
3. Construct `http.Client` with `http.Transport{TLSClientConfig: tlsConfig}`.
4. Every 5s, send a POST payload `{"amount": 100, "currency": "EUR"}` and log the active client certificate serial number alongside the server response.



### 5.3 SPIRE Server & Agent Configuration

* **`conf/server/server.conf`:**
* `trust_domain = "lab.local"`
* `default_x509_svid_ttl = "120s"`
* `ca_ttl = "24h"`
* Datastore: `sql` (`sqlite3` at `/opt/spire/data/server/datastore.sqlite3`)
* NodeAttestor: `join_token`
* KeyManager: `disk`


* **`conf/agent/agent.conf`:**
* `trust_domain = "lab.local"`
* `server_address = "spire-server"`
* `server_port = "8081"`
* `socket_path = "/tmp/spire-agent/public/api.sock"`
* `insecure_bootstrap = true` (acceptable for local lab only)
* NodeAttestor: `join_token`
* WorkloadAttestor: `docker` (mounting `/var/run/docker.sock:ro` on `spire-agent` so workloads are attested cleanly via Docker labels without UID collisions)



### 5.4 Registration Entries (`scripts/bootstrap.sh`)

1. Generate a one-time join token via `spire-server token generate -spiffeID spiffe://lab.local/spire/agent/local-dev`.
2. Start `spire-agent` with `-joinToken <token>`.
3. Register three workload entries bound to the parent Agent ID:
* **Ledger Server:**
`-spiffeID spiffe://lab.local/ns/finance/sa/ledger-server -selector docker:label:app:ledger-server`
* **Order Client:**
`-spiffeID spiffe://lab.local/ns/orders/sa/order-client -selector docker:label:app:order-client`
* **Rogue Client (Negative Test):**
`-spiffeID spiffe://lab.local/ns/external/sa/rogue-client -selector docker:label:app:rogue-client`



---

## 6. Verification & Acceptance Criteria

| Test Case | Action | Expected Result |
| --- | --- | --- |
| **TC-1: Zero-Secret Inspection** | Run `docker inspect order-client ledger-server` and inspect container filesystems. | Zero `.key` or `.pem` files exist on disk; only the Unix socket `/tmp/spire-agent/public/api.sock` is mounted. |
| **TC-2: Authorized mTLS** | Start stack via `make up` and tail `order-client` logs. | `200 OK` responses every 5s; logs confirm mutual verification of both SPIFFE IDs. |
| **TC-3: SVID Rotation** | Observe `order-client` logs continuously for 90 seconds. | At ~60s (50% of the 120s TTL), the logged `svid_serial` changes automatically with zero failed requests or connection resets. |
| **TC-4: Unauthorized Identity Rejection** | Run `docker compose run --rm -l app=rogue-client order-client`. | `rogue-client` receives a valid SVID from SPIRE, but `ledger-server` immediately rejects the TLS handshake with `remote error: tls: bad certificate`. |
