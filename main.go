package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"github.com/drengskapr/external-dns-webhook-ngcloud/internal/ngcloud"
	"github.com/drengskapr/external-dns-webhook-ngcloud/internal/webhook"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	cfg, err := loadConfig()
	if err != nil {
		klog.Fatalf("config error: %v", err)
	}

	client, err := ngcloud.New(cfg.ngcloud)
	if err != nil {
		klog.Fatalf("init ngcloud client: %v", err)
	}

	handler := webhook.NewHandler(client, cfg.zoneMap, cfg.domainFilter)
	server := webhook.NewServer(handler, cfg.serverPort)

	klog.InfoS("starting webhook server", "port", cfg.serverPort)
	if err := server.Start(); err != nil {
		klog.Fatalf("server: %v", err)
	}
}

type config struct {
	ngcloud      ngcloud.Config
	zoneMap      map[string]string
	domainFilter []string
	serverPort   string
}

func loadConfig() (config, error) {
	token := os.Getenv("NGCLOUD_TOKEN")
	if token == "" {
		return config{}, fmt.Errorf("NGCLOUD_TOKEN is required")
	}

	zoneMap, err := parseZoneMap(os.Getenv("NGCLOUD_ZONE_MAP"))
	if err != nil {
		return config{}, fmt.Errorf("NGCLOUD_ZONE_MAP: %w", err)
	}
	if len(zoneMap) == 0 {
		return config{}, fmt.Errorf("NGCLOUD_ZONE_MAP is required (e.g. example.com=<uuid>)")
	}

	serviceID := envInt("NGCLOUD_SERVICE_ID", 111)
	opCreate := envInt("NGCLOUD_OP_CREATE", 45)
	opDelete := envInt("NGCLOUD_OP_DELETE", 46)
	defaultTTL := int64(envInt("NGCLOUD_DEFAULT_TTL", 120))
	pollMaxAttempts := envInt("POLL_MAX_ATTEMPTS", 60)
	pollInterval := envDuration("POLL_INTERVAL", 5*time.Second)

	var domainFilter []string
	if df := os.Getenv("DOMAIN_FILTER"); df != "" {
		domainFilter = strings.Split(df, ",")
	}

	return config{
		ngcloud: ngcloud.Config{
			BaseURL:         envString("NGCLOUD_BASE_URL", "https://deck-api.ngcloud.ru/api/v1/index.cfm"),
			Token:           token,
			ServiceID:       serviceID,
			OpCreate:        opCreate,
			OpDelete:        opDelete,
			DefaultTTL:      defaultTTL,
			PollMaxAttempts: pollMaxAttempts,
			PollInterval:    pollInterval,
		},
		zoneMap:      zoneMap,
		domainFilter: domainFilter,
		serverPort:   envString("SERVER_PORT", "8888"),
	}, nil
}

// parseZoneMap parses "zone1=uuid1,zone2=uuid2" into a map.
func parseZoneMap(raw string) (map[string]string, error) {
	if raw == "" {
		return nil, nil
	}
	m := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("invalid pair %q, expected zone=uuid", pair)
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return m, nil
}

func envString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
		klog.Warningf("invalid value for %s: %q, using default %d", key, v, def)
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
		klog.Warningf("invalid value for %s: %q, using default %s", key, v, def)
	}
	return def
}
