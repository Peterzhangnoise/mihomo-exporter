package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type MemoryMessage struct {
	Inuse   int64 `json:"inuse"`
	OSLimit int64 `json:"oslimit"`
}

type ConnectionsResponse struct {
	Connections []interface{} `json:"connections"`
}

type VersionResponse struct {
	Version string `json:"version"`
}

type TrafficMessage struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

type ProxyDelayHistory struct {
	Time  string `json:"time"`
	Delay int64  `json:"delay"`
}

type ProxyItem struct {
	Name    string              `json:"name"`
	Type    string              `json:"type"`
	Alive   bool                `json:"alive"`
	History []ProxyDelayHistory `json:"history"`
	Now     string              `json:"now"`
	All     []string            `json:"all"`
}

type ProxiesResponse struct {
	Proxies map[string]ProxyItem `json:"proxies"`
}

var (
	mihomoProxyDelay = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mihomo_proxy_delay_ms",
			Help: "Mihomo proxy delay",
		},
		[]string{"proxy"},
	)

	mihomoProxyAlive = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mihomo_proxy_alive",
			Help: "Whether proxy is alive",
		},
		[]string{"proxy"},
	)

	mihomoProxySelected = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mihomo_proxy_selected",
			Help: "Current selected proxy in selector",
		},
		[]string{"group", "proxy"},
	)

	mihomoMemoryUsageBytes = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "mihomo_memory_usage_bytes",
			Help: "Mihomo memory usage",
		},
	)

	mihomoConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "mihomo_active_connections",
			Help: "Current active connections",
		},
	)

	mihomoVersionInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mihomo_version_info",
			Help: "Mihomo version info",
		},
		[]string{"version"},
	)

	mihomoUploadBytesTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "mihomo_upload_bytes_total",
			Help: "Total uploaded bytes from Mihomo",
		},
	)

	mihomoDownloadBytesTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "mihomo_download_bytes_total",
			Help: "Total downloaded bytes from Mihomo",
		},
	)

	mihomoUploadBps = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "mihomo_upload_bps",
			Help: "Current upload speed in bytes/sec",
		},
	)

	mihomoDownloadBps = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "mihomo_download_bps",
			Help: "Current download speed in bytes/sec",
		},
	)

	mihomoExporterUp = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "mihomo_exporter_up",
			Help: "Whether exporter can connect to Mihomo",
		},
	)

	mihomoProxySwitchTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mihomo_proxy_switch_total",
			Help: "Proxy switch count",
		},
		[]string{"group", "proxy"},
	)
)

func init() {
	prometheus.MustRegister(
		mihomoMemoryUsageBytes,
		mihomoConnections,
		mihomoVersionInfo,
		mihomoUploadBytesTotal,
		mihomoDownloadBytesTotal,
		mihomoUploadBps,
		mihomoDownloadBps,
		mihomoExporterUp,
		mihomoProxyDelay,
		mihomoProxyAlive,
		mihomoProxySelected,
		mihomoProxySwitchTotal,
	)
}

func getenv(key, fallback string) string {
	v := os.Getenv(key)

	if v == "" {
		return fallback
	}

	return v
}

func main() {

	// CLI flags
	flagHost := flag.String("host", "", "mihomo host (e.g. 127.0.0.1:9090)")
	flagSecret := flag.String("secret", "", "mihomo secret")
	flagAddr := flag.String("listen", "", "listen addr")

	flag.Parse()

	// ENV fallback + default
	host := getenv("MIHOMO_HOST", "127.0.0.1:9090")
	secret := getenv("MIHOMO_SECRET", "")
	addr := getenv("LISTEN_ADDR", ":9988")

	// CLI override ENV
	if *flagHost != "" {
		host = *flagHost
	}
	if *flagSecret != "" {
		secret = *flagSecret
	}
	if *flagAddr != "" {
		addr = *flagAddr
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runTrafficCollector(ctx, host, secret)
	go runMemoryCollector(ctx, host, secret)
	go runConnectionsCollector(ctx, host, secret)
	go runVersionCollector(ctx, host, secret)
	go runProxiesCollector(ctx, host, secret)

	http.Handle("/metrics", promhttp.Handler())

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")

		fmt.Fprint(w, `
		<h2>Mihomo Exporter</h2>
		<p><a href="/metrics">metrics</a></p>
		<p><a href="/healthz">healthz</a></p>
		`)
	})

	server := &http.Server{
		Addr:              addr,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("Prometheus exporter listening on %s", addr)

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	<-sigCh

	log.Println("Shutting down...")

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	_ = server.Shutdown(shutdownCtx)
}
func runProxiesCollector(ctx context.Context, host, secret string) {
	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	currentSelections := map[string]string{}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		url := fmt.Sprintf("http://%s/proxies", host)

		req, err := http.NewRequest("GET", url, nil)

		if err != nil {
			time.Sleep(10 * time.Second)
			continue
		}

		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}

		resp, err := client.Do(req)

		if err != nil {
			log.Printf("Proxy request failed: %v", err)
			time.Sleep(10 * time.Second)
			continue
		}

		var data ProxiesResponse

		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			resp.Body.Close()
			time.Sleep(10 * time.Second)
			continue
		}

		resp.Body.Close()

		// 清理旧 selector 状态
		mihomoProxySelected.Reset()

		for name, proxy := range data.Proxies {

			// alive
			if proxy.Alive {
				mihomoProxyAlive.WithLabelValues(name).Set(1)
			} else {
				mihomoProxyAlive.WithLabelValues(name).Set(0)
			}

			// delay
			if len(proxy.History) > 0 {
				last := proxy.History[len(proxy.History)-1]

				if last.Delay > 0 {
					mihomoProxyDelay.WithLabelValues(name).Set(float64(last.Delay))
				}
			}

			// selector/group
			if proxy.Type == "Selector" || proxy.Type == "URLTest" || proxy.Type == "Fallback" {
				if proxy.Now != "" {
					mihomoProxySelected.WithLabelValues(name, proxy.Now).Set(1)
				}
			}

			// switch counter
			if currentSelections[name] != proxy.Now {
				currentSelections[name] = proxy.Now

				mihomoProxySwitchTotal.
					WithLabelValues(name, proxy.Now).
					Inc()
			}
		}

		time.Sleep(15 * time.Second)
	}
}

