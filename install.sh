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
#   --token TOKEN           Beelzebub Cloud token; also turns cloud reporting on
#   --uri URL               Beelzebub Cloud API base (required alongside --token)
#   --github-token TOKEN    Token for private plugin repos (BEELZEBUB_GITHUB_TOKEN)
#   --ref REF               Git tag or branch to clone when not inside a checkout
#   --no-run                Build/install only; do not start the local runtime
#   -y, --yes               Assume yes to prompts (auto-install prerequisites, fully unattended)
#   -h, --help              Show this help
set -eu

MODE=""
PLUGINS=""
TOKEN="${BEELZEBUB_CLOUD_AUTH_TOKEN:-}"
URI="${BEELZEBUB_CLOUD_URI:-}"
GH_TOKEN="${BEELZEBUB_GITHUB_TOKEN:-}"
REF="${BEELZEBUB_INSTALL_REF:-}"
ASSUME_YES=0
RUN_LOCAL=1
REPO_URL="https://github.com/beelzebub-labs/beelzebub.git"
MAKE_CMD="${MAKE:-make}"

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

# A truncated `curl | sh` download must define, never execute.
main() {

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
    --uri)
      [ $# -ge 2 ] || die "--uri requires a value"
      URI="$2"
      shift 2
      ;;
    --github-token)
      [ $# -ge 2 ] || die "--github-token requires a value"
      GH_TOKEN="$2"
      shift 2
      ;;
    --ref)
      [ $# -ge 2 ] || die "--ref requires a value"
      REF="$2"
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
ask() { # ask "Question" -> echoes the answer, trimmed ("" on no tty)
  [ "$HAVE_TTY" -eq 1 ] || { printf '\n'; return 0; }
  printf '%s' "$1" > /dev/tty
  IFS= read -r _a < /dev/tty || _a=""
  printf '%s' "$_a" | sed 's/^[[:space:]"'"'"']*//; s/[[:space:]"'"'"']*$//'
}

confirm() {
  [ "$HAVE_TTY" -eq 1 ] || { [ "$2" = "y" ]; return; }
  while :; do
    _c="$(ask "$1")"
    [ -n "$_c" ] || _c="$2"
    case "$_c" in
      y|Y|yes|YES) return 0 ;;
      n|N|no|NO)   return 1 ;;
    esac
    info "Please answer y or n." >&2
  done
}

choose() {
  _q=$1; _def=$2; shift 2
  [ "$HAVE_TTY" -eq 1 ] || { printf '%s' "$_def"; return 0; }
  while :; do
    _s="$(ask "$_q")"
    [ -n "$_s" ] || _s="$_def"
    for _v in "$@"; do
      [ "$_s" = "$_v" ] && { printf '%s' "$_s"; return 0; }
    done
    info "Please answer one of: $*" >&2
  done
}

banner

# Resolve the checkout from the script location so invocation from another
# directory still updates the intended repository. Piped execution falls back
# to the current directory and clones when no checkout is present.
is_beelzebub_checkout() { # is_beelzebub_checkout <dir>
  [ -f "$1/go.mod" ] && [ -f "$1/Makefile" ] \
    && grep -q '^module github\.com/beelzebub-labs/beelzebub' "$1/go.mod" 2>/dev/null
}

SCRIPT_DIR=""
if [ -n "${0:-}" ] && [ "${0#-}" = "$0" ]; then
  SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" 2>/dev/null && pwd) || SCRIPT_DIR=""
fi

if [ -n "$SCRIPT_DIR" ] && is_beelzebub_checkout "$SCRIPT_DIR"; then
  REPO_DIR="$SCRIPT_DIR"
elif is_beelzebub_checkout "$(pwd)"; then
  REPO_DIR=$(pwd)
