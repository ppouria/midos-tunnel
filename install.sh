#!/bin/bash
# ============================================================================
#  Midos Tunnel - one-line installer
#
#      bash <(curl -fsSL https://raw.githubusercontent.com/1Rootzen/midos-tunnel/main/install.sh)
#
#  Installs everything the panel and its tunnels need, without asking anything:
#  system packages, the midos-tunnel binary (checksum-verified, from the latest
#  GitHub release), the kernel-tunnel base (modules, sysctl tuning) and the web
#  panel as a systemd service. It ends by printing the panel link, user name
#  and password. Run it again at any time to update; an existing panel keeps
#  its link, user and password.
#
#  Environment (all optional):
#      MIDOS_VERSION=v2.6.7      install this release instead of the latest
#      MIDOS_MIRROR=https://...  download the release files from a mirror
#      MIDOS_PANEL_PORT=2053     panel port on first install (random otherwise)
#      MIDOS_NO_KERNEL=1         skip the kernel-tunnel base
#      MIDOS_RESET_PASSWORD=1    on an update, also issue a new panel password
#
#  It never flushes firewall tables and never touches x-ui, xray, SSH or any
#  other software on the server. Everything it runs is logged to
#  /var/log/midos-install.log.
# ============================================================================
set -uo pipefail

REPO="1Rootzen/midos-tunnel"
BIN="/usr/local/sbin/midos-tunnel"
LEGACY_BIN="/usr/local/sbin/midos-veil" # where older installs kept the binary
KERNEL_SCRIPT="/usr/local/sbin/Midos-tunnel.sh"
CONF_DIR="/etc/Midos-tunnel"
PANEL_CONF="${CONF_DIR}/panel.json"
LOG="/var/log/midos-install.log"

# ---------------------------------------------------------------- look
ESC=$'\033'
N="${ESC}[0m" BOLD="${ESC}[1m" DIM="${ESC}[2m"
RED="${ESC}[38;5;203m" GRN="${ESC}[38;5;46m" GRN2="${ESC}[38;5;34m" YEL="${ESC}[38;5;221m"
WHT="${ESC}[1;97m" GRY="${ESC}[38;5;244m" BLU="${ESC}[38;5;111m"
TTY=0
[[ -t 1 ]] && TTY=1
[[ "$TTY" == 1 ]] || { N="" BOLD="" DIM="" RED="" GRN="" GRN2="" YEL="" WHT="" GRY="" BLU=""; }

logo() {
    local rows=(
        "███╗   ███╗██╗██████╗  ██████╗ ███████╗"
        "████╗ ████║██║██╔══██╗██╔═══██╗██╔════╝"
        "██╔████╔██║██║██║  ██║██║   ██║███████╗"
        "██║╚██╔╝██║██║██║  ██║██║   ██║╚════██║"
        "██║ ╚═╝ ██║██║██████╔╝╚██████╔╝███████║"
        "╚═╝     ╚═╝╚═╝╚═════╝  ╚═════╝ ╚══════╝"
    )
    local shades=(63 63 69 69 75 75) i
    echo
    for i in "${!rows[@]}"; do
        if [[ "$TTY" == 1 ]]; then printf '   %s%s%s\n' "${ESC}[38;5;${shades[$i]}m" "${rows[$i]}" "$N"; else printf '   %s\n' "${rows[$i]}"; fi
    done
    printf '              %sT  U  N  N  E  L%s\n' "${BOLD}${BLU}" "$N"
    printf '   %s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n' "$GRY" "$N"
    printf '   %sTunnel engine + web panel · installer%s\n\n' "$GRY" "$N"
}

fail() {
    [[ "$TTY" == 1 ]] && printf '%s[?25h' "$ESC"
    printf '\n  %s✘ %s%s\n' "$RED" "$1" "$N" >&2
    if [[ "${STARTED:-0}" == 1 && -s "$LOG" ]]; then
        printf '  %sLast lines of %s:%s\n' "$GRY" "$LOG" "$N" >&2
        tail -n 12 "$LOG" | sed 's/^/    /' >&2
    fi
    echo >&2
    exit 1
}

# ---------------------------------------------------------------- progress
BAR_W=34
DONE_PCT=0 # progress reached by the finished steps
SPIN=(⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏)

