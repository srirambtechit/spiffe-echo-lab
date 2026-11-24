package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

const defaultSocket = "unix:///tmp/spire-agent/public/api.sock"

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	source, err := workloadapi.NewX509Source(ctx, workloadapi.WithClientOptions(workloadapi.WithAddr(env("SPIFFE_ENDPOINT_SOCKET", defaultSocket))))
	if err != nil {
		log.Fatalf("create X509 source: %v", err)
	}
	defer source.Close()
	expected, err := spiffeid.FromString(env("EXPECTED_SERVER_ID", "spiffe://lab.local/ns/finance/sa/ledger-server"))
	if err != nil {
		log.Fatalf("invalid EXPECTED_SERVER_ID: %v", err)
	}
	tlsConfig := tlsconfig.MTLSClientConfig(source, source, tlsconfig.AuthorizeID(expected))
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig, DisableKeepAlives: true}, Timeout: 10 * time.Second}
	target := env("TARGET_URL", "https://ledger-server:8443/api/v1/entries")
	post := func() {
		svid, err := source.GetX509SVID()
		if err != nil {
			log.Printf("get active SVID: %v", err)
			return
		}
		started := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewBufferString(`{"amount":100,"currency":"EUR"}`))
		if err != nil {
			log.Printf("create request: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("request failed serial=%X latency=%s: %v", svid.Certificates[0].SerialNumber, time.Since(started), err)
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		peer := "unknown"
		if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
			if id, e := x509svid.IDFromCert(resp.TLS.PeerCertificates[0]); e == nil {
				peer = id.String()
			}
		}
		log.Printf("serial=%X expires=%s peer=%s status=%s latency=%s response=%s", svid.Certificates[0].SerialNumber, svid.Certificates[0].NotAfter.Format(time.RFC3339), peer, resp.Status, time.Since(started), string(body))
	}
	post()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			post()
		}
	}
}
