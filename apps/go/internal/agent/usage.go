package agent

import (
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/muschterm/tui/apps/go/internal/protocol"
)

const usageDialect = "tui-go.usage.v1"

// normalizeUsage replaces reported snapshots. It never sums cost or mistakes
// cumulative consumption for occupancy. Native partial observations use the
// pinned extension; ACP usage_update remains supported for other adapters.
func normalizeUsage(t *protocol.Thread, u Update) {
	var wire struct {
		Dialect, Model, Source, ReportedAt, Scope, SessionID string
		Used, Size                                           *int64
		Cost                                                 *struct {
			Amount    json.Number
			Currency  string
			Estimated bool
		}
	}
	if json.Unmarshal(u.Raw, &wire) != nil {
		return
	}
	if u.Kind == "tui_usage_update" && wire.Dialect != usageDialect {
		return
	}
	if wire.Used != nil && *wire.Used < 0 || wire.Size != nil && *wire.Size <= 0 {
		return
	}
	if wire.SessionID != "" && t.SessionID != "" && wire.SessionID != t.SessionID {
		return
	}
	if wire.Used == nil && wire.Size == nil && wire.Cost == nil && !(u.Kind == "tui_usage_update" && wire.Scope == "awaiting next request") {
		return
	}
	stamp := wire.ReportedAt
	if stamp == "" {
		stamp = now()
	} else if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
		return
	}
	if old := t.Usage; old != nil && old.SessionID == wire.SessionID {
		before, e1 := time.Parse(time.RFC3339Nano, old.ReportedAt)
		after, e2 := time.Parse(time.RFC3339Nano, stamp)
		if e1 == nil && e2 == nil && after.Before(before) {
			return
		}
	}
	source := label(wire.Source)
	if source == "" {
		source = u.Kind
	}
	next := &protocol.Usage{Used: -1, Source: source, ReportedAt: stamp, Model: label(wire.Model), Scope: label(wire.Scope), SessionID: wire.SessionID}
	if wire.Used != nil {
		next.Used = *wire.Used
	}
	if wire.Size != nil {
		next.Size = *wire.Size
	}
	// A new context observation doesn't erase the session's last cost snapshot.
	if old := t.Usage; old != nil && old.SessionID == wire.SessionID {
		next.Cost = old.Cost
	}
	if wire.Cost != nil {
		amount := wire.Cost.Amount.String()
		currency := wire.Cost.Currency
		if len(amount) > 64 {
			return
		}
		if i := strings.IndexAny(amount, "eE"); i >= 0 {
			exponent, err := strconv.Atoi(amount[i+1:])
			if err != nil || exponent < -100 || exponent > 100 {
				return
			}
		}
		value, ok := new(big.Rat).SetString(amount)
		if !ok || value.Sign() < 0 || len(currency) != 3 || strings.Trim(currency, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
			return
		}
		next.Cost = &protocol.UsageCost{Amount: amount, Currency: currency, Estimated: wire.Cost.Estimated, Source: source, ReportedAt: stamp, Scope: "session cumulative"}
	}
	t.Usage = next
}
