#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# nightshift.sh — Autonomous Linear → Pull Request agent
# "Move tickets to Next. Go to sleep. Wake up to PRs."
#
# Usage:  ./nightshift.sh              # start the poll loop
#         ./nightshift.sh cleanup      # interactive cleanup of stale resources
#         ./nightshift.sh cleanup --force  # non-interactive cleanup
# Config: .env (copy from .env.example)
# Logs:   .agent-logs/<TICKET>.log
# ─────────────────────────────────────────────────────────────────────────────

set -euo pipefail

VERSION="1.1.0"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ─── Require Bash 4+ (for associative arrays) ────────────────────────────────

if [ "${BASH_VERSINFO[0]}" -lt 4 ]; then
  echo "❌ Nightshift requires bash 4 or later (you have bash ${BASH_VERSION})."
  echo "   macOS ships with bash 3.2 — install a newer version:"
  echo "   brew install bash"
  echo "   Then run: /opt/homebrew/bin/bash nightshift.sh"
  exit 1
fi

# ─── Load .env ───────────────────────────────────────────────────────────────

ENV_FILE="$SCRIPT_DIR/.env"
if [ -f "$ENV_FILE" ]; then
  set -a
  # shellcheck source=/dev/null
  source "$ENV_FILE"
  set +a
fi

# ─── Config & Defaults ───────────────────────────────────────────────────────

LINEAR_API_KEY="${LINEAR_API_KEY:-}"
LINEAR_TEAM_KEY="${LINEAR_TEAM_KEY:-ENG}"
TRIGGER_STATE="${TRIGGER_STATE:-Next}"
IN_PROGRESS_STATE="${IN_PROGRESS_STATE:-In Progress}"
IN_REVIEW_STATE="${IN_REVIEW_STATE:-In Review}"
REPO_PATH="${REPO_PATH:-}"
MAIN_BRANCH="${MAIN_BRANCH:-main}"
MAX_CONCURRENT="${MAX_CONCURRENT:-3}"
POLL_INTERVAL="${POLL_INTERVAL:-30}"
USE_AGENT_TEAMS="${USE_AGENT_TEAMS:-false}"
GEMINI_API_KEY="${GEMINI_API_KEY:-}"
GEMINI_MODEL="${GEMINI_MODEL:-gemini-2.5-pro}"
MAX_REVIEW_RETRIES="${MAX_REVIEW_RETRIES:-1}"

# Safety guards
MAX_DISPATCHES="${MAX_DISPATCHES:-10}"
MAX_RETRIES="${MAX_RETRIES:-3}"
AGENT_TIMEOUT_MINUTES="${AGENT_TIMEOUT_MINUTES:-45}"

# Telegram notifications
TELEGRAM_ENABLED="${TELEGRAM_ENABLED:-false}"
TELEGRAM_BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-}"
TELEGRAM_CHAT_ID="${TELEGRAM_CHAT_ID:-}"

# Paths
LOG_DIR="$SCRIPT_DIR/.agent-logs"
WORKTREE_BASE="$SCRIPT_DIR/.worktrees"

# ─── Global State ────────────────────────────────────────────────────────────

declare -a ACTIVE_PIDS=()
declare -A PID_TO_IDENTIFIER=()
declare -A FAILED_ATTEMPTS=()
SHUTTING_DOWN=false
TOTAL_DISPATCHES=0
SUCCESS_COUNT=0
FAIL_COUNT=0
SESSION_START=$SECONDS

# Resolved Linear state IDs (populated at startup)
STATE_ID_TRIGGER=""
STATE_ID_IN_PROGRESS=""
STATE_ID_IN_REVIEW=""

# ─── Validation ──────────────────────────────────────────────────────────────

validate_config() {
  local errors=0

  if [ -z "$LINEAR_API_KEY" ]; then
    echo "❌ LINEAR_API_KEY is required — set it in .env" >&2
    errors=$((errors + 1))
  fi

  if [ -z "$REPO_PATH" ]; then
    echo "❌ REPO_PATH is required — set it in .env" >&2
    errors=$((errors + 1))
  elif [ ! -d "$REPO_PATH/.git" ]; then
    echo "❌ REPO_PATH ($REPO_PATH) is not a git repository" >&2
    errors=$((errors + 1))
  fi

  for cmd in curl jq git gh claude; do
    if ! command -v "$cmd" &>/dev/null; then
      echo "❌ Required command not found: $cmd" >&2
      errors=$((errors + 1))
    fi
  done

  if [ "$errors" -gt 0 ]; then
    echo "" >&2
    echo "Fix the errors above and try again. See README.md for setup instructions." >&2
    exit 1
  fi
}

# ─── Logging ─────────────────────────────────────────────────────────────────

log() {
  echo "[$(date '+%H:%M:%S')] $*"
}

tlog() {
  local identifier="$1"
  shift
  echo "[$(date '+%H:%M:%S')] [$identifier] $*"
}

# ─── Telegram Notifications ──────────────────────────────────────────────────

notify() {
  [[ "${TELEGRAM_ENABLED:-false}" != "true" ]] && return
  [[ -z "${TELEGRAM_BOT_TOKEN:-}" || -z "${TELEGRAM_CHAT_ID:-}" ]] && return

  local message="$1"
  # Run in a background subshell to be non-blocking
  (
    curl -s -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
      --data-urlencode "chat_id=${TELEGRAM_CHAT_ID}" \
      --data-urlencode "text=${message}" \
      --data-urlencode "parse_mode=Markdown" > /dev/null 2>&1
  ) &
}

