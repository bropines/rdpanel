package aclpolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Parse converts a HuJSON/JSON byte slice into a Document, validating basic syntax.
func Parse(raw []byte) (*Document, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, errors.New("policy document is empty")
	}

	// Standardize HuJSON into compliant standard JSON (removes comments, trailing commas)
	stdJSON, err := Standardize(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid HuJSON syntax: %w", err)
	}

	var doc Document
	dec := json.NewDecoder(strings.NewReader(string(stdJSON)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("invalid policy schema: %w", err)
	}

	// Basic document validation
	for name, role := range doc.Roles {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("role name cannot be empty")
		}
		if len(name) > 64 {
			return nil, fmt.Errorf("role name %q exceeds 64 characters", name)
		}
		_ = role
	}

	for idx, rule := range doc.ACLs {
		if strings.TrimSpace(rule.Name) == "" {
			return nil, fmt.Errorf("rule #%d: name is required", idx+1)
		}
		action := strings.ToLower(strings.TrimSpace(rule.Action))
		if action != "allow" && action != "deny" {
			return nil, fmt.Errorf("rule %q: invalid action %q (must be 'allow' or 'deny')", rule.Name, rule.Action)
		}
		if len(rule.Src) == 0 {
			return nil, fmt.Errorf("rule %q: src selector list cannot be empty", rule.Name)
		}
		if len(rule.Dst) == 0 {
			return nil, fmt.Errorf("rule %q: dst selector list cannot be empty", rule.Name)
		}
	}

	for idx, tc := range doc.Tests {
		if strings.TrimSpace(tc.Src) == "" {
			return nil, fmt.Errorf("test #%d: src is required", idx+1)
		}
		if strings.TrimSpace(tc.Dst) == "" {
			return nil, fmt.Errorf("test #%d: dst is required", idx+1)
		}
		exp := strings.ToLower(strings.TrimSpace(tc.Expect))
		if exp != "allow" && exp != "deny" {
			return nil, fmt.Errorf("test #%d: invalid expect %q (must be 'allow' or 'deny')", idx+1, tc.Expect)
		}
	}

	return &doc, nil
}
