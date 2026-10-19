# Tailscale-Style JSON/HuJSON Access Policy File for RD Panel

## Overview

Currently, RD Panel models Connection Access Control (ACL) rules and Control Roles as discrete relational entities in SQLite (`connection_acls` and `control_roles`), managed individually via REST endpoints (`/api/v1/admin/acls`, `/api/v1/admin/control-roles`) and dialog-driven UI forms.

For power users, platform engineers, and GitOps workflows, managing security policies as a unified, version-controlled declarative document (similar to **Tailscale ACL Policy files**) provides major operational advantages:
1. **Single Source of Truth:** Entire authorization posture (roles, rules, group mappings, test assertions) in one human-readable file.
2. **HuJSON Support:** Standard JSON with comments (`//` and `/* */`) and trailing commas for readable annotations.
3. **Built-in Test Suite (`tests`):** Define test assertions directly in the policy file that are evaluated on save to prevent regression and lockouts.
4. **GitOps & API Compatibility:** Version-control policies in Git and sync via CLI/API.

---

## Policy Schema (Draft)

```jsonc
{
  // Schema version for future compatibility
  "version": 1,

  // Custom Control Roles definitions
  "roles": {
    "view-only": {
      "note": "Read-only observation without interaction",
      "allow_keyboard_mouse": false,
      "allow_clipboard": false,
      "allow_file_transfer": false,
      "allow_audio": true,
      "allow_tcp_tunneling": false,
      "allow_terminal": false,
      "allow_restart": false
    },
    "support-tier1": {
      "note": "Standard desktop support without reboot or tunneling",
      "allow_keyboard_mouse": true,
      "allow_clipboard": true,
      "allow_file_transfer": true,
      "allow_audio": true,
      "allow_tcp_tunneling": false,
      "allow_terminal": false,
      "allow_restart": false
    }
  },

  // Connection Access Rules evaluated by priority (top-to-bottom or explicit priority)
  "acls": [
    {
      "name": "Deny contractor access to production core",
      "action": "deny",
      "src": ["group:contractors"],
      "dst": ["device_group:production-servers"],
      "priority": 10
    },
    {
      "name": "IT Support to general workstations",
      "action": "allow",
      "src": ["group:it-support"],
      "dst": ["device_group:workstations"],
      "role": "support-tier1",
      "priority": 100
    },
    {
      "name": "Senior Sysadmin full access",
      "action": "allow",
      "src": ["user:alex"],
      "dst": ["*"],
      "priority": 200
    }
  ],

  // In-line test assertions verified before policy changes are applied
  "tests": [
    {
      "src": "user:contractor_john",
      "dst": "device:prod-db-01",
      "expect": "deny"
    },
    {
      "src": "user:alex",
      "dst": "device:prod-db-01",
      "expect": "allow"
    },
    {
      "src": "user:support_maria",
      "dst": "device:workstation-14",
      "expect": "allow",
      "role": "support-tier1"
    }
  ]
}
```

---

## Planned Capabilities

### 1. Backend Engine
- **JSON/HuJSON Parser & Validator:** Parse HuJSON (stripping comments/trailing commas), validate schema, identifiers, and foreign references (ensuring referenced users, groups, devices exist).
- **Atomic Transactional Apply:** `PUT /api/v1/admin/acl/policy` compiles the JSON policy into underlying SQLite rows atomically.
- **Automated Pre-commit Test Runner:** Evaluates all assertions under `"tests"` against the compiled rules. If any test fails, the transaction is aborted with a 422 error detailing the failing test assertion.
- **Export Endpoint:** `GET /api/v1/admin/acl/policy` reverses existing DB rules into the clean JSON representation.

### 2. Admin UI: "Policy File" Tab in Access Control
- In `web/src/pages/acl/AclPage.tsx`, add a third tab: **Policy File**.
- Interactive code editor (Monaco or CodeMirror) with:
  - Syntax highlighting for HuJSON / JSON.
  - In-browser validation and schema error markers.
  - "Run Tests & Preview Diff" button.
  - Visual diff viewer showing added/removed/modified rules before confirmation.

### 3. CLI Tooling
- `rdpanel acl export > policy.hujson`
- `rdpanel acl validate -f policy.hujson`
- `rdpanel acl apply -f policy.hujson`