format_duration() {
  local total_seconds=$1
  local hours=$((total_seconds / 3600))
  local minutes=$(( (total_seconds % 3600) / 60 ))
  if [ "$hours" -gt 0 ]; then
    echo "${hours}h ${minutes}m"
  else
    echo "${minutes}m"
  fi
}

# ─── Linear GraphQL API ──────────────────────────────────────────────────────

linear_gql() {
  local query="$1"
  local variables="${2:-{}}"

  # jq -Rs . safely encodes the query string as a JSON string literal.
  # $variables is already valid JSON produced by a prior jq call.
  local query_json payload
  query_json=$(printf '%s' "$query" | jq -Rs .)
  payload=$(printf '{"query":%s,"variables":%s}' "$query_json" "$variables")

  curl -s \
    -X POST \
    -H "Content-Type: application/json" \
    -H "Authorization: ${LINEAR_API_KEY}" \
    --data "$payload" \
    "https://api.linear.app/graphql"
}

resolve_state_ids() {
  log "Resolving Linear workflow states for team '${LINEAR_TEAM_KEY}'..."

  local response nodes
  response=$(linear_gql \
    '{ teams { nodes { key states { nodes { id name } } } } }')

  nodes=$(echo "$response" | jq -r \
    --arg k "$LINEAR_TEAM_KEY" \
    '.data.teams.nodes[] | select(.key == $k) | .states.nodes // empty' 2>/dev/null || true)

  if [ -z "$nodes" ] || [ "$nodes" = "null" ]; then
    echo "❌ Failed to fetch Linear workflow states." >&2
    echo "   Check LINEAR_API_KEY and LINEAR_TEAM_KEY (yours: '${LINEAR_TEAM_KEY}')." >&2
    echo "   Available teams: $(echo "$response" | jq -r '.data.teams.nodes[].key' 2>/dev/null | paste -sd ', ')" >&2
    exit 1
  fi

  STATE_ID_TRIGGER=$(echo "$nodes" | jq -r ".[] | select(.name == \"${TRIGGER_STATE}\") | .id")
  STATE_ID_IN_PROGRESS=$(echo "$nodes" | jq -r ".[] | select(.name == \"${IN_PROGRESS_STATE}\") | .id")
  STATE_ID_IN_REVIEW=$(echo "$nodes" | jq -r ".[] | select(.name == \"${IN_REVIEW_STATE}\") | .id")

  local missing=0
  [ -z "$STATE_ID_TRIGGER" ]     && { echo "❌ State not found: \"${TRIGGER_STATE}\"" >&2;     missing=$((missing + 1)); }
  [ -z "$STATE_ID_IN_PROGRESS" ] && { echo "❌ State not found: \"${IN_PROGRESS_STATE}\"" >&2; missing=$((missing + 1)); }
  [ -z "$STATE_ID_IN_REVIEW" ]   && { echo "❌ State not found: \"${IN_REVIEW_STATE}\"" >&2;   missing=$((missing + 1)); }

  if [ "$missing" -gt 0 ]; then
    echo "" >&2
    echo "   Available states: $(echo "$nodes" | jq -r '.[].name' | paste -sd ', ')" >&2
    echo "   Update TRIGGER_STATE / IN_PROGRESS_STATE / IN_REVIEW_STATE in .env" >&2
    exit 1
  fi

  log "✅ States resolved: \"$TRIGGER_STATE\" | \"$IN_PROGRESS_STATE\" | \"$IN_REVIEW_STATE\""
}

fetch_trigger_issues() {
  # Build the payload using the exact same approach as the confirmed-working manual curl.
  # Inline the state name directly — no GraphQL variables, no escaping layers.
  local payload response
  payload=$(jq -n --arg state "$TRIGGER_STATE" \
    '{"query": ("{ teams { nodes { issues(filter: { state: { name: { eq: \"" + $state + "\" } } }, orderBy: updatedAt, first: 20) { nodes { id identifier title description url } } } } }")}')

  response=$(curl -s -X POST \
    -H "Content-Type: application/json" \
    -H "Authorization: ${LINEAR_API_KEY}" \
    --data "$payload" \
    "https://api.linear.app/graphql")

  # Log raw response if nothing found, to aid debugging
  local count
  count=$(echo "$response" | jq '[.data.teams.nodes[].issues.nodes[]?] | length' 2>/dev/null || echo "0")
  if [ "${count}" = "0" ]; then
    log "  ↳ Raw response: $(echo "$response" | jq -c . 2>/dev/null || echo "$response")"
  fi

  echo "$response" | jq -c '.data.teams.nodes[].issues.nodes[]?' 2>/dev/null || true
}

linear_set_state() {
  local issue_id="$1"
  local state_id="$2"

  local response success
  response=$(linear_gql \
    'mutation($id: String!, $stateId: String!) {
      issueUpdate(id: $id, input: { stateId: $stateId }) { success }
    }' \
    "$(jq -n --arg id "$issue_id" --arg s "$state_id" '{"id":$id,"stateId":$s}')")

  success=$(echo "$response" | jq -r '.data.issueUpdate.success // false' 2>/dev/null || echo "false")
  if [ "$success" != "true" ]; then
    echo "⚠️  linear_set_state failed: $(echo "$response" | jq -c . 2>/dev/null || echo "$response")" >&2
    return 1
  fi
}

