package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// KindAlert is a message about the server itself — a certificate that does not renew, a backup
// that fails — for the owner, through the same queue as everything else: it survives a mail
// server that is down right now, and is sent once however often its cause is noticed.
const KindAlert = "system.alert"

// Alert is what such a message says.
type Alert struct {
	Subject string `json:"subject"`
	Text    string `json:"text"`
}

// EnqueueAlert queues an alert for every channel. key names the occasion («cert:2026-09-20»):
// the same key is queued once.
func EnqueueAlert(ctx context.Context, tx Execer, now time.Time, key string, alert Alert) error {
	alert.Subject, alert.Text = strings.TrimSpace(alert.Subject), strings.TrimSpace(alert.Text)
	if alert.Subject == "" || key == "" {
		return errors.New("an alert needs a subject and a key")
	}
	if len(key) > 80 {
		key = key[:80]
	}
	for _, channel := range []string{ChannelEmail, ChannelTelegram} {
		if err := Enqueue(ctx, tx, now, NewTask{Channel: channel, Kind: KindAlert, DedupeKey: "alert:" + key + ":" + channel, Payload: alert}); err != nil {
			return err
		}
	}
	return nil
}

// ReadAlert takes the alert out of a task.
func ReadAlert(task Task) (Alert, error) {
	var alert Alert
	if err := json.Unmarshal(task.Payload, &alert); err != nil || alert.Subject == "" {
		return Alert{}, Permanent(errors.New("unreadable alert"))
	}
	return alert, nil
}
