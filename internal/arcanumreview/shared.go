package arcanumreview

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"
)

const SearchInterval = 30 * time.Second
const SlowInterval = 5 * time.Minute
const InfoInterval = 10 * time.Minute

type PRNeed struct {
	Author           string
	Comments, Checks bool
}
type Demand struct {
	PRs     map[int64]PRNeed
	Queries []string
}
type PRObservation struct {
	Snapshot              PRSnapshot
	StartedAt, FinishedAt time.Time
	Err                   error
}
type SharedStore interface {
	GetState(context.Context, string) (string, error)
	SetState(context.Context, string, string) error
	PRIsMerged(context.Context, int64) (bool, error)
	PublishPRObservation(context.Context, int64, string, string, time.Time, time.Time, bool) error
}
type Coverage struct {
	Groups                     map[string]bool
	SearchAt                   time.Time
	CommentsAt, ChecksAt       time.Time
	Comments, Checks           bool
	CommentsError, ChecksError string
}
type PendingRead struct {
	NextAt    time.Time
	AttemptAt time.Time
	Cause     error
}

func (e *PendingRead) Error() string {
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "waiting for scheduled Arcanum observation"
}
func (e *PendingRead) Unwrap() error { return e.Cause }

type searchObservation struct {
	finished time.Time
	result   SearchResult
	at       time.Time
	err      error
}
type slowObservation struct {
	revision               uint64
	base                   PRSnapshot
	comments               []PRComment
	checks                 []PRCheck
	commentsAt, checksAt   time.Time
	commentsErr, checksErr error
}

// SharedReader owns network work. Consumer methods only read observations.
// Three fixed workers keep search independent of slow IO and SQLite publishing.
type SharedReader struct {
	cli              *CLIClient
	store            SharedStore
	now              func() time.Time
	mu               sync.Mutex
	searchMu, slowMu sync.Mutex
	demand           Demand
	facts            map[int64]PRObservation
	slow             map[int64]slowObservation
	searches         map[string]searchObservation
	authors          map[int64]string
	terminal         map[int64]bool
	pending          map[int64]PRObservation
	pendingGood      map[int64]PRObservation
	changed          chan struct{}
	publishReady     chan struct{}
	slowQueue        []int64
	slowPosition     int
	queueStepStarted time.Time
	// RefreshDemand re-reads the compact local demand immediately before IO.
	revisions      map[int64]uint64
	statusAttempts map[int64]PRObservation
	RefreshDemand  func(context.Context) error
}

