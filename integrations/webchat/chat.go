// Package webchat connects a temporary local chat UI to explicit Wisp capabilities.
// Messages are facts, replies require a tool, and no chat history enters context.
package webchat

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"

	wisp "github.com/jacobm-gavin/wisp-agent"
)

type Message struct {
	ID    string `json:"id"`
	Text  string `json:"text"`
	Reply string `json:"reply,omitempty"`
	RunID string `json:"run_id,omitempty"`
	Error string `json:"error,omitempty"`
}

// Chat holds up to 100 messages in memory for the lifetime of the host process.
// It is shared by local browser tabs, not an authenticated multi-user service.
type Chat struct {
	mu       sync.Mutex
	emit     wisp.Emit
	messages []*Message
}

func New() *Chat                           { return &Chat{} }
func (c *Chat) Messages() wisp.EventSource { return source{c} }
func (c *Chat) Respond() wisp.Tool         { return replyTool{c} }

type source struct{ c *Chat }

func (source) Definition() wisp.EventDefinition {
	return wisp.EventDefinition{Name: "web.message", Description: "A user submits a message in the temporary local chat."}
}
func (s source) Run(ctx context.Context, emit wisp.Emit) error {
	s.c.mu.Lock()
	if s.c.emit != nil {
		s.c.mu.Unlock()
		return errors.New("chat source already running")
	}
	s.c.emit = emit
	s.c.mu.Unlock()
	defer func() { s.c.mu.Lock(); s.c.emit = nil; s.c.mu.Unlock() }()
	<-ctx.Done()
	return ctx.Err()
}

type replyTool struct{ c *Chat }

func (replyTool) Definition() wisp.ToolDefinition {
	return wisp.ToolDefinition{Name: "respond_to_user", Description: "Send one reply to the temporary chat message identified by message_id in the triggering event. Final model text is not delivered. Do not reply to another message ID.", Parameters: json.RawMessage(`{"type":"object","properties":{"message_id":{"type":"string"},"text":{"type":"string"}},"required":["message_id","text"],"additionalProperties":false}`)}
}
func (t replyTool) Execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var args struct {
		ID   string `json:"message_id"`
		Text string `json:"text"`
	}
	if err := decode(strings.NewReader(string(raw)), &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Text) == "" || len(args.Text) > 16384 {
		return nil, errors.New("reply must be 1–16384 bytes")
	}
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	for _, m := range t.c.messages {
		if m.ID == args.ID {
			if m.Reply != "" {
				return nil, errors.New("message already has a reply")
			}
			m.Reply = args.Text
			return json.RawMessage(`{"delivered":true}`), nil
		}
	}
	return nil, errors.New("unknown temporary message ID")
}

// Handler must be mounted explicitly alongside Runtime.Handler on a loopback host.
func (c *Chat) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/chat", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(struct {
			Messages []*Message `json:"messages"`
			Ready    bool       `json:"ready"`
		}{c.messages, c.emit != nil})
	})
	mux.HandleFunc("POST /api/chat/messages", func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host || u.Scheme != "http" {
				http.Error(w, "same-origin requests required", 403)
				return
			}
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "same-origin requests required", 403)
			return
		}
		media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if media != "application/json" {
			http.Error(w, "application/json required", 415)
			return
		}
		var args struct {
			Text string `json:"text"`
		}
		if err := decode(http.MaxBytesReader(w, r.Body, 20000), &args); err != nil || strings.TrimSpace(args.Text) == "" || len(args.Text) > 8192 {
			http.Error(w, "message must be 1–8192 bytes of text", 400)
			return
		}
		c.mu.Lock()
		emit := c.emit
		if emit == nil {
			c.mu.Unlock()
			http.Error(w, "chat source is not ready", 503)
			return
		}
		if len(c.messages) >= 100 {
			c.mu.Unlock()
			http.Error(w, "temporary chat is full; restart the host for a new session", 429)
			return
		}
		m := &Message{ID: rand.Text(), Text: args.Text}
		c.messages = append(c.messages, m)
		c.mu.Unlock()
		data, _ := json.Marshal(struct {
			ID   string `json:"message_id"`
			Text string `json:"text"`
		}{m.ID, args.Text})
		id, err := emit(r.Context(), wisp.Event{Data: data})
		c.mu.Lock()
		defer c.mu.Unlock()
		if err != nil {
			m.Error = "Message was not accepted: " + err.Error()
			http.Error(w, "message was not accepted", 503)
			return
		}
		m.RunID = id
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(m)
	})
	return mux
}
func decode(r io.Reader, out any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}