func runMemoryCollector(ctx context.Context, host, secret string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		url := fmt.Sprintf("ws://%s/memory", host)

		headers := http.Header{}

		if secret != "" {
			headers.Set("Authorization", "Bearer "+secret)
		}

		conn, _, err := websocket.DefaultDialer.Dial(url, headers)

		if err != nil {
			log.Printf("Memory WS connect failed: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		for {
			_, msg, err := conn.ReadMessage()

			if err != nil {
				_ = conn.Close()
				break
			}

			var mem MemoryMessage

			if err := json.Unmarshal(msg, &mem); err != nil {
				continue
			}

			mihomoMemoryUsageBytes.Set(float64(mem.Inuse))
		}

		time.Sleep(3 * time.Second)
	}
}

func runConnectionsCollector(ctx context.Context, host, secret string) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		url := fmt.Sprintf("http://%s/connections", host)

		req, err := http.NewRequest("GET", url, nil)

		if err != nil {
			time.Sleep(10 * time.Second)
			continue
		}

		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}

		resp, err := client.Do(req)

		if err != nil {
			log.Printf("Connections request failed: %v", err)
			time.Sleep(10 * time.Second)
			continue
		}

		var data ConnectionsResponse

		if err := json.NewDecoder(resp.Body).Decode(&data); err == nil {
			mihomoConnections.Set(float64(len(data.Connections)))
		}

		resp.Body.Close()

		time.Sleep(5 * time.Second)
	}
}

func runVersionCollector(ctx context.Context, host, secret string) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	versionSet := false

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if versionSet {
			time.Sleep(1 * time.Hour)
			continue
		}

		url := fmt.Sprintf("http://%s/version", host)

		req, err := http.NewRequest("GET", url, nil)

		if err != nil {
			time.Sleep(30 * time.Second)
			continue
		}

		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}

		resp, err := client.Do(req)

		if err != nil {
			log.Printf("Version request failed: %v", err)
			time.Sleep(30 * time.Second)
			continue
		}

		var data VersionResponse

		if err := json.NewDecoder(resp.Body).Decode(&data); err == nil {
			mihomoVersionInfo.WithLabelValues(data.Version).Set(1)
			versionSet = true
		}

		resp.Body.Close()
	}
}

func runTrafficCollector(ctx context.Context, host, secret string) {
	var lastUp int64
	var lastDown int64
	var lastTime time.Time

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		url := fmt.Sprintf("ws://%s/traffic", host)

		headers := http.Header{}

		if secret != "" {
			headers.Set("Authorization", "Bearer "+secret)
		}

		log.Printf("Connecting to %s", url)

		conn, _, err := websocket.DefaultDialer.Dial(url, headers)

		if err != nil {
			log.Printf("WebSocket connect failed: %v", err)
			mihomoExporterUp.Set(0)

			time.Sleep(5 * time.Second)
			continue
		}

		mihomoExporterUp.Set(1)

		log.Println("Connected to Mihomo")

		for {
			select {
			case <-ctx.Done():
				_ = conn.Close()
				return
			default:
			}

			_, msg, err := conn.ReadMessage()

			if err != nil {
				log.Printf("Read error: %v", err)
				mihomoExporterUp.Set(0)

				_ = conn.Close()
				break
			}

			var traffic TrafficMessage

			if err := json.Unmarshal(msg, &traffic); err != nil {
				log.Printf("JSON parse error: %v", err)
				continue
			}

			now := time.Now()

			if !lastTime.IsZero() {
				duration := now.Sub(lastTime).Seconds()

				if duration > 0 {
					upDiff := traffic.Up - lastUp
					downDiff := traffic.Down - lastDown

					if upDiff >= 0 {
						mihomoUploadBytesTotal.Add(float64(upDiff))
						mihomoUploadBps.Set(float64(upDiff) / duration)
					}

					if downDiff >= 0 {
						mihomoDownloadBytesTotal.Add(float64(downDiff))
						mihomoDownloadBps.Set(float64(downDiff) / duration)
					}
				}
			}

			lastUp = traffic.Up
			lastDown = traffic.Down
			lastTime = now
		}

		time.Sleep(3 * time.Second)
	}
}
