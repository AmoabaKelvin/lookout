package main

import (
	"fmt"
	"net/smtp"
	"strings"
)

// SMTPNotifier sends alerts as HTML email over STARTTLS (port 587 style).
// Implicit-TLS servers (port 465) are tracked in issue #12.
type SMTPNotifier struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	To       []string
}

func (s *SMTPNotifier) Send(alert Alert) error {
	v := visualFor(alert)
	subject := fmt.Sprintf("[%s] %s — %s", v.label, alert.Hostname, alert.Title)

	var msg strings.Builder
	msg.WriteString("From: " + s.From + "\r\n")
	msg.WriteString("To: " + strings.Join(s.To, ", ") + "\r\n")
	msg.WriteString("Subject: " + subject + "\r\n")
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(smtpHTMLBody(alert, v))

	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	auth := smtp.PlainAuth("", s.Username, s.Password, s.Host)
	return smtp.SendMail(addr, auth, s.From, s.To, []byte(msg.String()))
}

// emailColors returns an accessible palette for the email keyed off the alert
// label. The webhook hex colours (alertVisual.hex) aren't reused here because a
// solid yellow warning banner can't carry readable white text; instead each
// state gets a strong accent plus a tinted pill background with dark text.
func emailColors(label string) (accent, pillBg, pillText string) {
	switch label {
	case "RESOLVED":
		return "#1f9d57", "#e7f6ec", "#1f7a45"
	case "WARNING":
		return "#c77700", "#fdf3e2", "#9a5b00"
	default: // CRITICAL
		return "#d64545", "#fdecea", "#b42318"
	}
}

func smtpHTMLBody(alert Alert, v alertVisual) string {
	accent, pillBg, pillText := emailColors(v.label)

	summary := "A monitored metric on this host crossed its threshold and needs your attention."
	if !alert.IsFiring {
		summary = "Good news — this metric has recovered and is back within its threshold."
	}

	return strings.NewReplacer(
		"{{accent}}", accent,
		"{{pillBg}}", pillBg,
		"{{pillText}}", pillText,
		"{{label}}", v.label,
		"{{title}}", alert.Title,
		"{{summary}}", summary,
		"{{host}}", alert.Hostname,
		"{{metric}}", alert.Metric,
		"{{value}}", formatValue(alert.Value, alert.Unit),
		"{{threshold}}", formatValue(alert.Threshold, alert.Unit),
	).Replace(emailTemplate)
}

// emailTemplate is a self-contained, table-based HTML email built for broad
// client support (Gmail, Outlook, Apple Mail): inline styles, a system font
// stack, and a fixed 600px width. Placeholders are filled by smtpHTMLBody.
const emailTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="x-apple-disable-message-reformatting">
<title>Lookout alert</title>
</head>
<body style="margin:0;padding:0;background-color:#f2f3f5;">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background-color:#f2f3f5;">
    <tr>
      <td align="center" style="padding:32px 16px;">
        <table role="presentation" width="600" cellpadding="0" cellspacing="0" style="width:600px;max-width:600px;background-color:#ffffff;border:1px solid #e7e9ee;border-radius:16px;overflow:hidden;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;">
          <tr><td style="height:4px;background-color:{{accent}};line-height:4px;font-size:4px;">&nbsp;</td></tr>
          <tr>
            <td style="padding:24px 32px 20px 32px;border-bottom:1px solid #eef0f3;">
              <table role="presentation" width="100%" cellpadding="0" cellspacing="0">
                <tr>
                  <td style="vertical-align:middle;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:19px;font-weight:600;color:#15171A;letter-spacing:-0.3px;">Lookout</td>
                  <td align="right" style="vertical-align:middle;font-size:13px;color:#9aa0ab;">Monitoring alert</td>
                </tr>
              </table>
            </td>
          </tr>
          <tr>
            <td style="padding:28px 32px 4px 32px;">
              <span style="display:inline-block;padding:5px 12px;border-radius:999px;background-color:{{pillBg}};color:{{pillText}};font-size:12px;font-weight:700;letter-spacing:0.6px;">{{label}}</span>
              <h1 style="margin:18px 0 8px 0;font-size:22px;line-height:1.3;font-weight:600;color:#14161a;letter-spacing:-0.3px;">{{title}}</h1>
              <p style="margin:0 0 24px 0;font-size:15px;line-height:1.55;color:#6b7280;">{{summary}}</p>
            </td>
          </tr>
          <tr>
            <td style="padding:0 32px 8px 32px;">
              <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background-color:#f8f9fb;border:1px solid #eef0f3;border-radius:12px;">
                <tr>
                  <td style="padding:13px 18px;border-bottom:1px solid #eef0f3;font-size:14px;color:#6b7280;">Host</td>
                  <td align="right" style="padding:13px 18px;border-bottom:1px solid #eef0f3;font-size:14px;font-weight:500;color:#14161a;">{{host}}</td>
                </tr>
                <tr>
                  <td style="padding:13px 18px;border-bottom:1px solid #eef0f3;font-size:14px;color:#6b7280;">Metric</td>
                  <td align="right" style="padding:13px 18px;border-bottom:1px solid #eef0f3;font-size:13px;font-weight:500;color:#14161a;font-family:'SFMono-Regular',Consolas,'Liberation Mono',Menlo,monospace;">{{metric}}</td>
                </tr>
                <tr>
                  <td style="padding:13px 18px;border-bottom:1px solid #eef0f3;font-size:14px;color:#6b7280;">Current value</td>
                  <td align="right" style="padding:13px 18px;border-bottom:1px solid #eef0f3;font-size:15px;font-weight:700;color:{{accent}};">{{value}}</td>
                </tr>
                <tr>
                  <td style="padding:13px 18px;font-size:14px;color:#6b7280;">Threshold</td>
                  <td align="right" style="padding:13px 18px;font-size:14px;font-weight:500;color:#14161a;">{{threshold}}</td>
                </tr>
              </table>
            </td>
          </tr>
          <tr>
            <td style="padding:22px 32px 28px 32px;">
              <p style="margin:0;font-size:12px;line-height:1.5;color:#9aa0ab;">You're receiving this because Lookout is monitoring this host. This is an automated message — there's no need to reply.</p>
            </td>
          </tr>
        </table>
        <div style="margin-top:16px;font-size:12px;color:#b3b8c2;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;">Lookout · self-hosted server monitoring</div>
      </td>
    </tr>
  </table>
</body>
</html>`