func NewSharedReader(cli *CLIClient, store SharedStore) *SharedReader {
	return &SharedReader{cli: cli, store: store, now: time.Now, demand: Demand{PRs: map[int64]PRNeed{}}, facts: map[int64]PRObservation{}, slow: map[int64]slowObservation{}, searches: map[string]searchObservation{}, authors: map[int64]string{}, terminal: map[int64]bool{}, pending: map[int64]PRObservation{}, pendingGood: map[int64]PRObservation{}, changed: make(chan struct{}, 1), publishReady: make(chan struct{}, 1), revisions: map[int64]uint64{}, statusAttempts: map[int64]PRObservation{}}
}
func (r *SharedReader) Changed() <-chan struct{} { return r.changed }
func (r *SharedReader) signal() {
	select {
	case r.publishReady <- struct{}{}:
	default:
	}
	select {
	case r.changed <- struct{}{}:
	default:
	}
}
func (r *SharedReader) SetDemand(ctx context.Context, d Demand) error {
	if d.PRs == nil {
		d.PRs = map[int64]PRNeed{}
	}
	// Load only facts relevant to this active scope; never scan the terminal ledger.
	for id, n := range d.PRs {
		r.mu.Lock()
		known := r.terminal[id]
		author := r.authors[id]
		r.mu.Unlock()
		if !known {
			merged, err := r.store.PRIsMerged(ctx, id)
			if err != nil {
				return err
			}
			if merged {
				r.mu.Lock()
				r.terminal[id] = true
				r.mu.Unlock()
			}
		}
		if n.Author == "" && author == "" {
			hint, err := r.store.GetState(ctx, "arc.author."+strconv.FormatInt(id, 10))
			if err != nil {
				return err
			}
			author = hint
		}
		if n.Author != "" {
			author = n.Author
		}
		if loginPattern.MatchString(author) {
			r.mu.Lock()
			r.authors[id] = author
			r.mu.Unlock()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.demand = Demand{PRs: map[int64]PRNeed{}, Queries: slices.Clone(d.Queries)}
	for id, n := range d.PRs {
		r.demand.PRs[id] = n
	}
	// Newly discovered source candidates reuse the discovery row immediately.
	for id := range d.PRs {
		for _, search := range r.searches {
			for _, item := range search.result.Items {
				if item.ID != id || item.Snapshot.Status == "" || (item.Snapshot.CommonStatus == "" && item.Snapshot.Status != "merged") {
					continue
				}
				if r.terminal[id] && item.Snapshot.Status != "merged" {
					continue
				}
				old := r.facts[id]
				if old.StartedAt.Before(search.at) || item.Snapshot.Status == "merged" && !r.terminal[id] {
					o := PRObservation{Snapshot: item.Snapshot, StartedAt: search.at, FinishedAt: search.finished}
					if !samePRFacts(old.Snapshot, o.Snapshot) {
						r.revisions[id]++
					}
					r.facts[id] = o
					r.pending[id] = o
					r.pendingGood[id] = o
					if item.Snapshot.Status == "merged" {
						r.terminal[id] = true
					}
				}
			}
		}
	}
	for id := range r.facts {
		if _, ok := d.PRs[id]; !ok {
			delete(r.facts, id)
			delete(r.slow, id)
		}
	}
	// Search pages belong to current queries only. Author rows are kept only for demand.
	keep := map[string]bool{}
	for _, q := range d.Queries {
		keep[q] = true
	}
	for q := range r.searches {
		if !keep[q] {
			delete(r.searches, q)
		}
	}
	for id := range r.authors {
		if _, ok := d.PRs[id]; !ok {
			delete(r.authors, id)
			if r.pendingGood[id].Snapshot.Status != "merged" {
				delete(r.terminal, id)
			}
		}
	}
	for id := range r.revisions {
		if _, ok := d.PRs[id]; !ok {
			delete(r.revisions, id)
			delete(r.statusAttempts, id)
			if r.pendingGood[id].Snapshot.Status != "merged" {
				delete(r.terminal, id)
			}
		}
	}
	return nil
}
func (r *SharedReader) deadline(ctx context.Context, key string) (time.Time, error) {
	v, err := r.store.GetState(ctx, "arc.cooldown."+key)
	if err != nil {
		return time.Time{}, err
	}
	if v == "" {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339Nano, v)
	return at, err
}
func (r *SharedReader) reserve(ctx context.Context, key string, interval time.Duration) (bool, error) {
	at, err := r.deadline(ctx, key)
	if err != nil {
		return false, err
	}
	now := r.now().UTC()
	if now.Before(at) {
		return false, nil
	}
	return true, r.store.SetState(ctx, "arc.cooldown."+key, now.Add(interval).Format(time.RFC3339Nano))
}
func queryKey(q string) string {
	return fmt.Sprintf("search.%x", sha256.Sum256([]byte(q+SearchFields+"100")))
}
func (r *SharedReader) active(id int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.demand.PRs[id]
	return ok && !r.terminal[id]
}
func (r *SharedReader) refresh(ctx context.Context) error {
	if r.RefreshDemand != nil {
		return r.RefreshDemand(ctx)
	}
	return nil
}
func (r *SharedReader) put(ctx context.Context, o PRObservation) {
	id := o.Snapshot.ID
	r.mu.Lock()
	if r.terminal[id] && o.Snapshot.Status != "merged" {
		r.mu.Unlock()
		return
	}
	if old, ok := r.facts[id]; ok && old.StartedAt.After(o.StartedAt) && o.Snapshot.Status != "merged" {
		r.mu.Unlock()
		return
	}
	if o.Snapshot.Status == "merged" && o.Err == nil {
		r.terminal[id] = true
	}
	if o.Err == nil {
		if !samePRFacts(r.facts[id].Snapshot, o.Snapshot) {
			r.revisions[id]++
		}
		r.facts[id] = o
		r.pendingGood[id] = o
		delete(r.statusAttempts, id)
		if loginPattern.MatchString(o.Snapshot.Author.Name) {
			r.authors[id] = o.Snapshot.Author.Name
		}
	}
	if old, ok := r.pending[id]; !ok || !old.StartedAt.After(o.StartedAt) || o.Snapshot.Status == "merged" {
		r.pending[id] = o
	}
	r.mu.Unlock()
	r.signal()
	if o.Err == nil && loginPattern.MatchString(o.Snapshot.Author.Name) {
		_ = r.store.SetState(ctx, "arc.author."+strconv.FormatInt(id, 10), o.Snapshot.Author.Name)
	}
}
func (r *SharedReader) SearchOnce(ctx context.Context) error {
	r.searchMu.Lock()
	defer r.searchMu.Unlock()
	if err := r.refresh(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	queries := slices.Clone(r.demand.Queries)
	r.mu.Unlock()
	// Discovery runs first. Positive rows can cover known PRs without author IO.
	covered := map[int64]bool{}
	var failures []error
	run := func(q string, wanted map[int64]bool) {
		if ctx.Err() != nil {
			return
		}
		if err := r.refresh(ctx); err != nil {
			failures = append(failures, err)
			return
		}
		r.mu.Lock()
		if wanted == nil && !slices.Contains(r.demand.Queries, q) {
			r.mu.Unlock()
			return
		}
		if wanted != nil {
			for id := range wanted {
				if _, ok := r.demand.PRs[id]; !ok || r.terminal[id] {
					delete(wanted, id)
				}
			}
			if len(wanted) == 0 {
				r.mu.Unlock()
				return
			}
		}
		r.mu.Unlock()
		allowed, err := r.reserve(ctx, queryKey(q), SearchInterval)
		if err != nil {
			failures = append(failures, err)
			return
		}
		if !allowed {
			return
		}
		started := r.now().UTC()
		result, err := r.cli.Search(ctx, q, 100, wanted)
		finished := r.now().UTC()
		if waitErr := r.backoff(ctx, queryKey(q), err); waitErr != nil {
			failures = append(failures, waitErr)
		}
		r.mu.Lock()
		if wanted == nil {
			r.searches[q] = searchObservation{result: result, at: started, finished: finished, err: err}
		}
		r.mu.Unlock()
		for _, item := range result.Items {
			covered[item.ID] = item.Snapshot.Status != "" && (item.Snapshot.CommonStatus != "" || item.Snapshot.Status == "merged")
			r.mu.Lock()
			_, needed := r.demand.PRs[item.ID]
			r.mu.Unlock()
			if needed && item.Snapshot.Status != "" && (item.Snapshot.CommonStatus != "" || item.Snapshot.Status == "merged") {
				r.put(ctx, PRObservation{Snapshot: item.Snapshot, StartedAt: started, FinishedAt: finished})
			}
		}
		if err != nil {
			failures = append(failures, err)
		}
		// Only missing/failed status reads mark a footer attempt failed.
		if wanted != nil {
			for id := range wanted {
				if !covered[id] {
					r.mu.Lock()
					r.statusAttempts[id] = PRObservation{Snapshot: PRSnapshot{ID: id}, StartedAt: started, FinishedAt: finished, Err: errors.New("PR search status unavailable")}
					r.mu.Unlock()
					r.put(ctx, PRObservation{Snapshot: PRSnapshot{ID: id}, StartedAt: started, FinishedAt: finished, Err: errors.New("PR search status unavailable")})
				}
			}
		}
		r.signal()
	}
	for _, q := range queries {
		run(q, nil)
	}
	if err := r.refresh(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	byAuthor := map[string]map[int64]bool{}
	// A positive row in a healthy active discovery query owns its next refresh.
	// Tick/IO skew near the 30s boundary must not create a duplicate author query.
	discoveryCovered := map[int64]bool{}
	for _, query := range r.demand.Queries {
		observation, ok := r.searches[query]
		if !ok || observation.err != nil {
			continue
		}
		for _, item := range observation.result.Items {
			if item.Snapshot.Status != "" && (item.Snapshot.CommonStatus != "" || item.Snapshot.Status == "merged") {
				discoveryCovered[item.ID] = true
			}
		}
	}
	for id := range r.demand.PRs {
		if r.terminal[id] || covered[id] || discoveryCovered[id] || (!r.facts[id].StartedAt.IsZero() && r.now().Sub(r.facts[id].StartedAt) < SearchInterval) {
			continue
		}
		author := r.authors[id]
		if author != "" {
			if byAuthor[author] == nil {
				byAuthor[author] = map[int64]bool{}
			}
			byAuthor[author][id] = true
		}
	}
	r.mu.Unlock()
	authors := make([]string, 0, len(byAuthor))
	for author := range byAuthor {
		authors = append(authors, author)
	}
	slices.Sort(authors)
	for _, author := range authors {
		ids := byAuthor[author]
		for id := range ids {
			if !r.active(id) {
				delete(ids, id)
			}
		}
		if len(ids) > 0 {
			run("author("+author+")", ids)
		}
	}
	return errors.Join(failures...)
}
func (r *SharedReader) SlowOnce(ctx context.Context) error {
	r.slowMu.Lock()
	defer r.slowMu.Unlock()
	if err := r.refresh(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	ids := make([]int64, 0, len(r.demand.PRs))
	for id := range r.demand.PRs {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	slices.Sort(ids)
	r.mu.Lock()
	r.slowQueue = slices.Clone(ids)
	r.queueStepStarted = time.Time{}
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.slowQueue = nil; r.mu.Unlock() }()
	var failures []error
	for position, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := r.refresh(ctx); err != nil {
			return err
		}
		if !r.active(id) {
			continue
		}
		r.mu.Lock()
		n := r.demand.PRs[id]
		o := r.facts[id]
		author := r.authors[id]
		failedStatus := r.statusAttempts[id]
		revision := r.revisions[id]
		r.mu.Unlock()
		key := strconv.FormatInt(id, 10)
		if o.Snapshot.Status == "" || r.now().Sub(o.StartedAt) > 2*SearchInterval {
			if author != "" && failedStatus.StartedAt.IsZero() {
				continue
			}
			allowed, err := r.reserve(ctx, "get."+key, InfoInterval)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if allowed {
				started := r.now().UTC()
				r.noteSlowStart(position, started)
				s, err := r.cli.ReadPRInfo(ctx, id)
				if waitErr := r.backoff(ctx, "get."+key, err); waitErr != nil {
					failures = append(failures, waitErr)
				}
				if loginPattern.MatchString(s.Author.Name) {
					r.mu.Lock()
					r.authors[id] = s.Author.Name
					r.mu.Unlock()
					_ = r.store.SetState(ctx, "arc.author."+key, s.Author.Name)
				}
				if err == nil && s.Status == "merged" {
					r.put(ctx, PRObservation{Snapshot: s, StartedAt: started, FinishedAt: r.now().UTC()})
				}
				if err == nil && s.Status != "merged" && !loginPattern.MatchString(s.Author.Name) {
					err = errors.New("PR author unavailable")
				}
				if err != nil {
					observation := PRObservation{Snapshot: PRSnapshot{ID: id}, StartedAt: started, FinishedAt: r.now().UTC(), Err: err}
					r.mu.Lock()
					r.statusAttempts[id] = observation
					r.mu.Unlock()
					r.put(ctx, observation)
					failures = append(failures, err)
				}
			}
			continue
		}
		if !n.Comments && !n.Checks {
			continue
		}
		ca, err := r.deadline(ctx, "comments."+key)
		if err != nil {
			return err
		}
		ka, err := r.deadline(ctx, "checks."+key)
		if err != nil {
			return err
		}
		if r.now().Before(ca) || r.now().Before(ka) {
			continue
		}
		// Freeze one fresh search generation for the union of all slow needs.
		if r.now().Sub(o.StartedAt) > SearchInterval {
			continue
		}
		so := slowObservation{base: o.Snapshot, revision: revision}
		if n.Comments && r.active(id) {
			allowed, err := r.reserve(ctx, "comments."+key, SlowInterval)
			if err != nil {
				return err
			}
			if allowed {
				so.commentsAt = r.now().UTC()
				r.noteSlowStart(position, so.commentsAt)
				so.comments, so.commentsErr = r.cli.ReadPRComments(ctx, id)
				if waitErr := r.backoff(ctx, "comments."+key, so.commentsErr); waitErr != nil {
					failures = append(failures, waitErr)
				}
			}
		}
		if err := r.refresh(ctx); err != nil {
			return err
		}
		r.mu.Lock()
		n = r.demand.PRs[id]
		stillCurrent := revision == r.revisions[id]
		r.mu.Unlock()
		if n.Checks && stillCurrent && r.active(id) {
			if o.Snapshot.PublishedDiffID > 0 {
				allowed, err := r.reserve(ctx, "checks."+key, SlowInterval)
				if err != nil {
					return err
				}
				if allowed {
					so.checksAt = r.now().UTC()
					r.noteSlowStart(position, so.checksAt)
					so.checks, so.checksErr = r.cli.ReadPRChecks(ctx, o.Snapshot.PublishedDiffID)
					if waitErr := r.backoff(ctx, "checks."+key, so.checksErr); waitErr != nil {
						failures = append(failures, waitErr)
					}
				}
			} else if o.Snapshot.RevisionKnown {
				// An explicitly empty published revision is a search fact, not a fake CLI attempt.
				so.checksAt = o.StartedAt
				so.checks = []PRCheck{}
			}
		}

		r.mu.Lock()
		if !r.terminal[id] {
			r.slow[id] = so
		}
		r.mu.Unlock()
		r.signal()
		if so.commentsErr != nil {
			failures = append(failures, so.commentsErr)
		}
		if so.checksErr != nil {
			failures = append(failures, so.checksErr)
		}
	}
	return errors.Join(failures...)
}
func (r *SharedReader) PublishOnce(ctx context.Context) error {
	r.mu.Lock()
	pending := make(map[int64]PRObservation, len(r.pending))
	for id, o := range r.pending {
		pending[id] = o
	}
	r.mu.Unlock()
	var failures []error
	for id, o := range pending {
		r.mu.Lock()
		good, hasGood := r.pendingGood[id]
		r.mu.Unlock()
		if hasGood {
			if err := r.store.PublishPRObservation(ctx, good.Snapshot.ID, good.Snapshot.Status, good.Snapshot.CommonStatus, good.StartedAt, good.FinishedAt, true); err != nil {
				failures = append(failures, err)
				continue
			}
			r.mu.Lock()
			if latest, ok := r.pendingGood[id]; ok && latest.StartedAt.Equal(good.StartedAt) {
				delete(r.pendingGood, id)
			}
			r.mu.Unlock()
		}
		if !hasGood || o.Err != nil {
			if err := r.store.PublishPRObservation(ctx, o.Snapshot.ID, o.Snapshot.Status, o.Snapshot.CommonStatus, o.StartedAt, o.FinishedAt, o.Err == nil); err != nil {
				failures = append(failures, err)
				continue
			}
		}
		r.mu.Lock()
		if latest, ok := r.pending[id]; ok && latest.StartedAt.Equal(o.StartedAt) {
			delete(r.pending, id)
		}
		r.mu.Unlock()
	}

	return errors.Join(failures...)
}
func (r *SharedReader) Run(ctx context.Context, onError func(error)) {
	var wg sync.WaitGroup
	for _, worker := range []struct {
		period time.Duration
		run    func(context.Context) error
	}{{time.Second, r.SearchOnce}, {time.Second, r.SlowOnce}, {10 * time.Second, r.PublishOnce}} {
		wg.Add(1)
		go func(period time.Duration, run func(context.Context) error) {
			defer wg.Done()
			ticker := time.NewTicker(period)
			defer ticker.Stop()
			for {
				if err := run(ctx); err != nil && onError != nil && ctx.Err() == nil {
					onError(err)
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				case <-func() <-chan struct{} {
					if period == 10*time.Second {
						return r.publishReady
					}
					return nil
				}():
				}
			}
		}(worker.period, worker.run)
	}
	wg.Wait()
}
func cloneSnapshot(s PRSnapshot) PRSnapshot {
	b, _ := json.Marshal(s)
	var result PRSnapshot
	_ = json.Unmarshal(b, &result)
	return result
}
func (r *SharedReader) ReadPRSnapshot(ctx context.Context, id int64) (PRSnapshot, error) {
	r.mu.Lock()
	terminal := r.terminal[id]
	o, ok := r.facts[id]
	attempt := r.statusAttempts[id]
	author := r.authors[id]
	so, hasSlow := r.slow[id]
	revision := r.revisions[id]
	r.mu.Unlock()
	if terminal {
		return PRSnapshot{ID: id, Status: "merged", CommonStatus: "merged", Coverage: &Coverage{}}, nil
	}
	if !ok || r.now().Sub(o.StartedAt) > 2*SearchInterval {
		key := "get." + strconv.FormatInt(id, 10)
		if author != "" {
			key = queryKey("author(" + author + ")")
		}
		next, err := r.deadline(ctx, key)
		if err != nil {
			return PRSnapshot{}, err
		}
		if next.IsZero() {
			next = r.now().Add(SearchInterval)
		}
		return PRSnapshot{}, &PendingRead{NextAt: next, AttemptAt: attempt.StartedAt, Cause: attempt.Err}
	}
	s := cloneSnapshot(o.Snapshot)
	s.Coverage = &Coverage{SearchAt: o.StartedAt}
	if hasSlow {
		s.Coverage.CommentsAt, s.Coverage.ChecksAt = so.commentsAt, so.checksAt
		if so.commentsErr != nil {
			s.Coverage.CommentsError = so.commentsErr.Error()
		}
		if so.checksErr != nil {
			s.Coverage.ChecksError = so.checksErr.Error()
		}
		if so.revision == revision && so.base.HeadCommit == s.HeadCommit && so.base.PublishedDiffID == s.PublishedDiffID && so.base.CommonStatus == s.CommonStatus {
			s.Comments = slices.Clone(so.comments)
			s.Checks = slices.Clone(so.checks)
			s.Coverage.Comments = !so.commentsAt.IsZero() && so.commentsErr == nil
			s.Coverage.Checks = !so.checksAt.IsZero() && so.checksErr == nil
		}
	}

	result := cloneSnapshot(s)
	result.Coverage = s.Coverage
	return result, nil
}
func (r *SharedReader) ReadPRChecks(_ context.Context, diff int64) ([]PRCheck, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, so := range r.slow {
		if so.base.PublishedDiffID == diff && !so.checksAt.IsZero() {
			return slices.Clone(so.checks), so.checksErr
		}
	}
	return nil, &PendingRead{NextAt: r.now().Add(SlowInterval)}
}
func (r *SharedReader) list(q string) ([]PullRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.searches[q]
	if !ok {
		return nil, &PendingRead{NextAt: r.now().Add(SearchInterval)}
	}
	if o.err != nil {
		return nil, &PendingRead{NextAt: o.at.Add(SearchInterval), AttemptAt: o.at, Cause: o.err}
	}
	if !o.result.Complete {
		return nil, errors.New("incomplete discovery search")
	}
	result := make([]PullRequest, 0, len(o.result.Items))
	for _, p := range o.result.Items {
		if p.Author.Name == "" || p.Summary == "" {
			return nil, errors.New("incomplete discovery metadata")
		}
		result = append(result, PullRequest{ID: p.ID, Author: p.Author.Name, Summary: p.Summary})
	}
	return result, nil
}
func AssignedQuery(login string) string { return "open(true);published(true);assignee(" + login + ")" }
func ChangesQuery(login string, checks bool) string {
	q := "open(true);published(true);author(" + login + ")"
	if !checks {
		q += ";common_status(waiting_for_changes)"
	}
	return q
}
func (r *SharedReader) ListAssigned(_ context.Context, login string) ([]PullRequest, error) {
	return r.list(AssignedQuery(login))
}
func (r *SharedReader) ListAuthoredWaitingForChanges(_ context.Context, login string) ([]PullRequest, error) {
	return r.list(ChangesQuery(login, false))
}
func (r *SharedReader) ListAuthoredOpen(_ context.Context, login string) ([]PullRequest, error) {
	return r.list(ChangesQuery(login, true))
}

func samePRFacts(a, b PRSnapshot) bool {
	return a.ID == b.ID && a.Status == b.Status && a.CommonStatus == b.CommonStatus && a.HeadCommit == b.HeadCommit && a.PublishedDiffID == b.PublishedDiffID
}

func (r *SharedReader) SearchObservationAt(query string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.searches[query].at
}
func (r *SharedReader) NextSlowAt(ctx context.Context, id int64) (time.Time, error) {
	key := strconv.FormatInt(id, 10)
	next, err := r.deadline(ctx, "comments."+key)
	if err != nil {
		return time.Time{}, err
	}
	checks, err := r.deadline(ctx, "checks."+key)
	if err != nil {
		return time.Time{}, err
	}
	if checks.After(next) {
		next = checks
	}
	r.mu.Lock()
	queue := slices.Clone(r.slowQueue)
	position := r.slowPosition
	step := r.queueStepStarted
	r.mu.Unlock()
	if index := slices.Index(queue, id); index >= 0 && !step.IsZero() {
		turns := index - position + 1
		if index < position {
			turns = len(queue) - position + index + 1
		}
		// Both logical slow reads have bounded contexts. This is a conservative
		// queue deadline anchored to real progress, never renewed by a cache hit.
		queued := step.Add(time.Duration(turns) * 2 * cliTimeout)
		if queued.After(next) {
			next = queued
		}
	}
	return next, nil
}

func (r *SharedReader) noteSlowStart(position int, started time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.slowPosition = position
	r.queueStepStarted = started
}
