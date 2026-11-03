//go:build integration

package httpapi_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

type scrape map[string]*dto.MetricFamily

// scrape reads /metrics without any user header, as a Prometheus server would.
func (e *env) scrape(t *testing.T) scrape {
	t.Helper()
	resp, _ := e.do(t, "GET", "/metrics", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics: status %d: %s", resp.StatusCode, resp.Raw)
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(resp.Raw))
	if err != nil {
		t.Fatalf("parse /metrics: %v", err)
	}
	return families
}

func labelsOf(m *dto.Metric) map[string]string {
	out := map[string]string{}
	for _, l := range m.GetLabel() {
		out[l.GetName()] = l.GetValue()
	}
	return out
}

// series returns every sample of a family keyed by its labels, rendered as
// "k=v,k=v" in label order. Counters and gauges give their value; histograms
// give their sample count.
func (s scrape) series(name string) map[string]float64 {
	out := map[string]float64{}
	for _, m := range s[name].GetMetric() {
		var parts []string
		for _, l := range m.GetLabel() {
			parts = append(parts, l.GetName()+"="+l.GetValue())
		}
		v := m.GetCounter().GetValue() + m.GetGauge().GetValue() + float64(m.GetHistogram().GetSampleCount())
		out[strings.Join(parts, ",")] = v
	}
	return out
}

// delta is after minus before, for series that changed only.
func delta(before, after map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for k, v := range after {
		if d := v - before[k]; d != 0 {
			out[k] = d
		}
	}
	return out
}

func sameDelta(t *testing.T, what string, got, want map[string]float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s changed = %v, want exactly %v", what, got, want)
		return
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s changed = %v, want exactly %v", what, got, want)
			return
		}
	}
}

func TestMetrics(t *testing.T) {
	e := setup(t)

	t.Run("served without authentication in Prometheus text format", func(t *testing.T) {
		resp, _ := e.do(t, "GET", "/metrics", "", "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d, want 200", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("Content-Type = %q, want Prometheus text format", ct)
		}
	})

	t.Run("checks are counted by type and result", func(t *testing.T) {
		passing := e.pendingEntity(t, "individual", "Ada Lovelace")
		failing := e.pendingEntity(t, "individual", "Ivan SANCTIONED")
		before := e.scrape(t)

		e.runCheck(t, complianceOfficer, passing, "sanctions_screening")
		e.runCheck(t, complianceOfficer, failing, "sanctions_screening")

		after := e.scrape(t)
		sameDelta(t, "kyc_checks_total", delta(before.series("kyc_checks_total"), after.series("kyc_checks_total")), map[string]float64{
			"check_type=sanctions_screening,result=pass": 1,
			"check_type=sanctions_screening,result=fail": 1,
		})
		// The fail was a Hard Fail, so it is also a System Rejection.
		sameDelta(t, "kyc_status_transitions_total", delta(before.series("kyc_status_transitions_total"), after.series("kyc_status_transitions_total")), map[string]float64{
			"from=pending_review,to=rejected": 1,
		})
	})

	t.Run("a provider error is counted as such, not as a check", func(t *testing.T) {
		id := e.pendingEntity(t, "individual", "Ada PROVIDERDOWN")
		before := e.scrape(t)
		if resp, _ := e.runCheck(t, complianceOfficer, id, "pep_screening"); resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("status %d, want 502", resp.StatusCode)
		}
		after := e.scrape(t)
		sameDelta(t, "kyc_provider_errors_total", delta(before.series("kyc_provider_errors_total"), after.series("kyc_provider_errors_total")),
			map[string]float64{"check_type=pep_screening": 1})
		sameDelta(t, "kyc_checks_total", delta(before.series("kyc_checks_total"), after.series("kyc_checks_total")), map[string]float64{})
	})

	t.Run("a status transition is counted by from and to", func(t *testing.T) {
		id := e.createEntity(t, "individual", "Ada Lovelace")
		before := e.scrape(t)
		e.uploadedDocument(t, id)
		after := e.scrape(t)
		sameDelta(t, "kyc_status_transitions_total", delta(before.series("kyc_status_transitions_total"), after.series("kyc_status_transitions_total")),
			map[string]float64{"from=unverified,to=pending_review": 1})
	})

	t.Run("backlog gauges match pending and dead notifications", func(t *testing.T) {
		// No worker runs in this harness, so every notification stays pending.
		e.pendingEntity(t, "individual", "Ada Lovelace")
		e.adminExec(t, "UPDATE outbox SET dead_at = now() WHERE id = (SELECT id FROM outbox ORDER BY created_at LIMIT 1)")

		wantPending := e.queryInt(t, "SELECT count(*) FROM outbox WHERE published_at IS NULL AND dead_at IS NULL")
		wantDead := e.queryInt(t, "SELECT count(*) FROM outbox WHERE dead_at IS NOT NULL")
		got := e.scrape(t).series("kyc_notifications")
		if got["state=pending"] != float64(wantPending) || got["state=dead"] != float64(wantDead) {
			t.Errorf("kyc_notifications = %v, want pending=%d dead=%d", got, wantPending, wantDead)
		}
		if wantPending == 0 || wantDead != 1 {
			t.Fatalf("test setup: pending=%d dead=%d", wantPending, wantDead)
		}
	})

	t.Run("latency is labelled by route pattern, not raw path", func(t *testing.T) {
		a := e.createEntity(t, "individual", "Ada Lovelace")
		b := e.createEntity(t, "individual", "Grace Hopper")
		before := e.scrape(t)
		e.do(t, "GET", "/entities/"+a, complianceOfficer, "")
		e.do(t, "GET", "/entities/"+b, complianceOfficer, "")
		e.do(t, "GET", "/no/such/"+a, complianceOfficer, "")
		after := e.scrape(t)

		got := delta(before.series("kyc_http_request_duration_seconds"), after.series("kyc_http_request_duration_seconds"))
		delete(got, "method=GET,route=/metrics,status=200") // the scrape itself
		sameDelta(t, "kyc_http_request_duration_seconds", got, map[string]float64{
			"method=GET,route=/entities/{id},status=200": 2,
			"method=GET,route=unmatched,status=404":      1,
		})
		for _, m := range after["kyc_http_request_duration_seconds"].GetMetric() {
			if r := labelsOf(m)["route"]; strings.Contains(r, a) || strings.Contains(r, b) {
				t.Errorf("a series is labelled with a raw id: %v", labelsOf(m))
			}
		}
	})
}
