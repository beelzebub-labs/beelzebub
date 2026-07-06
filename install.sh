#!/bin/sh

#
# Usage:
#   ./install.sh                         # interactive
#   ./install.sh --local                 # local build (Go), no prompts
#   ./install.sh --docker                # containerized (Docker)
#   ./install.sh --plugin github.com/org/x --plugin github.com/org/y --token TOK
#
# Flags (all optional; missing values are prompted for on a terminal):
#   --local | --docker      Install type (default: prompt, else local)
#   --plugin LINK           Plugin to install (repeatable); added to beelzebub.plugins
#   --token TOKEN           Beelzebub Cloud token (BEELZEBUB_CLOUD_AUTH_TOKEN)
#   --github-token TOKEN    Token for private plugin repos (BEELZEBUB_GITHUB_TOKEN)
#   -y, --yes               Assume yes to prompts (auto-install prerequisites)
#   -h, --help              Show this help
set -eu

MODE=""
PLUGINS=""
TOKEN="${BEELZEBUB_CLOUD_AUTH_TOKEN:-}"
GH_TOKEN="${BEELZEBUB_GITHUB_TOKEN:-}"
ASSUME_YES=0
REPO_URL="https://github.com/mariocandela/beelzebub.git"

# --- brand palette (truecolor; disabled when not a TTY or NO_COLOR is set) ----
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

# banner prints the wordmark with the brand gradient (purple → blue → cyan).
banner() {
  _cols=$(tput cols 2>/dev/null || echo 80)
  printf '\n'
  if [ -n "$E" ] && [ "${_cols:-80}" -ge 70 ]; then
    # big ANSI Shadow wordmark, vertical purple → cyan gradient
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

# --- glyph spinner (from the Caronte dashboard) ------------------------------
# Rotating glyph + infernal phrases, in the brand purple/blue.
glyph_at()  { i=$(( $1 % 8 )); set -- ✢ ✳ ✶ ✻ ✽ ✻ ✶ ✳; shift "$i"; printf '%s' "$1"; }
phrase_at() { i=$(( $1 % 6 ));  set -- 'Setting the trap' 'Summoning daemons' 'Weaving illusions' 'Baiting the swarm' 'Opening the gates' 'Stirring the pit'; shift "$i"; printf '%s' "$1"; }

# shimmer TEXT POS — TEXT with a bright crest sweeping across it (Claude-style
# loading shimmer). Purple only: light purple crest fading through brand purple to dim.
shimmer() {
  awk -v t="$1" -v p="$2" \
    -v c0="$B$E[38;2;201;160;255m" -v c1="$E[38;2;168;85;247m" -v c2="$E[38;2;120;74;168m" -v lo="$D" -v r="$R" 'BEGIN{
    n=length(t); if(n<1){exit}
    p=p%n
    for(i=1;i<=n;i++){ d=(i-1)-p; if(d<0)d=-d
      c=(d==0)?c0:(d==1)?c1:(d==2)?c2:lo
      printf "%s%s%s",c,substr(t,i,1),r
    }
  }'
}

# spin_run "done message" cmd...  — run cmd with a glyph spinner + shimmering
# phrase; on failure print its output and abort. Plain exec without a TTY.
spin_run() {
  _done="$1"; shift
  if [ -z "$E" ] || [ "$HAVE_TTY" -ne 1 ]; then "$@"; return; fi
  _log=$(mktemp 2>/dev/null || echo "/tmp/bzb.$$.log")
  "$@" >"$_log" 2>&1 &
  _pid=$!; _i=0; _p=0
  while kill -0 "$_pid" 2>/dev/null; do
    printf '\r  %s%s%s  %s\033[K' "$BLU" "$(glyph_at $_i)" "$R" "$(shimmer "$(phrase_at $_p)" "$_i")" > /dev/tty
    _i=$((_i + 1)); [ $((_i % 24)) -eq 0 ] && _p=$((_p + 1))
    sleep 0.1 2>/dev/null || sleep 1   # fractional where supported (busybox falls back to 1s)
  done
  wait "$_pid"; _rc=$?
  printf '\r\033[K' > /dev/tty
  if [ "$_rc" -ne 0 ]; then cat "$_log" >&2; rm -f "$_log"; die "$_done failed"; fi
  rm -f "$_log"; ok "$_done"
}

# --- flags -------------------------------------------------------------------
while [ $# -gt 0 ]; do
  case "$1" in
    --local)        MODE="local"; shift ;;
    --docker)       MODE="docker"; shift ;;
    --plugin)       PLUGINS="$PLUGINS $2"; shift 2 ;;
    --token)        TOKEN="$2"; shift 2 ;;
    --github-token) GH_TOKEN="$2"; shift 2 ;;
    -y|--yes)       ASSUME_YES=1; shift ;;
    -h|--help)      awk 'NR==1{next} /^#/{sub(/^# ?/,""); print; seen=1; next} seen{exit}' "$0"; exit 0 ;;
    *)              die "unknown option: $1" ;;
  esac