linear_comment() {
  local issue_id="$1"
  local body="$2"

  local response success
  response=$(linear_gql \
    'mutation($issueId: String!, $body: String!) {
      commentCreate(input: { issueId: $issueId, body: $body }) { success }
    }' \
    "$(jq -n --arg id "$issue_id" --arg b "$body" '{"issueId":$id,"body":$b}')")

  success=$(echo "$response" | jq -r '.data.commentCreate.success // false' 2>/dev/null || echo "false")
  if [ "$success" != "true" ]; then
    echo "⚠️  linear_comment failed: $(echo "$response" | jq -c . 2>/dev/null || echo "$response")" >&2
    return 1
  fi
}

# ─── Gemini Review Gate ──────────────────────────────────────────────────────

gemini_review() {
  local diff="$1"
  local ticket_title="$2"
  local ticket_description="$3"

  # Skip if no key configured — return PASS
  if [ -z "${GEMINI_API_KEY:-}" ]; then
    echo "PASS (Gemini not configured)"
    return 0
  fi

  local review_prompt
  review_prompt="You are a senior code reviewer. Review this diff against the ticket requirements.

## Ticket: ${ticket_title}
${ticket_description}

## Diff:
${diff}

## Review for:
1. Does the diff fully implement the ticket requirements?
2. Are there any bugs, logic errors, or edge cases missed?
3. Are there security concerns?
4. Does it follow reasonable coding conventions?
5. Are tests included and do they cover the key scenarios?

## Response format:
Start your response with exactly one of:
- VERDICT: PASS — if the implementation is good to merge (minor nits are fine)
- VERDICT: FAIL — if there are issues that should be fixed before merging

Then provide your review comments."

  local json_payload
  json_payload=$(jq -n \
    --arg prompt "$review_prompt" \
    '{
      "contents": [{"parts": [{"text": $prompt}]}],
      "generationConfig": {"temperature": 0.1, "maxOutputTokens": 4096}
    }')

  local response review_text
  response=$(curl -s \
    "https://generativelanguage.googleapis.com/v1beta/models/${GEMINI_MODEL}:generateContent?key=${GEMINI_API_KEY}" \
    -H "Content-Type: application/json" \
    -d "$json_payload")

  review_text=$(echo "$response" | jq -r '.candidates[0].content.parts[0].text // "ERROR: Could not parse Gemini response"' 2>/dev/null || echo "ERROR: Gemini request failed")

  echo "$review_text"

  if echo "$review_text" | grep -qi "VERDICT: PASS"; then
    return 0
  else
    return 1
  fi
}

# ─── Worktree Management ─────────────────────────────────────────────────────

create_worktree() {
  local identifier="$1"
  local branch_name
  branch_name="nightshift/$(echo "$identifier" | tr '[:upper:]' '[:lower:]')"
  local worktree_path="$WORKTREE_BASE/$identifier"

  mkdir -p "$WORKTREE_BASE"

  cd "$REPO_PATH"
  git fetch origin "$MAIN_BRANCH" --quiet >/dev/null 2>&1 || true

  # Remove stale worktree if exists
  if [ -d "$worktree_path" ]; then
    git worktree remove "$worktree_path" --force >/dev/null 2>&1 || rm -rf "$worktree_path"
  fi

  # Remove stale branch if exists
  git branch -D "$branch_name" >/dev/null 2>&1 || true

  git worktree add "$worktree_path" -b "$branch_name" "origin/$MAIN_BRANCH" >/dev/null 2>&1

  echo "$worktree_path"
}

cleanup_worktree() {
  local identifier="$1"
  local worktree_path="$WORKTREE_BASE/$identifier"

  cd "$REPO_PATH"
  git worktree remove "$worktree_path" --force 2>/dev/null || true
}

# ─── Cleanup ────────────────────────────────────────────────────────────────

# Lightweight cleanup run silently on every startup
startup_cleanup() {
  log "Running startup cleanup..."
  git -C "$REPO_PATH" worktree prune 2>/dev/null || true
  git -C "$REPO_PATH" fetch --prune 2>/dev/null || true
  log "✅ Startup cleanup done"
}

