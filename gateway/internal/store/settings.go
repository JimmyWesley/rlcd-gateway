package store

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/config"
)

// Bodies policies: which bodies (request, sent and response) are kept.
const (
	BodiesFull = "full"
	// BodiesErrorsOnly keeps bodies of failed calls only, plus the request
	// bodies recall needs (see Detail.KeepBody).
	BodiesErrorsOnly = "errors_only"
	// BodiesNone keeps no bodies, like log_bodies=false.
	BodiesNone = "none"
)

// Retention defaults.
const (
	DefaultDetailMaxAge    = 14 * Day
	DefaultSummaryMaxAge   = 90 * Day
	DefaultMaxTotalBytes   = 2 << 30
	DefaultConversationTTL = 7 * Day
)

// Day is 24 hours, the unit of the retention settings.
const Day = 24 * time.Hour

// Settings is the "storage" config section.
type Settings struct {
	// DetailMaxAge deletes request details (bodies, response, stage
	// details) older than this; 0 keeps them forever.
	DetailMaxAge Duration `json:"detail_max_age"`
	// SummaryMaxAge drops whole monthly index files once their newest
	// possible summary is older than this; 0 keeps them forever.
	SummaryMaxAge Duration `json:"summary_max_age"`
	// MaxTotalBytes caps records, blobs and index; over it, the oldest
	// details are deleted first. 0 means no cap.
	MaxTotalBytes int64 `json:"max_total_bytes"`
	// Bodies is full | errors_only | none.
	Bodies string `json:"bodies"`
	// ConversationTTL is how long an idle conversation keeps its pruning
	// state, routing pin, recall events and the bodies its markers need.
	ConversationTTL Duration `json:"conversation_ttl"`
}

// DefaultSettings are the safe defaults.
func DefaultSettings() Settings {
	return Settings{DetailMaxAge: Duration(DefaultDetailMaxAge), SummaryMaxAge: Duration(DefaultSummaryMaxAge),
		MaxTotalBytes: DefaultMaxTotalBytes, Bodies: BodiesFull, ConversationTTL: Duration(DefaultConversationTTL)}
}

// SettingsFrom reads the section, falling back to the defaults for missing
// or invalid values.
func SettingsFrom(c config.Config) Settings {
	s := DefaultSettings()
	raw := c.Section("storage")
	if len(raw) == 0 {
		return s
	}
	var in settingsPatch
	if json.Unmarshal(raw, &in) != nil {
		return s
	}
	if next, err := s.apply(in); err == nil {
		s = next
	}
	return s
}

// settingsPatch is a partial update; absent fields keep their value.
type settingsPatch struct {
	DetailMaxAge    *Duration `json:"detail_max_age"`
	SummaryMaxAge   *Duration `json:"summary_max_age"`
	MaxTotalBytes   *int64    `json:"max_total_bytes"`
	Bodies          *string   `json:"bodies"`
	ConversationTTL *Duration `json:"conversation_ttl"`
}

// Limits keep a setting from turning the janitor into a shredder.
const (
	minMaxAge          = time.Hour
	minConversationTTL = time.Hour
	minTotalBytes      = 64 << 20
)

func (s Settings) apply(p settingsPatch) (Settings, error) {
	if p.DetailMaxAge != nil {
		if d := *p.DetailMaxAge; d != 0 && time.Duration(d) < minMaxAge {
			return s, fmt.Errorf("detail_max_age must be 0 (keep forever) or at least 1h")
		}
		s.DetailMaxAge = *p.DetailMaxAge
	}
	if p.SummaryMaxAge != nil {
		if d := *p.SummaryMaxAge; d != 0 && time.Duration(d) < minMaxAge {
			return s, fmt.Errorf("summary_max_age must be 0 (keep forever) or at least 1h")
		}
		s.SummaryMaxAge = *p.SummaryMaxAge
	}
	if p.MaxTotalBytes != nil {
		if n := *p.MaxTotalBytes; n != 0 && n < minTotalBytes {
			return s, fmt.Errorf("max_total_bytes must be 0 (no cap) or at least %d (64 MB)", minTotalBytes)
		}
		s.MaxTotalBytes = *p.MaxTotalBytes
	}
	if p.Bodies != nil {
		switch *p.Bodies {
		case BodiesFull, BodiesErrorsOnly, BodiesNone:
			s.Bodies = *p.Bodies
		default:
			return s, fmt.Errorf("bodies must be %q, %q or %q", BodiesFull, BodiesErrorsOnly, BodiesNone)
		}
	}
	if p.ConversationTTL != nil {
		if time.Duration(*p.ConversationTTL) < minConversationTTL {
			return s, fmt.Errorf("conversation_ttl must be at least 1h")
		}
		s.ConversationTTL = *p.ConversationTTL
	}
	return s, nil
}

// EffectiveBodies is the bodies policy in force: log_bodies=false means none.
func EffectiveBodies(c config.Config) string {
	if !c.LogBodies {
		return BodiesNone
	}
	return SettingsFrom(c).Bodies
}

// ApplyBodies drops the bodies the policy does not keep, before Save.
// errors_only keeps every body of a failed call, and the request body of a
// call recall depends on (KeepBody).
func ApplyBodies(policy string, d *Detail) {
	switch policy {
	case BodiesFull:
		return
	case BodiesErrorsOnly:
		if d.Status >= 400 || d.Error != "" {
			return
		}
		if !d.KeepBody {
			d.RequestBody = ""
		}
		d.SentBody, d.ResponseBody = "", ""
	default:
		d.RequestBody, d.SentBody, d.ResponseBody = "", "", ""
	}
}

// Duration is a time.Duration written as "14d", "36h" or "90m" in JSON. It
// also reads a number of seconds.
type Duration time.Duration

func (d Duration) String() string {
	t := time.Duration(d)
	switch {
	case t == 0:
		return "0"
	case t%Day == 0:
		return strconv.FormatInt(int64(t/Day), 10) + "d"
	case t%time.Hour == 0:
		return strconv.FormatInt(int64(t/time.Hour), 10) + "h"
	case t%time.Minute == 0:
		return strconv.FormatInt(int64(t/time.Minute), 10) + "m"
	}
	return t.String()
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var secs float64
	if json.Unmarshal(b, &secs) == nil {
		if secs < 0 || secs > math.MaxInt64/float64(time.Second) {
			return fmt.Errorf("duration out of range")
		}
		*d = Duration(time.Duration(secs * float64(time.Second)))
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"14d\" or a number of seconds")
	}
	v, err := ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// ParseDuration reads "14d", "1d12h", "36h", "0" or any time.ParseDuration.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	var days time.Duration
	if i := strings.Index(s, "d"); i > 0 {
		n, err := strconv.ParseFloat(s[:i], 64)
		if err != nil || n < 0 || n > 100*365 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		days = time.Duration(n * float64(Day))
		s = s[i+1:]
		if s == "" {
			return days, nil
		}
	}
	v, err := time.ParseDuration(s)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid duration %q (use e.g. \"14d\", \"36h\")", s)
	}
	return days + v, nil
}
