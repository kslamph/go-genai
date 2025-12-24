package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sunbankio/omniproxy/internal/api"
	"github.com/sunbankio/omniproxy/internal/config"
	"github.com/sunbankio/omniproxy/internal/manager"
	"github.com/sunbankio/omniproxy/pkg/utils"
)

func main() {
	configPath := flag.String("config", "omniproxy.yaml", "Path to the configuration file")
	port := flag.Int("port", 8143, "Port to listen on")
	logLevel := flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	flag.Parse()

	// 1. Initialize Logger
	utils.InitLogger(*logLevel)
	utils.L().Info("Starting OmniProxy...")

	// 2. Load Config
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		utils.L().Fatalf("Failed to load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 3. Initialize Pool Manager
	pm, err := manager.NewPoolManager(ctx, cfg)
	if err != nil {
		utils.L().Fatalf("Failed to initialize pool manager: %v", err)
	}

	// 4. Initialize Server
	server := api.NewServer(pm)

	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: server,
	}

	// 5. Graceful Shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		<-sigChan

		utils.L().Info("Shutting down gracefully...")
		ctxShut, cancelShut := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShut()

		if err := httpServer.Shutdown(ctxShut); err != nil {
			utils.L().Errorf("Server forced to shutdown: %v", err)
		}
	}()

	utils.L().Infof("OmniProxy listening on :%d", *port)
	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		utils.L().Fatalf("Server failed: %v", err)
	}
}
