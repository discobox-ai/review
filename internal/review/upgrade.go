package review

import "slices"

// IdentityScheme freezes the canonical comparison and its encoding.
const IdentityScheme = "myers-lines-v1"

// Identity is the verified identity of a complete content pair. An absent
// entry means verification is unavailable, not that the file is empty.
type Identity struct {
	Digest      string
	Fingerprint string
	Runs        []string
}

// LegacyApproval keeps the original claim separate from active approvals.
// Versions 1–3 did not retain evidence of modes, kinds or line terminators,
// so even matching checksums cannot prove a canonical approval.
type LegacyApproval struct {
	Approved    bool     `json:"approved"`
	Nullified   bool     `json:"nullified,omitempty"`
	Digest      string   `json:"digest,omitempty"`
	Hunks       []string `json:"hunks,omitempty"`
	At          string   `json:"at,omitempty"`
	Fingerprint string   `json:"fingerprint"`
	Retired     bool     `json:"retired,omitempty"`
}

func (v *Verdict) retireLegacy() {
	for i := range v.Legacy {
		v.Legacy[i].Retired = true
	}
}

func (v *Verdict) replaceLegacy(fingerprint string) {
	if v.Scheme != "" || !v.Approved && !v.Nullified && len(v.Hunks) == 0 {
		return
	}
	v.Legacy = append(v.Legacy, LegacyApproval{Approved: v.Approved, Nullified: v.Nullified, Digest: v.Digest, Hunks: slices.Clone(v.Hunks), At: v.At, Fingerprint: fingerprint, Retired: true})
	v.Hunks = nil
}

// NeedsReview names the reviewers whose unprovable legacy claims remain.
func (s *State) NeedsReview(path string) []string {
	return s.named(path, func(v *Verdict) bool {
		return slices.ContainsFunc(v.Legacy, func(l LegacyApproval) bool { return !l.Retired })
	})
}

// ReconcileCanonical migrates only verified paths and compares canonical
// approvals. A complete manifest also retires claims for paths no longer
// changed; an incomplete manifest cannot prove that a missing path is gone.
func (s *State) ReconcileCanonical(ids map[string]Identity, complete bool) bool {
	before := string(s.Bytes())
	for _, f := range s.Files {
		id, available := ids[f.Path]
		if available && id.Digest == "" {
			continue
		}
		if !available && !complete {
			continue
		}
		for _, v := range f.Verdicts {
			if !available {
				if v.Scheme == "" && (v.Approved || v.Nullified || len(v.Hunks) > 0) {
					v.Legacy = append(v.Legacy, LegacyApproval{
						Approved: v.Approved, Nullified: v.Nullified, Digest: v.Digest,
						Hunks: slices.Clone(v.Hunks), At: v.At, Retired: true,
					})
					v.Scheme, s.Scheme = IdentityScheme, IdentityScheme
				}
				v.retireLegacy()
				if v.Approved {
					v.Approved, v.Nullified = false, true
				}
				v.Hunks = nil
				continue
			}
			if v.Scheme == "" && (v.Approved || v.Nullified || len(v.Hunks) > 0) {
				// The established no-digest claim explicitly approves the
				// current change; it needs no inference about a previous one.
				bind := v.Approved && v.Digest == ""
				v.Legacy = append(v.Legacy, LegacyApproval{
					Approved: v.Approved, Nullified: v.Nullified, Digest: v.Digest,
					Hunks: slices.Clone(v.Hunks), At: v.At, Fingerprint: id.Fingerprint,
					Retired: bind || v.Nullified || !v.Approved && len(v.Hunks) == 0,
				})
				v.Approved, v.Digest, v.Hunks = bind, "", nil
				v.Scheme = IdentityScheme
				s.Scheme = IdentityScheme
				if bind {
					v.Digest = id.Digest
					if v.At == "" {
						v.At = Now()
					}
				}
			}
			for i := range v.Legacy {
				if v.Legacy[i].Fingerprint != id.Fingerprint {
					v.Legacy[i].Retired = true
				}
			}
			if v.Scheme != IdentityScheme {
				continue
			}
			if v.Approved && v.Digest != id.Digest {
				v.Approved, v.Nullified = false, true
			}
			v.Hunks = slices.DeleteFunc(v.Hunks, func(d string) bool { return !slices.Contains(id.Runs, d) })
		}
	}
	return before != string(s.Bytes())
}

// mergeLegacy keeps history when a handwritten later verdict replaces one.
// Retirement is monotonic, including when duplicate copies disagree on it.
func (v *Verdict) mergeLegacy(earlier []LegacyApproval) {
	for _, claim := range earlier {
		at := slices.IndexFunc(v.Legacy, func(l LegacyApproval) bool {
			return l.Approved == claim.Approved && l.Nullified == claim.Nullified &&
				l.Digest == claim.Digest && slices.Equal(l.Hunks, claim.Hunks) &&
				l.At == claim.At && l.Fingerprint == claim.Fingerprint
		})
		if at < 0 {
			v.Legacy = append(v.Legacy, claim)
		} else {
			v.Legacy[at].Retired = v.Legacy[at].Retired || claim.Retired
		}
	}
}
