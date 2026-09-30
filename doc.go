// Package wisp implements a minimal, declarative runtime for persistent agents.
// Each event creates a fresh run. Models request explicitly declared tools;
// execution history is durable but is never implicitly reused as context.
package wisp
