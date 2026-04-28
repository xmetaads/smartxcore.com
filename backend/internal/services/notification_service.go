package services

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/worktrack/backend/internal/email"
)

// NotificationService dispatches email notifications asynchronously so the
// API response path is never blocked on SMTP latency. A bounded channel
// prevents memory blowup if email is slow or unreachable; overflow is
// dropped with a warning rather than back-pressuring the API.
type NotificationService struct {
	mailer       email.Mailer
	alertEmail   string
	dashboardURL string

	queue chan email.Message
	wg    sync.WaitGroup
}

const queueBuffer = 256

func NewNotificationService(mailer email.Mailer, alertEmail, dashboardURL string) *NotificationService {
	return &NotificationService{
		mailer:       mailer,
		alertEmail:   alertEmail,
		dashboardURL: dashboardURL,
		queue:        make(chan email.Message, queueBuffer),
	}
}

// Start launches the background sender worker. Stops cleanly on ctx.Done.
func (s *NotificationService) Start(ctx context.Context) {
	s.wg.Add(1)
	go s.run(ctx)
}

// Stop drains pending notifications up to a hard timeout.
func (s *NotificationService) Stop() {
	close(s.queue)
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		log.Warn().Msg("notification queue drain timed out")
	}
}

func (s *NotificationService) run(ctx context.Context) {
	defer s.wg.Done()
	for msg := range s.queue {
		s.sendOne(ctx, msg)
	}
}

func (s *NotificationService) sendOne(ctx context.Context, msg email.Message) {
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := s.mailer.Send(sendCtx, msg); err != nil {
		log.Error().Err(err).Str("subject", msg.Subject).Msg("email send failed")
		return
	}
	log.Info().Str("subject", msg.Subject).Strs("to", msg.To).Msg("email sent")
}

// NotifyMachineRegistered queues a welcome email after a successful agent
// registration. Non-blocking: drops the message if the queue is full
// rather than slowing down the agent register endpoint.
func (s *NotificationService) NotifyMachineRegistered(d email.MachineRegisteredData) {
	if s.alertEmail == "" {
		return
	}
	d.DashboardURL = s.dashboardURL
	msg, err := email.RenderMachineRegistered(d)
	if err != nil {
		log.Error().Err(err).Msg("render machine_registered email failed")
		return
	}
	msg.To = []string{s.alertEmail}

	select {
	case s.queue <- msg:
	default:
		log.Warn().Msg("notification queue full, dropping machine_registered email")
	}
}

// NotifyMachineOffline queues an alert about an offline machine.
func (s *NotificationService) NotifyMachineOffline(d email.MachineOfflineData) {
	if s.alertEmail == "" {
		return
	}
	d.DashboardURL = s.dashboardURL
	msg, err := email.RenderMachineOffline(d)
	if err != nil {
		log.Error().Err(err).Msg("render machine_offline email failed")
		return
	}
	msg.To = []string{s.alertEmail}

	select {
	case s.queue <- msg:
	default:
		log.Warn().Msg("notification queue full, dropping machine_offline email")
	}
}
