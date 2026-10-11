package main

import (
	"sync"
	"time"
)

// lastModelEntry is the model of the newest request seen for one credential.
type lastModelEntry struct {
	model       string
	requestedAt time.Time
}

var (
	lastModelsMu sync.Mutex
	// lastModels is keyed by auth index, the same id the usage document
	// reports per account. It covers requests since plugin load only.
	lastModels = map[string]lastModelEntry{}
)

// recordLastModel remembers the model of the newest request per credential.
// The host dispatches usage records asynchronously, so a late record for an
// older request must not replace a newer one. A record with the same request
// time is a follow-up for the same request, such as the image tool's token
// record after a Codex response, and must not replace the request's model.
func recordLastModel(rec usageRecord) {
	if rec.AuthIndex == "" || rec.Model == "" {
		return
	}
	at := rec.RequestedAt
	if at.IsZero() {
		at = time.Now()
	}

	lastModelsMu.Lock()
	defer lastModelsMu.Unlock()
	if current, ok := lastModels[rec.AuthIndex]; ok && !at.After(current.requestedAt) {
		return
	}
	lastModels[rec.AuthIndex] = lastModelEntry{model: rec.Model, requestedAt: at}
}

// lastModelFor returns the model of the newest request for a credential, or
// "" when none has been seen since plugin load.
func lastModelFor(authIndex string) string {
	if authIndex == "" {
		return ""
	}
	lastModelsMu.Lock()
	defer lastModelsMu.Unlock()
	return lastModels[authIndex].model
}
