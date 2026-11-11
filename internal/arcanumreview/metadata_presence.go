package arcanumreview

import (
	"errors"
	"regexp"
	"sync"
	"time"
)

const metadataPresenceTTL = 45 * time.Second

var presenceClientPattern = regexp.MustCompile(`^[a-zA-Z0-9-]{1,64}$`)

type metadataPage struct {
	sequence int64
	active   bool
	expires  time.Time
}

// MetadataPresence is ephemeral display-read permission, never polling authority.
// Inactive records fence delayed heartbeats until their lease expires.
type MetadataPresence struct {
	mu      sync.Mutex
	pages   map[string]metadataPage
	since   time.Time
	changed chan struct{}
}

func NewMetadataPresence() *MetadataPresence {
	return &MetadataPresence{pages: make(map[string]metadataPage), changed: make(chan struct{}, 1)}
}
func (p *MetadataPresence) Update(id string, sequence int64, active, refresh bool, now time.Time) error {
	if !presenceClientPattern.MatchString(id) || sequence <= 0 {
		return errors.New("valid client_id and positive sequence required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	wasActive := p.activeLocked(now)
	old, exists := p.pages[id]
	if exists && sequence <= old.sequence {
		return nil
	}
	if !exists && len(p.pages) >= 1024 {
		return errors.New("too many board presence clients")
	}
	p.pages[id] = metadataPage{sequence: sequence, active: active, expires: now.Add(metadataPresenceTTL)}
	if active && (!wasActive || refresh) {
		p.since = now
	}
	select {
	case p.changed <- struct{}{}:
	default:
	}
	return nil
}
func (p *MetadataPresence) activeLocked(now time.Time) bool {
	active := false
	for id, page := range p.pages {
		if !now.Before(page.expires) {
			delete(p.pages, id)
		} else if page.active {
			active = true
		}
	}
	return active
}
func (p *MetadataPresence) State(now time.Time) (bool, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.activeLocked(now), p.since
}
