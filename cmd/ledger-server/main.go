package main

import (
	"context"
	"encoding/json"
	"errors"
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
	svid, err := source.GetX509SVID()
	if err != nil {
		log.Fatalf("get initial SVID: %v", err)
	}
	log.Printf("serving as %s serial=%X expires=%s", svid.ID, svid.Certificates[0].SerialNumber, svid.Certificates[0].NotAfter.Format(time.RFC3339))

	allowed, err := spiffeid.FromString(env("ALLOWED_CLIENT_ID", "spiffe://lab.local/ns/orders/sa/order-client"))
	if err != nil {
		log.Fatalf("invalid ALLOWED_CLIENT_ID: %v", err)
	}
	tlsConfig := tlsconfig.MTLSServerConfig(source, source, tlsconfig.AuthorizeID(allowed))
	serverID := svid.ID.String()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/entries", func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "client certificate required", http.StatusUnauthorized)
			return
		}
		clientID, err := x509svid.IDFromCert(r.TLS.PeerCertificates[0])
		if err != nil {
			http.Error(w, "invalid client identity", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted", "server_spiffe_id": serverID, "client_spiffe_id": clientID.String(), "svid_serial": r.TLS.PeerCertificates[0].SerialNumber.Text(16)})
	})
	srv := &http.Server{Addr: env("LISTEN_ADDR", ":8443"), Handler: mux, TLSConfig: tlsConfig, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Printf("listening on %s", srv.Addr)
	if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("HTTPS server: %v", err)
	}
}