# Full cleanup command: worktrees, branches, remote refs, old logs
run_cleanup() {
  local force="${1:-false}"
  local worktrees_removed=0
  local merged_deleted=0
  local unmerged_deleted=0
  local logs_deleted=0

  echo ""
  echo "🧹 Nightshift Cleanup"
  echo ""

  # ── Validate REPO_PATH ────────────────────────────────────────────────────
  if [ -z "$REPO_PATH" ]; then
    echo "❌ REPO_PATH is required — set it in .env" >&2
    exit 1
  elif [ ! -d "$REPO_PATH/.git" ]; then
    echo "❌ REPO_PATH ($REPO_PATH) is not a git repository" >&2
    exit 1
  fi

  # ── Stale worktrees ───────────────────────────────────────────────────────
  if [ -d "$WORKTREE_BASE" ]; then
    local worktree_dirs=()
    while IFS= read -r -d '' dir; do
      worktree_dirs+=("$dir")
    done < <(find "$WORKTREE_BASE" -mindepth 1 -maxdepth 1 -type d -print0 2>/dev/null)

    if [ "${#worktree_dirs[@]}" -gt 0 ]; then
      echo "Worktrees to remove (${#worktree_dirs[@]}):"
      for dir in "${worktree_dirs[@]}"; do
        echo "  - $(basename "$dir")"
      done

      if [ "$force" = true ] || confirm "Remove these worktrees?"; then
        for dir in "${worktree_dirs[@]}"; do
          git -C "$REPO_PATH" worktree remove "$dir" --force 2>/dev/null || rm -rf "$dir"
          worktrees_removed=$((worktrees_removed + 1))
        done
      fi
      echo ""
    fi
  fi

  git -C "$REPO_PATH" worktree prune 2>/dev/null || true

  # ── Merged local branches ─────────────────────────────────────────────────
  local merged_branches=()
  mapfile -t merged_branches < <(git -C "$REPO_PATH" branch --merged "$MAIN_BRANCH" 2>/dev/null \
    | grep -vE "^\*|main|staging|master" \
    | sed 's/^[[:space:]]*//' || true)

  if [ "${#merged_branches[@]}" -gt 0 ]; then
    echo "Merged branches to delete (${#merged_branches[@]}):"
    printf "  - %s\n" "${merged_branches[@]}"

    if [ "$force" = true ] || confirm "Delete these merged branches?"; then
      for branch in "${merged_branches[@]}"; do
        git -C "$REPO_PATH" branch -d "$branch" 2>/dev/null && merged_deleted=$((merged_deleted + 1))
      done
    fi
    echo ""
  fi

  # ── Unmerged nightshift branches ──────────────────────────────────────────
  local unmerged=""
  unmerged=$(git -C "$REPO_PATH" branch --no-merged "$MAIN_BRANCH" 2>/dev/null \
    | grep -E "nightshift/" \
    | sed 's/^[[:space:]]*//' || true)

  if [ -n "$unmerged" ]; then
    echo "⚠️  Unmerged Nightshift branches (from failed runs):"
    echo "$unmerged" | while IFS= read -r b; do echo "  - $b"; done

    # Check for open PRs before deleting
    local safe_to_delete=()
    local has_open_pr=()
    while IFS= read -r branch; do
      [ -z "$branch" ] && continue
      local pr_count
      pr_count=$(gh pr list --repo "$(git -C "$REPO_PATH" remote get-url origin 2>/dev/null)" \
        --head "$branch" --state open --json number 2>/dev/null \
        | jq 'length' 2>/dev/null || echo "0")
      if [ "$pr_count" -gt 0 ]; then
        has_open_pr+=("$branch")
      else
        safe_to_delete+=("$branch")
      fi
    done <<< "$unmerged"

    if [ "${#has_open_pr[@]}" -gt 0 ]; then
      echo ""
      echo "  Skipping (have open PRs):"
      for b in "${has_open_pr[@]}"; do echo "    - $b"; done
    fi

    if [ "${#safe_to_delete[@]}" -gt 0 ]; then
      echo ""
      if [ "$force" = true ] || confirm "Force-delete ${#safe_to_delete[@]} unmerged branch(es) without open PRs?"; then
        for branch in "${safe_to_delete[@]}"; do
          git -C "$REPO_PATH" branch -D "$branch" 2>/dev/null && unmerged_deleted=$((unmerged_deleted + 1))
        done
      fi
    fi
    echo ""
  fi

  # ── Prune remote tracking refs ────────────────────────────────────────────
  echo "Pruning stale remote tracking refs..."
  git -C "$REPO_PATH" fetch --prune 2>/dev/null || true
  echo ""

  # ── Old agent logs (>7 days) ──────────────────────────────────────────────
  if [ -d "$LOG_DIR" ]; then
    local old_logs=()
    while IFS= read -r -d '' f; do
      old_logs+=("$f")
    done < <(find "$LOG_DIR" -name "*.log" -mtime +7 -print0 2>/dev/null)

    if [ "${#old_logs[@]}" -gt 0 ]; then
      echo "Agent logs older than 7 days (${#old_logs[@]}):"
      for f in "${old_logs[@]}"; do echo "  - $(basename "$f")"; done

      if [ "$force" = true ] || confirm "Delete these old log files?"; then
        for f in "${old_logs[@]}"; do
          rm -f "$f" && logs_deleted=$((logs_deleted + 1))
        done
      fi
      echo ""
    fi
  fi

  # ── Summary ───────────────────────────────────────────────────────────────
  echo "🧹 Cleanup complete:"
  echo "  - Removed $worktrees_removed worktree(s)"
  echo "  - Deleted $merged_deleted merged branch(es)"
  echo "  - Force-deleted $unmerged_deleted unmerged branch(es)"
  echo "  - Pruned remote tracking refs"
  echo "  - Cleared $logs_deleted agent log(s) older than 7 days"
  echo ""
}

confirm() {
  local prompt="$1"
  echo -n "$prompt [y/N] "
  local answer
  read -r answer
  [[ "$answer" =~ ^[Yy]$ ]]
}

# ─── Claude Invocation ───────────────────────────────────────────────────────

run_claude() {
  local prompt="$1"
  local log_file="$2"
  local timeout_minutes="${AGENT_TIMEOUT_MINUTES}"

  if [ "${USE_AGENT_TEAMS}" = "true" ]; then
    timeout "${timeout_minutes}m" bash -c \
      'CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1 claude \
        --dangerously-skip-permissions \
        --print \
        --output-format text \
        -p "$1" >> "$2" 2>&1' _ "$prompt" "$log_file"
  else
    timeout "${timeout_minutes}m" claude \
      --dangerously-skip-permissions \
      --print \
      --output-format text \
      -p "$prompt" >> "$log_file" 2>&1
  fi
}

