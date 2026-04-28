package email

import "context"

// Mailer abstracts the email backend so we can swap SMTP for SES API,
// Postmark, or a no-op mailer in tests.
type Mailer interface {
	Send(ctx context.Context, msg Message) error
}

type Message struct {
	To       []string
	Subject  string
	HTMLBody string
	TextBody string
}

// NoopMailer drops emails on the floor — used in tests and dev when
// SMTP is not configured. It logs at info level so failures don't appear.
type NoopMailer struct{}

func (NoopMailer) Send(_ context.Context, _ Message) error { return nil }
