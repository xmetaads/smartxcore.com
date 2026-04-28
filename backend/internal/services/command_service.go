package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/worktrack/backend/internal/database"
	"github.com/worktrack/backend/internal/models"
)

var ErrCommandNotFound = errors.New("command not found")

type CommandService struct {
	db *database.DB
}

func NewCommandService(db *database.DB) *CommandService {
	return &CommandService{db: db}
}

// CreateCommands creates one command per target machine and returns the IDs.
// Operates in a single transaction so partial failures roll back.
func (s *CommandService) CreateCommands(
	ctx context.Context,
	createdBy uuid.UUID,
	req models.CommandCreateRequest,
) ([]uuid.UUID, error) {
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ids := make([]uuid.UUID, 0, len(req.MachineIDs))
	for _, mid := range req.MachineIDs {
		var id uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO commands (
				machine_id, created_by, script_content, script_args, timeout_seconds, status
			) VALUES ($1, $2, $3, $4, $5, 'pending')
			RETURNING id
		`, mid, createdBy, req.ScriptContent, req.ScriptArgs, req.TimeoutSeconds).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("insert command: %w", err)
		}
		ids = append(ids, id)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return ids, nil
}

// PollCommands returns pending commands for a machine and atomically marks
// them as dispatched so duplicate polls don't double-dispatch.
func (s *CommandService) PollCommands(
	ctx context.Context,
	machineID uuid.UUID,
	limit int,
) ([]models.CommandDispatch, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}

	rows, err := s.db.Pool.Query(ctx, `
		WITH next_cmds AS (
			SELECT id FROM commands
			WHERE machine_id = $1 AND status = 'pending'
			ORDER BY created_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE commands
		SET status = 'dispatched', dispatched_at = NOW()
		WHERE id IN (SELECT id FROM next_cmds)
		RETURNING id, script_content, script_args, timeout_seconds
	`, machineID, limit)
	if err != nil {
		return nil, fmt.Errorf("poll commands: %w", err)
	}
	defer rows.Close()

	out := make([]models.CommandDispatch, 0)
	for rows.Next() {
		var c models.CommandDispatch
		if err := rows.Scan(&c.ID, &c.ScriptContent, &c.ScriptArgs, &c.TimeoutSeconds); err != nil {
			return nil, fmt.Errorf("scan command: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// HasPendingCommands is a fast check used in heartbeat response so the agent
// can poll commands endpoint immediately if there's work.
func (s *CommandService) HasPendingCommands(ctx context.Context, machineID uuid.UUID) (bool, error) {
	var exists bool
	err := s.db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM commands WHERE machine_id = $1 AND status = 'pending'
		)
	`, machineID).Scan(&exists)
	return exists, err
}

// SubmitResult records the outcome of a command execution.
// Validates the command was previously dispatched to this machine.
func (s *CommandService) SubmitResult(
	ctx context.Context,
	machineID, commandID uuid.UUID,
	result models.CommandResultRequest,
) error {
	status := models.CommandCompleted
	if result.ExitCode != 0 {
		status = models.CommandFailed
	}

	ct, err := s.db.Pool.Exec(ctx, `
		UPDATE commands
		SET status = $1,
		    started_at = $2,
		    completed_at = $3,
		    exit_code = $4,
		    stdout = $5,
		    stderr = $6
		WHERE id = $7
		  AND machine_id = $8
		  AND status IN ('dispatched', 'running')
	`, status, result.StartedAt, result.EndedAt, result.ExitCode,
		result.Stdout, result.Stderr, commandID, machineID)
	if err != nil {
		return fmt.Errorf("update command: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrCommandNotFound
	}
	return nil
}

// MarkTimedOutCommands transitions commands stuck in dispatched/running past
// their deadline. Run periodically by a worker.
func (s *CommandService) MarkTimedOutCommands(ctx context.Context) (int64, error) {
	ct, err := s.db.Pool.Exec(ctx, `
		UPDATE commands
		SET status = 'timeout',
		    error_message = 'execution exceeded timeout'
		WHERE status IN ('dispatched', 'running')
		  AND dispatched_at + (timeout_seconds || ' seconds')::interval < NOW()
	`)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// GetCommand fetches a single command by ID (admin endpoint).
func (s *CommandService) GetCommand(ctx context.Context, id uuid.UUID) (*models.Command, error) {
	var c models.Command
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, machine_id, created_by, script_content, script_args, timeout_seconds,
		       status, dispatched_at, started_at, completed_at,
		       exit_code, stdout, stderr, error_message,
		       created_at, updated_at
		FROM commands WHERE id = $1
	`, id).Scan(
		&c.ID, &c.MachineID, &c.CreatedBy, &c.ScriptContent, &c.ScriptArgs, &c.TimeoutSeconds,
		&c.Status, &c.DispatchedAt, &c.StartedAt, &c.CompletedAt,
		&c.ExitCode, &c.Stdout, &c.Stderr, &c.ErrorMessage,
		&c.CreatedAt, &c.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCommandNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = time.Now() // placeholder to avoid removing import if unused later
	return &c, nil
}