build_prompt() {
  local identifier="$1"
  local title="$2"
  local description="$3"

  if [ "${USE_AGENT_TEAMS}" = "true" ]; then
    cat <<PROMPT
You are a lead agent implementing a Linear ticket. You have a team of agents available.

## Ticket: ${identifier} — ${title}
${description}

## Your approach:
1. First, read the codebase to understand the project structure, conventions, and testing patterns.
2. Plan your implementation approach and break it into parallel tasks where possible.
3. Delegate implementation tasks to your teammates:
   - One teammate for the core implementation
   - One teammate for writing/updating tests
   - One teammate for reviewing the changes against the ticket requirements
4. Coordinate the results and ensure everything is consistent.
5. Run the full test suite and fix any failures.
6. Run the linter and fix any issues.

## Rules:
- Stay focused on this ticket only — do not modify unrelated code.
- Follow existing project conventions and patterns exactly.
- If you get stuck or need human input, say BLOCKED: <reason> and stop.
- Do NOT create PRs or push branches — Nightshift handles that.

## When done:
Provide a brief summary of what was implemented and any important decisions made.
PROMPT
  else
    cat <<PROMPT
You are implementing a Linear ticket.

## Ticket: ${identifier} — ${title}
${description}

## Instructions:
1. Read the codebase to understand the project structure and conventions.
2. Implement the ticket requirements.
3. Write or update tests as needed.
4. Run the test suite and fix any failures.
5. Run the linter and fix any issues.

## Rules:
- Stay focused on this ticket only — do not modify unrelated code.
- Follow existing project conventions and patterns exactly.
- If you get stuck or need human input, say BLOCKED: <reason> and stop.
- Do NOT create PRs or push branches — Nightshift handles that.

## When done:
Provide a brief summary of what was implemented and any important decisions made.
PROMPT
  fi
}

# ─── Ticket Processor (runs in background subshell) ──────────────────────────

