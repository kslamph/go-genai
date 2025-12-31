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
	"github.com/sunbankio/omniproxy/internal/auth"
	"github.com/sunbankio/omniproxy/internal/config"
	"github.com/sunbankio/omniproxy/internal/manager"
	"github.com/sunbankio/omniproxy/pkg/utils"
)

func main() {
	defaultConfigPath := config.GetDefaultConfigPath()
	configPath := flag.String("config", defaultConfigPath, "Path to the configuration file")
	port := flag.Int("port", 8143, "Port to listen on")
	logLevel := flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	flag.Parse()

	// 1. Initialize Logger
	utils.InitLogger(*logLevel)
	defer utils.CloseLogger()
	utils.L().Info("Starting OmniProxy...")

	// 2. Load Config
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		utils.L().Fatalf("Failed to load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 3. Initialize Auth Manager
	authMgr := auth.NewManager()

	// 4. Initialize Provider Service
	ps, err := manager.NewProviderServiceManager(ctx, cfg)
	if err != nil {
		utils.L().Fatalf("Failed to initialize provider service: %v", err)
	}

	// Start cleanup routine for client pools
	go manager.StartCleanupRoutine(ctx, ps.GetRegistry())

	// 5. Initialize Server
	server := api.NewServer(ps, authMgr)

	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: server,
	}

	// 6. Graceful Shutdown
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
