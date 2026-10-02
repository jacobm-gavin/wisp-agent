package main

import (
	wisp "github.com/jacobm-gavin/wisp-agent"
	"github.com/jacobm-gavin/wisp-agent/integrations/webchat"
	"github.com/jacobm-gavin/wisp-agent/tools/bash"
	"github.com/jacobm-gavin/wisp-agent/tools/files"
)

var chat = webchat.New()

var Agent = wisp.Agent{
	Name:         "Temporary chat demo",
	Model:        "qwen",
	Instructions: []string{"instructions/base.md"},
	Events:       []wisp.EventSource{chat.Messages()},
	Tools:        []wisp.Tool{chat.Respond()},
}

// workspaceAgent explicitly grants the optional developer-demo capabilities.
func workspaceAgent(path string) (wisp.Agent, func() error, error) {
	w, err := files.Open(path)
	if err != nil {
		return wisp.Agent{}, nil, err
	}
	shell, err := bash.New(path)
	if err != nil {
		w.Close()
		return wisp.Agent{}, nil, err
	}
	agent := Agent
	agent.Name = "Workspace chat demo"
	agent.Tools = []wisp.Tool{chat.Respond(), w.ReadFiles(), w.WriteFiles(), shell}
	return agent, w.Close, nil
}
