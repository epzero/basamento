package prompt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

type canonPart struct {
	Name    string      `json:"name"`
	Kind    string      `json:"kind"`
	Text    string      `json:"text"`
	History []canonHist `json:"history,omitempty"`
}

type canonHist struct {
	Role         string `json:"role"`
	OriginalText string `json:"original_text"`
	Text         string `json:"text"`
}

type canonAssembled struct {
	Ref   string      `json:"ref"`
	Parts []canonPart `json:"parts"`
}

// ContentHash returns SHA-256 hex of the assembled prompt canonical encoding.
func ContentHash(a *Assembled) string {
	if a == nil {
		return ""
	}
	return hashCanon(canonAssembled{Ref: a.Ref, Parts: canonParts(a.Parts)})
}

// PrefixHash returns SHA-256 hex of the cacheable prefix, or empty without one.
func PrefixHash(parts []AssembledPart) string {
	var pref []canonPart
	for _, p := range parts {
		if p.Kind == KindStatic || p.Kind == KindSemiStatic {
			pref = append(pref, toCanonPart(p))
		}
	}
	if len(pref) == 0 {
		return ""
	}
	return hashCanon(canonAssembled{Parts: pref})
}

func canonParts(parts []AssembledPart) []canonPart {
	out := make([]canonPart, len(parts))
	for i, p := range parts {
		out[i] = toCanonPart(p)
	}
	return out
}

func toCanonPart(p AssembledPart) canonPart {
	cp := canonPart{Name: p.Name, Kind: string(p.Kind), Text: p.Text}
	if len(p.History) > 0 {
		cp.History = make([]canonHist, len(p.History))
		for i, h := range p.History {
			cp.History[i] = canonHist(h)
		}
	}
	return cp
}

func hashCanon(c canonAssembled) string {
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// TemplateContentHash identifies template content for idempotent registration.
func TemplateContentHash(t *Template) string {
	if t == nil {
		return ""
	}
	type canonIn struct {
		Name  string `json:"name"`
		Trust string `json:"trust"`
	}
	type canonSeg struct {
		Name   string `json:"name"`
		Kind   string `json:"kind"`
		Text   string `json:"text"`
		Budget int    `json:"budget,omitempty"`
	}
	type canonTpl struct {
		ID          string     `json:"id"`
		Version     string     `json:"version"`
		TotalBudget int        `json:"total_budget,omitempty"`
		Inputs      []canonIn  `json:"inputs"`
		Segments    []canonSeg `json:"segments"`
	}
	byName := map[string]canonIn{}
	for _, in := range t.Inputs {
		byName[in.Name] = canonIn{Name: in.Name, Trust: string(CanonicalTrust(in.Trust))}
	}
	ins := make([]canonIn, 0, len(byName))
	for _, in := range byName {
		ins = append(ins, in)
	}
	sort.Slice(ins, func(i, j int) bool {
		if ins[i].Name != ins[j].Name {
			return ins[i].Name < ins[j].Name
		}
		return ins[i].Trust < ins[j].Trust
	})
	segs := make([]canonSeg, len(t.Segments))
	for i, s := range t.Segments {
		segs[i] = canonSeg{Name: s.Name, Kind: string(s.Kind), Text: s.Text, Budget: s.Budget}
	}
	c := canonTpl{
		ID:          t.ID,
		Version:     t.Version,
		TotalBudget: t.TotalBudget,
		Inputs:      ins,
		Segments:    segs,
	}
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
