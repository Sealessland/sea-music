package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/exaring/otelpgx"
	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	moderationv1 "github.com/sealessland/sea-music/internal/gen/moderation/v1"
	"github.com/sealessland/sea-music/internal/moderation"
	"github.com/sealessland/sea-music/internal/moderation/grpcadapter"
	"github.com/sealessland/sea-music/internal/platform/config"
	"github.com/sealessland/sea-music/internal/platform/logging"
	"github.com/sealessland/sea-music/internal/platform/telemetry"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
)

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger, err := logging.New(os.Stdout, cfg.LogLevel, "moderation-agent")
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTelemetry, err := telemetry.Setup(ctx, "sea-music-moderation-agent", cfg.Telemetry.OTLPEndpoint)
	if err != nil {
		return fmt.Errorf("configure telemetry: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
		defer cancel()
		if err := shutdownTelemetry(shutdownCtx); err != nil {
			logger.Error("telemetry shutdown failed", "error", err)
		}
	}()

	connectionConfig, err := pgx.ParseConfig(cfg.Database.URL)
	if err != nil {
		return fmt.Errorf("parse moderation database configuration: %w", err)
	}
	connectionConfig.Tracer = otelpgx.NewTracer()
	database := stdlib.OpenDB(*connectionConfig)
	defer database.Close()
	database.SetMaxOpenConns(cfg.Database.MaxOpen)
	database.SetMaxIdleConns(cfg.Database.MaxIdle)
	database.SetConnMaxLifetime(cfg.Database.ConnectionMaxAge)
	if err := database.PingContext(ctx); err != nil {
		return fmt.Errorf("ping moderation database: %w", err)
	}

	listener, err := net.Listen("tcp", cfg.Moderation.GRPCAddress)
	if err != nil {
		return fmt.Errorf("listen for moderation gRPC: %w", err)
	}
	defer listener.Close()

	store := moderation.NewPostgresStore(database)
	service := moderation.NewService(store)
	registry := prometheus.NewRegistry()
	serverMetrics := grpcprom.NewServerMetrics(grpcprom.WithServerHandlingTimeHistogram())
	registry.MustRegister(serverMetrics)
	grpcOptions := []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.UnaryInterceptor(serverMetrics.UnaryServerInterceptor()),
	}
	if !cfg.Moderation.Insecure {
		transportCredentials, err := moderationServerCredentials(cfg)
		if err != nil {
			return err
		}
		grpcOptions = append(grpcOptions, grpc.Creds(transportCredentials))
	}
	grpcServer := grpc.NewServer(grpcOptions...)
	moderationv1.RegisterModerationServiceServer(grpcServer, grpcadapter.NewServer(service))
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	healthServer.SetServingStatus(moderationv1.ModerationService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	serverMetrics.InitializeMetrics(grpcServer)
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	metricsMux.HandleFunc("/livez", func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusNoContent) })
	metricsMux.HandleFunc("/readyz", func(response http.ResponseWriter, request *http.Request) {
		checkContext, cancel := context.WithTimeout(request.Context(), cfg.HTTP.ReadinessTimeout)
		defer cancel()
		if err := database.PingContext(checkContext); err != nil {
			http.Error(response, "not ready", http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	})
	metricsServer := &http.Server{Addr: cfg.Moderation.MetricsAddress, Handler: metricsMux, ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout}

	hostname, _ := os.Hostname()
	workerID := fmt.Sprintf("%s-%d", hostname, os.Getpid())
	evaluator, err := newEvaluator(ctx, cfg)
	if err != nil {
		return err
	}
	evaluator = moderation.InstrumentEvaluator(evaluator, moderation.NewAgentMetrics(registry))
	runner := moderation.NewRunner(store, evaluator, workerID, cfg.Moderation.LeaseDuration, cfg.Moderation.EvaluationTimeout)
	runnerContext, cancelRunner := context.WithCancel(ctx)
	runnerStopped := make(chan struct{})
	go func() {
		defer close(runnerStopped)
		runModerationLoop(runnerContext, runner, cfg.Moderation.PollInterval, logger)
	}()
	defer func() {
		cancelRunner()
		select {
		case <-runnerStopped:
		case <-time.After(cfg.HTTP.ShutdownTimeout):
			logger.Error("moderation runner shutdown timed out")
		}
	}()

	serveErr := make(chan error, 1)
	go func() { serveErr <- grpcServer.Serve(listener) }()
	metricsErr := make(chan error, 1)
	go func() { metricsErr <- metricsServer.ListenAndServe() }()
	logger.Info("moderation agent started", "grpc_address", cfg.Moderation.GRPCAddress, "metrics_address", cfg.Moderation.MetricsAddress, "worker_id", workerID, "mode", cfg.Moderation.Mode)

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return fmt.Errorf("serve moderation gRPC: %w", err)
		}
		return nil
	case err := <-metricsErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve moderation metrics: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	healthServer.SetServingStatus(moderationv1.ModerationService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancelShutdown()
	if err := metricsServer.Shutdown(shutdownContext); err != nil {
		logger.Error("moderation metrics shutdown failed", "error", err)
	}
	stopped := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(cfg.HTTP.ShutdownTimeout):
		grpcServer.Stop()
	}
	logger.Info("moderation agent stopped", "worker_id", workerID)
	return nil
}

func moderationServerCredentials(cfg config.Config) (credentials.TransportCredentials, error) {
	certificate, err := tls.LoadX509KeyPair(cfg.Moderation.TLSCertFile, cfg.Moderation.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load moderation server certificate: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.Moderation.TLSCAFile)
	if err != nil {
		return nil, fmt.Errorf("read moderation client CA: %w", err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("parse moderation client CA")
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate},
		ClientCAs: clientCAs, ClientAuth: tls.RequireAndVerifyClientCert,
	}), nil
}

func newEvaluator(ctx context.Context, cfg config.Config) (moderation.Evaluator, error) {
	if cfg.Moderation.Provider == "disabled" {
		return moderation.NewManualEscalationEvaluator(), nil
	}
	temperature := float32(0)
	maxTokens := 800
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey: cfg.Moderation.ProviderAPIKey, BaseURL: cfg.Moderation.ProviderBaseURL,
		Model: cfg.Moderation.ProviderModel, Timeout: cfg.Moderation.EvaluationTimeout,
		Temperature: &temperature, MaxCompletionTokens: &maxTokens,
		ResponseFormat: &openai.ChatCompletionResponseFormat{Type: openai.ChatCompletionResponseFormatTypeJSONObject},
	})
	if err != nil {
		return nil, fmt.Errorf("configure Eino moderation model: %w", err)
	}
	evaluator, err := moderation.NewEinoEvaluator(chatModel, cfg.Moderation.Provider, cfg.Moderation.ProviderModel)
	if err != nil {
		return nil, err
	}
	critic, err := moderation.NewEinoCritic(chatModel, cfg.Moderation.Provider, cfg.Moderation.ProviderModel)
	if err != nil {
		return nil, err
	}
	return moderation.NewAgentEvaluator(evaluator, critic, moderation.DecisionPolicy{
		ApproveThreshold: cfg.Moderation.ApproveThreshold,
		RejectThreshold:  cfg.Moderation.RejectThreshold,
	})
}
