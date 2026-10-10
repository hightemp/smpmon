package alert

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"time"

	"github.com/hightemp/smpmon/internal/config"
)

// Channels lists the valid notify values.
var Channels = []string{"tui", "desktop", "exec", "webhook"}

// Compile turns config rules into engine rules, reporting all problems.
func Compile(rules []config.AlertRule) ([]*Rule, error) {
	var out []*Rule
	var errs []error
	names := map[string]bool{}
	for i, rc := range rules {
		where := fmt.Sprintf("alerts[%d]", i)
		if rc.Name != "" {
			where = "alert " + rc.Name
		}
		fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(where+": "+format, args...)) }
		if rc.Name == "" {
			fail("name is required")
		} else if names[rc.Name] {
			fail("duplicate name")
		}
		names[rc.Name] = true
		sel, op, thr, err := ParseExpr(rc.Expr)
		if err != nil {
			fail("%v", err)
		}
		r := &Rule{
			Name: rc.Name, Expr: rc.Expr, Selector: sel, Op: op, Threshold: thr,
			For: rc.For, Level: Level(rc.Level), Hysteresis: rc.Hysteresis, Cooldown: rc.Cooldown,
			Hosts: rc.Hosts, Notify: rc.Notify, Exec: rc.Exec, Webhook: rc.Webhook,
		}
		switch r.Level {
		case "":
			r.Level = Warn
		case Warn, Crit:
		default:
			fail("level must be warn or crit")
		}
		if r.Cooldown == 0 {
			r.Cooldown = time.Minute
		}
		if len(r.Notify) == 0 {
			r.Notify = []string{"tui"}
		}
		for _, n := range r.Notify {
			if !slices.Contains(Channels, n) {
				fail("unknown notify channel %q (want %v)", n, Channels)
			}
		}
		if slices.Contains(r.Notify, "exec") && r.Exec == "" {
			fail("notify: exec needs an exec command")
		}
		if slices.Contains(r.Notify, "webhook") {
			if u, err := url.Parse(r.Webhook); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
				fail("notify: webhook needs an http(s) webhook URL")
			}
		}
		if r.Hysteresis < 0 || r.For < 0 || r.Cooldown < 0 {
			fail("for, hysteresis and cooldown must not be negative")
		}
		out = append(out, r)
	}
	return out, errors.Join(errs...)
}