bar() { # bar <percent> <label> <spinner>
    local pct=$1 fill i on="" off=""
    fill=$((pct * BAR_W / 100))
    # built by concatenation: tr and ${s:0:n} both break on multi-byte cells
    for ((i = 0; i < BAR_W; i++)); do
        if ((i < fill)); then on+="█"; else off+="░"; fi
    done
    printf '\r%s[K  %s%s%s%s%s %s%3d%%%s  %s %s' "$ESC" "$GRN" "$on" "$GRY" "$off" "$N" \
        "$WHT" "$pct" "$N" "$GRN$3$N" "$2"
}

# step <weight> <soft:0|1> <label> <function>
# Runs one install step in the background while the bar moves, then records
# the result above the bar. A soft step may fail without stopping the install.
step() {
    local weight=$1 soft=$2 label=$3 fn=$4 pid rc n=0 start end pct secs t0=$SECONDS
    STARTED=1
    start=$DONE_PCT
    end=$((DONE_PCT + weight))
    printf '\n===== %s =====\n' "$label" >> "$LOG"
    ( "$fn" ) >> "$LOG" 2>&1 < /dev/null &
    pid=$!
    if [[ "$TTY" == 1 ]]; then
        while kill -0 "$pid" 2> /dev/null; do
            # creep toward the end of this step's slice without reaching it
            pct=$((start + weight * 900 * n / ((n + 150) * 1000)))
            bar "$pct" "$label" "${SPIN[$((n % 10))]}"
            n=$((n + 1))
            sleep 0.1
        done
    else
        printf '  … %s\n' "$label"
    fi
    wait "$pid"
    rc=$?
    secs=$((SECONDS - t0))
    [[ "$TTY" == 1 ]] && printf '\r%s[K' "$ESC"
    if [[ $rc -eq 0 ]]; then
        printf '  %s✔%s %s %s%ss%s\n' "$GRN" "$N" "$label" "$GRY" "$secs" "$N"
    elif [[ "$soft" == 1 ]]; then
        printf '  %s!%s %s %s(skipped, see %s)%s\n' "$YEL" "$N" "$label" "$GRY" "$LOG" "$N"
    else
        fail "$label failed"
    fi
    DONE_PCT=$end
    [[ "$TTY" == 1 ]] && bar "$DONE_PCT" "" " "
}

# ---------------------------------------------------------------- steps
step_packages() {
    export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1
    if command -v apt-get > /dev/null 2>&1; then
        apt-get -o DPkg::Lock::Timeout=600 update -q || true
        apt-get -o DPkg::Lock::Timeout=600 install -y -q \
            -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold \
            curl wget ca-certificates iproute2 iptables iputils-ping procps tar coreutils
    elif command -v dnf > /dev/null 2>&1; then
        dnf -y install curl wget ca-certificates iproute iptables iputils procps-ng tar coreutils
    else
        yum -y install curl wget ca-certificates iproute iptables iputils procps-ng tar coreutils
    fi
}

fetch() { curl -fsSL --connect-timeout 20 --retry 4 --retry-delay 3 -m 900 "$1" -o "$2"; }

step_binary() {
    fetch "${BASE}/${ASSET}" "${TMP}/${ASSET}" || { echo "download failed: ${BASE}/${ASSET}"; return 1; }
    fetch "${BASE}/${ASSET}.sha256" "${TMP}/${ASSET}.sha256" || { echo "checksum download failed"; return 1; }
    local want got
    want="$(awk '{print $1}' "${TMP}/${ASSET}.sha256")"
    got="$(sha256sum "${TMP}/${ASSET}" | awk '{print $1}')"
    [[ -n "$want" && "$want" == "$got" ]] || { echo "checksum mismatch: want $want got $got"; return 1; }
    [[ "$(head -c 4 "${TMP}/${ASSET}" | od -An -tx1 | tr -d ' \n')" == "7f454c46" ]] || { echo "not a Linux binary"; return 1; }
    local old="$BIN"
    [[ -x "$old" ]] || old="$LEGACY_BIN"
    [[ -x "$old" ]] && "$old" -version 2>&1 | awk '{print $2}' > "${TMP}/old.version"
    install -m 755 "${TMP}/${ASSET}" "${BIN}.new" && mv -f "${BIN}.new" "$BIN"
    "$BIN" -version 2>&1 | awk '{print $2}' > "${TMP}/new.version"
}

