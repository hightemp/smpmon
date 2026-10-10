package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Dispatcher sends events to the desktop, commands and webhooks.
type Dispatcher struct {
	HTTP *http.Client
	// Command builds the desktop notification command; tests replace it.
	desktop func(title, body string, crit bool) *exec.Cmd
}

// WebhookPayload is the JSON posted to webhooks.
type WebhookPayload struct {
	Alert     string    `json:"alert"`
	State     string    `json:"state"` // firing or resolved
	Level     string    `json:"level"`
	Host      string    `json:"host"`
	Metric    string    `json:"metric"`
	Value     float64   `json:"value"`
	Op        string    `json:"op"`
	Threshold float64   `json:"threshold"`
	Message   string    `json:"message"`
	Time      time.Time `json:"time"`
}

func payload(e Event) WebhookPayload {
	state := "firing"
	if e.Resolved {
		state = "resolved"
	}
	return WebhookPayload{
		Alert: e.Rule.Name, State: state, Level: string(e.Rule.Level), Host: e.Host, Metric: string(e.Key),
		Value: e.Value, Op: e.Rule.Op, Threshold: e.Rule.Threshold, Message: e.Summary(), Time: e.Time,
	}
}

// Notify implements Notifier.
func (d *Dispatcher) Notify(ctx context.Context, e Event) {
	for _, ch := range e.Rule.Notify {
		var err error
		switch ch {
		case "desktop":
			err = d.toDesktop(ctx, e)
		case "exec":
			err = d.toExec(ctx, e)
		case "webhook":
			err = d.toWebhook(ctx, e)
		}
		if err != nil {
			slog.Warn("alert notification failed", "channel", ch, "alert", e.Rule.Name, "err", err)
		}
	}
}

func (d *Dispatcher) toDesktop(ctx context.Context, e Event) error {
	title := "smpmon: " + e.Rule.Name
	crit := e.Rule.Level == Crit && !e.Resolved
	mk := d.desktop
	if mk == nil {
		mk = desktopCommand
	}
	cmd := mk(title, e.Summary(), crit)
	if cmd == nil {
		return fmt.Errorf("desktop notifications are not supported on %s", runtime.GOOS)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func desktopCommand(title, body string, crit bool) *exec.Cmd {
	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd", "netbsd":
		urgency := "normal"
		if crit {
			urgency = "critical"
		}
		return exec.Command("notify-send", "-a", "smpmon", "-u", urgency, title, body)
	case "darwin":
		script := fmt.Sprintf("display notification %s with title %s", appleQuote(body), appleQuote(title))
		return exec.Command("osascript", "-e", script)
	case "windows":
		ps := `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$t = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$n = $t.GetElementsByTagName('text')
$n.Item(0).AppendChild($t.CreateTextNode($env:SMPMON_TITLE)) > $null
$n.Item(1).AppendChild($t.CreateTextNode($env:SMPMON_BODY)) > $null
$id = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe'
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($id).Show([Windows.UI.Notifications.ToastNotification]::new($t))`
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
		cmd.Env = append(os.Environ(), "SMPMON_TITLE="+title, "SMPMON_BODY="+body)
		return cmd
	}
	return nil
}

// appleQuote quotes a string for AppleScript.
func appleQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// alertEnv describes the event to exec'd commands.
func alertEnv(e Event) []string {
	p := payload(e)
	return []string{
		"SMPMON_ALERT=" + p.Alert, "SMPMON_ALERT_STATE=" + p.State, "SMPMON_ALERT_LEVEL=" + p.Level,
		"SMPMON_ALERT_HOST=" + p.Host, "SMPMON_ALERT_METRIC=" + p.Metric,
		"SMPMON_ALERT_VALUE=" + strconv.FormatFloat(p.Value, 'g', -1, 64),
		"SMPMON_ALERT_THRESHOLD=" + strconv.FormatFloat(p.Threshold, 'g', -1, 64),
		"SMPMON_ALERT_MESSAGE=" + p.Message,
	}
}

func (d *Dispatcher) toExec(ctx context.Context, e Event) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", e.Rule.Exec)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", e.Rule.Exec)
	}
	cmd.Env = append(os.Environ(), alertEnv(e)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (d *Dispatcher) toWebhook(ctx context.Context, e Event) error {
	body, err := json.Marshal(payload(e))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.Rule.Webhook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "smpmon")
	client := d.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook answered %s", resp.Status)
	}
	return nil
}
