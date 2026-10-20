package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/muthuishere/wfnexus/apps/api/internal/notify"
)

// Channels (ADR 0023): list them, send on one, read what came in, and poll
// one now. Sending is a write on runs — it is the same act a `send:` step
// performs — and reading is a read on runs.

func (s *Server) channelRoutes(r interface {
	Get(string, http.HandlerFunc)
	Post(string, http.HandlerFunc)
}) {
	r.Get("/channels", s.listChannels)
	r.Get("/channels/messages", s.listChannelMessages)
	r.Post("/channels/send", s.sendOnChannel)
	r.Post("/channels/{name}/receive", s.receiveChannel)
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"default": s.eng.DefaultChannel(), "channels": s.eng.Channels(r.Context())})
}

func (s *Server) listChannelMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	msgs, err := s.eng.ChannelMessages(r.Context(), q.Get("channel"), q.Get("run"), limit)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, msgs)
}

// maxSendFile bounds an uploaded attachment; the body is base64, so the
// request is a third larger.
const maxSendFile = 25 << 20

type sendBody struct {
	Channel string `json:"channel"`
	Text    string `json:"text"`
	Target  string `json:"target"`
	// Attachment is a URL the adapter fetches. A file from the caller's own
	// machine comes as File instead, because a path there means nothing here.
	Attachment string `json:"attachment"`
	File       *struct {
		Name string `json:"name"`
		Data string `json:"data"` // base64
	} `json:"file"`
}

func (s *Server) sendOnChannel(w http.ResponseWriter, r *http.Request) {
	var b sendBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSendFile*4/3+1<<20)).Decode(&b); err != nil {
		writeErr(w, 400, err)
		return
	}
	if b.Text == "" && b.File == nil && b.Attachment == "" {
		writeErr(w, 400, errors.New("nothing to send: give text, a file, or an attachment url"))
		return
	}
	env := notify.Envelope{Type: notify.TypeSend, Text: b.Text, Target: b.Target}
	if b.Attachment != "" {
		env.Attachment = &notify.Attachment{URL: b.Attachment}
	}
	if b.File != nil {
		raw, err := base64.StdEncoding.DecodeString(b.File.Data)
		if err != nil || len(raw) > maxSendFile {
			writeErr(w, 400, fmt.Errorf("file must be base64 and at most %d MB", maxSendFile>>20))
			return
		}
		dir, err := os.MkdirTemp("", "wfx-send-")
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		defer os.RemoveAll(dir)
		name := filepath.Base(b.File.Name)
		if name == "." || name == "/" || name == "" {
			name = "attachment"
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			writeErr(w, 500, err)
			return
		}
		env.Attachment = &notify.Attachment{Path: path, Filename: name}
	}
	who := actorOf(r, stepBody{})
	ch, res, err := s.eng.SendOnChannel(r.Context(), b.Channel, env)
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	log.Printf("channels: %s sent a message on %s", who.String(), ch)
	writeJSON(w, 200, map[string]any{"ok": true, "channel": ch, "id": res.ID})
}

func (s *Server) receiveChannel(w http.ResponseWriter, r *http.Request) {
	n, err := s.eng.ReceiveNow(r.Context(), urlName(r, "name"))
	if err != nil {
		writeErr(w, 502, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "new": n})
}
