//go:build windows

package eventlog

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os/exec"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/worktrack/agent/internal/api"
)

// EventID → high-level event type mapping we report to the backend.
var eventIDToType = map[int]string{
	6005: "boot",
	6006: "shutdown",
	7001: "logon",
	7002: "logoff",
	4624: "logon",
	4634: "logoff",
	4800: "lock",
	4801: "unlock",
}

// Reader queries the Windows Event Log for tracked event IDs and forwards
// new events to the backend. It uses wevtutil for portability across
// Windows versions and runs in user mode (no admin required).
//
// State: the timestamp of the most recent event submitted is persisted so
// restarts don't replay history.
type Reader struct {
	client       *api.Client
	pollInterval time.Duration
	cursorStore  *CursorStore
}

func NewReader(client *api.Client, pollInterval time.Duration, cursorStore *CursorStore) *Reader {
	return &Reader{
		client:       client,
		pollInterval: pollInterval,
		cursorStore:  cursorStore,
	}
}

func (r *Reader) Run(ctx context.Context) {
	timer := time.NewTimer(r.pollInterval)
	defer timer.Stop()

	for {
		if err := r.pollAndSubmit(ctx); err != nil {
			log.Warn().Err(err).Msg("event poll cycle failed")
		}

		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			timer.Reset(r.pollInterval)
		}
	}
}

func (r *Reader) pollAndSubmit(ctx context.Context) error {
	cursor := r.cursorStore.Get()

	channels := []string{"System", "Security"}
	allEvents := make([]api.EventInput, 0, 64)
	newest := cursor

	for _, ch := range channels {
		events, err := r.queryChannel(ctx, ch, cursor)
		if err != nil {
			log.Warn().Err(err).Str("channel", ch).Msg("query channel failed")
			continue
		}
		for _, ev := range events {
			if ev.OccurredAt.After(newest) {
				newest = ev.OccurredAt
			}
		}
		allEvents = append(allEvents, events...)
	}

	if len(allEvents) == 0 {
		return nil
	}

	submitCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if err := r.client.SubmitEvents(submitCtx, api.EventBatch{Events: allEvents}); err != nil {
		return fmt.Errorf("submit events: %w", err)
	}

	if newest.After(cursor) {
		r.cursorStore.Set(newest)
	}
	log.Info().Int("count", len(allEvents)).Msg("events submitted")
	return nil
}

// queryChannel runs `wevtutil qe` to fetch events from the given channel
// since the cursor timestamp. Output is XML, parsed inline.
func (r *Reader) queryChannel(ctx context.Context, channel string, since time.Time) ([]api.EventInput, error) {
	ids := make([]int, 0, len(eventIDToType))
	for id := range eventIDToType {
		ids = append(ids, id)
	}

	queryXPath := buildXPath(ids, since)

	args := []string{
		"qe", channel,
		"/q:" + queryXPath,
		"/c:200",
		"/rd:false",
		"/f:xml",
	}

	cmd := exec.CommandContext(ctx, "wevtutil", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("wevtutil: %w", err)
	}

	return parseEventsXML(out)
}

// buildXPath constructs the XPath expression for wevtutil to filter on
// our tracked event IDs and a "since" timestamp.
func buildXPath(ids []int, since time.Time) string {
	idClauses := ""
	for i, id := range ids {
		if i > 0 {
			idClauses += " or "
		}
		idClauses += "EventID=" + strconv.Itoa(id)
	}
	timeClause := ""
	if !since.IsZero() {
		timeClause = fmt.Sprintf(" and TimeCreated[@SystemTime>'%s']", since.UTC().Format(time.RFC3339Nano))
	}
	return fmt.Sprintf("*[System[(%s)%s]]", idClauses, timeClause)
}

// === XML parsing ===

type winEventRoot struct {
	XMLName xml.Name      `xml:"Events"`
	Events  []winEvent    `xml:"Event"`
}

type winEvent struct {
	System winEventSystem `xml:"System"`
	Data   []winEventData `xml:"EventData>Data"`
}

type winEventSystem struct {
	EventID     int           `xml:"EventID"`
	Channel     string        `xml:"Channel"`
	Computer    string        `xml:"Computer"`
	TimeCreated winEventTime  `xml:"TimeCreated"`
	Security    winEventSecurity `xml:"Security"`
}

type winEventTime struct {
	SystemTime string `xml:"SystemTime,attr"`
}

type winEventSecurity struct {
	UserID string `xml:"UserID,attr"`
}

type winEventData struct {
	Name  string `xml:"Name,attr"`
	Value string `xml:",chardata"`
}

// parseEventsXML accepts the XML output of `wevtutil qe`.
// wevtutil emits a sequence of <Event> elements (no root); we wrap
// them in <Events> before unmarshaling.
func parseEventsXML(data []byte) ([]api.EventInput, error) {
	wrapped := append([]byte("<Events>"), data...)
	wrapped = append(wrapped, []byte("</Events>")...)

	var root winEventRoot
	if err := xml.Unmarshal(wrapped, &root); err != nil {
		return nil, fmt.Errorf("unmarshal xml: %w", err)
	}

	out := make([]api.EventInput, 0, len(root.Events))
	for _, e := range root.Events {
		typeName, ok := eventIDToType[e.System.EventID]
		if !ok {
			continue
		}

		ts, err := time.Parse(time.RFC3339Nano, e.System.TimeCreated.SystemTime)
		if err != nil {
			continue
		}

		var userName *string
		for _, d := range e.Data {
			if d.Name == "TargetUserName" || d.Name == "SubjectUserName" {
				if d.Value != "" {
					v := d.Value
					userName = &v
					break
				}
			}
		}

		eid := e.System.EventID
		meta := map[string]string{"channel": e.System.Channel, "computer": e.System.Computer}
		metaJSON, _ := json.Marshal(meta)

		out = append(out, api.EventInput{
			EventType:      typeName,
			OccurredAt:     ts.UTC(),
			WindowsEventID: &eid,
			UserName:       userName,
			Metadata:       metaJSON,
		})
	}
	return out, nil
}
