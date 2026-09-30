package main

import wisp "github.com/jacobm-gavin/wisp-agent"

// Agent is intentionally dormant until capabilities are explicitly declared.
var Agent = wisp.Agent{
	Name:         "Minimal agent",
	Model:        "qwen",
	Instructions: []string{"instructions/base.md"},
	Events:       []wisp.EventSource{},
	Tools:        []wisp.Tool{},
}