else
  command -v git >/dev/null 2>&1 || die "run this inside a cloned beelzebub repo, or install git so it can be cloned."
  CLONE_DIR="${BEELZEBUB_INSTALL_DIR:-beelzebub}"
  case "$CLONE_DIR" in
    *[!A-Za-z0-9._/-]*|/*|..|../*) die "invalid clone directory: $CLONE_DIR" ;;
  esac
  if [ -e "$CLONE_DIR" ]; then
    is_beelzebub_checkout "$CLONE_DIR" || die "clone directory already exists: $CLONE_DIR"
    step "Reusing existing checkout: $CLONE_DIR"
    git -C "$CLONE_DIR" pull --ff-only >/dev/null 2>&1 || true
  else
    step "Cloning Beelzebub..."
    if [ -n "$REF" ]; then
      git clone --depth 1 --branch "$REF" "$REPO_URL" "$CLONE_DIR"
    else
      git clone --depth 1 "$REPO_URL" "$CLONE_DIR"
    fi
  fi
  REPO_DIR=$(CDPATH= cd -- "$CLONE_DIR" && pwd)
fi

cd "$REPO_DIR"

if [ -z "$MODE" ]; then
  info "How do you want to run Beelzebub?"
  printf '  %s1%s) Local %s(needs Go)%s\n' "$B" "$R" "$D" "$R"
  printf '  %s2%s) Docker %s(needs… Docker)%s\n' "$B" "$R" "$D" "$R"
  case "$(choose 'Choose [1]: ' 1 1 2)" in
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
    if [ "$ASSUME_YES" -eq 1 ] || confirm "$1 is required. Install it with \"$cmd\"? [y/N]: " n; then
      sh -c "$cmd" >/dev/null 2>&1 && return 0 || die "failed to install $1"
    fi
  fi
  die "$1 is required. $2"
}

if [ "$MODE" = "docker" ]; then
  command -v docker >/dev/null 2>&1 \
    || die "docker is required. Install it from https://docs.docker.com/get-docker and re-run."
  if ! docker compose version >/dev/null 2>&1 && ! command -v docker-compose >/dev/null 2>&1; then
    _cc="$(pkg_install docker-compose-plugin)"
    if [ -n "$_cc" ] && { [ "$ASSUME_YES" -eq 1 ] || confirm "Docker Compose is required. Install it with \"$_cc\"? [y/N]: " n; }; then
      sh -c "$_cc" || true
    fi
    docker compose version >/dev/null 2>&1 || command -v docker-compose >/dev/null 2>&1 \
      || die "Docker Compose is required. Install the Compose plugin and re-run."
  fi
else
  need go  "Install it from https://go.dev/dl and re-run."
  need git "Install git from your package manager and re-run."
  need make "Install make from your package manager and re-run."
fi

# Keep plugin declarations in YAML so local and Docker deployments use the same list.
PLUGIN_CONFIG="configurations/plugins.yaml"
PLUGIN_CONFIG_DIR="configurations/plugins"
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

looks_like_token() {
  case "$1" in *[!A-Za-z0-9._-]*) return 1 ;; esac
  [ "${#1}" -ge 16 ]
}
looks_like_host() {
  case "${1#*://}" in
    localhost|localhost[:/]*|*.*) return 0 ;;
    *) return 1 ;;
  esac
}

if [ -z "$TOKEN" ] && [ "$HAVE_TTY" -eq 1 ]; then
  info "Beelzebub Cloud token — https://beelzebub.ai/products/beelzebub-cloud/"
  while :; do
    TOKEN="$(ask 'Token (blank to skip): ')"
    [ -n "$TOKEN" ] || break
    looks_like_token "$TOKEN" && break
    info "That is not a token — paste the whole value, or leave blank to skip." >&2
  done
fi

if [ -n "$TOKEN" ] && [ -z "$URI" ]; then
  if [ "$HAVE_TTY" -eq 1 ]; then
    while :; do
      URI="$(ask 'Cloud URL: ')"
      [ -n "$URI" ] && looks_like_host "$URI" && break
      info "That is not a URL — paste the address you sign in to." >&2
    done
  fi
  [ -n "$URI" ] || die "a cloud token needs a URL. Pass --uri URL, or set BEELZEBUB_CLOUD_URI."
fi

if [ -n "$URI" ]; then
  case "$URI" in http://*|https://*) ;; *) URI="https://$URI" ;; esac
  URI="${URI%/}"
  case "$URI" in */api/integration) ;; *) URI="$URI/api/integration" ;; esac
fi