step_kernel_script() {
    fetch "${BASE}/Midos-tunnel.sh" "${TMP}/Midos-tunnel.sh" || return 1
    bash -n "${TMP}/Midos-tunnel.sh" || return 1
    install -m 755 "${TMP}/Midos-tunnel.sh" "$KERNEL_SCRIPT"
}

step_kernel_base() {
    [[ -f "$KERNEL_SCRIPT" ]] || return 1
    bash "$KERNEL_SCRIPT" prepare-kernel
}

step_panel() {
    mkdir -p "$CONF_DIR" && chmod 700 "$CONF_DIR"
    # an older install moves its panel and tunnels to the current unit names
    "$BIN" migrate || true
    if [[ -f "$PANEL_CONF" ]]; then
        if [[ "${MIDOS_RESET_PASSWORD:-}" == "1" ]]; then
            "$BIN" panel passwd -config "$PANEL_CONF" > "${TMP}/passwd.txt" || return 1
        fi
        systemctl daemon-reload
        systemctl enable midos-tunnel-panel.service
        systemctl restart midos-tunnel-panel.service
        systemctl try-restart 'midos-tunnel@*' || true # running tunnels move onto the new binary
    else
        "$BIN" panel init -config "$PANEL_CONF" -port "${MIDOS_PANEL_PORT:-0}" > "${TMP}/panel.txt" || return 1
        touch "${TMP}/first"
        # init only enables the unit; a panel that was already running would
        # keep its old address, so start it again on the new config
        systemctl daemon-reload
        systemctl restart midos-tunnel-panel.service
    fi
    # the link printed at the end must really answer
    local url local_url i
    url="$("$BIN" panel url -config "$PANEL_CONF" 2> /dev/null | grep -oE 'https?://[^[:space:]]+' | head -n 1)"
    [[ -n "$url" ]] || { echo "the panel has no address"; return 1; }
    local_url="$(echo "$url" | sed -E 's#^(https?://)[^/:]+#\1127.0.0.1#')"
    for i in $(seq 1 30); do
        curl -sk -o /dev/null --max-time 3 "$local_url" && { echo "panel answers at $url"; return 0; }
        sleep 1
    done
    echo "the panel does not answer at $local_url"
    systemctl --no-pager status midos-tunnel-panel.service | tail -n 20
    return 1
}

step_firewall() {
    # open the panel port only where a host firewall is active; rules are added, never replaced
    local port
    port="$(grep -oE '"listen":[[:space:]]*"[^"]*:[0-9]+' "$PANEL_CONF" | grep -oE '[0-9]+$')"
    [[ -n "$port" ]] || return 0
    if command -v ufw > /dev/null 2>&1 && ufw status 2> /dev/null | grep -q "Status: active"; then
        ufw allow "${port}/tcp"
    fi
    if command -v firewall-cmd > /dev/null 2>&1 && firewall-cmd --state > /dev/null 2>&1; then
        firewall-cmd -q --permanent --add-port="${port}/tcp" && firewall-cmd -q --reload
    fi
    return 0
}

# ---------------------------------------------------------------- run
logo
[[ $EUID -eq 0 ]] || fail "Run as root (sudo -i), then run the command again."
command -v systemctl > /dev/null 2>&1 || fail "systemd is required."
case "$(uname -m)" in
    x86_64 | amd64) ASSET="midos-tunnel" ;;
    aarch64 | arm64) ASSET="midos-tunnel-arm64" ;;
    *) fail "Unsupported CPU architecture: $(uname -m) (amd64 and arm64 are supported)." ;;
esac
command -v apt-get > /dev/null 2>&1 || command -v dnf > /dev/null 2>&1 || command -v yum > /dev/null 2>&1 \
    || fail "No supported package manager found (apt, dnf or yum)."

if [[ -n "${MIDOS_MIRROR:-}" ]]; then
    BASE="${MIDOS_MIRROR%/}" REL="mirror"
elif [[ -n "${MIDOS_VERSION:-}" ]]; then
    BASE="https://github.com/${REPO}/releases/download/${MIDOS_VERSION}" REL="$MIDOS_VERSION"
else
    BASE="https://github.com/${REPO}/releases/latest/download" REL="latest release"
fi
OS="$(. /etc/os-release 2> /dev/null && echo "${PRETTY_NAME:-Linux}")"
printf '  %s%s · %s · %s%s\n\n' "$GRY" "${OS:-Linux}" "$(uname -m)" "$REL" "$N"

