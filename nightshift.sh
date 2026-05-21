#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# nightshift.sh — Autonomous Linear → Pull Request agent
# "Move tickets to Next. Go to sleep. Wake up to PRs."
#
# Usage:  ./nightshift.sh              # start the poll loop
#         ./nightshift.sh setup        # interactive setup wizard (.env + repos.json)
#         ./nightshift.sh cleanup      # interactive cleanup of stale resources
#         ./nightshift.sh cleanup --force  # non-interactive cleanup
# Config: .env + repos.json (run ./nightshift.sh setup, or copy the examples)
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
WORKTREE_BASE="$HOME/.nightshift-worktrees"

# Repo registry — maps Linear project names to git repos
REPOS_FILE="${REPOS_FILE:-$SCRIPT_DIR/repos.json}"
REPOS_BASE="${REPOS_BASE:-$HOME/.nightshift-repos}"

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
STATE_ID_IN_REVIEW=""

# ─── Validation ──────────────────────────────────────────────────────────────

validate_config() {
  local errors=0

  if [ -z "$LINEAR_API_KEY" ]; then
    echo "❌ LINEAR_API_KEY is required — set it in .env" >&2
    errors=$((errors + 1))
  fi

  # Repo source: a repos.json registry, a REPO_PATH fallback, or both.
  local has_registry=false
  if [ -f "$REPOS_FILE" ]; then
    if jq -e '.repos | type == "object"' "$REPOS_FILE" >/dev/null 2>&1; then
      has_registry=true
    else
      echo "❌ $REPOS_FILE exists but is not valid JSON with a \".repos\" object" >&2
      errors=$((errors + 1))
    fi
  fi

  if [ -n "$REPO_PATH" ] && [ ! -d "$REPO_PATH/.git" ]; then
    echo "❌ REPO_PATH ($REPO_PATH) is not a git repository" >&2
    errors=$((errors + 1))
  fi

  if [ "$has_registry" = false ] && [ -z "$REPO_PATH" ]; then
    echo "❌ No repos configured — run ./nightshift.sh setup," >&2
    echo "   create $REPOS_FILE, or set REPO_PATH in .env" >&2
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
  STATE_ID_IN_REVIEW=$(echo "$nodes" | jq -r ".[] | select(.name == \"${IN_REVIEW_STATE}\") | .id")

  local missing=0
  [ -z "$STATE_ID_TRIGGER" ]   && { echo "❌ State not found: \"${TRIGGER_STATE}\"" >&2;   missing=$((missing + 1)); }
  [ -z "$STATE_ID_IN_REVIEW" ] && { echo "❌ State not found: \"${IN_REVIEW_STATE}\"" >&2; missing=$((missing + 1)); }

  if [ "$missing" -gt 0 ]; then
    echo "" >&2
    echo "   Available states: $(echo "$nodes" | jq -r '.[].name' | paste -sd ', ')" >&2
    echo "   Update TRIGGER_STATE / IN_REVIEW_STATE in .env" >&2
    exit 1
  fi

  log "✅ States resolved: \"$TRIGGER_STATE\" | \"$IN_REVIEW_STATE\""
}

