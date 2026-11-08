//go:build integration

package testutil

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// MailpitMessage is the part of a Mailpit message the tests look at.
type MailpitMessage struct {
	ID      string
	Subject string
	Text    string
	HTML    string
	From    struct{ Address, Name string }
	To      []struct{ Address string }
}

// WaitForEmail polls Mailpit until a message for to whose subject contains
// subject arrives, and returns it. Delivery is asynchronous, hence the wait.
func (m Mailpit) WaitForEmail(t *testing.T, to, subject string) MailpitMessage {
	t.Helper()
	var found MailpitMessage
	require.Eventually(t, func() bool {
		var list struct {
			Messages []struct {
				ID      string
				Subject string
				To      []struct{ Address string }
			} `json:"messages"`
		}
		if !m.getJSON(t, "/api/v1/messages?limit=200", &list) {
			return false
		}
		for _, msg := range list.Messages {
			if len(msg.To) == 1 && strings.EqualFold(msg.To[0].Address, to) && strings.Contains(msg.Subject, subject) {
				return m.getJSON(t, "/api/v1/message/"+msg.ID, &found)
			}
		}
		return false
	}, 15*time.Second, 100*time.Millisecond, "no email %q for %s", subject, to)
	return found
}

// Count returns how many messages were sent to to.
func (m Mailpit) Count(t *testing.T, to string) int {
	t.Helper()
	var list struct {
		Messages []struct{ To []struct{ Address string } } `json:"messages"`
	}
	require.True(t, m.getJSON(t, "/api/v1/messages?limit=200", &list))
	n := 0
	for _, msg := range list.Messages {
		for _, rcpt := range msg.To {
			if strings.EqualFold(rcpt.Address, to) {
				n++
			}
		}
	}
	return n
}

func (m Mailpit) getJSON(t *testing.T, path string, out any) bool {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, m.APIURL+path, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(out) == nil
}
