#!/bin/bash
# Backend smoke test for tui-traffic, run inside a lab guest.
#
# The contract (see tui-tools/tui-lab): this script runs on the guest as the
# unprivileged lab user, escalates with `sudo -n` only, prints a short PASS/FAIL
# table and exits non-zero if anything failed. The binary under test is at
# $TUI_LAB_BIN (default: tui-traffic on PATH).
#
# What it proves is that the tool reads the machine's *real* network and agrees
# with the machine's own tooling — not that a fake renders. The lab already
# covers --version and a --demo frame; this covers the three read paths.
#
# Two things are specific to this tool and worth stating, because they are what
# the assertions below are shaped around.
#
# A rate is a difference over time, so `--check` takes two samples an interval
# apart and answers in about that long. Every invocation here therefore costs a
# second, and the ones that do not need a rate pass `--interval 100ms`.
#
# And there are three possible sources for the connections screen — the
# conntrack command, the kernel's own file, and counting sockets — with a
# different number behind each. Which one a guest uses is a fact about the
# guest, not a pass or a fail: what is asserted is that the tool names the one
# it used and never claims a byte figure nobody measured.
set -uo pipefail

bin="${TUI_LAB_BIN:-tui-traffic}"
# TOOL is the manifest name, which is what a compatibility result is keyed on.
TOOL=tui-traffic
# fast is the interval for the assertions that do not read a rate, so the suite
# does not spend a second per invocation waiting for a second sample.
fast="--interval 100ms"
pass=0
fail=0

# check runs one assertion. It takes a label, a command and a grep pattern the
# command's output must match. Output is captured so a failure can show it.
check() {
  local label="$1" command="$2" pattern="$3" output status
  output=$(eval "$command" 2>&1)
  status=$?
  if [[ $status -eq 0 ]] && grep -qE "$pattern" <<<"$output"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s (exit %d)\n' "$label" "$status"
    sed 's/^/      | /' <<<"$output" | head -12
    fail=$((fail + 1))
  fi
}

# check_absent is the inverse of a grep assertion: the command must succeed and
# its output must NOT contain the pattern. It is what proves something did not
# happen, which is most of what a read-only tool has to prove.
check_absent() {
  local label="$1" command="$2" pattern="$3" output status
  output=$(eval "$command" 2>&1)
  status=$?
  if [[ $status -eq 0 ]] && ! grep -qE "$pattern" <<<"$output"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s (exit %d)\n' "$label" "$status"
    sed 's/^/      | /' <<<"$output" | head -12
    fail=$((fail + 1))
  fi
}

# ok records an assertion made in the script itself rather than by grepping a
# command's output.
ok() {
  local label="$1" condition="$2"
  if [[ $condition -eq 0 ]]; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s\n' "$label"
    fail=$((fail + 1))
  fi
}

