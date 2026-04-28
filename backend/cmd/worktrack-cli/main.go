package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/term"

	"github.com/worktrack/backend/internal/auth"
	"github.com/worktrack/backend/internal/config"
	"github.com/worktrack/backend/internal/database"
	"github.com/worktrack/backend/internal/logger"
)

const usage = `WorkTrack CLI

Commands:
  migrate              Apply pending database migrations
  create-admin         Create the initial admin user
  reset-password       Reset an admin user's password

Flags:
  -migrations <dir>    Migrations directory (default: ./migrations)
  -email <email>       Admin email (for create-admin/reset-password)
  -name <name>         Admin display name (for create-admin)

Environment: reads .env or env vars (DATABASE_URL is required).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	cmd := os.Args[1]
	rest := os.Args[2:]

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	migrationsDir := fs.String("migrations", "./migrations", "migrations directory")
	email := fs.String("email", "", "admin email")
	name := fs.String("name", "", "admin display name")
	role := fs.String("role", "admin", "admin role (admin or viewer)")
	if err := fs.Parse(rest); err != nil {
		fmt.Fprintf(os.Stderr, "parse flags: %v\n", err)
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	logger.Init(cfg.Server.LogLevel, cfg.Server.Environment)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	db, err := database.New(ctx, cfg.Database)
	if err != nil {
		fmt.Fprintf(os.Stderr, "database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	switch cmd {
	case "migrate":
		if err := database.Migrate(ctx, db, *migrationsDir); err != nil {
			fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("migrations applied")
	case "create-admin":
		if err := database.Migrate(ctx, db, *migrationsDir); err != nil {
			fmt.Fprintf(os.Stderr, "migrate before create-admin: %v\n", err)
			os.Exit(1)
		}
		if err := createAdmin(ctx, db, *email, *name, *role); err != nil {
			fmt.Fprintf(os.Stderr, "create-admin: %v\n", err)
			os.Exit(1)
		}
	case "reset-password":
		if err := resetPassword(ctx, db, *email); err != nil {
			fmt.Fprintf(os.Stderr, "reset-password: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
}

func createAdmin(ctx context.Context, db *database.DB, email, name, role string) error {
	if email == "" {
		email = prompt("Email: ")
	}
	if name == "" {
		name = prompt("Display name: ")
	}
	if role != "admin" && role != "viewer" {
		return fmt.Errorf("role must be 'admin' or 'viewer', got %q", role)
	}

	password := promptPassword("Password (min 12 chars): ")
	if len(password) < 12 {
		return errors.New("password must be at least 12 characters")
	}
	confirm := promptPassword("Confirm password: ")
	if password != confirm {
		return errors.New("passwords do not match")
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO admin_users (email, password_hash, name, role)
		VALUES ($1, $2, $3, $4)
	`, email, hash, name, role)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("an admin with email %s already exists (use reset-password instead)", email)
		}
		return err
	}
	fmt.Printf("admin created: %s (%s)\n", email, role)
	return nil
}

func resetPassword(ctx context.Context, db *database.DB, email string) error {
	if email == "" {
		email = prompt("Email: ")
	}

	password := promptPassword("New password (min 12 chars): ")
	if len(password) < 12 {
		return errors.New("password must be at least 12 characters")
	}
	confirm := promptPassword("Confirm password: ")
	if password != confirm {
		return errors.New("passwords do not match")
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	ct, err := db.Pool.Exec(ctx, `
		UPDATE admin_users SET password_hash = $1, updated_at = NOW() WHERE email = $2
	`, hash, email)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("no admin found with email %s", email)
	}
	fmt.Println("password reset")
	return nil
}

func prompt(label string) string {
	fmt.Print(label)
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

func promptPassword(label string) string {
	fmt.Print(label)
	bytes, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		fmt.Fprintf(os.Stderr, "read password: %v\n", err)
		os.Exit(1)
	}
	return string(bytes)
}