export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1
TMP="$(mktemp -d)"
trap '[[ "$TTY" == 1 ]] && printf "%s[?25h" "$ESC"; rm -rf "$TMP"' EXIT
trap 'fail "Interrupted"' INT TERM
: > "$LOG"
[[ "$TTY" == 1 ]] && printf '%s[?25l' "$ESC" # hide the cursor while the bar runs

step 28 0 "Installing system packages" step_packages
step 32 0 "Downloading midos-tunnel (checksum verified)" step_binary
step 4 1 "Installing the tunnel manager" step_kernel_script
if [[ "${MIDOS_NO_KERNEL:-}" != "1" ]]; then
    step 22 1 "Preparing kernel tunnels (modules, BBR)" step_kernel_base
else
    DONE_PCT=$((DONE_PCT + 22))
fi
step 10 0 "Starting the web panel" step_panel
step 4 1 "Opening the panel port" step_firewall
[[ "$TTY" == 1 ]] && { bar 100 "${GRN}Done${N}" "✔"; printf '%s[?25h\n' "$ESC"; }

# ---------------------------------------------------------------- result
pick() { sed -n "s/^[[:space:]]*$1[[:space:]]*:[[:space:]]*//p" "$2" 2> /dev/null | head -n 1; }
VER="$(cat "${TMP}/new.version" 2> /dev/null)"
OLDV="$(cat "${TMP}/old.version" 2> /dev/null)"
if [[ -f "${TMP}/first" ]]; then
    URL="$(pick 'Panel address' "${TMP}/panel.txt")"
    USR="$(pick 'User name' "${TMP}/panel.txt")"
    PASS="$(pick 'Password' "${TMP}/panel.txt")"
    TITLE="Midos Tunnel ${VER} is installed"
else
    URL="$("$BIN" panel url -config "$PANEL_CONF" 2> /dev/null | grep -oE 'https?://[^[:space:]]+' | head -n 1)"
    USR="$(grep -oE '"username":[[:space:]]*"[^"]*"' "$PANEL_CONF" | sed 's/.*"\([^"]*\)"$/\1/')"
    PASS="$(sed -n 's/.*New password for [^:]*:[[:space:]]*//p' "${TMP}/passwd.txt" 2> /dev/null | head -n 1)"
    if [[ -n "$OLDV" && "$OLDV" != "$VER" ]]; then TITLE="Midos Tunnel updated ${OLDV} -> ${VER}"; else TITLE="Midos Tunnel ${VER} is up to date"; fi
fi
[[ -n "$PASS" ]] || PASS="(unchanged - new one: midos-tunnel panel passwd)"

lines=("$TITLE" "" "Panel link   ${URL:-?}" "User name    ${USR:-admin}" "Password     ${PASS}")
width=0
for l in "${lines[@]}"; do ((${#l} > width)) && width=${#l}; done
width=$((width + 4))
hr="$(printf '%*s' "$width" '' | sed 's/ /─/g')"
echo
printf '  %s╭%s╮%s\n' "$GRN2" "$hr" "$N"
for i in "${!lines[@]}"; do
    l="${lines[$i]}"
    pad=$((width - 2 - ${#l}))
    case $i in
        0) body="${GRN}${BOLD}✔ ${l}${N}"; pad=$((pad - 2)) ;;
        2 | 3 | 4) body="${GRY}${l:0:13}${N}${WHT}${l:13}${N}" ;;
        *) body="$l" ;;
    esac
    printf '  %s│%s  %s%*s%s│%s\n' "$GRN2" "$N" "$body" "$pad" '' "$GRN2" "$N"
done
printf '  %s╰%s╯%s\n' "$GRN2" "$hr" "$N"
if [[ -f "${TMP}/first" || -s "${TMP}/passwd.txt" ]]; then
    printf '  %s! Save the password now: it is stored only as a hash.%s\n' "$YEL" "$N"
fi
printf '  %sThe certificate is self-signed, so the browser warns once; you can get a real one in Settings.%s\n' "$GRY" "$N"
printf '  %sIf your provider has a cloud firewall, open the panel port there too.%s\n\n' "$GRY" "$N"
printf '  %sForgot the password%s  midos-tunnel panel passwd\n' "$GRY" "$N"
printf '  %sKernel tunnel menu%s   Midos-tunnel.sh\n' "$GRY" "$N"
printf '  %sUpdate later%s         run this installer again\n\n' "$GRY" "$N"