# --- compatibility evidence -------------------------------------------------
#
# The manifest's `tested` list is generated, not claimed: it is rebuilt from
# compat/results.jsonl by tui-kit/tools/compat-sync.py, and this is where the
# lines of that file come from. The version recorded is the one the tool itself
# probed, read back out of --check, so it describes the machine that really ran
# the suite rather than what the tester assumed was installed.
#
# On this tool the usual outcome is that nothing is recorded, and that is
# correct: conntrack is the only backend declared and most machines do not have
# it. There is nothing to claim about a program that is not there.
record_compat() {
  local report="$1" outcome="$2" distro today block recorded=0
  block=$(sed -n '/"compat": \[/,/^  \]/p' <<<"$report")
  distro=$(. /etc/os-release && echo "${ID}-${VERSION_ID:-rolling}")
  today=$(date -u +%Y-%m-%d)

  while read -r backend version; do
    [[ -z $backend || -z $version ]] && continue
    local line
    line=$(printf '{"backend":"%s","date":"%s","distro":"%s","result":"%s","suite":"smoke","tool":"%s","version":"%s"}' \
      "$backend" "$today" "$distro" "$outcome" "$TOOL" "$version")
    printf 'compat-result: %s\n' "$line"
    if [[ -n ${TUI_COMPAT_RESULTS:-} ]]; then
      printf '%s\n' "$line" >>"$TUI_COMPAT_RESULTS"
    fi
    recorded=$((recorded + 1))
  done < <(awk '
    /"backend":/ { gsub(/[",]/, ""); b = $2 }
    /"version":/ { gsub(/[",]/, ""); if (b != "") { print b, $2; b = "" } }
  ' <<<"$block")

  if [[ $recorded -eq 0 ]]; then
    echo "      no backend version was probed, so no compatibility result is recorded"
  fi
}

echo "--- tui-traffic smoke on $(. /etc/os-release && echo "$PRETTY_NAME")"
if command -v conntrack >/dev/null 2>&1; then
  echo "      conntrack: $(conntrack --version 2>&1 | head -1)"
else
  echo "      conntrack: not installed, so the connections screen will fall back"
fi
echo "      accounting: $(cat /proc/sys/net/netfilter/nf_conntrack_acct 2>/dev/null || echo 'no conntrack in this kernel')"

# --- the report block ------------------------------------------------------
#
# --report is unprivileged, takes no sample and runs no command beyond a version
# probe, so it is smoked without sudo: a user who cannot escalate is exactly the
# one who most needs to be able to file a usable bug. These are the five cases
# every tool in the family asserts.

check "report names the backend" \
  "$bin --report" \
  '^backend: proc'

check "report says the run was live" \
  "$bin --report" \
  '^mode: live$'

check "report works in demo mode too" \
  "$bin --demo --report" \
  '^backend: demo$'

check "and says so on the mode line" \
  "$bin --demo --report" \
  '^mode: demo'

# The distro and kernel lines are excluded from the host-name search rather
# than from the promise: they are built from /etc/os-release and from uname's
# release and machine fields, never from its nodename, and on a guest called
# "fedora" or "ubuntu" — which is most of them — the host name is a substring
# of the distribution's own. Everything else in the block is searched.
check "report leaks neither a home path nor the host name" \
  "$bin --report | grep -vE '^(distro|kernel): ' | grep -cE '/home/|$(uname -n)' || true" \
  '^0$'

# The two lines this tool adds to the block, which are what a bug report about
# a missing byte column is answered with.
check "report names the source of each screen" \
  "$bin --report" \
  '^interfaces from: /proc/net/dev$'
check "report says whether the kernel counts bytes" \
  "$bin --report" \
  '^conntrack accounting: (on|off|unknown)$'

# --- the read path ---------------------------------------------------------

report=$("$bin" --check 2>&1)

# 1. It reads at all, unprivileged, and names what it drove. Everything except
#    the conntrack table is world-readable, so this runs as the plain lab user
#    — which is itself the assertion that the tool does not escalate to look at
#    things it can see without.
check "check reads the machine unprivileged" \
  "$bin --check $fast" \
  '"backend": "proc"'

# 2. Loopback exists on every machine there has ever been, and it is the one
#    interface whose absence would mean the parser, not the machine.
check "the interfaces screen found loopback" \
  "$bin --check $fast" \
  '"name": "lo"'

# 3. Every rate was measured over a window, and the window is reported with it.
#    A number whose window is not stated cannot be checked.
check "every rate carries the window it was measured over" \
  "$bin --check $fast | grep -c '\"windowNanos\": 0' || true" \
  '^0$'

# 4. The sources block names all three, which is what makes a number in this
#    JSON checkable rather than merely present.
for source in interfaces connections sockets; do
  check "the JSON names the $source source" \
    "$bin --check $fast | sed -n '/\"sources\"/,/}/p'" \
    "\"$source\": \".+\""
done

# 5. The connections screen used one of the three sources and said which.
check "the connections source is one this tool knows" \
  "$bin --check $fast" \
  '"source": "(conntrack|proc-nf-conntrack|sockets)"'

# 6. And it never claims a byte figure nobody measured. With accounting off —
#    which is the default on every distribution the lab runs — there must be no
#    talkers at all.
accounting=$(sed -n 's/.*"accounting": "\(.*\)".*/\1/p' <<<"$report" | head -1)
talkers=$(sed -n '/"talkers": \[/,/\]/p' <<<"$report" | grep -c '"bytes":')
if [[ "$accounting" == "on" ]] || [[ $talkers -eq 0 ]]; then
  printf 'PASS  no byte figures are shown unless the kernel counted them (accounting=%s, talkers=%d)\n' \
    "$accounting" "$talkers"
  pass=$((pass + 1))
else
  printf 'FAIL  %d talkers with accounting=%s: those bytes were never measured\n' \
    "$talkers" "$accounting"
  fail=$((fail + 1))
fi

# 7. The socket tables were read, and the counts add up to the total. A screen
#    whose columns do not add up is a screen nobody can trust.
check "the sockets screen read something" \
  "$bin --check $fast | sed -n '/\"sockets\"/,\$p'" \
  '"total": [1-9]'

sockets_total=$(sed -n '/"sockets": {/,/^  }/p' <<<"$report" |
  sed -n 's/.*"total": \([0-9]*\).*/\1/p' | head -1)
states_sum=$(sed -n '/"sockets": {/,/^  }/p' <<<"$report" |
  sed -n '/"states": \[/,/\]/p' | sed -n 's/.*"count": \([0-9]*\).*/\1/p' |
  awk '{ n += $1 } END { print n + 0 }')
if [[ "$sockets_total" == "$states_sum" ]]; then
  printf 'PASS  the socket states add up to the total (%s)\n' "$sockets_total"
  pass=$((pass + 1))
else
  printf 'FAIL  %s sockets but the states add to %s\n' "$sockets_total" "$states_sum"
  fail=$((fail + 1))
fi

# 8. sshd is the one service every lab guest runs, so port 22 is listening on
#    all of them. It is the assertion that the socket parser produced the right
#    port out of a hexadecimal column, checked against something known.
check "port 22 is listening, which is how the lab reached this guest" \
  "$bin --check $fast | sed -n '/\"listening\"/,/\]/p'" \
  '"localPort": 22'

# 9. And the exit code is not a verdict about the network. A saturated link and
#    a machine with no conntrack are both successful runs of tui-traffic.
"$bin" --check $fast >/dev/null 2>&1
status=$?
ok "--check exits 0 whatever the network is doing" $((status == 0 ? 0 : 1))

# --- read-only -------------------------------------------------------------
#
# This is the tool's whole claim, so it is asserted rather than assumed. There
# is no write path in the source for these to catch — the backend interface has
# no method that writes — which is exactly why the assertions are here: they
# are what would notice if one ever appeared.

# 10. The one setting this tool reads and could plausibly be tempted to set.
before_acct=$(cat /proc/sys/net/netfilter/nf_conntrack_acct 2>/dev/null)
before_links=$(ip -br link 2>/dev/null | awk '{ print $1, $2 }')
before_count=$(ls -1 /proc/net 2>/dev/null | sort)
"$bin" --check $fast >/dev/null 2>&1
after_acct=$(cat /proc/sys/net/netfilter/nf_conntrack_acct 2>/dev/null)
after_links=$(ip -br link 2>/dev/null | awk '{ print $1, $2 }')
after_count=$(ls -1 /proc/net 2>/dev/null | sort)

ok "--check left net.netfilter.nf_conntrack_acct alone" \
  $([[ "$before_acct" == "$after_acct" ]] && echo 0 || echo 1)
ok "--check left every interface in the state it found it" \
  $([[ "$before_links" == "$after_links" ]] && echo 0 || echo 1)
ok "--check loaded no kernel module that added a /proc/net file" \
  $([[ "$before_count" == "$after_count" ]] && echo 0 || echo 1)

# 11. No daemon. The tool exits when it is done and leaves nothing running,
#     which is a family promise and one a monitoring tool is especially able
#     to break.
"$bin" --check $fast >/dev/null 2>&1
sleep 1
leftover=$(pgrep -x tui-traffic 2>/dev/null | wc -l)
ok "nothing is left running after --check" $([[ $leftover -eq 0 ]] && echo 0 || echo 1)

# 12. And the tool writes no state of its own. It keeps a minute of history in
#     memory and nothing anywhere else.
config_home="${XDG_CONFIG_HOME:-$HOME/.config}"
ok "--check wrote no state under \$HOME" \
  $([[ ! -e "$config_home/tui-traffic" && ! -e "$HOME/.tui-traffic" ]] && echo 0 || echo 1)
ok "--check wrote no state under /var or /etc" \
  $([[ ! -e /var/lib/tui-traffic && ! -e /etc/tui-traffic/state ]] && echo 0 || echo 1)

# 13. The demo touches nothing at all, and says so on every screen it prints.
check "the demo says it is a demo" \
  "$bin --demo --check $fast" \
  '"backend": "demo"'
# The demo machine has a loopback of its own, like every machine, so the
# interface looked for here is a real one of this guest's: finding it in a demo
# reading would mean the demo had read the host.
real_link=$(ip -br link 2>/dev/null | awk '$1 != "lo" { print $1; exit }' | cut -d@ -f1)
if [[ -n "$real_link" ]]; then
  check_absent "the demo reads none of this machine's interfaces" \
    "$bin --demo --check $fast" \
    "\"name\": \"$real_link\""
else
  echo "      this guest has no interface beyond loopback, so there is nothing to look for"
fi

if [[ $fail -eq 0 ]]; then
  record_compat "$report" pass
else
  record_compat "$report" fail
fi

echo "--- tui-traffic: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
