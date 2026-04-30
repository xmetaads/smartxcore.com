package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"
	"github.com/rs/zerolog/log"

	"github.com/worktrack/backend/internal/auth"
	"github.com/worktrack/backend/internal/config"
	"github.com/worktrack/backend/internal/database"
	"github.com/worktrack/backend/internal/email"
	"github.com/worktrack/backend/internal/handlers"
	"github.com/worktrack/backend/internal/logger"
	"github.com/worktrack/backend/internal/middleware"
	"github.com/worktrack/backend/internal/services"
	"github.com/worktrack/backend/internal/sse"
)

const Version = "0.1.0"

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	logger.Init(cfg.Server.LogLevel, cfg.Server.Environment)
	log.Info().Str("version", Version).Str("env", cfg.Server.Environment).Msg("starting WorkTrack backend")

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db, err := database.New(rootCtx, cfg.Database)
	if err != nil {
		log.Fatal().Err(err).Msg("database init failed")
	}
	defer db.Close()

	if cfg.Database.AutoMigrate {
		if err := database.Migrate(rootCtx, db, cfg.Database.MigrationsDir); err != nil {
			log.Fatal().Err(err).Msg("auto-migrate failed")
		}
	}

	machineSvc := services.NewMachineService(db, cfg.Agent.TokenLength)
	commandSvc := services.NewCommandService(db)
	adminSvc := services.NewAdminService(db)
	deploymentSvc := services.NewDeploymentService(db, machineSvc, cfg.Agent.TokenLength)
	aiPackageSvc := services.NewAIPackageService(
		db,
		"/opt/worktrack/ai-uploads",
		"https://smartxcore.com/downloads/ai-client.exe",
	)

	jwtIssuer := auth.NewJWTIssuer(cfg.Auth.JWTSecret, cfg.Auth.AccessTokenTTL)
	authSvc := services.NewAuthService(db, jwtIssuer, cfg.Auth.RefreshTokenTTL)

	mailer := buildMailer(cfg.Email)
	notificationSvc := services.NewNotificationService(mailer, cfg.Email.AlertEmail, cfg.Email.DashboardURL)
	notificationSvc.Start(rootCtx)
	defer notificationSvc.Stop()

	alertSvc := services.NewAlertService(db, notificationSvc)

	deps := appDeps{
		cfg:             cfg,
		db:              db,
		machineSvc:      machineSvc,
		commandSvc:      commandSvc,
		adminSvc:        adminSvc,
		deploymentSvc:   deploymentSvc,
		aiPackageSvc:    aiPackageSvc,
		authSvc:         authSvc,
		notificationSvc: notificationSvc,
		jwtIssuer:       jwtIssuer,
	}
	app := buildApp(deps)

	go runTimeoutWorker(rootCtx, commandSvc)
	go runSessionCleanupWorker(rootCtx, authSvc)
	go runAlertWorker(rootCtx, alertSvc)
	go runOnlineSyncWorker(rootCtx, alertSvc)

	go func() {
		addr := fmt.Sprintf(":%d", cfg.Server.Port)
		log.Info().Str("addr", addr).Msg("http server listening")
		if err := app.Listen(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("server stopped")
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info().Msg("shutdown signal received")

	shutdownCtx, sCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer sCancel()
	if err := app.ShutdownWithContext(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("graceful shutdown error")
	}
	log.Info().Msg("server stopped cleanly")
}

func buildMailer(cfg config.EmailConfig) email.Mailer {
	if cfg.Host == "" || cfg.Username == "" || cfg.Password == "" || cfg.From == "" {
		log.Warn().Msg("SMTP not configured — email notifications disabled")
		return email.NoopMailer{}
	}
	port := cfg.Port
	if port == 0 {
		port = 587
	}
	log.Info().Str("smtp_host", cfg.Host).Int("smtp_port", port).Msg("smtp mailer configured")
	return email.NewSMTPMailer(cfg.Host, port, cfg.Username, cfg.Password, cfg.From)
}

type appDeps struct {
	cfg             *config.Config
	db              *database.DB
	machineSvc      *services.MachineService
	commandSvc      *services.CommandService
	adminSvc        *services.AdminService
	deploymentSvc   *services.DeploymentService
	aiPackageSvc    *services.AIPackageService
	authSvc         *services.AuthService
	notificationSvc *services.NotificationService
	jwtIssuer       *auth.JWTIssuer
}

func buildApp(d appDeps) *fiber.App {
	app := fiber.New(fiber.Config{
		AppName:               "WorkTrack",
		ServerHeader:          "WorkTrack",
		DisableStartupMessage: true,
		BodyLimit:             4 * 1024 * 1024,
		ReadTimeout:           30 * time.Second,
		WriteTimeout:          30 * time.Second,
		IdleTimeout:           120 * time.Second,
		ErrorHandler:          errorHandler,
		// We sit behind nginx on the same VPS (loopback), so c.IP()
		// would otherwise return 127.0.0.1 for every request and the
		// machines table would record "127.0.0.1" as everyone's
		// public_ip — exactly the bug we hit. Telling Fiber to trust
		// X-Forwarded-For from 127.0.0.1 lets it pick up the real
		// client address that nginx already populates.
		ProxyHeader:             "X-Forwarded-For",
		EnableTrustedProxyCheck: true,
		TrustedProxies:          []string{"127.0.0.1", "::1"},
	})

	app.Use(recover.New())
	app.Use(requestid.New())
	app.Use(cors.New(cors.Config{
		AllowOrigins:     joinOrigins(d.cfg.CORS.AllowedOrigins),
		AllowMethods:     "GET,POST,PUT,PATCH,DELETE,OPTIONS",
		AllowHeaders:     "Authorization,Content-Type,X-Agent-Token,X-Request-ID",
		AllowCredentials: true,
	}))

	health := handlers.NewHealthHandler(d.db, Version)
	app.Get("/healthz", health.Live)
	app.Get("/readyz", health.Ready)

	v1 := app.Group("/api/v1")

	// === SSE hub for real-time push to connected agents ===
	// One process-wide instance; injected into the handlers that
	// publish events (AI package activate, command create) and the
	// SSE handler that fans them out to subscribed agents.
	hub := sse.NewHub()

	// === Agent endpoints ===
	agent := v1.Group("/agent")
	agentH := handlers.NewAgentHandler(d.machineSvc, d.commandSvc, d.aiPackageSvc, d.notificationSvc)
	streamH := handlers.NewAgentStreamHandler(hub, d.machineSvc)

	// === Public agent endpoints (no X-Agent-Token required) ===
	deploymentH := handlers.NewDeploymentHandler(d.deploymentSvc, d.notificationSvc)
	registerLimiter := limiter.New(limiter.Config{Max: 10, Expiration: time.Minute})
	enrollLimiter := limiter.New(limiter.Config{Max: 60, Expiration: time.Minute})

	agent.Post("/register", registerLimiter, agentH.Register)
	agent.Post("/enroll", enrollLimiter, deploymentH.Enroll)

	// === Authenticated agent endpoints — middleware applied per-route to
	// avoid Fiber's `Group("", mw)` quirk where empty-prefix sub-groups
	// accidentally apply middleware to the parent group too. ===
	agentAuth := middleware.AgentAuth(d.machineSvc)
	agentLimiter := limiter.New(limiter.Config{
		Max:        d.cfg.Limits.AgentPerMinute,
		Expiration: time.Minute,
	})
	agent.Post("/heartbeat", agentAuth, agentLimiter, agentH.Heartbeat)
	agent.Post("/events", agentAuth, agentLimiter, agentH.SubmitEvents)
	agent.Get("/commands", agentAuth, agentLimiter, agentH.PollCommands)
	agent.Post("/commands/:id/result", agentAuth, agentLimiter, agentH.SubmitResult)
	agent.Post("/ai-launched", agentAuth, agentLimiter, agentH.AILaunched)

	// SSE push channel — agent opens this on startup and keeps it
	// alive. NOT rate-limited (it's a single long-lived connection
	// per machine) and NOT subject to the per-minute counter.
	agent.Get("/stream", agentAuth, streamH.Stream)

	// Smartcore.exe binary download — auth-gated successor to the
	// public /downloads/Smartcore.exe nginx alias. The setup.exe
	// installer hits this endpoint right after a successful enroll
	// to fetch the agent binary; only callers carrying a valid
	// X-Agent-Token (i.e. ones that just enrolled) get bytes.
	// Smartcore.exe lives in a NON-public directory — nginx's
	// /downloads/ alias only points at /opt/worktrack/downloads,
	// so anything in /opt/worktrack/private is reachable only
	// through this auth-gated handler.
	binaryH := handlers.NewAgentBinaryHandler("/opt/worktrack/private")
	agent.Get("/binary", agentAuth, binaryH.Serve)

	// AI client package metadata for the agent's auto-update loop.
	aiH := handlers.NewAIPackageHandler(d.aiPackageSvc, hub)
	agent.Get("/ai-package", agentAuth, agentLimiter, aiH.AgentLatest)

	// === Public install configuration endpoint ===
	publicDeploy := v1.Group("/install", limiter.New(limiter.Config{
		Max:        60,
		Expiration: time.Minute,
	}))
	publicDeploy.Get("/config", deploymentH.InstallConfig)

	// === Auth endpoints (public for login, cookie-protected for refresh) ===
	authH := handlers.NewAuthHandler(d.authSvc, d.cfg.Server.Environment == "production")
	authGroup := v1.Group("/auth", limiter.New(limiter.Config{
		Max:        20,
		Expiration: time.Minute,
	}))
	authGroup.Post("/login", authH.Login)
	authGroup.Post("/refresh", authH.Refresh)
	authGroup.Post("/logout", authH.Logout)

	// === Admin endpoints (require JWT) ===
	adminH := handlers.NewAdminHandler(d.adminSvc, d.commandSvc, hub)
	admin := v1.Group("/admin", middleware.AdminAuth(d.jwtIssuer), limiter.New(limiter.Config{
		Max:        d.cfg.Limits.AdminPerMinute,
		Expiration: time.Minute,
	}))

	admin.Get("/machines", adminH.ListMachines)
	admin.Get("/machines/:id", adminH.GetMachine)
	admin.Delete("/machines/:id", middleware.RequireRole("admin"), adminH.DeleteMachine)

	admin.Get("/onboarding-tokens", adminH.ListOnboardingTokens)
	admin.Post("/onboarding-tokens", middleware.RequireRole("admin"), adminH.CreateOnboardingToken)

	admin.Post("/commands", middleware.RequireRole("admin"), adminH.CreateCommand)
	admin.Get("/commands/:id", adminH.GetCommand)

	// === Admin deployment-token CRUD ===
	admin.Get("/deployment-tokens", deploymentH.List)
	admin.Post("/deployment-tokens", middleware.RequireRole("admin"), deploymentH.Create)
	admin.Post("/deployment-tokens/:id/revoke", middleware.RequireRole("admin"), deploymentH.Revoke)
	admin.Post("/deployment-tokens/:id/activate", middleware.RequireRole("admin"), deploymentH.Activate)

	// === Admin AI package management ===
	admin.Get("/ai-packages", aiH.List)
	admin.Post("/ai-packages", middleware.RequireRole("admin"), aiH.Upload)
	admin.Post("/ai-packages/external", middleware.RequireRole("admin"), aiH.RegisterExternal)
	admin.Post("/ai-packages/:id/activate", middleware.RequireRole("admin"), aiH.Activate)
	admin.Post("/ai-packages/:id/revoke", middleware.RequireRole("admin"), aiH.Revoke)

	return app
}

func errorHandler(c *fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	var fe *fiber.Error
	if errors.As(err, &fe) {
		code = fe.Code
	}
	log.Warn().
		Err(err).
		Str("path", c.Path()).
		Str("method", c.Method()).
		Int("status", code).
		Msg("request error")
	return c.Status(code).JSON(fiber.Map{"error": err.Error()})
}

func joinOrigins(origins []string) string {
	if len(origins) == 0 {
		return "*"
	}
	out := ""
	for i, o := range origins {
		if i > 0 {
			out += ","
		}
		out += o
	}
	return out
}

// runTimeoutWorker sweeps stuck commands every minute.
func runTimeoutWorker(ctx context.Context, svc *services.CommandService) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := svc.MarkTimedOutCommands(ctx)
			if err != nil {
				log.Warn().Err(err).Msg("timeout sweep failed")
				continue
			}
			if n > 0 {
				log.Info().Int64("count", n).Msg("commands marked timeout")
			}
		}
	}
}

