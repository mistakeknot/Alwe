// Package sessionsearch composes the cass backend and the local catalog into
// one session-search surface.
//
// Neither backend is required, and every tool is servable from either one
// alone. cass supplies better ranking and a larger corpus; the local catalog
// supplies availability. When cass is missing, unhealthy, or still lock-busy
// after its retry budget, answers come from the local catalog and say so — the
// caller gets a weaker answer instead of an error, which is the whole point of
// the arrangement.
package sessionsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mistakeknot/Alwe/pkg/localindex"
	"github.com/mistakeknot/Alwe/pkg/observer"
)

// Hit is one merged result.
type Hit struct {
	SessionID  string  `json:"session_id"`
	Provider   string  `json:"provider"`
	Score      float64 `json:"score"`
	FilePath   string  `json:"file_path"`
	Snippet    string  `json:"snippet"`
	Timestamp  string  `json:"timestamp"`
	LineNumber int     `json:"line_number"`
	// Source names the backend that produced this hit ("cass", "local", or
	// "cass+local" when both found it).
	Source string `json:"source"`
}

// Score on a merged Hit is the reciprocal-rank-fusion score, not a backend's
// raw score: the two backends' scales are not comparable. See dedupe.

// Result is a search response plus provenance.
type Result struct {
	Hits []Hit `json:"hits"`
	// Sources lists the backends that actually answered.
	Sources []string `json:"sources"`
	// Degraded is true when at least one backend was unavailable, so the
	// caller knows the result set may be incomplete.
	Degraded bool `json:"degraded"`
	// Notice explains any degradation in one line, for humans and agents.
	Notice string `json:"notice,omitempty"`
}

// Service searches sessions across the available backends.
type Service struct {
	cass  *observer.CassObserver // nil when cass is not installed
	local *localindex.Index      // nil when the catalog could not be opened
	// localErr records why the catalog is unavailable, for the notice.
	localErr error
	// clock is overridable so timeline windows are testable.
	clock func() time.Time
}

// Option configures a Service.
type Option func(*Service)

// WithCass supplies a cass observer explicitly (used by tests).
func WithCass(o *observer.CassObserver) Option { return func(s *Service) { s.cass = o } }

// WithLocal supplies a local catalog explicitly (used by tests).
func WithLocal(ix *localindex.Index) Option { return func(s *Service) { s.local = ix } }

// WithClock overrides the clock used for timeline windows (used by tests).
func WithClock(f func() time.Time) Option { return func(s *Service) { s.clock = f } }

// New builds a Service, attaching whichever backends are available. It only
// fails when neither backend can be reached, since with no backend at all
// there is nothing to degrade to.
func New(opts ...Option) (*Service, error) {
	s := &Service{}
	for _, opt := range opts {
		opt(s)
	}

	if s.cass == nil {
		if obs, err := observer.New(); err == nil {
			s.cass = obs
		}
	}
	if s.local == nil {
		if ix, err := localindex.Open(localindex.DefaultPath()); err == nil {
			s.local = ix
		} else {
			s.localErr = err
		}
	}

	if s.cass == nil && s.local == nil {
		return nil, fmt.Errorf("no search backend: cass not found in PATH and local catalog unavailable: %v", s.localErr)
	}
	return s, nil
}

// Close releases the local catalog, if any.
func (s *Service) Close() error {
	if s.local != nil {
		return s.local.Close()
	}
	return nil
}

// HasCass reports whether the cass backend is attached.
func (s *Service) HasCass() bool { return s.cass != nil }

// HasLocal reports whether the local catalog is attached.
func (s *Service) HasLocal() bool { return s.local != nil }

// LocalCoverage reports what the local catalog holds, so drift against cass is
// observable rather than silent.
func (s *Service) LocalCoverage(ctx context.Context) (localindex.Coverage, error) {
	if s.local == nil {
		return localindex.Coverage{}, fmt.Errorf("local catalog unavailable")
	}
	return s.local.Coverage(ctx)
}

