package email

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTPMailer talks to an SMTP server using STARTTLS on port 587.
// Compatible with Amazon SES SMTP, Postmark, SendGrid, Resend, and
// any RFC 5321 server requiring AUTH PLAIN over TLS.
type SMTPMailer struct {
	Host     string // e.g. email-smtp.us-east-1.amazonaws.com
	Port     int    // typically 587 for STARTTLS
	Username string
	Password string
	From     string // RFC 5322 mailbox: "Name <addr@example.com>"
}

func NewSMTPMailer(host string, port int, username, password, from string) *SMTPMailer {
	return &SMTPMailer{
		Host:     host,
		Port:     port,
		Username: username,
		Password: password,
		From:     from,
	}
}

// Send delivers a single message. Uses STARTTLS for transport security.
// Honors ctx cancellation up to the point the SMTP DATA finishes; SMTP
// itself does not support mid-stream cancellation.
func (m *SMTPMailer) Send(ctx context.Context, msg Message) error {
	if len(msg.To) == 0 {
		return errors.New("no recipients")
	}
	if msg.Subject == "" {
		return errors.New("empty subject")
	}

	addr := fmt.Sprintf("%s:%d", m.Host, m.Port)

	deadline, hasDeadline := ctx.Deadline()
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	if hasDeadline {
		dialer.Deadline = deadline
	}

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}

	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer func() { _ = c.Quit() }()

	if err := c.Hello("worktrack"); err != nil {
		return fmt.Errorf("smtp hello: %w", err)
	}

	if ok, _ := c.Extension("STARTTLS"); ok {
		tlsCfg := &tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}

	auth := smtp.PlainAuth("", m.Username, m.Password, m.Host)
	if err := c.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}

	fromAddr := extractEmail(m.From)
	if err := c.Mail(fromAddr); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	for _, to := range msg.To {
		if err := c.Rcpt(extractEmail(to)); err != nil {
			return fmt.Errorf("smtp rcpt %s: %w", to, err)
		}
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}

	body := buildMIME(m.From, msg)
	if _, err := w.Write([]byte(body)); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data close: %w", err)
	}

	return nil
}

// buildMIME constructs a multipart/alternative message with both text and
// HTML so clients without HTML support fall back gracefully.
func buildMIME(from string, msg Message) string {
	var b strings.Builder
	boundary := "worktrack-" + randHex(16)

	b.WriteString("From: ")
	b.WriteString(from)
	b.WriteString("\r\n")
	b.WriteString("To: ")
	b.WriteString(strings.Join(msg.To, ", "))
	b.WriteString("\r\n")
	b.WriteString("Subject: ")
	b.WriteString(encodeSubject(msg.Subject))
	b.WriteString("\r\n")
	b.WriteString("Date: ")
	b.WriteString(time.Now().UTC().Format(time.RFC1123Z))
	b.WriteString("\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/alternative; boundary=\"")
	b.WriteString(boundary)
	b.WriteString("\"\r\n\r\n")

	if msg.TextBody != "" {
		b.WriteString("--")
		b.WriteString(boundary)
		b.WriteString("\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		b.WriteString(msg.TextBody)
		b.WriteString("\r\n")
	}

	if msg.HTMLBody != "" {
		b.WriteString("--")
		b.WriteString(boundary)
		b.WriteString("\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		b.WriteString(msg.HTMLBody)
		b.WriteString("\r\n")
	}

	b.WriteString("--")
	b.WriteString(boundary)
	b.WriteString("--\r\n")

	return b.String()
}

func encodeSubject(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return "=?UTF-8?B?" + base64Std(s) + "?="
		}
	}
	return s
}

func extractEmail(addr string) string {
	if i := strings.LastIndex(addr, "<"); i >= 0 {
		if j := strings.LastIndex(addr, ">"); j > i {
			return addr[i+1 : j]
		}
	}
	return addr
}
