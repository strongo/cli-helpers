package cliui

import "github.com/strongo/cli-helpers/cliinstall"

// Row is one target's combined catalog, status and (optionally) planned or
// completed install data for a listing, details, dry-run or install-result
// view. The caller — typically cliinstall/cobracmd, but equally a
// hand-rolled CLI with no framework at all — assembles Row values from
// cliinstall.Entries/Relevant/ByID and cliinstall.Probe/Install; this
// package reads them but never calls into cliinstall itself
// (cli-install#req:core-framework-neutral).
type Row struct {
	// Entry is the target's catalog entry. Zero for a name that was not a
	// catalog id (Plan.Failure's Kind is then selfupdate.KindUnknownTarget;
	// RowName falls back to Plan.Target for that case).
	Entry cliinstall.Entry
	// Relevant reports whether Entry is one of the host's relevant targets
	// (cli-install#req:relevance-matrix).
	Relevant bool
	// Relevance is the host -> target relevance text; empty when Relevant
	// is false (cli-install#req:non-relevant-target-allowed).
	Relevance string
	// Status is the target's probed install state — the pre-plan probe for
	// a bare listing row, or the same Status a Plan itself carries
	// (Plan.Status) once one exists.
	Status cliinstall.Status
	// Plan is set once a method/destination (or cask) has been decided for
	// this target: a dry run's Result (cli-install#req:install-dry-run) or
	// a real batch Install's Result (cli-install#req:multi-target-batch).
	// Nil for a bare listing row, where only Status is known.
	Plan *cliinstall.Result
}

// RowName is the id this row is about: Entry.ID when the target is a known
// catalog entry, otherwise Plan.Target — the raw name that was not a
// catalog id (cli-install#req:unknown-target-refused).
func RowName(row Row) string {
	if row.Entry.ID != "" {
		return row.Entry.ID
	}
	if row.Plan != nil {
		return row.Plan.Target
	}
	return row.Status.ID
}