# 0 ok, 1 rejected, 2 could not check.
verify_cloud() {
  command -v curl >/dev/null 2>&1 || return 2
  _out=$(mktemp 2>/dev/null) || return 2
  _curl=0
  _code=$(curl -sS -o "$_out" -w '%{http_code}' --max-time 15 \
    -H "Authorization: $TOKEN" "$URI/honeypots" 2>/dev/null) || _curl=$?
  _first=$(head -c 1 "$_out" 2>/dev/null)
  rm -f "$_out"
  # 6 = could not resolve host: a typo, not a blip.
  if [ "$_curl" -eq 6 ]; then
    VERIFY_MSG="cannot resolve the host in $URI — check the URL"
    return 1
  fi
  [ "$_curl" -eq 0 ] || { VERIFY_MSG="cannot reach $URI"; return 2; }
  case "$_code" in
    000) VERIFY_MSG="cannot reach $URI"; return 2 ;;
    200)
      if [ "$_first" = "[" ] || [ "$_first" = "{" ]; then
        return 0
      fi
      VERIFY_MSG="$URI served a web page, not the API — check the URL"
      return 1 ;;
    401|403) VERIFY_MSG="token rejected by $URI"; return 1 ;;
    404) VERIFY_MSG="$URI answered 404 — either the URL is wrong, or the token is not valid there"; return 1 ;;
    *) VERIFY_MSG="unexpected reply from $URI (HTTP $_code)"; return 1 ;;
  esac
}

if [ -n "$TOKEN" ]; then
  _verdict=0
  verify_cloud || _verdict=$?
  case $_verdict in
    0) ok "Cloud credentials verified" ;;
    2) info "Could not verify the cloud credentials: $VERIFY_MSG. Continuing." ;;
    *)
      info "$VERIFY_MSG"
      confirm 'Continue anyway? [y/N]: ' n || die "fix the token or the URL and re-run." ;;
  esac
fi

# Compose reads these values from .env; keep the file private because it may contain tokens.
set_env() {
  { grep -v "^$1=" .env 2>/dev/null || true; printf '%s=%s\n' "$1" "$2"; } > .env.tmp && mv .env.tmp .env
  chmod 600 .env
}

if [ -n "$TOKEN" ]; then
  set_env BEELZEBUB_CLOUD_ENABLED true
  set_env BEELZEBUB_CLOUD_URI "$URI"
  set_env BEELZEBUB_CLOUD_AUTH_TOKEN "$TOKEN"
fi

# Pass the GitHub token to both the CLI and Docker build without exposing it in command arguments.
if [ -n "$GH_TOKEN" ]; then
  export BEELZEBUB_GITHUB_TOKEN="$GH_TOKEN"
  set_env BEELZEBUB_GITHUB_TOKEN "$GH_TOKEN"
fi

if [ "$MODE" = "docker" ]; then
  # A bound port would abort `compose up` after the image is built.
  port_busy() {
    if command -v lsof >/dev/null 2>&1; then
      lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1
    elif command -v ss >/dev/null 2>&1; then
      ss -lnt 2>/dev/null | grep -qE "[:.]$1[[:space:]]"
    elif command -v netstat >/dev/null 2>&1; then
      netstat -an 2>/dev/null | grep -qE "[:.]$1[[:space:]].*LISTEN"
    else
      return 1
    fi
  }

  all_ports() {
    sed -n 's/^[[:space:]]*-[[:space:]]*"\{0,1\}\([0-9][0-9]*\):[0-9].*/\1/p' docker-compose.yml
  }
  # `!override` and `!reset` need Compose 2.24. Older versions merge lists instead
  # of replacing them, so there the ports are commented out in place.
  compose_can_override() {
    _v=$(docker compose version --short 2>/dev/null | tr -d 'v')
    _maj=${_v%%.*}; _min=${_v#*.}; _min=${_min%%.*}
    case "$_maj$_min" in *[!0-9]*|"") return 1 ;; esac
    [ "$_maj" -gt 2 ] || { [ "$_maj" -eq 2 ] && [ "$_min" -ge 24 ]; }
  }
  drop_port() {
    sed -i.bak "s|^\([[:space:]]*\)- \"$1:|\1# - \"$1:|" docker-compose.yml && rm -f docker-compose.yml.bak
  }

  # Scanning the LAN needs the host's interfaces. No equivalent on Docker
  # Desktop, where the bridge stays and the plugin is pinned to a CIDR.
  if [ "$(uname -s)" = "Linux" ]; then
    if compose_can_override; then
      printf 'services:\n  beelzebub:\n    network_mode: host\n    ports: !reset []\n' > docker-compose.override.yml
    else
      printf 'services:\n  beelzebub:\n    network_mode: host\n' > docker-compose.override.yml
      for _p in $(all_ports); do drop_port "$_p"; done
      # An emptied `ports:` is no longer a list, so the key goes too.
      sed -i.bak 's|^\([[:space:]]*\)ports:|\1# ports:|' docker-compose.yml && rm -f docker-compose.yml.bak
    fi
    ok "Host network: the sensor sees this machine's interfaces"
  else
    BUSY=""; FREE=""
    for _p in $(all_ports); do
      if port_busy "$_p"; then BUSY="$BUSY $_p"; else FREE="$FREE      - \"$_p:$_p\"
