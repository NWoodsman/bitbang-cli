package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/richlegrand/bitbang/internal/identity"
	"github.com/richlegrand/bitbang/internal/links"
)

// linkPoll is how often live sessions are re-checked against the link
// table. Deletion is applied the moment the table is replaced; this
// ticker exists for expiry, where the clock moves with nobody touching
// the file. Expiry is measured in days, so a minute is ample.
const linkPoll = time.Minute

// linkState owns the listener's link table and every path that replaces
// it. Readers take a snapshot pointer; nothing mutates a table in place,
// so a poll cannot observe a half-applied edit.
type linkState struct {
	path     string
	offered  []string
	code     string
	codeURL  func(code string, flags ...string) string
	readOnly bool // ephemeral identity: no file, just the implicit row

	mu    sync.RWMutex
	table *links.Table
	// mod is the file's modtime as loaded, checked before write-back so a
	// mint cannot clobber an edit made while the listener was running.
	mod time.Time
}

func newLinkState(program string, offered []string, code string, ephemeral bool,
	codeURL func(string, ...string) string) (*linkState, error) {

	ls := &linkState{
		path:     "",
		offered:  offered,
		code:     code,
		codeURL:  codeURL,
		readOnly: ephemeral,
	}
	if !ephemeral {
		ls.path = filepath.Join(identity.Dir(program), links.Filename)
	}
	if err := ls.reload(); err != nil {
		return nil, err
	}
	return ls, nil
}

// current returns the table as it stands now.
func (ls *linkState) current() *links.Table {
	ls.mu.RLock()
	defer ls.mu.RUnlock()
	return ls.table
}

// reload reads the file, mints codes for entries that have none, writes
// those back, and replaces the table. An unreadable or invalid file is
// an error and leaves the previous table in place: an empty table means
// "no links", which is defined as one code granting everything, so
// degrading to it would silently widen access.
func (ls *linkState) reload() error {
	var entries []links.Terms
	var mod time.Time
	if !ls.readOnly {
		var err error
		entries, mod, err = links.Load(ls.path)
		if err != nil {
			return err
		}
	}

	// An entry with no code is a mint request: editing the file is how
	// links are created, so there is no `link new`.
	minted := []string(nil)
	if len(entries) > 0 {
		var err error
		entries, minted, err = links.Mint(entries, identity.NewAccessCode)
		if err != nil {
			return err
		}
	}
	if len(minted) > 0 {
		if err := links.Save(ls.path, entries, mod); err != nil {
			return fmt.Errorf("writing minted codes: %w", err)
		}
		// Re-stat so the next mint compares against what we just wrote.
		if _, m, err := links.Load(ls.path); err == nil {
			mod = m
		}
	}

	table, warnings, err := links.Build(entries, ls.offered, ls.code)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}

	ls.mu.Lock()
	ls.table, ls.mod = table, mod
	ls.mu.Unlock()
	return nil
}

// listing renders the table: every link, what it grants, when it lapses,
// and the URL that reaches it. The listing is the state of the world, so
// it prints expired rows too -- dropping them looks like the file failed
// to load.
func (ls *linkState) listing(bold, reset string) string {
	table := ls.current()
	entries := table.Entries()
	if len(entries) == 1 {
		// Only the implicit row: this listener has no link table, so the
		// plain URL banner above has already said everything.
		return ""
	}

	now := time.Now()
	labelW, scopeW := 0, 0
	rows := make([][3]string, 0, len(entries))
	for _, e := range entries {
		scopes := strings.Join(e.Grants(ls.offered), " ")
		if scopes == "" {
			scopes = "(nothing)"
		}
		rows = append(rows, [3]string{e.Label, scopes, expiryNote(e, now)})
		if n := len(e.Label); n > labelW {
			labelW = n
		}
		if n := len(scopes); n > scopeW {
			scopeW = n
		}
	}

	var b strings.Builder
	b.WriteString("\n")
	for i, e := range entries {
		url := ls.codeURL(e.Code)
		fmt.Fprintf(&b, "  %-*s  %-*s  %-14s  %s%s%s\n",
			labelW, rows[i][0], scopeW, rows[i][1], rows[i][2], bold, url, reset)
	}
	return b.String()
}

// expiryNote is the human column: nothing for a link that does not
// expire, a rough countdown for one that does, and `expired` for one
// that already has.
func expiryNote(t links.Terms, now time.Time) string {
	if t.Expires == nil {
		return ""
	}
	if t.Check(now) != nil {
		return "expired"
	}
	// Round up: a link set to expire in six days has six days left for
	// almost all of its life, and truncating shows "5d" the moment it is
	// minted, which reads as an off-by-one.
	left := t.Expires.Sub(now)
	switch {
	case left > 24*time.Hour:
		return fmt.Sprintf("expires in %dd", ceilUnits(left, 24*time.Hour))
	case left > time.Hour:
		return fmt.Sprintf("expires in %dh", ceilUnits(left, time.Hour))
	case left > time.Minute:
		return fmt.Sprintf("expires in %dm", ceilUnits(left, time.Minute))
	default:
		return "expires shortly"
	}
}

func ceilUnits(d, unit time.Duration) int {
	return int((d + unit - 1) / unit)
}

// labels lists the table's labels, sorted, for `link` subcommands.
func (ls *linkState) labels() []string {
	var out []string
	for _, e := range ls.current().Entries() {
		out = append(out, e.Label)
	}
	sort.Strings(out)
	return out
}
