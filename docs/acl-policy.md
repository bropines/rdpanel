# Declarative Access Policy (HuJSON / JSON API)

RD Panel provides a declarative, Tailscale-style access control policy engine.
Instead of clicking through multiple forms and modal dialogs to manage individual
rules and roles, administrators can define the entire authorization posture in a
single version-controlled policy file formatted in **HuJSON** (JSON with comments
and trailing commas).

The policy file is managed via the panel UI (**Access Control → Policy Editor**),
the admin REST API, or an automated GitOps pipeline.

---

## 1. File Format and Structure

The policy file is written in **HuJSON** (`.hujson` / `.jsonc`). Standard single-line
`//` and block `/* */` comments as well as trailing commas are fully supported and
preserved.

### Example Policy

```jsonc
{
  // Schema version (currently 1)
  "version": 1,

  // Custom Control Roles defining session permissions
  "roles": {
    "view-only": {
      "note": "Read-only screen observation without control",
      "allow_keyboard_mouse": false,
      "allow_clipboard": false,
      "allow_file_transfer": false,
      "allow_audio": true,
      "allow_tcp_tunneling": false,
      "allow_terminal": false,
      "allow_restart": false
    },
    "support-tier1": {
      "note": "Standard IT support without reboot or tunneling",
      "allow_keyboard_mouse": true,
      "allow_clipboard": true,
      "allow_file_transfer": true,
      "allow_audio": true,
      "allow_tcp_tunneling": false,
      "allow_terminal": false,
      "allow_restart": false
    }
  },

  // Connection Access Rules evaluated by priority
  "acls": [
    {
      "name": "Deny contractor access to production databases",
      "action": "deny",
      "src": ["group:contractors"],
      "dst": ["device_group:production-db"],
      "priority": 10
    },
    {
      "name": "Helpdesk remote support for office workstations",
      "action": "allow",
      "src": ["group:it-support"],
      "dst": ["device_group:workstations"],
      "role": "support-tier1",
      "priority": 100
    },
    {
      "name": "Senior sysadmin emergency wildcard access",
      "action": "allow",
      "src": ["user:alex"],
      "dst": ["*"],
      "priority": 200
    }
  ],

  // Automated test assertions verified on save and in CI/CD
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
      "dst": "device:ws-finance-05",
      "expect": "allow",
      "role": "support-tier1"
    }
  ]
}
```

---

## 2. Policy Components

### 2.1 `roles` (Control Roles)

A map of named roles defining what actions an operator can perform inside an established RustDesk session.

| Permission Field | Type | Description |
|---|---|---|
| `allow_keyboard_mouse` | `boolean` | Send keyboard keystrokes and mouse events |
| `allow_clipboard` | `boolean` | Synchronize system clipboard |
| `allow_file_transfer` | `boolean` | Transfer files between local and remote machines |
| `allow_audio` | `boolean` | Receive remote audio stream |
| `allow_tcp_tunneling` | `boolean` | Establish arbitrary TCP tunnels through RustDesk |
| `allow_terminal` | `boolean` | Open remote command shell / terminal |
| `allow_restart` | `boolean` | Request remote machine reboot or reboot into safe mode |
| `note` | `string` | Optional descriptive note for administrators |

If an `allow` rule does not specify a `role`, the system default control role is used.

### 2.2 `acls` (Access Rules)

An array of access rules. Rules are evaluated in ascending order of `priority` (lower numbers evaluate first). The first rule that matches both the subject (`src`) and target device (`dst`) determines the decision (`allow` or `deny`).

| Field | Type | Description |
|---|---|---|
| `name` | `string` | Descriptive rule name |
| `action` | `string` | `"allow"` or `"deny"` |
| `src` | `string[]` | List of subject selectors (who is connecting) |
| `dst` | `string[]` | List of target selectors (which device is being accessed) |
| `role` | `string` | Name of the role defined in `roles` (only valid when `action: "allow"`) |
| `priority` | `number` | Positive integer (e.g. `10`, `100`, `200`). Lower numbers have higher precedence |
| `note` | `string` | Optional rule comment |

#### Selector Taxonomy

