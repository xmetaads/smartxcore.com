package services

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/worktrack/backend/internal/database"
	"github.com/worktrack/backend/internal/email"
)

// AlertService scans for offline machines and creates alert rows.
// One alert per machine per offline-window — duplicate suppression is done
// by checking for an open alert before creating a new one.
type AlertService struct {
	db            *database.DB
	notifications *NotificationService
}

func NewAlertService(db *database.DB, notifications *NotificationService) *AlertService {
	return &AlertService{db: db, notifications: notifications}
}

const (
	alertTypeOffline = "machine_offline"
)

// ScanOfflineMachines finds machines whose last_seen_at is older than the
// configured threshold and have no open offline alert yet, then opens a new
// alert and queues an email notification.
func (s *AlertService) ScanOfflineMachines(ctx context.Context, thresholdHours int) (int, error) {
	if thresholdHours <= 0 {
		thresholdHours = 24
	}

	rows, err := s.db.Pool.Query(ctx, `
		SELECT m.id, m.employee_email, m.employee_name, m.hostname, m.last_seen_at
		FROM machines m
		WHERE m.disabled_at IS NULL
		  AND m.last_seen_at IS NOT NULL
		  AND m.last_seen_at < NOW() - ($1 || ' hours')::interval
		  AND NOT EXISTS (
		      SELECT 1 FROM alerts a
		      WHERE a.machine_id = m.id
		        AND a.alert_type = $2
		        AND a.status = 'open'
		  )
	`, thresholdHours, alertTypeOffline)
	if err != nil {
		return 0, fmt.Errorf("query offline candidates: %w", err)
	}
	defer rows.Close()

	type pending struct {
		id            uuid.UUID
		employeeEmail string
		employeeName  string
		hostname      *string
		lastSeenAt    time.Time
	}
	var pendings []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.employeeEmail, &p.employeeName, &p.hostname, &p.lastSeenAt); err != nil {
			return 0, fmt.Errorf("scan candidate: %w", err)
		}
		pendings = append(pendings, p)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	created := 0
	for _, p := range pendings {
		host := ""
		if p.hostname != nil {
			host = *p.hostname
		}

		title := fmt.Sprintf("Máy %s offline > %dh", host, thresholdHours)
		_, err := s.db.Pool.Exec(ctx, `
			INSERT INTO alerts (machine_id, alert_type, severity, status, title, description, metadata)
			VALUES ($1, $2, 'warning', 'open', $3, $4, $5)
		`, p.id, alertTypeOffline, title,
			fmt.Sprintf("Last seen %s. Please contact employee.", p.lastSeenAt.Format(time.RFC3339)),
			fmt.Sprintf(`{"hostname":"%s","employee_email":"%s"}`, host, p.employeeEmail),
		)
		if err != nil {
			log.Warn().Err(err).Str("machine_id", p.id.String()).Msg("create alert failed")
			continue
		}
		created++

		if s.notifications != nil {
			s.notifications.NotifyMachineOffline(email.MachineOfflineData{
				EmployeeName:  p.employeeName,
				EmployeeEmail: p.employeeEmail,
				Hostname:      host,
				LastSeenAt:    p.lastSeenAt,
				OfflineHours:  thresholdHours,
			})
		}
	}

	return created, nil
}

// MarkOnlineMachinesResolved closes open offline alerts when a heartbeat
// has arrived recently. Run from the same worker after ScanOfflineMachines.
func (s *AlertService) MarkOnlineMachinesResolved(ctx context.Context) (int64, error) {
	ct, err := s.db.Pool.Exec(ctx, `
		UPDATE alerts a
		SET status = 'resolved', resolved_at = NOW()
		FROM machines m
		WHERE a.machine_id = m.id
		  AND a.alert_type = $1
		  AND a.status = 'open'
		  AND m.last_seen_at IS NOT NULL
		  AND m.last_seen_at > NOW() - INTERVAL '15 minutes'
	`, alertTypeOffline)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// SyncOnlineFlag flips machines.is_online based on heartbeat freshness.
// Run frequently (every minute or two) so the dashboard reflects reality.
// Threshold is 3x heartbeat interval (180s) — gives slack for one missed beat.
func (s *AlertService) SyncOnlineFlag(ctx context.Context) error {
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE machines
		SET is_online = (last_seen_at > NOW() - INTERVAL '3 minutes')
		WHERE disabled_at IS NULL
	`)
	return err
}