// Index refreshes the local catalog.
func (s *Service) Index(ctx context.Context, roots []string) (localindex.Stats, error) {
	if s.local == nil {
		return localindex.Stats{}, fmt.Errorf("local catalog unavailable: %v", s.localErr)
	}
	if len(roots) == 0 {
		roots = localindex.DefaultRoots()
	}
	return s.local.Index(ctx, roots)
}

// Search queries both backends and merges the results.
func (s *Service) Search(ctx context.Context, query, connector string, limit int) (Result, error) {
	if limit <= 0 {
		limit = 10
	}
	return s.merge(ctx, limit,
		func() ([]observer.SessionResult, error) {
			return s.cass.SearchSessions(ctx, query, connector, limit)
		},
		func() ([]localindex.Hit, error) {
			return s.local.Search(ctx, query, limit)
		},
		connector,
	)
}

// ContextForFile finds sessions that touched a path. Both backends implement
// this as a content search for the path itself.
func (s *Service) ContextForFile(ctx context.Context, filePath string, limit int) (Result, error) {
	if limit <= 0 {
		limit = 5
	}
	return s.merge(ctx, limit,
		func() ([]observer.SessionResult, error) {
			return s.cass.ContextForFile(ctx, filePath, limit)
		},
		func() ([]localindex.Hit, error) {
			return s.local.Search(ctx, filePath, limit)
		},
		"",
	)
}

