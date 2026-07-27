#!/bin/sh

#
# Usage:
#   ./install.sh                         # interactive
#   ./install.sh --local                 # local build (Go), no prompts
#   ./install.sh --docker                # containerized (Docker)
#   ./install.sh --plugin github.com/org/x --plugin github.com/org/y --token TOK
#
# Flags (all optional; the install mode and missing prerequisites may be prompted for on a terminal):
#   --local | --docker      Install type (default: prompt, else local)
#   --plugin LINK           Plugin to install (repeatable); added to configurations/plugins.yaml
#   --token TOKEN           Beelzebub Cloud token (BEELZEBUB_CLOUD_AUTH_TOKEN)
#   --github-token TOKEN    Token for private plugin repos (BEELZEBUB_GITHUB_TOKEN)
#   --no-run                Build/install only; do not start the local runtime
#   -y, --yes               Assume yes to prompts (auto-install prerequisites)
#   -h, --help              Show this help
set -eu

MODE=""
PLUGINS=""
TOKEN=""
GH_TOKEN="${BEELZEBUB_GITHUB_TOKEN:-}"
ASSUME_YES=0
RUN_LOCAL=1
REPO_URL="https://github.com/beelzebub-labs/beelzebub.git"
MAKE_CMD="${MAKE:-make}"

# Colors are disabled for non-TTY output and when NO_COLOR is set.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  E=$(printf '\033'); R="${E}[0m"; B="${E}[1m"; D="${E}[2m"
  GRN="${E}[38;2;58;208;127m"; BLU="${E}[38;2;120;140;250m"; RED="${E}[38;2;244;80;110m"
else
  E=""; R=""; B=""; D=""; GRN=""; BLU=""; RED=""
fi

info()  { printf '%s\n' "$*"; }
step()  { printf '%s→%s %s\n' "$BLU" "$R" "$*"; }
ok()    { printf '%s✓%s %s\n' "$GRN" "$R" "$*"; }
die()   { printf '%s✗ %s%s\n' "$RED" "$*" "$R" >&2; exit 1; }

banner() {
  _cols=$(tput cols 2>/dev/null || echo 80)
  printf '\n'
  if [ -n "$E" ] && [ "${_cols:-80}" -ge 70 ]; then
    printf '%s' "$B"
    printf '  %s[38;2;168;85;247m██████╗ ███████╗███████╗██╗     ███████╗███████╗██████╗ ██╗   ██╗██████╗ \n' "$E"
    printf '  %s[38;2;141;110;245m██╔══██╗██╔════╝██╔════╝██║     ╚══███╔╝██╔════╝██╔══██╗██║   ██║██╔══██╗\n' "$E"
    printf '  %s[38;2;114;136;243m██████╔╝█████╗  █████╗  ██║       ███╔╝ █████╗  ██████╔╝██║   ██║██████╔╝\n' "$E"
    printf '  %s[38;2;88;161;242m██╔══██╗██╔══╝  ██╔══╝  ██║      ███╔╝  ██╔══╝  ██╔══██╗██║   ██║██╔══██╗\n' "$E"
    printf '  %s[38;2;61;186;240m██████╔╝███████╗███████╗███████╗███████╗███████╗██████╔╝╚██████╔╝██████╔╝\n' "$E"
    printf '  %s[38;2;34;211;238m╚═════╝ ╚══════╝╚══════╝╚══════╝╚══════╝╚══════╝╚═════╝  ╚═════╝ ╚═════╝ %s\n' "$E" "$R"
  elif [ -n "$E" ]; then
    printf '  %s' "$B"
    printf '%s[38;2;168;85;247mb%s[38;2;150;103;247me%s[38;2;132;121;246me' "$E" "$E" "$E"
    printf '%s[38;2;114;139;245ml%s[38;2;96;157;244mz%s[38;2;78;175;242me' "$E" "$E" "$E"
    printf '%s[38;2;60;193;240mb%s[38;2;47;202;239mu%s[38;2;34;211;238mb%s\n' "$E" "$E" "$E" "$R"
  else
    printf '  beelzebub\n'
  fi
  printf '  %sCybersecurity at Machine Speed%s\n\n' "$D" "$R"
}

run_step() {
  _label="$1"
  shift
  step "$_label"
  if "$@"; then
    ok "$_label"
  else
    _rc=$?
    die "$_label failed (exit $_rc)"
  fi
}

while [ $# -gt 0 ]; do
  case "$1" in
    --local)        MODE="local"; shift ;;
    --docker)       MODE="docker"; shift ;;
    --plugin)
      [ $# -ge 2 ] || die "--plugin requires a repository link"
      PLUGINS="${PLUGINS}${PLUGINS:+
}$2"
      shift 2
      ;;
    --token)
      [ $# -ge 2 ] || die "--token requires a value"
      TOKEN="$2"
      shift 2
      ;;
    --github-token)
      [ $# -ge 2 ] || die "--github-token requires a value"
      GH_TOKEN="$2"
      shift 2
      ;;
    --no-run)       RUN_LOCAL=0; shift ;;
    -y|--yes)       ASSUME_YES=1; shift ;;
    -h|--help)      awk 'NR==1{next} /^#/{sub(/^# ?/,""); print; seen=1; next} seen{exit}' "$0"; exit 0 ;;
    *)              die "unknown option: $1" ;;
  esac