process_ticket() {
  local issue_json="$1"

  local issue_id identifier title description url
  issue_id=$(echo "$issue_json"    | jq -r '.id')
  identifier=$(echo "$issue_json"  | jq -r '.identifier')
  title=$(echo "$issue_json"       | jq -r '.title')
  description=$(echo "$issue_json" | jq -r '.description // "No description provided."')
  url=$(echo "$issue_json"         | jq -r '.url')

  local log_file="$LOG_DIR/${identifier}.log"
  echo "--- Attempt $(date -Iseconds) ---" >> "$log_file"

  tlog "$identifier" "Starting: $title"
  tlog "$identifier" "Log: $log_file"

  # ── Move to In Progress ────────────────────────────────────────────────────
  if linear_set_state "$issue_id" "$STATE_ID_IN_PROGRESS" 2>/dev/null; then
    tlog "$identifier" "Moved to '$IN_PROGRESS_STATE'"
  else
    tlog "$identifier" "⚠️  Could not update state to '$IN_PROGRESS_STATE' — continuing anyway"
  fi

  # ── Create Worktree ────────────────────────────────────────────────────────
  local worktree_path
  if ! worktree_path=$(create_worktree "$identifier" 2>&1); then
    tlog "$identifier" "❌ Worktree creation failed: $worktree_path"
    linear_set_state "$issue_id" "$STATE_ID_TRIGGER" 2>/dev/null || true
    linear_comment "$issue_id" "❌ **Nightshift: Setup failed**

Could not create an isolated git worktree. This may be a branch naming conflict.

Check that branch \`nightshift/$(echo "$identifier" | tr '[:upper:]' '[:lower:]')\` does not already exist on the remote.

Ticket moved back to **${TRIGGER_STATE}**." 2>/dev/null || true
    return 1
  fi

  tlog "$identifier" "Worktree: $worktree_path"
  cd "$worktree_path"

  # ── Run Claude ─────────────────────────────────────────────────────────────
  local prompt
  prompt=$(build_prompt "$identifier" "$title" "$description")

  tlog "$identifier" "Running Claude (agent-teams=${USE_AGENT_TEAMS}, timeout=${AGENT_TIMEOUT_MINUTES}m)..."
  local exit_code=0
  run_claude "$prompt" "$log_file" || exit_code=$?

  # ── Check for timeout ───────────────────────────────────────────────────────
  if [ "$exit_code" -eq 124 ]; then
    tlog "$identifier" "⏰ Timed out after ${AGENT_TIMEOUT_MINUTES} minutes"
    FAILED_ATTEMPTS["$identifier"]=$(( ${FAILED_ATTEMPTS["$identifier"]:-0} + 1 ))
    FAIL_COUNT=$((FAIL_COUNT + 1))
    linear_set_state "$issue_id" "$STATE_ID_TRIGGER" 2>/dev/null || true
    linear_comment "$issue_id" "⏰ **Nightshift: Agent timed out**

Claude timed out after ${AGENT_TIMEOUT_MINUTES} minutes working on this ticket.

The ticket may be too complex for a single session. Consider breaking it into smaller tasks.

Ticket moved back to **${TRIGGER_STATE}**." 2>/dev/null || true
    notify "⏰ *${identifier}* — ${title}
Timed out after ${AGENT_TIMEOUT_MINUTES}m. Moving back to ${TRIGGER_STATE}."
    cleanup_worktree "$identifier" 2>/dev/null || true
    return 1
  fi

  # ── Check for rate/usage limit ──────────────────────────────────────────────
  if grep -qi "rate.limit\|usage.limit\|exceeded.*limit\|too many requests" "$log_file" 2>/dev/null; then
    tlog "$identifier" "🛑 Usage/rate limit detected in agent output — triggering shutdown"
    FAILED_ATTEMPTS["$identifier"]=$(( ${FAILED_ATTEMPTS["$identifier"]:-0} + 1 ))
    FAIL_COUNT=$((FAIL_COUNT + 1))
    linear_set_state "$issue_id" "$STATE_ID_TRIGGER" 2>/dev/null || true
    linear_comment "$issue_id" "🛑 **Nightshift: Rate limit detected**

Claude hit a usage or rate limit while working on this ticket.

Ticket moved back to **${TRIGGER_STATE}**. Nightshift is shutting down to avoid further limit hits." 2>/dev/null || true
    notify "🛑 *Usage limit detected*
Nightshift stopped after ${TOTAL_DISPATCHES} dispatches.
✅ ${SUCCESS_COUNT} PRs created | ❌ ${FAIL_COUNT} failed"
    cleanup_worktree "$identifier" 2>/dev/null || true
    SHUTTING_DOWN=true
    return 1
  fi

  # ── Check for non-zero exit ─────────────────────────────────────────────────
  if [ "$exit_code" -ne 0 ]; then
    FAILED_ATTEMPTS["$identifier"]=$(( ${FAILED_ATTEMPTS["$identifier"]:-0} + 1 ))
    local attempts="${FAILED_ATTEMPTS["$identifier"]}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
    tlog "$identifier" "❌ Claude exited with error (exit $exit_code, attempt $attempts/$MAX_RETRIES)"
    linear_set_state "$issue_id" "$STATE_ID_TRIGGER" 2>/dev/null || true
    linear_comment "$issue_id" "❌ **Nightshift: Agent failed** (attempt ${attempts}/${MAX_RETRIES})

Claude exited with code \`${exit_code}\`. Will retry on next poll cycle (up to ${MAX_RETRIES} attempts).

Ticket moved back to **${TRIGGER_STATE}**." 2>/dev/null || true
    notify "❌ *${identifier}* — ${title}
Failed (attempt ${attempts}/${MAX_RETRIES}, exit code ${exit_code})"
    cleanup_worktree "$identifier" 2>/dev/null || true
    return 1
  fi

  # ── Check for BLOCKED ──────────────────────────────────────────────────────
  local claude_output blocked_line
  claude_output=$(cat "$log_file" 2>/dev/null || true)
  blocked_line=$(echo "$claude_output" | grep -i "^BLOCKED:" | head -1 || true)

  if [ -n "$blocked_line" ]; then
    tlog "$identifier" "⚠️  Blocked: $blocked_line"
    linear_set_state "$issue_id" "$STATE_ID_TRIGGER" 2>/dev/null || true
    linear_comment "$issue_id" "🚧 **Nightshift needs your input**

Claude got blocked on this ticket:

> ${blocked_line}

Please clarify in the ticket comments, then move back to **${TRIGGER_STATE}** to retry." 2>/dev/null || true
    notify "⚠️ *${identifier}* — Blocked
${blocked_line}"
    cleanup_worktree "$identifier" 2>/dev/null || true
    tlog "$identifier" "Moved back to '$TRIGGER_STATE'"
    return 0
  fi

  # ── Check for Changes ──────────────────────────────────────────────────────
  cd "$worktree_path"
  local has_changes
  has_changes=$(git status --porcelain 2>/dev/null || true)

  if [ -z "$has_changes" ]; then
    tlog "$identifier" "⚠️  No changes made"
    linear_set_state "$issue_id" "$STATE_ID_TRIGGER" 2>/dev/null || true
    linear_comment "$issue_id" "💭 **Nightshift: No code changes made**

Claude completed the session without modifying any files. This usually means the ticket description is too vague or needs more context.

Add more detail to the ticket description and move back to **${TRIGGER_STATE}** to retry. See the [Writing Good Tickets guide](https://github.com/your-org/nightshift/blob/main/docs/WRITING-GOOD-TICKETS.md) for tips." 2>/dev/null || true
    cleanup_worktree "$identifier" 2>/dev/null || true
    return 0
  fi

  git add -A

  # ── Gemini Review Gate ─────────────────────────────────────────────────────
  local review_passed=false
  local review_output=""
  local review_attempt=0

  if [ -n "${GEMINI_API_KEY:-}" ]; then
    tlog "$identifier" "Running Gemini review gate..."
    local gemini_log="$LOG_DIR/${identifier}-gemini.log"
    : > "$gemini_log"

    while [ "$review_attempt" -le "$MAX_REVIEW_RETRIES" ]; do
      local diff_content
      diff_content=$(git diff --cached 2>/dev/null || git diff HEAD 2>/dev/null || echo "")

      local review_verdict=0
      review_output=$(gemini_review "$diff_content" "$title" "$description") || review_verdict=$?

      {
        echo "--- Attempt $((review_attempt + 1)) / $((MAX_REVIEW_RETRIES + 1)) ---"
        echo "$review_output"
        echo ""
      } >> "$gemini_log"

      if [ "$review_verdict" -eq 0 ]; then
        review_passed=true
        tlog "$identifier" "✅ Gemini review: PASS"
        break
      fi

      tlog "$identifier" "🔄 Gemini flagged issues (attempt $((review_attempt + 1))/$((MAX_REVIEW_RETRIES + 1)))"

      if [ "$review_attempt" -lt "$MAX_REVIEW_RETRIES" ]; then
        local fix_prompt
        fix_prompt="A code reviewer found issues with your implementation. Please fix them.

## Reviewer feedback:
${review_output}

## Rules:
- Only address the specific issues mentioned in the feedback above.
- Do not change anything else.
- Run tests after fixing to make sure nothing broke."

        tlog "$identifier" "Asking Claude to fix review issues..."
        local fix_exit=0
        run_claude "$fix_prompt" "$log_file" || fix_exit=$?
        if [ "$fix_exit" -ne 0 ]; then
          tlog "$identifier" "⚠️  Fix attempt exited with code $fix_exit"
        fi
        git add -A
      fi

      review_attempt=$((review_attempt + 1))
    done

    if [ "$review_passed" = false ]; then
      tlog "$identifier" "⚠️  Gemini did not pass after $((review_attempt)) attempt(s) — creating PR with review comments"
    fi
  fi

  # ── Commit & Push ──────────────────────────────────────────────────────────
  cd "$worktree_path"
  local branch_name
  branch_name=$(git branch --show-current)

  local impl_mode=""
  [ "${USE_AGENT_TEAMS}" = "true" ] && impl_mode=" (Agent Teams)"

  git commit -m "$(cat <<EOF
feat: implement ${identifier} — ${title}

Implemented by Nightshift using Claude Code${impl_mode}

Linear: ${url}
EOF
)"

  git push -u origin "$branch_name"
  tlog "$identifier" "Pushed: $branch_name"

  # ── Build PR Body ──────────────────────────────────────────────────────────
  # Extract the last ~1200 chars of Claude output as a summary
  local summary
  summary=$(tail -n 40 "$log_file" 2>/dev/null || true)

  local review_section=""
  if [ -n "${GEMINI_API_KEY:-}" ]; then
    if [ "$review_passed" = true ]; then
      review_section="
---

✅ **Multi-model review:** Passed (Gemini \`${GEMINI_MODEL}\`)"
    else
      review_section="

---

⚠️ **Multi-model review:** Did not pass after ${review_attempt} attempt(s). Please review before merging:

<details>
<summary>Gemini review comments</summary>

\`\`\`
${review_output}
\`\`\`

</details>"
    fi
  fi

  local pr_body
  pr_body="## ${identifier}: ${title}

**Linear:** ${url}

## What was implemented

${summary}
${review_section}
---

*Implemented by [Nightshift](https://github.com/your-org/nightshift) 🌙 using Claude Code${impl_mode}*"

  # ── Create PR ──────────────────────────────────────────────────────────────
  cd "$REPO_PATH"
  local pr_url=""
  if ! pr_url=$(gh pr create \
    --title "${identifier}: ${title}" \
    --body "$pr_body" \
    --base "$MAIN_BRANCH" \
    --head "$branch_name" 2>&1); then
    tlog "$identifier" "❌ PR creation failed: $pr_url"
    linear_set_state "$issue_id" "$STATE_ID_TRIGGER" 2>/dev/null || true
    linear_comment "$issue_id" "❌ **Nightshift: PR creation failed**

The branch \`${branch_name}\` was pushed, but \`gh pr create\` failed.

Check that you have push access to the repository and that \`gh\` is authenticated.

Error:
\`\`\`
${pr_url}
\`\`\`

Ticket moved back to **${TRIGGER_STATE}**." 2>/dev/null || true
    cleanup_worktree "$identifier" 2>/dev/null || true
    return 1
  fi

  tlog "$identifier" "✅ PR created: $pr_url"
  SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
  notify "✅ *${identifier}* — ${title}
PR ready: ${pr_url}"

  # ── Move to In Review & Comment ────────────────────────────────────────────
  linear_set_state "$issue_id" "$STATE_ID_IN_REVIEW" 2>/dev/null || true
  linear_comment "$issue_id" "🌙 **Nightshift created a PR**

**PR:** ${pr_url}

Moved to **${IN_REVIEW_STATE}**. Ready for your review!" 2>/dev/null || true

  tlog "$identifier" "✅ Done — moved to '$IN_REVIEW_STATE'"

  # ── Cleanup ────────────────────────────────────────────────────────────────
  cleanup_worktree "$identifier" 2>/dev/null || true
}

# ─── Process Management ──────────────────────────────────────────────────────

cleanup_finished_pids() {
  [ "${#ACTIVE_PIDS[@]}" -eq 0 ] && return
  local new_pids=()
  for pid in "${ACTIVE_PIDS[@]}"; do
    if kill -0 "$pid" 2>/dev/null; then
      new_pids+=("$pid")
    else
      unset "PID_TO_IDENTIFIER[$pid]" 2>/dev/null || true
    fi
  done
  if [ "${#new_pids[@]}" -eq 0 ]; then
    ACTIVE_PIDS=()
  else
    ACTIVE_PIDS=("${new_pids[@]}")
  fi
}

is_ticket_active() {
  local check_id="$1"
  [ "${#PID_TO_IDENTIFIER[@]}" -eq 0 ] && return 1
  for pid in "${!PID_TO_IDENTIFIER[@]}"; do
    if [ "${PID_TO_IDENTIFIER[$pid]}" = "$check_id" ] && kill -0 "$pid" 2>/dev/null; then
      return 0
    fi
  done
  return 1
}

# ─── Graceful Shutdown ───────────────────────────────────────────────────────

handle_shutdown() {
  if [ "$SHUTTING_DOWN" = true ]; then
    return
  fi
  SHUTTING_DOWN=true
  echo ""
  log "🌅 Shutting down — waiting for ${#ACTIVE_PIDS[@]} active task(s) to finish..."

  while true; do
    cleanup_finished_pids
    local remaining="${#ACTIVE_PIDS[@]}"
    if [ "$remaining" -eq 0 ]; then
      break
    fi
    log "  Still running: $remaining task(s)..."
    sleep 5
  done

  local duration
  duration=$(format_duration $(( SECONDS - SESSION_START )))
  notify "🌅 *Nightshift session complete*
✅ ${SUCCESS_COUNT} PRs created
❌ ${FAIL_COUNT} failed
⏱ Session duration: ${duration}"
  log "👋 All done. Good morning."
  exit 0
}

trap handle_shutdown SIGINT SIGTERM

# ─── Startup Banner ──────────────────────────────────────────────────────────

print_banner() {
  local agent_mode review_mode notify_mode
  agent_mode="$( [ "${USE_AGENT_TEAMS}" = "true" ] && echo "Agent Teams" || echo "Single agent")"
  review_mode="$( [ -n "${GEMINI_API_KEY:-}" ] && echo "Gemini (${GEMINI_MODEL})" || echo "Disabled")"
  notify_mode="$( [ "${TELEGRAM_ENABLED}" = "true" ] && echo "Telegram" || echo "Disabled")"

  echo ""
  printf "🌙 Nightshift v%s\n" "$VERSION"
  printf "   Repo:           %s\n" "$REPO_PATH"
  printf "   Team:           %s\n" "$LINEAR_TEAM_KEY"
  printf "   Watching:       \"%s\" column\n" "$TRIGGER_STATE"
  printf "   Mode:           %s\n" "$agent_mode"
  printf "   Review:         %s\n" "$review_mode"
  printf "   Max concurrent: %s\n" "$MAX_CONCURRENT"
  printf "   Poll interval:  %ss\n" "$POLL_INTERVAL"
  printf "   Agent timeout:  %sm\n" "$AGENT_TIMEOUT_MINUTES"
  printf "   Max retries:    %s per ticket\n" "$MAX_RETRIES"
  printf "   Max dispatches: %s per session\n" "$MAX_DISPATCHES"
  printf "   Notifications:  %s\n" "$notify_mode"
  echo ""
  echo "Waiting for tickets... (Ctrl+C to stop)"
  echo ""
}

# ─── Main Loop ───────────────────────────────────────────────────────────────

main() {
  validate_config
  mkdir -p "$LOG_DIR" "$WORKTREE_BASE"
  startup_cleanup
  resolve_state_ids
  print_banner
  notify "🌙 *Nightshift started*
Watching \"${TRIGGER_STATE}\" column for ${LINEAR_TEAM_KEY} tickets"

  while true; do
    if [ "$SHUTTING_DOWN" = true ]; then
      break
    fi

    cleanup_finished_pids
    local active="${#ACTIVE_PIDS[@]}"
    local slots=$(( MAX_CONCURRENT - active ))

    # Fetch tickets and capture output so we can log it
    local issues_raw=""
    issues_raw=$(fetch_trigger_issues 2>/dev/null || true)

    # Count non-empty lines (each line is one JSON ticket object)
    local found=0
    if [ -n "$issues_raw" ]; then
      found=$(echo "$issues_raw" | grep -c '"identifier"' 2>/dev/null || true)
      found="${found:-0}"
    fi

    log "🔍 Poll — \"${TRIGGER_STATE}\": ${found} ticket(s) | active: ${active}/${MAX_CONCURRENT}"

    # ── Check session dispatch cap ─────────────────────────────────────────
    if [ "$TOTAL_DISPATCHES" -ge "$MAX_DISPATCHES" ]; then
      log "🛑 Reached max $MAX_DISPATCHES dispatches this session — shutting down"
      local duration
      duration=$(format_duration $(( SECONDS - SESSION_START )))
      notify "🛑 *Dispatch cap reached* (${MAX_DISPATCHES})
✅ ${SUCCESS_COUNT} PRs created | ❌ ${FAIL_COUNT} failed
⏱ ${duration}"
      break
    fi

    if [ "$slots" -gt 0 ] && [ "$found" -gt 0 ]; then
      local dispatched=0

      while IFS= read -r issue_json; do
        [ -z "$issue_json" ] && continue
        [ "$dispatched" -ge "$slots" ] && break

        # Re-check dispatch cap inside loop
        if [ "$TOTAL_DISPATCHES" -ge "$MAX_DISPATCHES" ]; then
          log "🛑 Reached max $MAX_DISPATCHES dispatches this session — stopping new dispatches"
          break
        fi

        local identifier
        identifier=$(echo "$issue_json" | jq -r '.identifier')

        # Skip tickets already being processed
        if is_ticket_active "$identifier"; then
          log "  ⏭  $identifier already in progress — skipping"
          continue
        fi

        # Skip tickets that have exceeded retry limit
        local attempts="${FAILED_ATTEMPTS["$identifier"]:-0}"
        if [ "$attempts" -ge "$MAX_RETRIES" ]; then
          log "  ❌ $identifier failed $attempts times — skipping until restart"
          continue
        fi

        log "🎯 Dispatching: $identifier — $(echo "$issue_json" | jq -r '.title')"

        # Spawn background worker (subshell for isolation)
        (
          set +e
          process_ticket "$issue_json"
        ) &

        local pid=$!
        ACTIVE_PIDS+=("$pid")
        PID_TO_IDENTIFIER[$pid]="$identifier"
        dispatched=$(( dispatched + 1 ))
        TOTAL_DISPATCHES=$(( TOTAL_DISPATCHES + 1 ))
      done <<< "$issues_raw"
    fi

    sleep "$POLL_INTERVAL"
  done
}

# ─── Entrypoint ───────────────────────────────────────────────────────────────

if [[ "${1:-}" == "cleanup" ]]; then
  if [[ "${2:-}" == "--force" ]]; then
    run_cleanup true
  else
    run_cleanup false
  fi
else
  main "$@"
fi