// Timeline reports recent activity, preferring cass and falling back to the
// local catalog. Local output is a JSON object with source:"local" so callers
// can tell the two apart.
func (s *Service) Timeline(ctx context.Context, since string) (string, error) {
	notice := "cass not installed; activity derived from the local catalog (results are local-only)"
	if s.cass != nil {
		out, err := s.cass.Timeline(ctx, since)
		if err == nil {
			return out, nil
		}
		if s.local == nil {
			return "", err
		}
		// cass failed but the catalog can answer: fall through rather than
		// propagating an outage.
		notice = "cass unavailable (" + err.Error() +
			"); activity derived from the local catalog (results are local-only)"
	}
	if s.local == nil {
		return "", fmt.Errorf("timeline unavailable: no cass and no local catalog")
	}

	res, err := s.local.Timeline(ctx, since, s.now())
	if err != nil {
		return "", err
	}
	res.Notice = notice
	out, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ExportSession renders a session, preferring cass and falling back to reading
// the transcript directly. The local path needs no catalog, so a session that
// has not been indexed yet still exports.
func (s *Service) ExportSession(ctx context.Context, path string) (string, error) {
	if s.cass != nil {
		out, err := s.cass.ExportSession(ctx, path)
		if err == nil {
			return out, nil
		}
	}
	md, err := localindex.ExportMarkdown(path)
	if err != nil {
		return "", err
	}
	return md, nil
}

// now returns the clock the service reads, overridable in tests.
func (s *Service) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

// Health reports per-backend availability. Unlike the old boolean, an
// unavailable cass no longer means the server is unhealthy — only the loss of
// every backend does.
type Health struct {
	Healthy bool `json:"healthy"`
	// Cass reports whether cass can answer queries. This is deliberately not
	// cass's own health verdict: cass calls itself unhealthy when its index is
	// merely past a 300-second staleness threshold, while still searching
	// perfectly well.
	Cass bool `json:"cass"`
	// CassSelfReport carries cass's own verdict and stated problems.
	CassSelfReport *observer.CassHealth `json:"cass_self_report,omitempty"`
	Local          bool                 `json:"local"`
	// Degraded means a capability is unavailable, not merely that a backend is
	// missing. Every MCP tool can now be served from either backend alone, so
	// losing one reduces ranking quality without losing capability.
	Degraded bool `json:"degraded"`
	// ReducedRanking flags that results come from the weaker-ranked backend
	// only — real information, but not a capability loss.
	ReducedRanking bool `json:"reduced_ranking,omitempty"`
	// LocalStale means the catalog has not been refreshed within its expected
	// window. Without this a wedged or unloaded indexer is invisible: searches
	// keep succeeding against progressively older data.
	LocalStale bool `json:"local_stale,omitempty"`
	// LocalAgeSeconds is how long since the catalog was last refreshed.
	//
	// Deliberately not omitempty: a just-refreshed catalog is legitimately 0,
	// and omitting it would render as null — indistinguishable from "not
	// computed". Age 0 is the healthiest possible answer and must be visible.
	LocalAgeSeconds int64 `json:"local_age_seconds"`
	// LocalStaleThresholdSeconds is the window used for that judgement.
	LocalStaleThresholdSeconds int                  `json:"local_stale_threshold_seconds,omitempty"`
	Notice                     string               `json:"notice,omitempty"`
	Coverage                   *localindex.Coverage `json:"local_coverage,omitempty"`
	BuildID                    string               `json:"build_id,omitempty"`
	BuildModule                string               `json:"build_module,omitempty"`
}

// Health probes the backends.
func (s *Service) Health(ctx context.Context) Health {
	h := Health{Local: s.local != nil}
	if s.cass != nil {
		report := s.cass.HealthReport(ctx)
		h.CassSelfReport = &report
		h.Cass = report.Reachable
	}
	h.Healthy = h.Cass || h.Local
	// Capability, not backend count: all five tools are servable from either
	// backend alone, so only losing both is a degradation.
	h.Degraded = !h.Healthy

	switch {
	case h.Cass && h.Local:
		// Both usable; surface cass's own complaint if it has one, since a
		// stale index still affects result freshness.
		if h.CassSelfReport != nil && !h.CassSelfReport.Healthy {
			h.Notice = "cass is reachable but reports: " +
				strings.Join(h.CassSelfReport.Errors, ", ")
		}
	case h.Local:
		h.ReducedRanking = true
		h.Notice = "cass unavailable; all tools served from the local catalog with local-only ranking"
	case h.Cass:
		h.Notice = "local catalog unavailable; all tools served by cass"
	default:
		h.Notice = "no backend available"
	}

	if s.local != nil {
		if cov, err := s.local.Coverage(ctx); err == nil {
			h.Coverage = &cov
			h.LocalStaleThresholdSeconds = localStaleThresholdSecs()
			if cov.LastIndexedUnix > 0 {
				h.LocalAgeSeconds = s.now().Unix() - cov.LastIndexedUnix
				if h.LocalAgeSeconds < 0 {
					h.LocalAgeSeconds = 0
				}
				h.LocalStale = h.LocalAgeSeconds > int64(h.LocalStaleThresholdSeconds)
			} else {
				// Never indexed at all: stale by definition, and the more urgent
				// case since there is nothing to fall back to.
				h.LocalStale = true
			}
			if h.LocalStale {
				h.Notice = appendNotice(h.Notice, staleCatalogNotice(h))
			}
		}
	}
	h.BuildID, h.BuildModule = buildInfo()
	return h
}

// defaultLocalStaleThresholdSecs is twice the scheduled refresh interval, so a
// single missed run does not raise an alarm but a stopped or wedged indexer
// does. The scheduled interval is 300s (see docs/charter-local-session-index.md).
const defaultLocalStaleThresholdSecs = 600

// localStaleThresholdSecs is the catalog-staleness window, overridable with
// ALWE_INDEX_STALE_THRESHOLD (seconds) for hosts on a different cadence.
func localStaleThresholdSecs() int {
	if v := os.Getenv("ALWE_INDEX_STALE_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultLocalStaleThresholdSecs
}

func staleCatalogNotice(h Health) string {
	if h.Coverage != nil && h.Coverage.LastIndexedUnix == 0 {
		return "local catalog has never been indexed — run `alwe index`"
	}
	return fmt.Sprintf(
		"local catalog last refreshed %ds ago, past its %ds window — the indexer may be stopped or wedged; run `alwe index`",
		h.LocalAgeSeconds, h.LocalStaleThresholdSeconds)
}

func appendNotice(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

// merge runs the available backends and combines their hits.
func (s *Service) merge(
	ctx context.Context,
	limit int,
	cassFn func() ([]observer.SessionResult, error),
	localFn func() ([]localindex.Hit, error),
	connector string,
) (Result, error) {
	var (
		res      Result
		cassHits []observer.SessionResult
		locHits  []localindex.Hit
		problems []string
	)

	if s.cass != nil {
		hits, err := cassFn()
		if err != nil {
			problems = append(problems, "cass: "+err.Error())
		} else {
			cassHits = hits
			res.Sources = append(res.Sources, "cass")
		}
	} else {
		problems = append(problems, "cass: not installed")
	}

	if s.local != nil {
		hits, err := localFn()
		if err != nil {
			problems = append(problems, "local: "+err.Error())
		} else {
			locHits = hits
			res.Sources = append(res.Sources, "local")
		}
	} else {
		problems = append(problems, "local: catalog unavailable")
	}

	if len(res.Sources) == 0 {
		return res, fmt.Errorf("all search backends failed: %s", strings.Join(problems, "; "))
	}

	res.Hits = dedupe(cassHits, locHits, connector, limit)
	res.Degraded = len(problems) > 0
	if res.Degraded {
		res.Notice = noticeFor(res.Sources, problems)
	}
	return res, nil
}

// key identifies a hit for dedupe purposes: the same transcript line found by
// either backend is one result, not two.
type key struct {
	path string
	line int
}

// rrfK is the standard reciprocal-rank-fusion damping constant. Larger values
// flatten the contribution of top ranks.
const rrfK = 60.0

// dedupe merges the backends' hits by reciprocal rank fusion.
//
// Raw scores cannot be compared across backends: cass reports corpus-wide BM25
// in the tens, while FTS5's inverted rank lands near zero. Sorting those in one
// list would silently bury every local hit whenever cass is up, which would
// make "merged" meaningless — the local catalog would only ever matter in total
// fallback. RRF ranks each backend's list independently and fuses positions, so
// a hit that is top-ranked locally can still surface, and a hit both backends
// found outranks one only a single backend found.
func dedupe(cassHits []observer.SessionResult, locHits []localindex.Hit, connector string, limit int) []Hit {
	idx := make(map[key]int, len(cassHits)+len(locHits))
	var out []Hit
	fused := map[int]float64{}

	add := func(h Hit, rank int) {
		k := key{path: h.FilePath, line: h.LineNumber}
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, h)
		} else if !strings.Contains(out[i].Source, h.Source) {
			// Corroborated by both backends — worth surfacing when diagnosing
			// drift between them.
			out[i].Source = "cass+local"
		}
		fused[i] += 1.0 / (rrfK + float64(rank))
	}

	// cass's line_number is not a transcript line number, so translate it into
	// the same coordinate space as the local catalog before deduping.
	cassConverted := make([]Hit, 0, len(cassHits))
	for _, h := range cassHits {
		cassConverted = append(cassConverted, Hit{
			SessionID: h.SessionID, Provider: h.Provider, Score: h.Score,
			FilePath: h.FilePath, Snippet: h.Snippet, Timestamp: h.Timestamp,
			LineNumber: h.LineNumber, Source: "cass",
		})
	}
	resolveCassLines(cassConverted)

	for rank, h := range cassConverted {
		add(h, rank+1)
	}
	for rank, h := range locHits {
		// The local catalog has no connector filter of its own, so apply it here.
		if connector != "" && h.Provider != connector {
			continue
		}
		add(Hit{
			SessionID: h.SessionID, Provider: h.Provider, Score: h.Score,
			FilePath: h.FilePath, Snippet: h.Snippet, Timestamp: h.Timestamp,
			LineNumber: h.LineNumber, Source: "local",
		}, rank+1)
	}

	// Report the fused score, since that is what the ordering reflects.
	for i := range out {
		out[i].Score = fused[i]
	}

	// Stable order: fused score descending, then path/line so equal scores
	// don't shuffle between runs.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].FilePath != out[j].FilePath {
			return out[i].FilePath < out[j].FilePath
		}
		return out[i].LineNumber < out[j].LineNumber
	})

	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func noticeFor(sources, problems []string) string {
	if len(sources) == 1 && sources[0] == "local" {
		return "cass unavailable — results are local-only and may rank differently (" +
			strings.Join(problems, "; ") + ")"
	}
	return strings.Join(problems, "; ")
}
