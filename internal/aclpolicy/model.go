package aclpolicy

// Document represents a declarative HuJSON/JSON ACL policy file.
type Document struct {
	Version int                 `json:"version,omitempty"`
	Roles   map[string]RoleSpec `json:"roles,omitempty"`
	ACLs    []ACLRuleSpec       `json:"acls,omitempty"`
	Tests   []TestCase          `json:"tests,omitempty"`
}

// RoleSpec defines a control role's session permissions in the policy document.
type RoleSpec struct {
	Note               string `json:"note,omitempty"`
	AllowKeyboardMouse bool   `json:"allow_keyboard_mouse"`
	AllowClipboard     bool   `json:"allow_clipboard"`
	AllowFileTransfer  bool   `json:"allow_file_transfer"`
	AllowAudio         bool   `json:"allow_audio"`
	AllowTCPTunneling  bool   `json:"allow_tcp_tunneling"`
	AllowTerminal      bool   `json:"allow_terminal"`
	AllowRestart       bool   `json:"allow_restart"`
	IsDefault          bool   `json:"is_default,omitempty"`
}

// ACLRuleSpec defines an individual access control rule.
type ACLRuleSpec struct {
	Name     string   `json:"name"`
	Action   string   `json:"action"` // "allow" or "deny"
	Src      []string `json:"src"`    // user:<username>, group:<name>, *
	Dst      []string `json:"dst"`    // device:<rustdesk_id>, device_group:<name>, *
	Role     string   `json:"role,omitempty"`
	Priority int      `json:"priority,omitempty"`
	Enabled  *bool    `json:"enabled,omitempty"`
	Note     string   `json:"note,omitempty"`
}

// TestCase defines an assertion evaluated before applying policy changes.
type TestCase struct {
	Src    string `json:"src"`            // e.g. "user:alice", "alice"
	Dst    string `json:"dst"`            // e.g. "device:12345", "12345"
	Expect string `json:"expect"`         // "allow" or "deny"
	Role   string `json:"role,omitempty"` // expected role name if expect is "allow"
}

// TestResult reports the outcome of evaluating a TestCase.
type TestResult struct {
	Test           TestCase `json:"test"`
	Passed         bool     `json:"passed"`
	ActualDecision string   `json:"actual_decision"`
	ActualRole     string   `json:"actual_role,omitempty"`
	Error          string   `json:"error,omitempty"`
}

// ValidationResult summarizes the dry-run compilation and testing.
type ValidationResult struct {
	Valid       bool         `json:"valid"`
	RolesCount  int          `json:"roles_count"`
	RulesCount  int          `json:"rules_count"`
	TestsCount  int          `json:"tests_count"`
	TestResults []TestResult `json:"test_results"`
	Errors      []string     `json:"errors,omitempty"`
}