done

# A terminal is needed to prompt; when piped (curl | sh) we read from /dev/tty.
HAVE_TTY=0
[ -e /dev/tty ] && [ -r /dev/tty ] && HAVE_TTY=1
ask() { # ask "Question" -> echoes answer ("" on no tty)
  [ "$HAVE_TTY" -eq 1 ] || { printf '\n'; return 0; }
  printf '%s' "$1" > /dev/tty
  IFS= read -r _a < /dev/tty || _a=""
  printf '%s' "$_a"
}

banner

# --- ensure we're in a checkout (clone if not) -------------------------------
if [ ! -f go.mod ] || [ ! -f Makefile ]; then
  command -v git >/dev/null 2>&1 || die "run this inside a cloned beelzebub repo, or install git so it can be cloned."
  step "Cloning Beelzebub..."
  git clone --depth 1 "$REPO_URL" beelzebub
  cd beelzebub
fi

# --- choose install type -----------------------------------------------------
if [ -z "$MODE" ]; then
  info "How do you want to run Beelzebub?"
  printf '  %s1%s) Local %s(needs Go)%s\n' "$B" "$R" "$D" "$R"
  printf '  %s2%s) Docker %s(needs… Docker)%s\n' "$B" "$R" "$D" "$R"
  case "$(ask 'Choose [1]: ')" in
    2) MODE="docker" ;;
    *) MODE="local" ;;
  esac
fi

# --- prerequisites (check; offer to install via a known package manager) -----
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
else
  need go  "Install it from https://go.dev/dl and re-run."
  need git "Install git from your package manager and re-run."
fi

# --- plugins (only via --plugin; add more anytime in beelzebub.plugins or the CLI) ---
for p in $PLUGINS; do
  [ -f beelzebub.plugins ] && grep -qxF "$p" beelzebub.plugins 2>/dev/null && continue
  printf '%s\n' "$p" >> beelzebub.plugins
done

# --- cloud token -------------------------------------------------------------
if [ -z "$TOKEN" ] && [ "$HAVE_TTY" -eq 1 ]; then
  info "Beelzebub Cloud token — Learn more at https://beelzebub.ai/products/beelzebub-cloud/"
  TOKEN="$(ask 'Token (blank to skip): ')"
fi
# set_env KEY VALUE — upsert a line into .env (created 0600).
set_env() {
  { grep -v "^$1=" .env 2>/dev/null || true; printf '%s=%s\n' "$1" "$2"; } > .env.tmp && mv .env.tmp .env
  chmod 600 .env
}
[ -n "$TOKEN" ] && set_env BEELZEBUB_CLOUD_AUTH_TOKEN "$TOKEN"

# GitHub token: used by the CLI (plugin install) and written to .env so
# `docker compose --build` passes it as a build arg — this lets a no-Go Docker
# host bake in private plugins during the image build.
if [ -n "$GH_TOKEN" ]; then
  export BEELZEBUB_GITHUB_TOKEN="$GH_TOKEN"
  set_env BEELZEBUB_GITHUB_TOKEN "$GH_TOKEN"
fi

# Record the mode so a later `plugin install` applies changes the right way.
printf '%s\n' "$MODE" > .beelzebub-mode

# --- hand off to the Makefile ------------------------------------------------
if [ "$MODE" = "docker" ]; then
  spin_run "Image built · container started" ${MAKE:-make} docker
  # Also build the local CLI so `./beelzebub plugin ...` manages the container (needs Go).
  command -v go >/dev/null 2>&1 && ${MAKE:-make} -s build >/dev/null 2>&1 || true
  info ""
  info "Manage plugins with: ./beelzebub plugin install <link>   (or edit beelzebub.plugins)"
else
  [ -n "$TOKEN" ] && export BEELZEBUB_CLOUD_AUTH_TOKEN="$TOKEN"
  spin_run "Plugins wired"     sh -c 'go run . plugin install --no-build'
  spin_run "Built ./beelzebub" ${MAKE:-make} -s build
  # Run detached (like Docker's -d) so the installer returns; logs to beelzebub.log.
  nohup ./beelzebub run > beelzebub.log 2>&1 &
  _rpid=$!
  sleep 1
  kill -0 "$_rpid" 2>/dev/null || { cat beelzebub.log >&2; die "runtime exited on startup — see beelzebub.log"; }
  ok "Runtime running (pid $_rpid) · logs: beelzebub.log · stop: kill $_rpid"
  info ""
  info "Manage plugins with: ./beelzebub plugin install <link>   (or edit beelzebub.plugins)"
fi