"; fi
    done
    if [ -n "$BUSY" ]; then
      info "Ports already in use:$BUSY"
      confirm 'Start without publishing them? [Y/n]: ' y || die "free those ports and re-run."
      if compose_can_override; then
        { printf 'services:\n  beelzebub:\n    ports: !override\n'; printf '%s' "$FREE"; } > docker-compose.override.yml
      else
        for _p in $BUSY; do drop_port "$_p"; done
      fi
      ok "Not publishing:$BUSY"
    fi
  fi

  # In Docker mode `plugin install` runs inside the image build, so the config a
  # plugin ships never reaches the host and the operator has no file to edit.
  # Copy it here instead — for every declared plugin, and never over an existing one.
  seed_plugin_configs() {
    _srcs=$(sed -n 's/^[[:space:]]*-[[:space:]]*source:[[:space:]]*//p' "$PLUGIN_CONFIG" 2>/dev/null | tr -d "\"'")
    for _s in $_srcs; do
      _tmp=""
      case "$_s" in
        ./*|/*|../*|~/*) _dir=$_s ;;
        *)
          command -v git >/dev/null 2>&1 || continue
          _tmp=$(mktemp -d) || continue
          git clone --depth 1 -q "https://${_s%@*}" "$_tmp" 2>/dev/null || { rm -rf "$_tmp"; continue; }
          _dir=$_tmp ;;
      esac
      _name=$(sed -n 's/^name:[[:space:]]*//p' "$_dir/plugins.yaml" 2>/dev/null | head -1 | tr -d "\"'")
      _ex="$_dir/$PLUGIN_CONFIG_DIR/$_name.yaml"
      if [ -n "$_name" ] && [ -f "$_ex" ] && [ ! -f "$PLUGIN_CONFIG_DIR/$_name.yaml" ]; then
        mkdir -p "$PLUGIN_CONFIG_DIR"
        cp "$_ex" "$PLUGIN_CONFIG_DIR/$_name.yaml"
        ok "Config for $_name: $PLUGIN_CONFIG_DIR/$_name.yaml"
      fi
      if [ -n "$_tmp" ]; then rm -rf "$_tmp"; fi
    done
  }
  seed_plugin_configs

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
  # The plugin CLI is a Go binary on the host. Plugins work without it — they are
  # declared and compiled in during the image build — so offer, do not require.
  if ! command -v go >/dev/null 2>&1; then
    if command -v brew >/dev/null 2>&1 || command -v pacman >/dev/null 2>&1 || command -v apk >/dev/null 2>&1; then
      _gp=go
    else
      _gp=golang
    fi
    _gc="$(pkg_install "$_gp")"
    # Optional, not a prerequisite: -y must not pull it in unasked.
    if [ -n "$_gc" ] && [ "$HAVE_TTY" -eq 1 ] && [ "$ASSUME_YES" -eq 0 ]; then
      info "Go is not installed. It builds ./beelzebub, which manages plugins from the shell."
      if confirm "Install it with \"$_gc\"? [y/N]: " n; then
        sh -c "$_gc" || info "Could not install Go; continuing without the plugin CLI."
      fi
    fi
  fi

  _cli_built=0
  if command -v go >/dev/null 2>&1 && command -v "$MAKE_CMD" >/dev/null 2>&1 \
     && "$MAKE_CMD" -s build >/dev/null 2>&1; then
    ok "Plugin CLI built"
    _cli_built=1
  fi
  info ""
  if [ "$_cli_built" -eq 1 ]; then
    info "Add a plugin with: ./beelzebub plugin install <link>"
  else
    info "Add a plugin: append \"  - source: <link>\" under \"plugins:\" in $PLUGIN_CONFIG,"
    info "then re-run this installer to rebuild with it."
  fi
else
  if [ -n "$TOKEN" ]; then
    export BEELZEBUB_CLOUD_ENABLED=true
    export BEELZEBUB_CLOUD_URI="$URI"
    export BEELZEBUB_CLOUD_AUTH_TOKEN="$TOKEN"
  fi
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

}

main "$@"
