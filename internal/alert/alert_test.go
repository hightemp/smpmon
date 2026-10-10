package alert

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "github.com/hightemp/smpmon/internal/collect" // metric units
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/metrics"
)

func TestParseExpr(t *testing.T) {
	cases := map[string]float64{
		"cpu.total.usage > 90":         90,
		"mem.used>=8GiB":               8 << 30,
		"net.*.rx_bps < 1.5M":          1.5e6,
		"disk.fs.*.used_percent > 95%": 95,
		"load.1 != -1":                 -1,
	}
	for expr, want := range cases {
		_, _, v, err := ParseExpr(expr)
		if err != nil || v != want {
			t.Errorf("%q: %v %v", expr, v, err)
		}
	}
	for _, bad := range []string{"", "cpu > ", "cpu ~ 3", "cpu > 3 bananas", "a..b > 1"} {
		if _, _, _, err := ParseExpr(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func rule(t *testing.T, rc config.AlertRule) *Rule {
	t.Helper()
	rs, err := Compile([]config.AlertRule{rc})
	if err != nil {
		t.Fatal(err)
	}
	return rs[0]
}

func TestEngine(t *testing.T) {
	st := metrics.NewStore(metrics.DefaultStoreOptions())
	r := rule(t, config.AlertRule{Name: "hot", Expr: "cpu.core.*.usage > 90", For: 3 * time.Second, Hysteresis: 10, Cooldown: 5 * time.Second, Level: "crit"})
	e := NewEngine([]*Rule{r}, nil)
	e.AddHost("local", st)

	t0 := time.Unix(1000, 0)
	set := func(at time.Time, c0, c1 float64) []Event {
		st.Write(metrics.Sample{Time: at, Points: []metrics.Point{{Key: "cpu.core.0.usage", Value: c0}, {Key: "cpu.core.1.usage", Value: c1}}})
		return e.Evaluate(at)
	}
	sec := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Second) }

	if ev := set(sec(0), 95, 10); len(ev) != 0 {
		t.Fatal("fired before for")
	}
	set(sec(1), 95, 10)
	set(sec(2), 95, 10)
	ev := set(sec(3), 95, 10)
	if len(ev) != 1 || ev[0].Key != "cpu.core.0.usage" || ev[0].Resolved {
		t.Fatalf("expected core 0 to fire: %+v", ev)
	}
	if act := e.Active(); len(act) != 1 || act[0].Rule.Level != Crit {
		t.Fatalf("active %+v", act)
	}
	// Hysteresis: 85 is below 90 but above 80, so it keeps firing.
	if ev := set(sec(4), 85, 10); len(ev) != 0 {
		t.Fatalf("resolved inside hysteresis: %+v", ev)
	}
	ev = set(sec(5), 50, 10)
	if len(ev) != 1 || !ev[0].Resolved {
		t.Fatalf("expected resolve: %+v", ev)
	}
	// Cooldown: firing again right away is suppressed.
	for i := 6; i <= 9; i++ {
		if ev := set(sec(i), 95, 10); len(ev) != 0 {
			t.Fatalf("fired during cooldown at %d", i)
		}
	}
	// A blip shorter than for doesn't fire.
	set(sec(20), 95, 10)
	set(sec(21), 10, 10)
	if ev := set(sec(25), 10, 10); len(ev) != 0 || len(e.Active()) != 0 {
		t.Fatal("blip fired")
	}
	// Stale data neither fires nor resolves.
	set(sec(30), 95, 95)
	set(sec(33), 95, 95)
	if len(e.Active()) != 2 {
		t.Fatalf("both cores should fire: %+v", e.Active())
	}
	if ev := e.Evaluate(sec(200)); len(ev) != 0 {
		t.Fatalf("stale data changed alerts: %+v", ev)
	}
	evs, seq := e.Events(0)
	if len(evs) != 4 || seq != 4 {
		t.Fatalf("events %d seq %d", len(evs), seq)
	}
	if more, _ := e.Events(seq); len(more) != 0 {
		t.Fatal("no new events expected")
	}
	if !strings.Contains(evs[0].Summary(), "hot: cpu.core.0.usage is 95.0% (> 90.0%)") {
		t.Fatalf("summary %q", evs[0].Summary())
	}
}

func TestDisappearedSeriesResolves(t *testing.T) {
	st := metrics.NewStore(metrics.DefaultStoreOptions())
	r := rule(t, config.AlertRule{Name: "full", Expr: "disk.fs.*.used_percent > 90"})
	e := NewEngine([]*Rule{r}, nil)
	e.AddHost("srv", st)
	now := time.Unix(1000, 0)
	st.Write(metrics.Sample{Time: now, Points: []metrics.Point{{Key: "disk.fs./mnt.used_percent", Value: 99}}})
	if ev := e.Evaluate(now); len(ev) != 1 || ev[0].Host != "srv" || !strings.Contains(ev[0].Summary(), "on srv") {
		t.Fatalf("%+v", ev)
	}
	st.Prune(now.Add(time.Second)) // the mount went away
	if ev := e.Evaluate(now.Add(2 * time.Second)); len(ev) != 1 || !ev[0].Resolved {
		t.Fatalf("not resolved: %+v", ev)
	}
}

func TestCompileErrors(t *testing.T) {
	_, err := Compile([]config.AlertRule{
		{Expr: "cpu > 1"},
		{Name: "a", Expr: "nope"},
		{Name: "a", Expr: "cpu > 1"},
		{Name: "b", Expr: "cpu > 1", Level: "panic"},
		{Name: "c", Expr: "cpu > 1", Notify: []string{"pigeon"}},
		{Name: "d", Expr: "cpu > 1", Notify: []string{"exec"}},
		{Name: "e", Expr: "cpu > 1", Notify: []string{"webhook"}, Webhook: "ftp://x"},
	})
	for _, want := range []string{"name is required", "want <metric>", "duplicate", "level", "pigeon", "exec command", "webhook URL"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

func TestDispatcher(t *testing.T) {
	var got WebhookPayload
	hits := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		hits <- struct{}{}
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "out")
	execCmd := `echo "$SMPMON_ALERT $SMPMON_ALERT_STATE $SMPMON_ALERT_VALUE" > ` + out
	if runtime.GOOS == "windows" {
		execCmd = `echo %SMPMON_ALERT% %SMPMON_ALERT_STATE% %SMPMON_ALERT_VALUE%> ` + out
	}
	var desktop []string
	d := &Dispatcher{desktop: func(title, body string, _ bool) *exec.Cmd {
		desktop = append(desktop, title+"|"+body)
		self, _ := os.Executable()
		return exec.Command(self, "-test.run=^$") // a command that succeeds
	}}
	r := rule(t, config.AlertRule{Name: "hot", Expr: "cpu.total.usage > 90", Notify: []string{"tui", "desktop", "exec", "webhook"},
		Exec: execCmd, Webhook: srv.URL})
	ev := Event{Time: time.Now(), Rule: r, Host: "local", Key: "cpu.total.usage", Value: 97}
	d.Notify(context.Background(), ev)

	select {
	case <-hits:
	case <-time.After(5 * time.Second):
		t.Fatal("webhook not called")
	}
	if got.Alert != "hot" || got.State != "firing" || got.Value != 97 || got.Threshold != 90 {
		t.Fatalf("payload %+v", got)
	}
	data, _ := os.ReadFile(out)
	if !strings.Contains(string(data), "hot firing 97") {
		t.Fatalf("exec output %q", data)
	}
	if len(desktop) != 1 || !strings.Contains(desktop[0], "smpmon: hot|") {
		t.Fatalf("desktop %v", desktop)
	}
}