| Selector | Meaning | Example |
|---|---|---|
| `*` | Matches any user or device | `*` |
| `user:<username>` | Matches an individual user account | `user:alex` |
| `group:<group_name>` | Matches any member of a user group | `group:devops` |
| `device:<id_or_hostname>` | Matches a device by RustDesk ID or hostname | `device:123456789`, `device:prod-srv-01` |
| `device_group:<group_name>` | Matches any device in a device group | `device_group:servers` |

> [!NOTE]
> Device ownership always takes precedence: if an operator owns the target device (or shares the same "shared" user group), connection is allowed under device owner permissions unless explicitly denied by an admin.

### 2.3 `tests` (Policy Assertions)

A suite of built-in tests that simulate connection scenarios and verify expected decisions.

Every time a policy is tested or saved, the backend evaluates all test assertions against the current inventory:
- `src`: Subject selector (`user:username`).
- `dst`: Target selector (`device:id` or `device:hostname`).
- `expect`: `"allow"` or `"deny"`.
- `role`: (Optional) Expected effective role name if connection is allowed.

If **any** test assertion fails, the policy update is **rejected atomically** with an HTTP 422 error, protecting against lockouts and permission regressions.

---

## 3. REST API Reference

All policy endpoints live under `/api/v1/admin/acl/policy` and require an authenticated admin session or personal API token with full access.

### 3.1 Get Current Policy

```http
GET /api/v1/admin/acl/policy
```

**Response (200 OK):**
```json
{
  "policy": "{\n  \"version\": 1,\n  \"roles\": { ... },\n  \"acls\": [ ... ],\n  \"tests\": [ ... ]\n}"
}
```

### 3.2 Dry-Run Validation & Test Simulation

Validates HuJSON syntax, ensures all referenced entities exist in the database, and executes all test assertions without saving changes.

```http
POST /api/v1/admin/acl/policy/test
Content-Type: application/json

{
  "policy": "{ ... hujson content ... }"
}
```

**Response (200 OK - Success):**
```json
{
  "valid": true,
  "tests_count": 3
}
```

**Response (200 OK - Test Failure):**
```json
{
  "valid": false,
  "tests_count": 3,
  "errors": [
    "test #1 failed: expected deny for user:contractor_john -> device:prod-db-01, but got allow"
  ]
}
```

### 3.3 Apply Policy (Atomic Update)

Parses, validates, executes all tests, and atomically replaces active connection ACLs and custom control roles in SQLite.

```http
PUT /api/v1/admin/acl/policy
Content-Type: application/json

{
  "policy": "{ ... hujson content ... }"
}
```

**Response (200 OK):**
```json
{
  "valid": true,
  "tests_count": 3
}
```

---

## 4. GitOps Workflow

You can store `policy.hujson` in a private Git repository and automate deployments using CI/CD.

### Example GitHub Actions Workflow

```yaml
name: Sync Access Policy
on:
  push:
    branches: [main]
    paths:
      - 'policies/policy.hujson'

jobs:
  deploy-policy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Validate and Apply Policy
        env:
          RDPANEL_URL: ${{ secrets.RDPANEL_URL }}
          RDPANEL_TOKEN: ${{ secrets.RDPANEL_API_TOKEN }}
        run: |
          # Read policy file into JSON payload
          PAYLOAD=$(jq -Rs '{policy: .}' policies/policy.hujson)

          echo "Running pre-flight policy validation..."
          TEST_RES=$(curl -fsS -X POST "$RDPANEL_URL/api/v1/admin/acl/policy/test" \
            -H "Authorization: Bearer $RDPANEL_TOKEN" \
            -H "Content-Type: application/json" \
            -d "$PAYLOAD")

          echo "$TEST_RES" | jq .
          if [ "$(echo "$TEST_RES" | jq .valid)" != "true" ]; then
            echo "Policy validation failed!"
            exit 1
          fi

          echo "Applying policy..."
          curl -fsS -X PUT "$RDPANEL_URL/api/v1/admin/acl/policy" \
            -H "Authorization: Bearer $RDPANEL_TOKEN" \
            -H "Content-Type: application/json" \
            -d "$PAYLOAD"
```
