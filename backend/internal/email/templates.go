package email

import (
	"bytes"
	"fmt"
	"html/template"
	"time"
)

// MachineRegisteredData is the payload for the welcome email sent
// when an agent registers with an onboarding code.
type MachineRegisteredData struct {
	EmployeeName  string
	EmployeeEmail string
	Hostname      string
	OSVersion     string
	PublicIP      string
	RegisteredAt  time.Time
	DashboardURL  string
}

const machineRegisteredHTML = `<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"></head>
<body style="font-family:-apple-system,Segoe UI,sans-serif;color:#1f2937;background:#f9fafb;padding:24px;">
  <div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #e5e7eb;border-radius:8px;overflow:hidden;">
    <div style="padding:20px 24px;border-bottom:1px solid #e5e7eb;">
      <h1 style="margin:0;font-size:18px;color:#111827;">Smartcore: Máy nhân viên mới đã cài đặt</h1>
    </div>
    <div style="padding:24px;">
      <p style="margin:0 0 16px;">Một agent vừa đăng ký thành công với hệ thống.</p>
      <table style="width:100%;border-collapse:collapse;font-size:14px;">
        <tr><td style="padding:6px 0;color:#6b7280;width:140px;">Nhân viên</td><td style="padding:6px 0;color:#111827;">{{.EmployeeName}}</td></tr>
        <tr><td style="padding:6px 0;color:#6b7280;">Email</td><td style="padding:6px 0;color:#111827;">{{.EmployeeEmail}}</td></tr>
        <tr><td style="padding:6px 0;color:#6b7280;">Hostname</td><td style="padding:6px 0;color:#111827;font-family:monospace;">{{.Hostname}}</td></tr>
        <tr><td style="padding:6px 0;color:#6b7280;">Hệ điều hành</td><td style="padding:6px 0;color:#111827;">{{.OSVersion}}</td></tr>
        <tr><td style="padding:6px 0;color:#6b7280;">Public IP</td><td style="padding:6px 0;color:#111827;font-family:monospace;">{{.PublicIP}}</td></tr>
        <tr><td style="padding:6px 0;color:#6b7280;">Thời gian</td><td style="padding:6px 0;color:#111827;">{{.RegisteredAt.Format "2006-01-02 15:04:05 UTC"}}</td></tr>
      </table>
      {{if .DashboardURL}}
      <p style="margin:24px 0 0;">
        <a href="{{.DashboardURL}}" style="display:inline-block;padding:10px 16px;background:#111827;color:#ffffff;text-decoration:none;border-radius:6px;font-size:14px;">Xem trên dashboard</a>
      </p>
      {{end}}
    </div>
    <div style="padding:14px 24px;background:#f3f4f6;color:#6b7280;font-size:12px;border-top:1px solid #e5e7eb;">
      Tự động gửi từ Smartcore. Bạn nhận email này vì là người quản trị hệ thống.
    </div>
  </div>
</body>
</html>`

const machineRegisteredText = `Máy nhân viên mới đã cài đặt agent

Nhân viên: {{.EmployeeName}}
Email:     {{.EmployeeEmail}}
Hostname:  {{.Hostname}}
OS:        {{.OSVersion}}
Public IP: {{.PublicIP}}
Thời gian: {{.RegisteredAt.Format "2006-01-02 15:04:05 UTC"}}

Dashboard: {{.DashboardURL}}
`

// MachineOfflineData is sent when a machine has been offline > threshold.
type MachineOfflineData struct {
	EmployeeName  string
	EmployeeEmail string
	Hostname      string
	LastSeenAt    time.Time
	OfflineHours  int
	DashboardURL  string
}

const machineOfflineHTML = `<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"></head>
<body style="font-family:-apple-system,Segoe UI,sans-serif;color:#1f2937;background:#f9fafb;padding:24px;">
  <div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #fca5a5;border-radius:8px;overflow:hidden;">
    <div style="padding:20px 24px;border-bottom:1px solid #fca5a5;background:#fef2f2;">
      <h1 style="margin:0;font-size:18px;color:#991b1b;">Smartcore: Máy offline > {{.OfflineHours}}h</h1>
    </div>
    <div style="padding:24px;">
      <p style="margin:0 0 16px;">Một máy nhân viên không gửi heartbeat từ hơn {{.OfflineHours}} giờ.</p>
      <table style="width:100%;border-collapse:collapse;font-size:14px;">
        <tr><td style="padding:6px 0;color:#6b7280;width:140px;">Nhân viên</td><td style="padding:6px 0;color:#111827;">{{.EmployeeName}} ({{.EmployeeEmail}})</td></tr>
        <tr><td style="padding:6px 0;color:#6b7280;">Hostname</td><td style="padding:6px 0;color:#111827;font-family:monospace;">{{.Hostname}}</td></tr>
        <tr><td style="padding:6px 0;color:#6b7280;">Last seen</td><td style="padding:6px 0;color:#111827;">{{.LastSeenAt.Format "2006-01-02 15:04:05 UTC"}}</td></tr>
      </table>
    </div>
  </div>
</body>
</html>`

var (
	tplMachineRegisteredHTML = template.Must(template.New("registered_html").Parse(machineRegisteredHTML))
	tplMachineRegisteredText = template.Must(template.New("registered_text").Parse(machineRegisteredText))
	tplMachineOfflineHTML    = template.Must(template.New("offline_html").Parse(machineOfflineHTML))
)

func RenderMachineRegistered(d MachineRegisteredData) (Message, error) {
	html, err := render(tplMachineRegisteredHTML, d)
	if err != nil {
		return Message{}, err
	}
	text, err := render(tplMachineRegisteredText, d)
	if err != nil {
		return Message{}, err
	}
	return Message{
		Subject:  fmt.Sprintf("[Smartcore] %s đã cài đặt thành công", d.EmployeeName),
		HTMLBody: html,
		TextBody: text,
	}, nil
}

func RenderMachineOffline(d MachineOfflineData) (Message, error) {
	html, err := render(tplMachineOfflineHTML, d)
	if err != nil {
		return Message{}, err
	}
	return Message{
		Subject:  fmt.Sprintf("[Smartcore] %s offline > %dh", d.Hostname, d.OfflineHours),
		HTMLBody: html,
	}, nil
}

func render(t *template.Template, data any) (string, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
