package tagteam

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// PanelBudgetLedger deliberately separates non-renewable cumulative work from
// renewable live concurrency. It is durably replaced as one transaction before
// a caller may launch any panel member.
type PanelBudgetLedger struct {
	SchemaVersion           int                         `json:"schema_version"`
	CumulativeWorkLimit     int                         `json:"cumulative_work_limit"`
	CumulativeWorkReserved  int                         `json:"cumulative_work_reserved"`
	CumulativeWorkConsumed  int                         `json:"cumulative_work_consumed"`
	LiveConcurrencyLimit    int                         `json:"live_concurrency_limit"`
	LiveConcurrencyReserved int                         `json:"live_concurrency_reserved"`
	LiveConcurrencyInUse    int                         `json:"live_concurrency_in_use"`
	Generation              uint64                      `json:"generation"`
	Reservations            map[string]PanelReservation `json:"reservations"`
}

type PanelReservation struct {
	PanelID               string    `json:"panel_id"`
	DefinitionDigest      string    `json:"definition_digest"`
	Members               int       `json:"members"`
	WorkAllowance         int       `json:"work_allowance"`
	Started               int       `json:"started"`
	Completed             int       `json:"completed"`
	CancelledNeverStarted int       `json:"cancelled_never_started"`
	Status                string    `json:"status"`
	Generation            uint64    `json:"generation"`
	ReservedAt            time.Time `json:"reserved_at"`
}

type PanelAdmissionError struct{ Reason string }

func (e *PanelAdmissionError) Error() string { return "panel admission blocked: " + e.Reason }

var panelBudgetMu sync.Mutex

func panelBudgetPath(runDir string) string { return filepath.Join(runDir, "panel-budget.json") }
func readPanelBudget(runDir string) (PanelBudgetLedger, error) {
	var l PanelBudgetLedger
	b, e := os.ReadFile(panelBudgetPath(runDir))
	if e != nil {
		return l, e
	}
	e = jsonUnmarshalStrict(b, &l)
	return l, e
}
func jsonUnmarshalStrict(b []byte, out any) error { return json.Unmarshal(b, out) }

func initializePanelBudget(runDir string, workLimit, concurrencyLimit int) error {
	panelBudgetMu.Lock()
	defer panelBudgetMu.Unlock()
	if _, err := os.Stat(panelBudgetPath(runDir)); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeJSONWithNewline(panelBudgetPath(runDir), PanelBudgetLedger{SchemaVersion: replayContractVersion, CumulativeWorkLimit: workLimit, LiveConcurrencyLimit: concurrencyLimit, Reservations: map[string]PanelReservation{}})
}

func admitPanel(runDir, id, digest string, members, work int) (PanelReservation, error) {
	panelBudgetMu.Lock()
	defer panelBudgetMu.Unlock()
	l, err := readPanelBudget(runDir)
	if err != nil {
		return PanelReservation{}, err
	}
	if prior, ok := l.Reservations[id]; ok {
		if prior.DefinitionDigest != digest {
			return PanelReservation{}, &DivergenceError{Reason: "panel_definition_changed", Expected: prior.DefinitionDigest, Actual: digest}
		}
		return prior, nil
	}
	if members <= 0 || work < 0 {
		return PanelReservation{}, fmt.Errorf("invalid panel requirement")
	}
	if l.LiveConcurrencyReserved+l.LiveConcurrencyInUse+members > l.LiveConcurrencyLimit {
		return PanelReservation{}, &PanelAdmissionError{Reason: "live_concurrency_exhausted"}
	}
	if l.CumulativeWorkReserved+l.CumulativeWorkConsumed+work > l.CumulativeWorkLimit {
		return PanelReservation{}, &PanelAdmissionError{Reason: "cumulative_work_exhausted"}
	}
	l.Generation++
	r := PanelReservation{PanelID: id, DefinitionDigest: digest, Members: members, WorkAllowance: work, Status: "reserved", Generation: l.Generation, ReservedAt: time.Now().UTC()}
	l.Reservations[id] = r
	l.LiveConcurrencyReserved += members
	l.CumulativeWorkReserved += work
	if err := writeJSONWithNewline(panelBudgetPath(runDir), l); err != nil {
		return PanelReservation{}, err
	}
	return r, nil
}

func startPanelMember(runDir, id string, generation uint64) error {
	return updatePanel(runDir, id, generation, func(l *PanelBudgetLedger, r *PanelReservation) error {
		if r.Started >= r.Members-r.CancelledNeverStarted {
			return fmt.Errorf("all members already started")
		}
		r.Started++
		r.Status = "running"
		l.LiveConcurrencyReserved--
		l.LiveConcurrencyInUse++
		if l.CumulativeWorkReserved <= 0 {
			return &PanelAdmissionError{Reason: "cumulative_work_exhausted"}
		}
		l.CumulativeWorkReserved--
		l.CumulativeWorkConsumed++
		return nil
	})
}
func completePanelMember(runDir, id string, generation uint64) error {
	return updatePanel(runDir, id, generation, func(l *PanelBudgetLedger, r *PanelReservation) error {
		if r.Completed >= r.Started {
			return fmt.Errorf("no running member")
		}
		r.Completed++
		l.LiveConcurrencyInUse--
		if r.Completed+r.CancelledNeverStarted == r.Members {
			r.Status = "completed"
		}
		return nil
	})
}
func cancelNeverStartedMember(runDir, id string, generation uint64) error {
	return updatePanel(runDir, id, generation, func(l *PanelBudgetLedger, r *PanelReservation) error {
		if r.Started+r.CancelledNeverStarted >= r.Members {
			return fmt.Errorf("no unstarted member")
		}
		r.CancelledNeverStarted++
		l.LiveConcurrencyReserved--
		l.CumulativeWorkReserved--
		if r.Completed+r.CancelledNeverStarted == r.Members {
			r.Status = "cancelled"
		}
		return nil
	})
}
func updatePanel(runDir, id string, g uint64, fn func(*PanelBudgetLedger, *PanelReservation) error) error {
	panelBudgetMu.Lock()
	defer panelBudgetMu.Unlock()
	l, e := readPanelBudget(runDir)
	if e != nil {
		return e
	}
	r, ok := l.Reservations[id]
	if !ok {
		return fmt.Errorf("panel reservation missing")
	}
	if r.Generation != g {
		return &DivergenceError{Reason: "stale_panel_fence"}
	}
	if e = fn(&l, &r); e != nil {
		return e
	}
	l.Reservations[id] = r
	l.Generation++
	return writeJSONWithNewline(panelBudgetPath(runDir), l)
}
