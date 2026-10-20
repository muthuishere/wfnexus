package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Channels (ADR 0023): the server's configured places to send and receive
// messages. Every verb here goes through the server's API, so `wfx send`
// travels exactly the path a `send:` step does.

// sendCmd is `wfx send [--channel c] [--file f | --url u] [--target t] text…`.
func sendCmd(args []string) error {
	var text []string
	valued := map[string]bool{"--channel": true, "-c": true, "--file": true, "--url": true, "--target": true, "--as": true}
	for i := 0; i < len(args); i++ {
		if valued[args[i]] {
			i++
			continue
		}
		if args[i] == "--json" {
			continue
		}
		text = append(text, args[i])
	}
	body := map[string]any{
		"channel": or(flagOf(args, "--channel", ""), flagOf(args, "-c", "")),
		"text":    strings.Join(text, " "),
		"target":  flagOf(args, "--target", ""),
		"actor":   actorOf(args),
	}
	if u := flagOf(args, "--url", ""); u != "" {
		body["attachment"] = u
	}
	if f := flagOf(args, "--file", ""); f != "" {
		// The file is read HERE and sent in the body: a path on this machine
		// means nothing on the server.
		raw, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		body["file"] = map[string]any{"name": filepath.Base(f), "data": base64.StdEncoding.EncodeToString(raw)}
	}
	if body["text"] == "" && body["file"] == nil && body["attachment"] == nil {
		return fmt.Errorf("usage: wfx send [--channel c] [--file f | --url u] text")
	}
	var out struct {
		Channel string `json:"channel"`
		ID      string `json:"id"`
	}
	if err := call("POST", "/api/channels/send", body, &out); err != nil {
		return err
	}
	if has(args, "--json") {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	if out.ID != "" {
		fmt.Printf("sent on %s (id %s)\n", out.Channel, out.ID)
	} else {
		fmt.Printf("sent on %s\n", out.Channel)
	}
	return nil
}

// messagesCmd is `wfx messages [--channel c] [--run id] [--limit n]`, newest first.
func messagesCmd(args []string) error {
	q := url.Values{}
	for flag, key := range map[string]string{"--channel": "channel", "--run": "run", "--limit": "limit"} {
		if v := flagOf(args, flag, ""); v != "" {
			q.Set(key, v)
		}
	}
	var msgs []struct {
		Channel    string `json:"channel"`
		MsgID      string `json:"msgId"`
		From       string `json:"from"`
		Name       string `json:"name"`
		Text       string `json:"text"`
		RunID      string `json:"runId"`
		Outcome    string `json:"outcome"`
		ReceivedAt string `json:"receivedAt"`
	}
	if err := call("GET", "/api/channels/messages?"+q.Encode(), nil, &msgs); err != nil {
		return err
	}
	if has(args, "--json") {
		return json.NewEncoder(os.Stdout).Encode(msgs)
	}
	if len(msgs) == 0 {
		fmt.Println("no messages received")
		return nil
	}
	for _, m := range msgs {
		who := m.From
		if m.Name != "" {
			who = m.Name + " <" + m.From + ">"
		}
		note := m.Outcome
		if m.RunID != "" {
			note += " run " + m.RunID
		}
		fmt.Printf("%-19.19s  %-10s %s: %s", m.ReceivedAt, m.Channel, who, firstLine(m.Text))
		if note != "" {
			fmt.Printf("   [%s]", strings.TrimSpace(note))
		}
		fmt.Println()
	}
	return nil
}

// channelsCmd lists channels; `wfx channels receive <name>` polls one now.
func channelsCmd(args []string) error {
	if len(args) >= 2 && args[0] == "receive" {
		var out struct {
			New int `json:"new"`
		}
		if err := call("POST", "/api/channels/"+url.PathEscape(args[1])+"/receive", map[string]any{}, &out); err != nil {
			return err
		}
		fmt.Printf("%d new message(s) on %s\n", out.New, args[1])
		return nil
	}
	var out struct {
		Default  string `json:"default"`
		Channels []struct {
			Name        string   `json:"name"`
			Kind        string   `json:"kind"`
			Default     bool     `json:"default"`
			Inbound     bool     `json:"inbound"`
			Allow       []string `json:"allow"`
			Cursor      string   `json:"cursor"`
			LastReceive string   `json:"lastReceive"`
			LastError   string   `json:"lastError"`
		} `json:"channels"`
	}
	if err := call("GET", "/api/channels", nil, &out); err != nil {
		return err
	}
	if has(args, "--json") {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	if len(out.Channels) == 0 {
		fmt.Println("no channels registered (registries.json `channels`)")
		return nil
	}
	if out.Default == "" {
		fmt.Println("no default channel: pauses reach notifiers only (channels.default / WFX_DEFAULT_CHANNEL)")
	}
	for _, c := range out.Channels {
		mark := " "
		if c.Default {
			mark = "*"
		}
		dir := "send"
		if c.Inbound {
			dir = "send+receive"
		}
		fmt.Printf("%s %-14s %-8s %-13s allow=%d", mark, c.Name, c.Kind, dir, len(c.Allow))
		if c.LastError != "" {
			fmt.Printf("  last receive FAILED: %s", firstLine(c.LastError))
		}
		fmt.Println()
	}
	return nil
}