fetch_trigger_issues() {
  # Build the payload using the exact same approach as the confirmed-working manual curl.
  # Inline the state name directly — no GraphQL variables, no escaping layers.
  local payload response
  payload=$(jq -n --arg state "$TRIGGER_STATE" \
    '{"query": ("{ teams { nodes { issues(filter: { state: { name: { eq: \"" + $state + "\" } } }, orderBy: updatedAt, first: 20) { nodes { id identifier title description url project { name } } } } } }")}')

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

# ─── Repo Registry & Resolution ──────────────────────────────────────────────

# Slugify a Linear project name into a filesystem-safe directory name.
repo_slug() {
  printf '%s' "$1" \
    | tr '[:upper:]' '[:lower:]' \
    | tr -c 'a-z0-9' '-' \
    | sed -E 's/-+/-/g; s/^-//; s/-$//'
}

# Look up a project in repos.json. On hit echoes "<url>\t<main_branch>", returns 0.
registry_lookup() {
  local project="$1"
  [ -z "$project" ] && return 1
  [ -f "$REPOS_FILE" ] || return 1

  local entry
  entry=$(jq -c --arg p "$project" '.repos[$p] // empty' "$REPOS_FILE" 2>/dev/null || true)
  [ -z "$entry" ] && return 1

  local url branch
  url=$(printf '%s' "$entry"    | jq -r '.url // empty')
  branch=$(printf '%s' "$entry" | jq -r '.main_branch // empty')
  [ -z "$branch" ] && branch="$MAIN_BRANCH"
  [ -z "$url" ] && return 1

  printf '%s\t%s\n' "$url" "$branch"
}

# Clone a repo into dest if not already present. An mkdir-based lock keeps two
# concurrent tickets in the same project from racing the same clone.
ensure_repo_cloned() {
  local url="$1" dest="$2"
  [ -d "$dest/.git" ] && return 0

  local lock="${dest}.clone-lock"
  local waited=0
  while ! mkdir "$lock" 2>/dev/null; do
    [ -d "$dest/.git" ] && return 0
    sleep 2
    waited=$((waited + 2))
    if [ "$waited" -ge 600 ]; then
      echo "clone lock timed out for $dest" >&2
      return 1
    fi
  done

  local rc=0
  if [ ! -d "$dest/.git" ]; then
    git clone "$url" "$dest" >/dev/null 2>&1 || rc=1
  fi
  rmdir "$lock" 2>/dev/null || true
  return "$rc"
}

# Resolve which repo a ticket targets, from its Linear project name.
# On success echoes "<repo_path>\t<main_branch>" and returns 0.
# On failure echoes a human-readable reason and returns 1.
resolve_repo() {
  local project="$1"

  local hit
  if hit=$(registry_lookup "$project"); then
    local url branch slug dest
    url=$(printf '%s' "$hit"    | cut -f1)
    branch=$(printf '%s' "$hit" | cut -f2)
    slug=$(repo_slug "$project")
    dest="$REPOS_BASE/$slug"

    if [ ! -d "$dest/.git" ]; then
      if ! git ls-remote --exit-code "$url" HEAD >/dev/null 2>&1; then
        echo "Cannot access \`${url}\` — the host running Nightshift needs git auth for it (an SSH key, or \`gh auth login\` for HTTPS URLs)."
        return 1
      fi
      mkdir -p "$REPOS_BASE"
      if ! ensure_repo_cloned "$url" "$dest"; then
        echo "Failed to clone \`${url}\`."
        return 1
      fi
    fi

    printf '%s\t%s\n' "$dest" "$branch"
    return 0
  fi

  # Fallback to the single-repo .env setting
  if [ -n "$REPO_PATH" ] && [ -d "$REPO_PATH/.git" ]; then
    printf '%s\t%s\n' "$REPO_PATH" "$MAIN_BRANCH"
    return 0
  fi

  if [ -z "$project" ]; then
    echo "This ticket has no Linear project, and no REPO_PATH fallback is configured."
  else
    echo "No repo is mapped for the project \"${project}\" in repos.json, and no REPO_PATH fallback is configured."
  fi
  return 1
}

# Echo the local path of every repo Nightshift knows about (one per line).
all_repo_paths() {
  {
    if [ -f "$REPOS_FILE" ]; then
      local p slug dest
      while IFS= read -r p; do
        [ -z "$p" ] && continue
        slug=$(repo_slug "$p")
        dest="$REPOS_BASE/$slug"
        [ -d "$dest/.git" ] && echo "$dest"
      done < <(jq -r '.repos | keys[]' "$REPOS_FILE" 2>/dev/null || true)
    fi
    [ -n "$REPO_PATH" ] && [ -d "$REPO_PATH/.git" ] && echo "$REPO_PATH"
  } | awk '!seen[$0]++'
}

# ─── Worktree Management ──────────────────────────────────────────────────────

branch_name_for() {
  echo "nightshift/$(echo "$1" | tr '[:upper:]' '[:lower:]')"
}

create_worktree() {
  local identifier="$1"
  local repo_path="$2"
  local main_branch="$3"
  local branch_name
  branch_name=$(branch_name_for "$identifier")
  local worktree_path="$WORKTREE_BASE/$identifier"

  (
    cd "$repo_path" || { echo "Cannot cd to repo: $repo_path" >&2; return 1; }

    git fetch origin "$main_branch" --quiet >/dev/null 2>&1 || true

    # Remove stale local branch if exists
    git branch -D "$branch_name" >/dev/null 2>&1 || true

    # Remove stale worktree if exists
    git worktree remove --force "$worktree_path" >/dev/null 2>&1 || true

    # Create worktree with a new branch from latest main
    if ! git worktree add -b "$branch_name" "$worktree_path" "origin/$main_branch" >/dev/null 2>&1; then
      echo "git worktree add failed for: $worktree_path" >&2
      return 1
    fi
  ) || return 1

  echo "$worktree_path"
}

cleanup_worktree() {
  local identifier="$1"
  local repo_path="$2"
  local worktree_path="$WORKTREE_BASE/$identifier"

  if [ -z "$identifier" ]; then
    return
  fi

  git -C "$repo_path" worktree remove --force "$worktree_path" >/dev/null 2>&1 || rm -rf "$worktree_path"
}

# ─── Cleanup ────────────────────────────────────────────────────────────────

# Lightweight cleanup run silently on every startup
startup_cleanup() {
  log "Running startup cleanup..."
  local repo
  while IFS= read -r repo; do
    [ -z "$repo" ] && continue
    git -C "$repo" fetch --prune 2>/dev/null || true
    git -C "$repo" worktree prune 2>/dev/null || true
  done < <(all_repo_paths)
  log "✅ Startup cleanup done"
}

# Full cleanup command: branches, remote refs, old logs
run_cleanup() {
  local force="${1:-false}"
  local merged_deleted=0
  local unmerged_deleted=0
  local logs_deleted=0

  echo ""
  echo "🧹 Nightshift Cleanup"
  echo ""

  # ── Gather repos ──────────────────────────────────────────────────────────
  local repos=()
  local r
  while IFS= read -r r; do
    [ -z "$r" ] && continue
    repos+=("$r")
  done < <(all_repo_paths)

  if [ "${#repos[@]}" -eq 0 ]; then
    echo "❌ No repos found — run ./nightshift.sh setup, create $REPOS_FILE, or set REPO_PATH in .env." >&2
    exit 1
  fi

  # ── Per-repo branch cleanup ───────────────────────────────────────────────
  local repo
  for repo in "${repos[@]}"; do
    echo "📁 $repo"

    # Merged local branches
    local merged_branches=()
    mapfile -t merged_branches < <(git -C "$repo" branch --merged "$MAIN_BRANCH" 2>/dev/null \
      | grep -vE "^\*|main|staging|master" \
      | sed 's/^[[:space:]]*//' || true)

    if [ "${#merged_branches[@]}" -gt 0 ]; then
      echo "  Merged branches to delete (${#merged_branches[@]}):"
      printf "    - %s\n" "${merged_branches[@]}"

      if [ "$force" = true ] || confirm "  Delete these merged branches?"; then
        for branch in "${merged_branches[@]}"; do
          git -C "$repo" branch -d "$branch" 2>/dev/null && merged_deleted=$((merged_deleted + 1))
        done
      fi
    fi

    # Unmerged nightshift branches
    local unmerged=""
    unmerged=$(git -C "$repo" branch --no-merged "$MAIN_BRANCH" 2>/dev/null \
      | grep -E "nightshift/" \
      | sed 's/^[[:space:]]*//' || true)

    if [ -n "$unmerged" ]; then
      echo "  ⚠️  Unmerged Nightshift branches (from failed runs):"
      echo "$unmerged" | while IFS= read -r b; do echo "    - $b"; done

      # Check for open PRs before deleting
      local safe_to_delete=()
      local has_open_pr=()
      while IFS= read -r branch; do
        [ -z "$branch" ] && continue
        local pr_count
        pr_count=$(gh pr list --repo "$(git -C "$repo" remote get-url origin 2>/dev/null)" \
          --head "$branch" --state open --json number 2>/dev/null \
          | jq 'length' 2>/dev/null || echo "0")
        if [ "$pr_count" -gt 0 ]; then
          has_open_pr+=("$branch")
        else
          safe_to_delete+=("$branch")
        fi
      done <<< "$unmerged"

      if [ "${#has_open_pr[@]}" -gt 0 ]; then
        echo "    Skipping (have open PRs):"
        for b in "${has_open_pr[@]}"; do echo "      - $b"; done
      fi

      if [ "${#safe_to_delete[@]}" -gt 0 ]; then
        if [ "$force" = true ] || confirm "  Force-delete ${#safe_to_delete[@]} unmerged branch(es) without open PRs?"; then
          for branch in "${safe_to_delete[@]}"; do
            git -C "$repo" branch -D "$branch" 2>/dev/null && unmerged_deleted=$((unmerged_deleted + 1))
          done
        fi
      fi
    fi

    git -C "$repo" worktree prune 2>/dev/null || true
    git -C "$repo" fetch --prune 2>/dev/null || true
    echo ""
  done

  # ── Stale worktrees ───────────────────────────────────────────────────────
  if [ -d "$WORKTREE_BASE" ]; then
    local worktree_dirs=()
    while IFS= read -r -d '' d; do
      worktree_dirs+=("$d")
    done < <(find "$WORKTREE_BASE" -mindepth 1 -maxdepth 1 -type d -print0 2>/dev/null)

    if [ "${#worktree_dirs[@]}" -gt 0 ]; then
      echo "Stale worktrees (${#worktree_dirs[@]}):"
      for d in "${worktree_dirs[@]}"; do echo "  - $(basename "$d")"; done

      if [ "$force" = true ] || confirm "Remove these worktrees?"; then
        for d in "${worktree_dirs[@]}"; do
          rm -rf "$d"
        done
        for repo in "${repos[@]}"; do
          git -C "$repo" worktree prune 2>/dev/null || true
        done
      fi
      echo ""
    fi
  fi

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
  echo "  - Scanned ${#repos[@]} repo(s)"
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
  local workdir="$1"
  local prompt="$2"
  local log_file="$3"
  local timeout_minutes="${AGENT_TIMEOUT_MINUTES}"

  cd "$workdir" || { echo "FATAL: cannot cd to workdir: $workdir" >> "$log_file"; return 1; }

  {
    echo "DEBUG: pwd = $(pwd)"
    echo "DEBUG: branch = $(git branch --show-current 2>/dev/null)"
  } >> "$log_file"

  if [ "${USE_AGENT_TEAMS}" = "true" ]; then
    timeout "${timeout_minutes}m" bash -c \
      'cd "$0" || exit 1; CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1 claude \
        --dangerously-skip-permissions \
        --print \
        --output-format text \
        -p "$1" >> "$2" 2>&1' "$workdir" "$prompt" "$log_file"
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

  # ── Resolve target repo from the ticket's Linear project ───────────────────
  local project_name repo_info repo_path repo_main
  project_name=$(echo "$issue_json" | jq -r '.project.name // empty')

  if ! repo_info=$(resolve_repo "$project_name"); then
    tlog "$identifier" "❌ Repo resolution failed: $repo_info"
    linear_set_state "$issue_id" "$STATE_ID_TRIGGER" 2>/dev/null || true
    linear_comment "$issue_id" "❌ **Nightshift: No repo for this ticket**

${repo_info}

Map this ticket's project in \`repos.json\` (or run \`./nightshift.sh setup\`), then move it back to **${TRIGGER_STATE}**." 2>/dev/null || true
    return 1
  fi
  repo_path=$(printf '%s' "$repo_info" | cut -f1)
  repo_main=$(printf '%s' "$repo_info" | cut -f2)
  tlog "$identifier" "Repo: $repo_path (main: $repo_main)"

  # ── Create Worktree ────────────────────────────────────────────────────────
  # Note: Linear's GitHub integration auto-moves tickets to "In Progress"
  # when a branch with the ticket ID is created, so no manual state update needed.
  local worktree_path branch_name
  branch_name=$(branch_name_for "$identifier")
  if ! worktree_path=$(create_worktree "$identifier" "$repo_path" "$repo_main"); then
    tlog "$identifier" "❌ Worktree creation failed"
    linear_set_state "$issue_id" "$STATE_ID_TRIGGER" 2>/dev/null || true
    linear_comment "$issue_id" "❌ **Nightshift: Setup failed**

Could not create worktree. This may be a branch naming conflict.

Check that branch \`${branch_name}\` does not already exist on the remote.

Ticket moved back to **${TRIGGER_STATE}**." 2>/dev/null || true
    return 1
  fi

  tlog "$identifier" "Worktree: $worktree_path (branch: $branch_name)"

  # ── Run Claude ─────────────────────────────────────────────────────────────
  local prompt
  prompt=$(build_prompt "$identifier" "$title" "$description")

  tlog "$identifier" "Running Claude (agent-teams=${USE_AGENT_TEAMS}, timeout=${AGENT_TIMEOUT_MINUTES}m)..."

  # Record log size before Claude runs — so post-run checks only look at NEW output
  local log_offset=0
  [ -f "$log_file" ] && log_offset=$(wc -c < "$log_file")

  local exit_code=0
  run_claude "$worktree_path" "$prompt" "$log_file" || exit_code=$?

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
    cleanup_worktree "$identifier" "$repo_path" 2>/dev/null || true
    return 1
  fi

  # ── Extract only THIS attempt's output (skip previous attempts in the log) ──
  local current_output
  current_output=$(tail -c +"$((log_offset + 1))" "$log_file" 2>/dev/null || true)

  # ── Check for rate/usage limit ──────────────────────────────────────────────
  if echo "$current_output" | grep -qi "rate.limit\|usage.limit\|exceeded.*limit\|too many requests" 2>/dev/null; then
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
    cleanup_worktree "$identifier" "$repo_path" 2>/dev/null || true
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
    cleanup_worktree "$identifier" "$repo_path" 2>/dev/null || true
    return 1
  fi

  # ── Check for BLOCKED ──────────────────────────────────────────────────────
  local blocked_line
  blocked_line=$(echo "$current_output" | grep -i "^BLOCKED:" | head -1 || true)

  if [ -n "$blocked_line" ]; then
    tlog "$identifier" "⚠️  Blocked: $blocked_line"
    linear_set_state "$issue_id" "$STATE_ID_TRIGGER" 2>/dev/null || true
    linear_comment "$issue_id" "🚧 **Nightshift needs your input**

Claude got blocked on this ticket:

> ${blocked_line}

Please clarify in the ticket comments, then move back to **${TRIGGER_STATE}** to retry." 2>/dev/null || true
    notify "⚠️ *${identifier}* — Blocked
${blocked_line}"
    cleanup_worktree "$identifier" "$repo_path" 2>/dev/null || true
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
    cleanup_worktree "$identifier" "$repo_path" 2>/dev/null || true
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
        run_claude "$worktree_path" "$fix_prompt" "$log_file" || fix_exit=$?
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
  # Extract Claude's final summary — last block after the last attempt marker
  local summary
  summary=$(awk '/^--- Attempt /{buf=""; next} {buf = buf $0 "\n"} END{printf "%s", buf}' "$log_file" 2>/dev/null \
    | grep -v '^DEBUG: ' \
    | tail -n 40 || true)

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
  cd "$repo_path"
  local pr_url=""
  if ! pr_url=$(gh pr create \
    --title "${identifier}: ${title}" \
    --body "$pr_body" \
    --base "$repo_main" \
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
    cleanup_worktree "$identifier" "$repo_path" 2>/dev/null || true
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
  cleanup_worktree "$identifier" "$repo_path" 2>/dev/null || true
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

  local repo_summary
  if [ -f "$REPOS_FILE" ]; then
    local registered
    registered=$(jq -r '.repos | length' "$REPOS_FILE" 2>/dev/null || echo 0)
    repo_summary="${registered} registered (repos.json)"
    [ -n "$REPO_PATH" ] && repo_summary="${repo_summary} + REPO_PATH fallback"
  else
    repo_summary="$REPO_PATH"
  fi

  echo ""
  printf "🌙 Nightshift v%s\n" "$VERSION"
  printf "   Repos:          %s\n" "$repo_summary"
  printf "   Worktrees:      %s\n" "$WORKTREE_BASE"
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
  mkdir -p "$LOG_DIR" "$WORKTREE_BASE" "$REPOS_BASE"
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

# ─── Setup Wizard ────────────────────────────────────────────────────────────

# ask <prompt> [default] — echoes the answer (or the default on empty input).
ask() {
  local prompt="$1" default="${2:-}" answer
  if [ -n "$default" ]; then
    read -r -p "$prompt [$default]: " answer || true
    echo "${answer:-$default}"
  else
    read -r -p "$prompt: " answer || true
    echo "$answer"
  fi
}

run_setup() {
  echo ""
  echo "🌙 Nightshift Setup"
  echo "   Generates .env and repos.json — press Enter to accept [defaults]."
  echo ""

  if [ -f "$ENV_FILE" ]; then
    confirm ".env already exists — overwrite it?" || { echo "Setup cancelled."; exit 0; }
    echo ""
  fi

  # ── Issue tracker ──────────────────────────────────────────────────────────
  echo "Issue tracker:"
  echo "  1) Linear"
  echo "  2) Jira             (coming soon)"
  echo "  3) GitHub Issues    (coming soon)"
  local tracker
  while true; do
    tracker=$(ask "Choose" "1")
    case "$tracker" in
      1) break ;;
      2|3) echo "  ⏳ Not supported yet — Linear only for now." ;;
      *) echo "  Enter 1, 2, or 3." ;;
    esac
  done
  echo ""

  # ── Implementation engine ──────────────────────────────────────────────────
  echo "Implementation engine:"
  echo "  1) Claude Code"
  echo "  2) Gemini           (coming soon)"
  local engine
  while true; do
    engine=$(ask "Choose" "1")
    case "$engine" in
      1) break ;;
      2) echo "  ⏳ Gemini as an engine isn't supported yet — Claude Code only for now." ;;
      *) echo "  Enter 1 or 2." ;;
    esac
  done
  echo ""

  # ── Linear ─────────────────────────────────────────────────────────────────
  local linear_key team trigger review main_branch concurrency
  while true; do
    linear_key=$(ask "Linear API key")
    [ -n "$linear_key" ] && break
    echo "  The Linear API key is required."
  done
  team=$(ask "Linear team key" "ENG")
  trigger=$(ask "Trigger state" "Next")
  review=$(ask "In-review state" "In Review")
  main_branch=$(ask "Default main branch" "main")
  concurrency=$(ask "Max concurrent tickets" "3")
  echo ""

  # ── Optional: Gemini review gate ───────────────────────────────────────────
  local gemini_key=""
  if confirm "Enable the Gemini review gate?"; then
    gemini_key=$(ask "Gemini API key")
  fi

  # ── Optional: Telegram notifications ───────────────────────────────────────
  local tg_enabled="false" tg_token="" tg_chat=""
  if confirm "Enable Telegram notifications?"; then
    tg_enabled="true"
    tg_token=$(ask "Telegram bot token")
    tg_chat=$(ask "Telegram chat ID")
  fi
  echo ""

  # ── Repos ──────────────────────────────────────────────────────────────────
  echo "Register repos — map each Linear project to a git repo."
  echo "Nightshift clones these on demand; nothing needs to be cloned yet."
  echo ""
  local repos_json='{"repos":{}}'
  while true; do
    local project url branch
    project=$(ask "Linear project name (blank to finish)")
    [ -z "$project" ] && break
    url=$(ask "  Git URL")
    if [ -z "$url" ]; then
      echo "  Skipped — no URL given."
      echo ""
      continue
    fi
    printf '  Checking access to %s ... ' "$url"
    if git ls-remote --exit-code "$url" HEAD >/dev/null 2>&1; then
      echo "ok"
    else
      echo "FAILED"
      echo "  ⚠️  Could not reach that repo. The host running Nightshift needs git"
      echo "     auth for it — an SSH key, or 'gh auth login' for HTTPS URLs."
      confirm "  Add it anyway?" || { echo ""; continue; }
    fi
    branch=$(ask "  Main branch" "$main_branch")
    repos_json=$(printf '%s' "$repos_json" | jq \
      --arg p "$project" --arg u "$url" --arg b "$branch" \
      '.repos[$p] = {url: $u, main_branch: $b}')
    echo "  ✅ Added \"$project\""
    echo ""
  done

  # ── Write .env ─────────────────────────────────────────────────────────────
  cat > "$ENV_FILE" <<EOF
# Generated by ./nightshift.sh setup on $(date -Iseconds)
# Re-run the wizard any time, or edit by hand.

LINEAR_API_KEY="${linear_key}"
LINEAR_TEAM_KEY="${team}"
TRIGGER_STATE="${trigger}"
IN_REVIEW_STATE="${review}"

# Optional single-repo fallback for tickets whose project is not in repos.json
# REPO_PATH=""
MAIN_BRANCH="${main_branch}"

MAX_CONCURRENT="${concurrency}"
POLL_INTERVAL="30"
USE_AGENT_TEAMS="false"

MAX_DISPATCHES="10"
MAX_RETRIES="3"
AGENT_TIMEOUT_MINUTES="45"

TELEGRAM_ENABLED="${tg_enabled}"
TELEGRAM_BOT_TOKEN="${tg_token}"
TELEGRAM_CHAT_ID="${tg_chat}"

GEMINI_API_KEY="${gemini_key}"
GEMINI_MODEL="gemini-2.5-pro"
MAX_REVIEW_RETRIES="1"
EOF

  # ── Write repos.json ───────────────────────────────────────────────────────
  printf '%s\n' "$repos_json" | jq . > "$REPOS_FILE"

  local repo_count
  repo_count=$(printf '%s' "$repos_json" | jq -r '.repos | length')

  echo ""
  echo "✅ Wrote $ENV_FILE"
  echo "✅ Wrote $REPOS_FILE (${repo_count} repo(s))"
  if [ "$repo_count" -eq 0 ]; then
    echo "⚠️  No repos registered yet — add them to repos.json or re-run setup."
  fi
  echo ""
  echo "Start Nightshift with: ./nightshift.sh"
}

# ─── Test Guard ────────────────────────────────────────────────────────────────
[[ "${NIGHTSHIFT_TESTING:-}" == "true" ]] && return 0 2>/dev/null

# ─── Entrypoint ───────────────────────────────────────────────────────────────

case "${1:-}" in
  setup)
    run_setup
    ;;
  cleanup)
    if [[ "${2:-}" == "--force" ]]; then
      run_cleanup true
    else
      run_cleanup false
    fi
    ;;
  *)
    main "$@"
    ;;
esac