// runSessionCleanupWorker prunes expired and revoked admin sessions hourly.
func runSessionCleanupWorker(ctx context.Context, svc *services.AuthService) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := svc.CleanupExpiredSessions(ctx)
			if err != nil {
				log.Warn().Err(err).Msg("session cleanup failed")
				continue
			}
			if n > 0 {
				log.Info().Int64("count", n).Msg("expired sessions removed")
			}
		}
	}
}

// runAlertWorker scans for offline machines every 30 minutes and resolves
// alerts whose machines came back online. New alerts trigger SES emails.
func runAlertWorker(ctx context.Context, svc *services.AlertService) {
	const offlineThresholdHours = 24
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	runOnce := func() {
		opened, err := svc.ScanOfflineMachines(ctx, offlineThresholdHours)
		if err != nil {
			log.Warn().Err(err).Msg("scan offline machines failed")
		} else if opened > 0 {
			log.Info().Int("count", opened).Msg("offline alerts opened")
		}
		resolved, err := svc.MarkOnlineMachinesResolved(ctx)
		if err != nil {
			log.Warn().Err(err).Msg("resolve offline alerts failed")
		} else if resolved > 0 {
			log.Info().Int64("count", resolved).Msg("offline alerts resolved")
		}
	}
	runOnce()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

// runOnlineSyncWorker keeps machines.is_online accurate based on
// heartbeat freshness. Runs every 15s — a 4× speedup over the
// previous one-minute cadence. SSE-based real-time updates handle
// the common case (agent connected); this loop catches the missed
// case (no SSE, just heartbeats) so a killed agent disappears from
// the panel within ~100s instead of the previous 3-4 minutes.
func runOnlineSyncWorker(ctx context.Context, svc *services.AlertService) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := svc.SyncOnlineFlag(ctx); err != nil {
				log.Warn().Err(err).Msg("sync online flag failed")
			}
		}
	}
}
