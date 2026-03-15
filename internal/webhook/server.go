package webhook

import (
	"fmt"
	"net/http"
)

const contentType = "application/external.dns.webhook+json;version=1"

// Server is the external-dns webhook HTTP server.
type Server struct {
	handler http.Handler
	port    string
}

func NewServer(h *Handler, port string) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.Negotiate)
	mux.HandleFunc("GET /healthz", h.Healthz)
	mux.HandleFunc("GET /records", h.GetRecords)
	mux.HandleFunc("POST /records", h.ApplyChanges)
	mux.HandleFunc("POST /adjustendpoints", h.AdjustEndpoints)

	return &Server{
		handler: mux,
		port:    port,
	}
}

func (s *Server) Start() error {
	addr := fmt.Sprintf(":%s", s.port)
	return http.ListenAndServe(addr, s.handler)
}

func (s *Server) Handler() http.Handler {
	return s.handler
}