done

# Use /dev/tty for prompts when the script is piped into a shell.
HAVE_TTY=0
if [ -r /dev/tty ] && [ -w /dev/tty ]; then
  # Some CI/PTY-like environments expose /dev/tty but do not allow it to be
  # opened. Probe the device before treating the process as interactive.
  if ( : </dev/tty >/dev/tty ) 2>/dev/null; then
    HAVE_TTY=1
  fi
fi
ask() { # ask "Question" -> echoes answer ("" on no tty)
  [ "$HAVE_TTY" -eq 1 ] || { printf '\n'; return 0; }
  printf '%s' "$1" > /dev/tty
  IFS= read -r _a < /dev/tty || _a=""
  printf '%s' "$_a"
}

banner

# Resolve the checkout from the script location so invocation from another
# directory still updates the intended repository. Piped execution falls back
# to the current directory and clones when no checkout is present.
SCRIPT_DIR=""
if [ -n "${0:-}" ] && [ "${0#-}" = "$0" ]; then
  SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" 2>/dev/null && pwd) || SCRIPT_DIR=""
fi

if [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/go.mod" ] && [ -f "$SCRIPT_DIR/Makefile" ]; then
  REPO_DIR="$SCRIPT_DIR"
elif [ -f "go.mod" ] && [ -f "Makefile" ]; then
  REPO_DIR=$(pwd)
else
  command -v git >/dev/null 2>&1 || die "run this inside a cloned beelzebub repo, or install git so it can be cloned."
  step "Cloning Beelzebub..."
  CLONE_DIR="${BEELZEBUB_INSTALL_DIR:-beelzebub}"
  case "$CLONE_DIR" in
    *[!A-Za-z0-9._/-]*|/*|..|../*) die "invalid clone directory: $CLONE_DIR" ;;
  esac
  [ ! -e "$CLONE_DIR" ] || die "clone directory already exists: $CLONE_DIR"
  git clone --depth 1 "$REPO_URL" "$CLONE_DIR"
  REPO_DIR=$(CDPATH= cd -- "$CLONE_DIR" && pwd)
fi

cd "$REPO_DIR"

if [ -z "$MODE" ]; then
  info "How do you want to run Beelzebub?"
  printf '  %s1%s) Local %s(needs Go)%s\n' "$B" "$R" "$D" "$R"
  printf '  %s2%s) Docker %s(needs… Docker)%s\n' "$B" "$R" "$D" "$R"
  case "$(ask 'Choose [1]: ')" in
    2) MODE="docker" ;;
    *) MODE="local" ;;
  esac
fi

# Offer installation through a detected package manager when a prerequisite is missing.
pkg_install() { # pkg_install <tool>; echoes a command that would install it, or ""
  if command -v brew     >/dev/null 2>&1; then echo "brew install $1";
  elif command -v apt-get >/dev/null 2>&1; then echo "sudo apt-get update && sudo apt-get install -y $1";
  elif command -v dnf    >/dev/null 2>&1; then echo "sudo dnf install -y $1";
  elif command -v yum    >/dev/null 2>&1; then echo "sudo yum install -y $1";
  elif command -v pacman >/dev/null 2>&1; then echo "sudo pacman -S --noconfirm $1";
  elif command -v zypper >/dev/null 2>&1; then echo "sudo zypper install -y $1";
  elif command -v apk    >/dev/null 2>&1; then echo "sudo apk add $1";
  else echo ""; fi
}
need() { # need <tool> <manual-instructions>
  command -v "$1" >/dev/null 2>&1 && return 0
  cmd="$(pkg_install "$1")"
  if [ -n "$cmd" ]; then
    reply="no"
    [ "$ASSUME_YES" -eq 1 ] && reply="yes" || reply="$(ask "$1 is required. Install it with \"$cmd\"? [y/N]: ")"
    case "$reply" in y|Y|yes|YES) sh -c "$cmd" >/dev/null 2>&1 && return 0 || die "failed to install $1" ;; esac
  fi
  die "$1 is required. $2"
}

if [ "$MODE" = "docker" ]; then
  need docker "Install it from https://docs.docker.com/get-docker and re-run."
  if ! docker compose version >/dev/null 2>&1 && ! command -v docker-compose >/dev/null 2>&1; then
    die "Docker Compose is required. Install the Docker Compose plugin and re-run."
  fi
else
  need go  "Install it from https://go.dev/dl and re-run."
  need git "Install git from your package manager and re-run."
  need make "Install make from your package manager and re-run."
fi

# Keep plugin declarations in YAML so local and Docker deployments use the same list.
PLUGIN_CONFIG="configurations/plugins.yaml"
yaml_quote() {
  printf "%s" "$1" | sed "s/'/''/g; s/^/'/; s/$/'/"
}

add_plugin_source() {
  _plugin="$1"
  mkdir -p configurations
  [ -f "$PLUGIN_CONFIG" ] || printf 'plugins: []\n' > "$PLUGIN_CONFIG"
  _quoted_plugin=$(yaml_quote "$_plugin")
  grep -Fq "source: $_plugin" "$PLUGIN_CONFIG" 2>/dev/null && return 0
  grep -Fq "source: '$_plugin'" "$PLUGIN_CONFIG" 2>/dev/null && return 0
  grep -Fq "source: \"$_plugin\"" "$PLUGIN_CONFIG" 2>/dev/null && return 0

  if grep -Eq '^plugins:[[:space:]]*\[\][[:space:]]*(#.*)?$' "$PLUGIN_CONFIG" 2>/dev/null; then
    printf 'plugins:\n  - source: %s\n' "$_quoted_plugin" > "$PLUGIN_CONFIG"
  elif grep -Eq '^plugins:[[:space:]]*$' "$PLUGIN_CONFIG" 2>/dev/null; then
    printf '  - source: %s\n' "$_quoted_plugin" >> "$PLUGIN_CONFIG"
  elif grep -Eq '^plugins:[[:space:]]*\[' "$PLUGIN_CONFIG" 2>/dev/null; then
    die "$PLUGIN_CONFIG uses flow-style plugins. Use block style before adding --plugin entries."
  else
    { printf 'plugins:\n'; printf '  - source: %s\n' "$_quoted_plugin"; } >> "$PLUGIN_CONFIG"
  fi
}

if [ -n "$PLUGINS" ]; then
  while IFS= read -r p; do
    [ -n "$p" ] && add_plugin_source "$p"
  done <<EOF
$PLUGINS
EOF
fi

# Compose reads these values from .env; keep the file private because it may contain tokens.
set_env() {
  { grep -v "^$1=" .env 2>/dev/null || true; printf '%s=%s\n' "$1" "$2"; } > .env.tmp && mv .env.tmp .env
  chmod 600 .env
}
[ -n "$TOKEN" ] && set_env BEELZEBUB_CLOUD_AUTH_TOKEN "$TOKEN"

# Pass the GitHub token to both the CLI and Docker build without exposing it in command arguments.
if [ -n "$GH_TOKEN" ]; then
  export BEELZEBUB_GITHUB_TOKEN="$GH_TOKEN"
  set_env BEELZEBUB_GITHUB_TOKEN "$GH_TOKEN"
fi

if [ "$MODE" = "docker" ]; then
  compose_up() {
    if docker compose version >/dev/null 2>&1; then
      docker compose up -d --build
    else
      docker-compose up -d --build
    fi
  }
  run_step "Build Docker image and start container" compose_up
  # Do not direct later plugin commands to Docker if this deployment failed.
  printf '%s\n' "$MODE" > .beelzebub-mode
  # Build a local CLI when possible so it can manage the container.
  if command -v go >/dev/null 2>&1 && command -v "$MAKE_CMD" >/dev/null 2>&1; then
    if "$MAKE_CMD" -s build >/dev/null 2>&1; then
      ok "Local plugin CLI built"
    else
      info "Local Go CLI was not built; manage Docker plugins with configurations/plugins.yaml and make docker."
    fi
  else
    info "Go is not installed; manage Docker plugins with configurations/plugins.yaml and make docker."
  fi
  info ""
  info "Manage plugins with: ./beelzebub plugin install <link>   (or edit configurations/plugins.yaml)"
else
  [ -n "$TOKEN" ] && export BEELZEBUB_CLOUD_AUTH_TOKEN="$TOKEN"
  run_step "Install declared plugins" sh -c 'go run . plugin install --no-build'
  run_step "Build ./beelzebub" "$MAKE_CMD" -s build
  # The default services include privileged ports, so non-root users finish the
  # build and start the runtime manually with a suitable service directory.
  if [ "$RUN_LOCAL" -eq 0 ]; then
    printf '%s\n' "$MODE" > .beelzebub-mode
    info "Local installation complete; runtime start skipped (--no-run)."
    exit 0
  fi
  if [ "$(id -u)" -ne 0 ]; then
    printf '%s\n' "$MODE" > .beelzebub-mode
    info "Local installation complete; runtime was not started because the default services include privileged ports."
    info "Start it manually with a suitable service directory, or use --no-run."
    exit 0
  fi
  # Run detached so the installer returns; keep runtime output in a log file.
  nohup ./beelzebub run > beelzebub.log 2>&1 &
  _rpid=$!
  sleep 1
  kill -0 "$_rpid" 2>/dev/null || { cat beelzebub.log >&2; die "runtime exited on startup — see beelzebub.log"; }
  # Later plugin commands should target local mode only after startup succeeds.
  printf '%s\n' "$MODE" > .beelzebub-mode
  ok "Runtime running (pid $_rpid) · logs: beelzebub.log · stop: kill $_rpid"
  info ""
  info "Manage plugins with: ./beelzebub plugin install <link>   (or edit configurations/plugins.yaml)"
fi
